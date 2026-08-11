package mlxlink

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseFloatSafe_ParsesScalars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  Value
	}{
		{"scientific notation", "15E-255", Value{Float: 1.5e-254, Valid: true}},
		{"zero", "0", Value{Float: 0, Valid: true}},
		{"negative", "-3.5", Value{Float: -3.5, Valid: true}},
		{"exponent", "1e10", Value{Float: 1e10, Valid: true}},
		{"surrounding spaces", "  42  ", Value{Float: 42, Valid: true}},
		{"voltage unit", "3.3V", Value{Float: 3.3, Valid: true}},
		{"temperature unit", "45C", Value{Float: 45, Valid: true}},
		{"spaced unit", "1.5 mW", Value{Float: 1.5, Valid: true}},
		{"negative with spaced unit", "-40 C", Value{Float: -40, Valid: true}},
		{"exponent with unit", "1.5e-3 mA", Value{Float: 1.5e-3, Valid: true}},
		// Trailing junk made of float characters is shrunk away rather than
		// rejected; the leading number is still the best available reading.
		{"dotted version", "1.2.3", Value{Float: 1.2, Valid: true}},
		{"overflow", "1e309", Value{}},
		{"negative overflow", "-1e309", Value{}},
		{"overflow with unit", "1e309V", Value{}},
		// Underflow is a correctly rounded 0 rather than an out of range error,
		// so it stays a usable reading; overflow above is not.
		{"underflow", "1e-400", Value{Float: 0, Valid: true}},
		{"hundreds of digits", strings.Repeat("9", 400), Value{}},
		{"dangling exponent", "1e", Value{}},
		{"dangling exponent sign", "1e+", Value{}},
		{"dangling negative exponent", "1E-", Value{}},
		{"not available", "N/A", Value{}},
		{"not available lowercase", "n/a", Value{}},
		{"not available mixed case", "N/a", Value{}},
		{"not available padded", " N/A ", Value{}},
		{"empty", "", Value{}},
		{"blank", "   ", Value{}},
		{"word", "garbage", Value{}},
		{"unit only", "dBm", Value{}},
		{"sign only", "-", Value{}},
		{"dot only", ".", Value{}},
		{"hexadecimal", "0x1f", Value{}},
		{"positive infinity", "Inf", Value{}},
		{"negative infinity", "-Inf", Value{}},
		{"not a number", "NaN", Value{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := parseFloatSafe(tt.input); got != tt.want {
				t.Fatalf("parseFloatSafe(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDecode_UnknownFieldsIgnored(t *testing.T) {
	t.Parallel()

	// Unknown sections and unknown fields inside a known section are skipped,
	// and the one field that is understood still decodes.
	raw := []byte(`{
		"result": {"output": {
			"Operational Info": {"State": "Active", "Loopback Mode": "No Loopback"},
			"Serdes Info": {"anything": {"values": ["1", "2"]}}
		}},
		"status": {"code": 0, "message": "success"}
	}`)

	data, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if data.Link.State != "Active" {
		t.Fatalf("expected the known field to decode, got %q", data.Link.State)
	}
	if !reflect.DeepEqual(data.Counters, (Counters{})) || !reflect.DeepEqual(data.Module, (Module{})) {
		t.Fatalf("expected absent sections to stay empty, got %+v", data)
	}
}

func TestDecode_MalformedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{"truncated", `{"result":`},
		{"empty input", ``},
		{"trailing garbage", `{} oops`},
		{"top level array", `[]`},
		{"top level null", `null`},
		{"top level string", `"mlxlink"`},
		{"top level number", `42`},
		// A response without result.output carries no data at all, which is a
		// different failure from a section this decoder does not know.
		{"empty object", `{}`},
		{"result without output", `{"result":{}}`},
		{"output is not an object", `{"result":{"output":[]}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := Decode([]byte(tt.raw))
			if err == nil {
				t.Fatalf("expected error for %q", tt.raw)
			}
			if !reflect.DeepEqual(data, PortData{}) {
				t.Fatalf("expected empty PortData on error, got %+v", data)
			}
		})
	}
}

func TestDecode_DeeplyNestedInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		depth int
	}{
		{"within decoder limits", 1_000},
		{"beyond decoder limits", 100_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := `{"a":` + strings.Repeat(`[`, tt.depth) + strings.Repeat(`]`, tt.depth) + `}`

			// Whatever the outcome, decoding attacker-shaped input must not
			// panic and must not yield partial data.
			data, _ := Decode([]byte(raw))
			if !reflect.DeepEqual(data, PortData{}) {
				t.Fatalf("expected empty PortData, got %+v", data)
			}
		})
	}
}

// mlxlinkFixture reads a captured mlxlink response.
func mlxlinkFixture(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "mlxlink", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func decodeFixture(t *testing.T, name string) PortData {
	t.Helper()

	data, err := Decode(mlxlinkFixture(t, name))
	if err != nil {
		t.Fatalf("Decode(%s) returned error: %v", name, err)
	}
	return data
}

func lanes(values ...float64) []LaneValue {
	lanes := make([]LaneValue, 0, len(values))
	for i, value := range values {
		lanes = append(lanes, LaneValue{Lane: i, Value: value})
	}
	return lanes
}

// TestDecode_GoldenRealCapture pins every field against the real MFT 4.34.1
// response. Unit conversions are exact here: dividing the parsed millivolts and
// milliamperes by 1000 rounds to the same float64 as the decimal literal.
func TestDecode_GoldenRealCapture(t *testing.T) {
	t.Parallel()

	got := decodeFixture(t, "mft-4.34.1-400g-dr4.json")

	want := PortData{
		Link: LinkInfo{
			State:           "Active",
			PhysicalState:   "ETH_AN_FSM_ENABLE",
			Speed:           "400G",
			Width:           "4x",
			FEC:             "Standard_RS-FEC - (544,514)",
			AutoNegotiation: "ON",
		},
		Counters: Counters{
			EffectivePhysicalErrors: Value{Float: 0, Valid: true},
			LinkDown:                Value{Float: 0, Valid: true},
			LinkErrorRecovery:       Value{Float: 0, Valid: true},
			// "15E-255" is 15 * 10^-255, which is 1.5e-254.
			EffectiveBER: Value{Float: 1.5e-254, Valid: true},
			RawBER:       Value{Float: 5e-10, Valid: true},

			RawPhysicalErrorsLane: lanes(3017647, 15132549, 7368641, 9233545),
			RawBERLane:            lanes(1e-10, 1e-9, 4e-10, 5e-10),
		},
		Module: Module{
			// "61 [-10..80]" keeps the reading and drops the range.
			TemperatureCelsius: Value{Float: 61, Valid: true},
			// 3235.5 mV / 1000.
			VoltageVolts: Value{Float: 3.2355, Valid: true},
			// 265.504 mA and 248.416 mA / 1000.
			BiasCurrentAmperes: lanes(0.265504, 0.265504, 0.248416, 0.248416),
			RxPowerDBm:         lanes(3.583, 3.253, 3.233, 2.658),
			TxPowerDBm:         lanes(2.193, 1.202, 1.708, 1.274),

			ModuleFWFault:   Value{Float: 0, Valid: true},
			DatapathFWFault: Value{Float: 0, Valid: true},

			TxFault:        lanes(0, 0, 0, 0),
			TxLOS:          lanes(0, 0, 0, 0),
			RxLOS:          lanes(0, 0, 0, 0),
			TxCDRLOL:       lanes(0, 0, 0, 0),
			RxCDRLOL:       lanes(0, 0, 0, 0),
			DatapathActive: lanes(1, 1, 1, 1),

			Info: ModuleInfo{
				Identifier:            "OSFP",
				Vendor:                "EXAMPLE",
				PartNumber:            "OSFP-400G-DR4",
				SerialNumber:          "S00XXX000000",
				Revision:              "1A",
				FirmwareVersion:       "40.242.17",
				ActiveHostCompliance:  "IB NDR",
				ActiveMediaCompliance: "400GBASE-DR4",
				CableType:             "Optical Module (separated)",
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded PortData mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestDecode_NotAvailableFields(t *testing.T) {
	t.Parallel()

	// na-fields.json marks the analog scalars and the BER fields "N/A" and
	// replaces one lane of the comma separated bias current with "N/A".
	got := decodeFixture(t, "na-fields.json")

	for name, value := range map[string]Value{
		"temperature":      got.Module.TemperatureCelsius,
		"voltage":          got.Module.VoltageVolts,
		"effective errors": got.Counters.EffectivePhysicalErrors,
		"effective ber":    got.Counters.EffectiveBER,
		"raw ber":          got.Counters.RawBER,
	} {
		if value.Valid {
			t.Errorf("expected %s to be unreadable, got %+v", name, value)
		}
	}
	for name, values := range map[string][]LaneValue{
		"rx power":     got.Module.RxPowerDBm,
		"tx power":     got.Module.TxPowerDBm,
		"raw ber lane": got.Counters.RawBERLane,
		"raw err lane": got.Counters.RawPhysicalErrorsLane,
	} {
		if values != nil {
			t.Errorf("expected %s to have no lanes, got %+v", name, values)
		}
	}

	// Only the unreadable lane drops out; its neighbours keep their numbers.
	wantBias := []LaneValue{
		{Lane: 0, Value: 0.265504},
		{Lane: 2, Value: 0.248416},
		{Lane: 3, Value: 0.248416},
	}
	if !reflect.DeepEqual(got.Module.BiasCurrentAmperes, wantBias) {
		t.Fatalf("expected %+v, got %+v", wantBias, got.Module.BiasCurrentAmperes)
	}

	// Fields that were left alone still decode.
	if got.Link.State != "Active" {
		t.Fatalf("expected the link state to survive, got %q", got.Link.State)
	}
	if !reflect.DeepEqual(got.Module.DatapathActive, lanes(1, 1, 1, 1)) {
		t.Fatalf("expected the datapath lanes to survive, got %+v", got.Module.DatapathActive)
	}
}

func TestDecode_LaneCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture string
		lanes   int
		width   string
	}{
		// The same capture rewritten to one and to eight lanes, in both the
		// "values" array and the comma separated form.
		{"single lane", "lanes-1x.json", 1, "1x"},
		{"eight lanes", "lanes-8x.json", 8, "8x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := decodeFixture(t, tt.fixture)

			if got.Link.Width != tt.width {
				t.Fatalf("expected width %q, got %q", tt.width, got.Link.Width)
			}
			for name, values := range map[string][]LaneValue{
				"bias current": got.Module.BiasCurrentAmperes,
				"rx power":     got.Module.RxPowerDBm,
				"tx power":     got.Module.TxPowerDBm,
				"tx fault":     got.Module.TxFault,
				"datapath":     got.Module.DatapathActive,
				"raw ber lane": got.Counters.RawBERLane,
				"raw err lane": got.Counters.RawPhysicalErrorsLane,
			} {
				if len(values) != tt.lanes {
					t.Errorf("expected %d %s lanes, got %d (%+v)", tt.lanes, name, len(values), values)
				}
				for i, value := range values {
					if value.Lane != i {
						t.Errorf("expected %s lane numbers to start at 0 and be dense, got %+v", name, values)
						break
					}
				}
			}
			// The first lane is the same reading whatever the lane count.
			if len(got.Module.BiasCurrentAmperes) == 0 {
				t.Fatalf("expected bias current lanes, got none")
			}
			if got := got.Module.BiasCurrentAmperes[0].Value; got != 0.265504 {
				t.Fatalf("expected lane 0 bias current 0.265504, got %v", got)
			}
		})
	}
}

func TestDecode_FaultsActive(t *testing.T) {
	t.Parallel()

	// faults-active.json raises every fault, deactivates the datapath lanes and
	// takes the link down.
	got := decodeFixture(t, "faults-active.json")

	if got.Link.State != "Down" || got.Link.PhysicalState != "Disabled" {
		t.Fatalf("expected a down link, got %+v", got.Link)
	}
	if got.Module.ModuleFWFault != (Value{Float: 1, Valid: true}) {
		t.Fatalf("expected the module fw fault to be raised, got %+v", got.Module.ModuleFWFault)
	}
	if got.Module.DatapathFWFault != (Value{Float: 1, Valid: true}) {
		t.Fatalf("expected the datapath fw fault to be raised, got %+v", got.Module.DatapathFWFault)
	}
	for name, values := range map[string][]LaneValue{
		"tx fault":   got.Module.TxFault,
		"tx los":     got.Module.TxLOS,
		"rx los":     got.Module.RxLOS,
		"tx cdr lol": got.Module.TxCDRLOL,
		"rx cdr lol": got.Module.RxCDRLOL,
	} {
		if !reflect.DeepEqual(values, lanes(1, 1, 1, 1)) {
			t.Errorf("expected %s raised on every lane, got %+v", name, values)
		}
	}
	// Only DPActivated counts as active, so every other state reads as 0.
	if !reflect.DeepEqual(got.Module.DatapathActive, lanes(0, 0, 0, 0)) {
		t.Fatalf("expected no active datapath lane, got %+v", got.Module.DatapathActive)
	}
}

func TestDecode_MissingModuleSection(t *testing.T) {
	t.Parallel()

	// missing-module-section.json drops "Module Info" entirely: the module
	// values disappear while the link and counters keep working.
	got := decodeFixture(t, "missing-module-section.json")

	if !reflect.DeepEqual(got.Module, (Module{})) {
		t.Fatalf("expected no module data, got %+v", got.Module)
	}
	if got.Link.State != "Active" || got.Link.Speed != "400G" {
		t.Fatalf("expected the link section to survive, got %+v", got.Link)
	}
	if got.Counters.RawBER != (Value{Float: 5e-10, Valid: true}) {
		t.Fatalf("expected the counters to survive, got %+v", got.Counters.RawBER)
	}
}

func TestDecode_StatusError(t *testing.T) {
	t.Parallel()

	// status-error.json reports a failure, which must not be read as data even
	// though the output section is still present.
	data, err := Decode(mlxlinkFixture(t, "status-error.json"))
	if err == nil {
		t.Fatal("expected an error for a non-zero status")
	}
	if !strings.Contains(err.Error(), "Failed to open device") {
		t.Fatalf("expected the mlxlink message to be reported, got %v", err)
	}
	if !reflect.DeepEqual(data, PortData{}) {
		t.Fatalf("expected no data alongside the error, got %+v", data)
	}
}

// moduleDocument wraps a handful of Module Info fields in the envelope Decode
// expects, for anomalies too specific to belong in a captured response.
func moduleDocument(fields string) []byte {
	return []byte(`{"result":{"output":{"Module Info":{` + fields + `}}},"status":{"code":0,"message":"success"}}`)
}

func TestDecode_ScalarFlagsMustBeBinary(t *testing.T) {
	t.Parallel()

	// These feed 0/1 gauges, so a value outside the contract is dropped rather
	// than exported verbatim.
	for _, value := range []string{"2", "-1", "0.0", "N/A", "yes", ""} {
		data, err := Decode(moduleDocument(`"Module FW Fault":"` + value + `","DataPath FW Fault":"` + value + `"`))
		if err != nil {
			t.Fatalf("Decode(%q) returned error: %v", value, err)
		}
		if data.Module.ModuleFWFault.Valid || data.Module.DatapathFWFault.Valid {
			t.Errorf("expected %q to be unreadable, got %+v / %+v",
				value, data.Module.ModuleFWFault, data.Module.DatapathFWFault)
		}
	}

	data, err := Decode(moduleDocument(`"Module FW Fault":"1","DataPath FW Fault":"0"`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if data.Module.ModuleFWFault != (Value{Float: 1, Valid: true}) {
		t.Fatalf("expected the raised fault, got %+v", data.Module.ModuleFWFault)
	}
	if data.Module.DatapathFWFault != (Value{Float: 0, Valid: true}) {
		t.Fatalf("expected the cleared fault, got %+v", data.Module.DatapathFWFault)
	}
}

func TestDecode_LaneFlagFamilyIsAllOrNothing(t *testing.T) {
	t.Parallel()

	// One unreadable lane drops the family: a sparse family would renumber the
	// lanes relative to every other per lane family of the same port.
	data, err := Decode(moduleDocument(`"Tx Fault [per lane]":{"values":["0","N/A","1"]}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if data.Module.TxFault != nil {
		t.Fatalf("expected no lanes for a partially unreadable family, got %+v", data.Module.TxFault)
	}

	data, err = Decode(moduleDocument(`"Tx Fault [per lane]":{"values":["0","1","0"]}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if !reflect.DeepEqual(data.Module.TxFault, lanes(0, 1, 0)) {
		t.Fatalf("expected every lane, got %+v", data.Module.TxFault)
	}

	// A value outside the 0/1 contract is treated the same way.
	data, err = Decode(moduleDocument(`"Rx LOS [per lane]":{"values":["0","2"]}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if data.Module.RxLOS != nil {
		t.Fatalf("expected no lanes for a non binary family, got %+v", data.Module.RxLOS)
	}
}

func TestDecode_DatapathStateMatchIsExact(t *testing.T) {
	t.Parallel()

	// Only the literal state counts: a differently cased spelling is a state
	// this decoder does not know, not an activation.
	data, err := Decode(moduleDocument(`"DataPath state [per lane]":{"values":["DPActivated","dpactivated","DPDeactivated"]}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if !reflect.DeepEqual(data.Module.DatapathActive, lanes(1, 0, 0)) {
		t.Fatalf("expected only the exact match to be active, got %+v", data.Module.DatapathActive)
	}

	// A lane without a state makes the family unreadable rather than inactive.
	data, err = Decode(moduleDocument(`"DataPath state [per lane]":{"values":["DPActivated","N/A"]}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if data.Module.DatapathActive != nil {
		t.Fatalf("expected no lanes when a state is unknown, got %+v", data.Module.DatapathActive)
	}
}

func TestDecode_StatusErrorOutranksPayloadShape(t *testing.T) {
	t.Parallel()

	// The payload is the wrong shape, but mlxlink already said why: reporting a
	// type error here would throw that explanation away.
	raw := []byte(`{"result":{"output":[]},"status":{"code":7,"message":"device failed"}}`)

	data, err := Decode(raw)
	if err == nil {
		t.Fatal("expected an error for a non-zero status")
	}
	if !strings.Contains(err.Error(), "device failed") || !strings.Contains(err.Error(), "7") {
		t.Fatalf("expected the mlxlink status to be reported, got %v", err)
	}
	if !reflect.DeepEqual(data, PortData{}) {
		t.Fatalf("expected no data alongside the error, got %+v", data)
	}
}

func TestFieldAliases_CoverEveryCanonicalName(t *testing.T) {
	t.Parallel()

	// Every canonical name the decoder asks for must have at least one spelling
	// in the table; a typo here would silently blank a field.
	canonical := []string{
		sectionModule, sectionOperational, sectionCounters, sectionFECHistogram, sectionSerDesTX,
		sectionEye, sectionPCIeEye,
		fieldState, fieldPhysicalState, fieldSpeed, fieldWidth, fieldFEC, fieldAutoNegotiation,
		fieldEffectivePhysicalErrors, fieldLinkDown, fieldLinkErrorRecovery,
		fieldEffectiveBER, fieldRawBER, fieldRawBERPerLane, fieldRawErrorsPerLane,
		fieldTemperature, fieldVoltage, fieldBiasCurrent, fieldRxPower, fieldTxPower,
		fieldModuleFWFault, fieldDatapathFWFault, fieldTxFault, fieldTxLOS, fieldRxLOS,
		fieldTxCDRLOL, fieldRxCDRLOL, fieldDatapathState,
		fieldIdentifier, fieldVendor, fieldPartNumber, fieldSerialNumber, fieldRevision,
		fieldFirmwareVersion, fieldActiveHostCompliance, fieldActiveMediaCompliance, fieldCableType,
	}

	for _, name := range canonical {
		if len(fieldAliases[name]) == 0 {
			t.Errorf("canonical name %q has no alias", name)
		}
	}
	if len(fieldAliases) != len(canonical) {
		t.Errorf("expected %d entries in the alias table, got %d", len(canonical), len(fieldAliases))
	}
}

func optionalDocument(sections string) []byte {
	return []byte(`{"result":{"output":{` + sections + `}},"status":{"code":0,"message":"success"}}`)
}

func TestDecode_GoldenCombinedRealCapture(t *testing.T) {
	t.Parallel()

	got := decodeFixture(t, "mft-4.34.1-400g-fec-serdes.json")

	if len(got.FECHistogram) != 16 {
		t.Fatalf("expected 16 FEC bins, got %d (%+v)", len(got.FECHistogram), got.FECHistogram)
	}
	if got.FECHistogram[0] != (FECHistogramBin{
		Bin: 0, ErrorCountMin: 0, ErrorCountMax: 0, Occurrences: 22858119037881,
	}) {
		t.Fatalf("unexpected first FEC bin: %+v", got.FECHistogram[0])
	}
	if got.FECHistogram[15] != (FECHistogramBin{
		Bin: 15, ErrorCountMin: 15, ErrorCountMax: 15, Occurrences: 0,
	}) {
		t.Fatalf("unexpected last FEC bin: %+v", got.FECHistogram[15])
	}

	wantFIR := make([]SerDesFIRCoefficient, 0, 20)
	for lane := 0; lane < 4; lane++ {
		for _, coefficient := range []struct {
			tap   string
			value float64
		}{
			{"main", 43},
			{"post1", 0},
			{"pre1", -15},
			{"pre2", 5},
			{"pre3", 0},
		} {
			wantFIR = append(wantFIR, SerDesFIRCoefficient{
				Lane: lane, Tap: coefficient.tap, Value: coefficient.value,
			})
		}
	}
	if !reflect.DeepEqual(got.SerDesTX.FIRCoefficients, wantFIR) {
		t.Fatalf("unexpected SerDes FIR coefficients\n got: %+v\nwant: %+v", got.SerDesTX.FIRCoefficients, wantFIR)
	}
	if want := lanes(0, 0, 0, 0); !reflect.DeepEqual(got.SerDesTX.DriveAmplitude, want) {
		t.Fatalf("unexpected SerDes drive amplitude: %+v", got.SerDesTX.DriveAmplitude)
	}
	if got.Module.Info.SerialNumber != "<redacted>" {
		t.Fatalf("expected sanitized serial number, got %q", got.Module.Info.SerialNumber)
	}
}

func TestDecode_FECHistogramRanges(t *testing.T) {
	t.Parallel()

	raw := optionalDocument(`"Histogram of FEC Errors":{
		"Header":{"values":["Range","Occurrences"]},
		"Bin 1":{"values":["[16:31]","18446744073709551615"]},
		"Bin 0":{"values":["[0]","7"]},
		"Future Field":"ignored"
	}`)
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	want := []FECHistogramBin{
		{Bin: 0, ErrorCountMin: 0, ErrorCountMax: 0, Occurrences: 7},
		{Bin: 1, ErrorCountMin: 16, ErrorCountMax: 31, Occurrences: ^uint64(0)},
	}
	if !reflect.DeepEqual(got.FECHistogram, want) {
		t.Fatalf("unexpected FEC histogram\n got: %+v\nwant: %+v", got.FECHistogram, want)
	}
}

func TestDecode_FECHistogramInvalidSectionIsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
	}{
		{
			name: "bad header",
			section: `"Header":{"values":["Occurrences","Range"]},
				"Bin 0":{"values":["[0]","1"]}`,
		},
		{
			name: "bin gap",
			section: `"Header":{"values":["Range","Occurrences"]},
				"Bin 0":{"values":["[0]","1"]},
				"Bin 2":{"values":["[2]","1"]}`,
		},
		{
			name: "descending range",
			section: `"Header":{"values":["Range","Occurrences"]},
				"Bin 0":{"values":["[2:1]","1"]}`,
		},
		{
			name: "negative range",
			section: `"Header":{"values":["Range","Occurrences"]},
				"Bin 0":{"values":["[-1]","1"]}`,
		},
		{
			name: "bad count",
			section: `"Header":{"values":["Range","Occurrences"]},
				"Bin 0":{"values":["[0]","1.5"]}`,
		},
		{
			name: "wrong value count",
			section: `"Header":{"values":["Range","Occurrences"]},
				"Bin 0":{"values":["[0]"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := optionalDocument(`
				"Histogram of FEC Errors":{` + tt.section + `},
				"Operational Info":{"State":"Active"}
			`)
			got, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode returned error: %v", err)
			}
			if got.FECHistogram != nil {
				t.Fatalf("expected invalid FEC section to be empty, got %+v", got.FECHistogram)
			}
			if got.Link.State != "Active" {
				t.Fatalf("expected another section to survive, got %+v", got.Link)
			}
		})
	}
}

func TestDecode_SerDesTXUnknownParameterIsIgnored(t *testing.T) {
	t.Parallel()

	raw := optionalDocument(`"Serdes Tuning Transmitter Info":{
		"Serdes TX parameters":{"values":["fir_pre1","future_param","drv_amp"]},
		"Lane 3":{"values":["-4","999","12"]},
		"Lane 1":{"values":["-2","888","10"]},
		"Future Field":{"values":["ignored"]}
	}`)
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	want := SerDesTX{
		FIRCoefficients: []SerDesFIRCoefficient{
			{Lane: 1, Tap: "pre1", Value: -2},
			{Lane: 3, Tap: "pre1", Value: -4},
		},
		DriveAmplitude: []LaneValue{
			{Lane: 1, Value: 10},
			{Lane: 3, Value: 12},
		},
	}
	if !reflect.DeepEqual(got.SerDesTX, want) {
		t.Fatalf("unexpected SerDes data\n got: %+v\nwant: %+v", got.SerDesTX, want)
	}
}

func TestDecode_SerDesTXLaneCountsAndHeaderOrder(t *testing.T) {
	t.Parallel()

	for _, laneCount := range []int{1, 4, 8} {
		laneCount := laneCount
		t.Run(fmt.Sprintf("%d lanes", laneCount), func(t *testing.T) {
			t.Parallel()

			laneFields := make([]string, 0, laneCount)
			for lane := laneCount - 1; lane >= 0; lane-- {
				laneFields = append(laneFields, fmt.Sprintf(
					`"Lane %d":{"values":["%d","%d","%d"]}`,
					lane, lane+10, -lane-1, lane+40,
				))
			}
			raw := optionalDocument(`"Serdes Tuning Transmitter Info":{
				"Serdes TX parameters":{"values":["drv_amp","fir_pre1","fir_main"]},` +
				strings.Join(laneFields, ",") + `}`)
			got, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode returned error: %v", err)
			}
			if len(got.SerDesTX.FIRCoefficients) != laneCount*2 || len(got.SerDesTX.DriveAmplitude) != laneCount {
				t.Fatalf("unexpected lane counts: %+v", got.SerDesTX)
			}
			wantFirst := []SerDesFIRCoefficient{
				{Lane: 0, Tap: "main", Value: 40},
				{Lane: 0, Tap: "pre1", Value: -1},
			}
			if !reflect.DeepEqual(got.SerDesTX.FIRCoefficients[:2], wantFirst) {
				t.Fatalf("header values were not paired with their parameters: %+v", got.SerDesTX.FIRCoefficients[:2])
			}
			if last := got.SerDesTX.DriveAmplitude[laneCount-1]; last != (LaneValue{Lane: laneCount - 1, Value: float64(laneCount + 9)}) {
				t.Fatalf("unexpected last drive amplitude: %+v", last)
			}
		})
	}
}

func TestDecode_SerDesTXInvalidSectionIsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
	}{
		{
			name: "duplicate header",
			section: `"Serdes TX parameters":{"values":["fir_pre1","fir_pre1"]},
				"Lane 0":{"values":["1","2"]}`,
		},
		{
			name: "lane length mismatch",
			section: `"Serdes TX parameters":{"values":["fir_pre1","drv_amp"]},
				"Lane 0":{"values":["1"]}`,
		},
		{
			name: "invalid lane number",
			section: `"Serdes TX parameters":{"values":["fir_pre1"]},
				"Lane future":{"values":["1"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := optionalDocument(`
				"Serdes Tuning Transmitter Info":{` + tt.section + `},
				"Operational Info":{"State":"Active"}
			`)
			got, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode returned error: %v", err)
			}
			if !reflect.DeepEqual(got.SerDesTX, (SerDesTX{})) {
				t.Fatalf("expected invalid SerDes section to be empty, got %+v", got.SerDesTX)
			}
			if got.Link.State != "Active" {
				t.Fatalf("expected another section to survive, got %+v", got.Link)
			}
		})
	}
}

func TestDecode_OptionalSectionIndexesMustFitInt(t *testing.T) {
	t.Parallel()

	t.Run("FEC bin", func(t *testing.T) {
		t.Parallel()

		raw := optionalDocument(`"Histogram of FEC Errors":{
			"Header":{"values":["Range","Occurrences"]},
			"Bin 18446744073709551615":{"values":["[0]","1"]}
		}`)
		got, err := Decode(raw)
		if err != nil {
			t.Fatalf("Decode returned error: %v", err)
		}
		if got.FECHistogram != nil {
			t.Fatalf("expected an overflowing bin number to invalidate the family, got %+v", got.FECHistogram)
		}
	})

	t.Run("SerDes lane", func(t *testing.T) {
		t.Parallel()

		raw := optionalDocument(`"Serdes Tuning Transmitter Info":{
			"Serdes TX parameters":{"values":["fir_main","drv_amp"]},
			"Lane 18446744073709551615":{"values":["40","8"]}
		}`)
		got, err := Decode(raw)
		if err != nil {
			t.Fatalf("Decode returned error: %v", err)
		}
		if !reflect.DeepEqual(got.SerDesTX, (SerDesTX{})) {
			t.Fatalf("expected an overflowing lane number to invalidate the section, got %+v", got.SerDesTX)
		}
	})
}

func TestDecode_SerDesTXBadLaneIsOmitted(t *testing.T) {
	t.Parallel()

	raw := optionalDocument(`"Serdes Tuning Transmitter Info":{
		"Serdes TX parameters":{"values":["fir_main","drv_amp","future_param"]},
		"Lane 0":{"values":["43","8","not-numeric"]},
		"Lane 1":{"values":["bad","9","ignored"]},
		"Lane 2":{"values":["41","10","ignored"]}
	}`)
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	want := SerDesTX{
		FIRCoefficients: []SerDesFIRCoefficient{
			{Lane: 0, Tap: "main", Value: 43},
			{Lane: 2, Tap: "main", Value: 41},
		},
		DriveAmplitude: []LaneValue{
			{Lane: 0, Value: 8},
			{Lane: 2, Value: 10},
		},
	}
	if !reflect.DeepEqual(got.SerDesTX, want) {
		t.Fatalf("unexpected SerDes data\n got: %+v\nwant: %+v", got.SerDesTX, want)
	}
}

func TestDecode_OptionalSectionsAbsent(t *testing.T) {
	t.Parallel()

	got, err := Decode(optionalDocument(`"Operational Info":{"State":"Active"}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if got.FECHistogram != nil || !reflect.DeepEqual(got.SerDesTX, (SerDesTX{})) {
		t.Fatalf("expected absent optional sections to be empty, got %+v", got)
	}
}

func eyeDocument(sectionName, fields string) []byte {
	return optionalDocument(fmt.Sprintf("%q:{%s}", sectionName, fields))
}

func TestDecode_GoldenEyeCombinedRealCapture(t *testing.T) {
	t.Parallel()

	got := decodeFixture(t, "mft-4.34.1-400g-eye.json")
	want := Eye{
		InitialFOM: []LaneValue{
			{Lane: 0, Value: 100},
			{Lane: 1, Value: 116},
			{Lane: 2, Value: 87},
			{Lane: 3, Value: 98},
		},
		LastFOM: []LaneValue{
			{Lane: 0, Value: 107},
			{Lane: 1, Value: 112},
			{Lane: 2, Value: 88},
			{Lane: 3, Value: 101},
		},
		UpperGrade: []LaneValue{
			{Lane: 0, Value: 108},
			{Lane: 1, Value: 111},
			{Lane: 2, Value: 85},
			{Lane: 3, Value: 100},
		},
		MidGrade: []LaneValue{
			{Lane: 0, Value: 124},
			{Lane: 1, Value: 121},
			{Lane: 2, Value: 111},
			{Lane: 3, Value: 114},
		},
		LowerGrade: []LaneValue{
			{Lane: 0, Value: 106},
			{Lane: 1, Value: 109},
			{Lane: 2, Value: 91},
			{Lane: 3, Value: 95},
		},
	}
	if !reflect.DeepEqual(got.Eye, want) {
		t.Fatalf("unexpected Eye data\n got: %+v\nwant: %+v", got.Eye, want)
	}

	// One invocation supplies all optional and baseline families. Pinning them
	// here catches a decoder integration that accidentally trades existing data
	// for Eye data.
	if len(got.FECHistogram) != 16 {
		t.Fatalf("expected 16 FEC bins, got %d", len(got.FECHistogram))
	}
	if len(got.SerDesTX.FIRCoefficients) != 20 || len(got.SerDesTX.DriveAmplitude) != 4 {
		t.Fatalf("unexpected SerDes data: %+v", got.SerDesTX)
	}
	if got.Link.State != "Active" || got.Counters.RawBER != (Value{Float: 5e-10, Valid: true}) {
		t.Fatalf("expected baseline data to survive, got link=%+v counters=%+v", got.Link, got.Counters)
	}
	if got.Module.Info.SerialNumber != "<redacted>" {
		t.Fatalf("expected sanitized serial number, got %q", got.Module.Info.SerialNumber)
	}
}

func TestDecodePCIeEye_GoldenRealCapture(t *testing.T) {
	t.Parallel()

	got, err := DecodePCIeEye(mlxlinkFixture(t, "mft-4.34.1-pcie-eye.json"))
	if err != nil {
		t.Fatalf("DecodePCIeEye returned error: %v", err)
	}

	want := PCIeEye{
		InitialFOM: lanes(145, 140, 145, 144, 130, 136, 137, 129, 145, 130, 136, 122, 144, 130, 125, 135),
		LastFOM:    lanes(134, 141, 138, 142, 121, 115, 136, 135, 136, 130, 121, 103, 122, 109, 111, 131),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected PCIe Eye data\n got: %+v\nwant: %+v", got, want)
	}
}

func TestDecode_EyeLaneNumbersAreExplicitAndSorted(t *testing.T) {
	t.Parallel()

	raw := eyeDocument("EYE Opening Info", `
		"FOM Mode":{"values":["future-mode"]},
		"Lane":{"values":["3","1"]},
		"Initial FOM":{"values":["30","10"]},
		"Last FOM":{"values":["31","11"]},
		"Upper Grades":{"values":["32","12"]},
		"Mid Grades":{"values":["33","13"]},
		"Lower Grades":{"values":["34","14"]},
		"Future Field":{"values":["ignored"]}
	`)
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	want := Eye{
		InitialFOM: []LaneValue{{Lane: 1, Value: 10}, {Lane: 3, Value: 30}},
		LastFOM:    []LaneValue{{Lane: 1, Value: 11}, {Lane: 3, Value: 31}},
		UpperGrade: []LaneValue{{Lane: 1, Value: 12}, {Lane: 3, Value: 32}},
		MidGrade:   []LaneValue{{Lane: 1, Value: 13}, {Lane: 3, Value: 33}},
		LowerGrade: []LaneValue{{Lane: 1, Value: 14}, {Lane: 3, Value: 34}},
	}
	if !reflect.DeepEqual(got.Eye, want) {
		t.Fatalf("unexpected sorted Eye data\n got: %+v\nwant: %+v", got.Eye, want)
	}
}

func TestDecode_EyeLaneCounts(t *testing.T) {
	t.Parallel()

	for _, laneCount := range []int{1, 4, 8, 16} {
		laneCount := laneCount
		t.Run(fmt.Sprintf("%d lanes", laneCount), func(t *testing.T) {
			t.Parallel()

			laneNumbers := make([]string, 0, laneCount)
			initial := make([]string, 0, laneCount)
			last := make([]string, 0, laneCount)
			upper := make([]string, 0, laneCount)
			mid := make([]string, 0, laneCount)
			lower := make([]string, 0, laneCount)
			for lane := 0; lane < laneCount; lane++ {
				laneNumbers = append(laneNumbers, fmt.Sprintf("%q", fmt.Sprint(lane)))
				initial = append(initial, fmt.Sprintf("%q", fmt.Sprint(100+lane)))
				last = append(last, fmt.Sprintf("%q", fmt.Sprint(200+lane)))
				upper = append(upper, fmt.Sprintf("%q", fmt.Sprint(300+lane)))
				mid = append(mid, fmt.Sprintf("%q", fmt.Sprint(400+lane)))
				lower = append(lower, fmt.Sprintf("%q", fmt.Sprint(500+lane)))
			}
			raw := eyeDocument("EYE Opening Info", fmt.Sprintf(`
				"Lane":{"values":[%s]},
				"Initial FOM":{"values":[%s]},
				"Last FOM":{"values":[%s]},
				"Upper Grades":{"values":[%s]},
				"Mid Grades":{"values":[%s]},
				"Lower Grades":{"values":[%s]}`,
				strings.Join(laneNumbers, ","), strings.Join(initial, ","), strings.Join(last, ","),
				strings.Join(upper, ","), strings.Join(mid, ","), strings.Join(lower, ",")))
			got, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode returned error: %v", err)
			}
			for name, values := range map[string][]LaneValue{
				"initial": got.Eye.InitialFOM,
				"last":    got.Eye.LastFOM,
				"upper":   got.Eye.UpperGrade,
				"mid":     got.Eye.MidGrade,
				"lower":   got.Eye.LowerGrade,
			} {
				if len(values) != laneCount {
					t.Errorf("expected %d %s lanes, got %+v", laneCount, name, values)
				}
			}
		})
	}
}

func TestDecode_EyeInvalidStructureOmitsFamily(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fields string
	}{
		{
			name: "missing required values",
			fields: `"Lane":{"values":["0"]},
				"Initial FOM":{"values":["1"]},"Last FOM":{"values":["2"]},
				"Upper Grades":{"values":["3"]},"Mid Grades":{"values":["4"]}`,
		},
		{
			name: "length mismatch",
			fields: `"Lane":{"values":["0","1"]},
				"Initial FOM":{"values":["1"]},"Last FOM":{"values":["2","3"]},
				"Upper Grades":{"values":["4","5"]},"Mid Grades":{"values":["6","7"]},
				"Lower Grades":{"values":["8","9"]}`,
		},
		{
			name: "duplicate lane",
			fields: `"Lane":{"values":["0","0"]},
				"Initial FOM":{"values":["1","2"]},"Last FOM":{"values":["3","4"]},
				"Upper Grades":{"values":["5","6"]},"Mid Grades":{"values":["7","8"]},
				"Lower Grades":{"values":["9","10"]}`,
		},
		{
			name: "negative lane",
			fields: `"Lane":{"values":["-1"]},
				"Initial FOM":{"values":["1"]},"Last FOM":{"values":["2"]},
				"Upper Grades":{"values":["3"]},"Mid Grades":{"values":["4"]},
				"Lower Grades":{"values":["5"]}`,
		},
		{
			name: "overflowing lane",
			fields: `"Lane":{"values":["18446744073709551615"]},
				"Initial FOM":{"values":["1"]},"Last FOM":{"values":["2"]},
				"Upper Grades":{"values":["3"]},"Mid Grades":{"values":["4"]},
				"Lower Grades":{"values":["5"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Decode(eyeDocument("EYE Opening Info", tt.fields))
			if err != nil {
				t.Fatalf("Decode returned error: %v", err)
			}
			if !reflect.DeepEqual(got.Eye, (Eye{})) {
				t.Fatalf("expected invalid Eye family to be empty, got %+v", got.Eye)
			}
		})
	}
}

func TestDecode_EyeInvalidNumberDropsWholeLane(t *testing.T) {
	t.Parallel()

	for _, invalid := range []string{"bad", "NaN", "Inf", "-Inf"} {
		invalid := invalid
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()

			raw := eyeDocument("EYE Opening Info", fmt.Sprintf(`
				"Lane":{"values":["0","1","2"]},
				"Initial FOM":{"values":["10","11","12"]},
				"Last FOM":{"values":["20","%s","22"]},
				"Upper Grades":{"values":["30","31","32"]},
				"Mid Grades":{"values":["40","41","42"]},
				"Lower Grades":{"values":["50","51","52"]}`, invalid))
			got, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode returned error: %v", err)
			}
			for name, values := range map[string][]LaneValue{
				"initial": got.Eye.InitialFOM,
				"last":    got.Eye.LastFOM,
				"upper":   got.Eye.UpperGrade,
				"mid":     got.Eye.MidGrade,
				"lower":   got.Eye.LowerGrade,
			} {
				if len(values) != 2 || values[0].Lane != 0 || values[1].Lane != 2 {
					t.Errorf("expected %s to omit lane 1, got %+v", name, values)
				}
			}
		})
	}
}

func TestDecode_EyeSectionAbsentIsEmpty(t *testing.T) {
	t.Parallel()

	got, err := Decode(optionalDocument(`"Operational Info":{"State":"Active"}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if !reflect.DeepEqual(got.Eye, (Eye{})) {
		t.Fatalf("expected absent Eye section to be empty, got %+v", got.Eye)
	}
}

func TestDecodePCIeEye_InvalidStructureAndNumbers(t *testing.T) {
	t.Parallel()

	t.Run("invalid structure omits family", func(t *testing.T) {
		t.Parallel()

		got, err := DecodePCIeEye(eyeDocument("EYE Opening Info (PCIe)", `
			"Lane":{"values":["0","1"]},
			"Initial FOM":{"values":["1"]},
			"Last FOM":{"values":["2","3"]}`))
		if err != nil {
			t.Fatalf("DecodePCIeEye returned error: %v", err)
		}
		if !reflect.DeepEqual(got, (PCIeEye{})) {
			t.Fatalf("expected invalid PCIe Eye family to be empty, got %+v", got)
		}
	})

	t.Run("invalid number omits lane", func(t *testing.T) {
		t.Parallel()

		got, err := DecodePCIeEye(eyeDocument("EYE Opening Info (PCIe)", `
			"Lane":{"values":["2","0","1"]},
			"Initial FOM":{"values":["12","10","NaN"]},
			"Last FOM":{"values":["22","20","21"]}`))
		if err != nil {
			t.Fatalf("DecodePCIeEye returned error: %v", err)
		}
		want := PCIeEye{
			InitialFOM: []LaneValue{{Lane: 0, Value: 10}, {Lane: 2, Value: 12}},
			LastFOM:    []LaneValue{{Lane: 0, Value: 20}, {Lane: 2, Value: 22}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected PCIe Eye data\n got: %+v\nwant: %+v", got, want)
		}
	})
}

func TestDecodePCIeEye_EnvelopeValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed json", raw: `{"result":`},
		{name: "missing output", raw: `{"result":{},"status":{"code":0,"message":"success"}}`},
		{name: "status failure", raw: `{"result":{"output":{}},"status":{"code":7,"message":"device failed"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := DecodePCIeEye([]byte(tt.raw))
			if err == nil {
				t.Fatal("expected error")
			}
			if !reflect.DeepEqual(got, (PCIeEye{})) {
				t.Fatalf("expected empty data on error, got %+v", got)
			}
		})
	}
}
