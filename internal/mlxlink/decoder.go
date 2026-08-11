package mlxlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Canonical names for base fields and top-level output sections. Their MFT
// specific spellings live in fieldAliases; structural keys inside the FEC and
// SerDes sections are validated by their dedicated parsers.
const (
	sectionModule       = "module_info"
	sectionOperational  = "operational_info"
	sectionCounters     = "counters_info"
	sectionFECHistogram = "fec_histogram"
	sectionSerDesTX     = "serdes_tx"
	sectionEye          = "eye"
	sectionPCIeEye      = "pcie_eye"

	fieldState           = "state"
	fieldPhysicalState   = "physical_state"
	fieldSpeed           = "speed"
	fieldWidth           = "width"
	fieldFEC             = "fec"
	fieldAutoNegotiation = "auto_negotiation"

	fieldEffectivePhysicalErrors = "effective_physical_errors"
	fieldLinkDown                = "link_down"
	fieldLinkErrorRecovery       = "link_error_recovery"
	fieldEffectiveBER            = "effective_ber"
	fieldRawBER                  = "raw_ber"
	fieldRawBERPerLane           = "raw_ber_per_lane"
	fieldRawErrorsPerLane        = "raw_errors_per_lane"

	fieldTemperature     = "temperature"
	fieldVoltage         = "voltage"
	fieldBiasCurrent     = "bias_current"
	fieldRxPower         = "rx_power"
	fieldTxPower         = "tx_power"
	fieldModuleFWFault   = "module_fw_fault"
	fieldDatapathFWFault = "datapath_fw_fault"
	fieldTxFault         = "tx_fault"
	fieldTxLOS           = "tx_los"
	fieldRxLOS           = "rx_los"
	fieldTxCDRLOL        = "tx_cdr_lol"
	fieldRxCDRLOL        = "rx_cdr_lol"
	fieldDatapathState   = "datapath_state"

	fieldIdentifier            = "identifier"
	fieldVendor                = "vendor"
	fieldPartNumber            = "part_number"
	fieldSerialNumber          = "serial_number"
	fieldRevision              = "revision"
	fieldFirmwareVersion       = "firmware_version"
	fieldActiveHostCompliance  = "active_host_compliance"
	fieldActiveMediaCompliance = "active_media_compliance"
	fieldCableType             = "cable_type"
)

// datapathActivated is the per lane state mlxlink reports for a lane that is
// carrying traffic; every other state counts as inactive.
const datapathActivated = "DPActivated"

// millisPerUnit converts the milli prefixed units mlxlink reports (mV, mA) into
// the base units the metrics use.
const millisPerUnit = 1000

// fieldAliases maps canonical base fields and top-level section names to the
// mlxlink JSON keys that may carry them. Keys are tried in order, so a newer
// spelling can be listed first without breaking older MFT releases.
//
// Verified against the MFT 4.34.1 captures in
// testdata/mlxlink/mft-4.34.1-400g-dr4.json and
// testdata/mlxlink/mft-4.34.1-400g-fec-serdes.json, plus the network and PCIe
// Eye captures in the same directory.
var fieldAliases = map[string][]string{
	sectionModule:       {"Module Info"},
	sectionOperational:  {"Operational Info"},
	sectionCounters:     {"Physical Counters and BER Info"},
	sectionFECHistogram: {"Histogram of FEC Errors"},
	sectionSerDesTX:     {"Serdes Tuning Transmitter Info"},
	sectionEye:          {"EYE Opening Info"},
	sectionPCIeEye:      {"EYE Opening Info (PCIe)"},

	fieldState:           {"State"},
	fieldPhysicalState:   {"Physical state"},
	fieldSpeed:           {"Speed"},
	fieldWidth:           {"Width"},
	fieldFEC:             {"FEC"},
	fieldAutoNegotiation: {"Auto Negotiation"},

	fieldEffectivePhysicalErrors: {"Effective Physical Errors"},
	fieldLinkDown:                {"Link Down Counter"},
	fieldLinkErrorRecovery:       {"Link Error Recovery Counter"},
	fieldEffectiveBER:            {"Effective Physical BER"},
	fieldRawBER:                  {"Raw Physical BER"},
	fieldRawBERPerLane:           {"Raw Physical BER Per Lane"},
	fieldRawErrorsPerLane:        {"Raw Physical Errors Per Lane"},

	fieldTemperature:     {"Temperature [C]"},
	fieldVoltage:         {"Voltage [mV]"},
	fieldBiasCurrent:     {"Bias Current [mA]"},
	fieldRxPower:         {"Rx Power Current [dBm]"},
	fieldTxPower:         {"Tx Power Current [dBm]"},
	fieldModuleFWFault:   {"Module FW Fault"},
	fieldDatapathFWFault: {"DataPath FW Fault"},
	fieldTxFault:         {"Tx Fault [per lane]"},
	fieldTxLOS:           {"Tx LOS [per lane]"},
	fieldRxLOS:           {"Rx LOS [per lane]"},
	fieldTxCDRLOL:        {"Tx CDR LOL [per lane]"},
	fieldRxCDRLOL:        {"Rx CDR LOL [per lane]"},
	fieldDatapathState:   {"DataPath state [per lane]"},

	fieldIdentifier:   {"Identifier"},
	fieldVendor:       {"Vendor Name"},
	fieldPartNumber:   {"Vendor Part Number"},
	fieldSerialNumber: {"Vendor Serial Number"},
	fieldRevision:     {"Rev"},
	// The module firmware. The "Firmware Version" of Tool Information is the
	// adapter firmware and belongs to a different device.
	fieldFirmwareVersion:       {"FW Version"},
	fieldActiveHostCompliance:  {"Active Set Host Compliance Code"},
	fieldActiveMediaCompliance: {"Active Set Media Compliance Code"},
	fieldCableType:             {"Cable Type"},
}

var errNoOutput = errors.New("missing result.output")

// document is the envelope every mlxlink --json response uses. The result stays
// raw so that the status can be read even when the payload has an unexpected
// shape: when mlxlink fails, its own message is the useful one.
type document struct {
	Result json.RawMessage `json:"result"`
	Status *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

// section is one group of fields under result.output.
type section map[string]json.RawMessage

// Decode converts raw mlxlink JSON into a PortData.
//
// Per-lane fields without an explicit lane list use their zero-based position.
// Eye fields instead use the lane numbers in mlxlink's explicit "Lane" list.
//
// Only a response that cannot be trusted as a whole fails: malformed JSON, a
// non-zero status, or a missing result.output. A missing section or field
// leaves the values it would have filled invalid instead, so one renamed key
// cannot cost a device every metric. Unknown fields are ignored.
//
// The returned error is a plain error on purpose: turning a failure into an
// ErrorReason is the caller's job, which keeps this parsing layer free of
// metric concerns.
func Decode(raw []byte) (PortData, error) {
	output, err := decodeOutput(raw)
	if err != nil {
		return PortData{}, err
	}

	return PortData{
		Link:         decodeLink(sectionOf(output, sectionOperational)),
		Counters:     decodeCounters(sectionOf(output, sectionCounters)),
		Module:       decodeModule(sectionOf(output, sectionModule)),
		FECHistogram: decodeFECHistogram(sectionOf(output, sectionFECHistogram)),
		SerDesTX:     decodeSerDesTX(sectionOf(output, sectionSerDesTX)),
		Eye:          decodeEye(sectionOf(output, sectionEye)),
	}, nil
}

// DecodePCIeEye converts a PCIe mlxlink --json response into eye-opening
// measurements. It applies the same envelope and status validation as Decode.
func DecodePCIeEye(raw []byte) (PCIeEye, error) {
	output, err := decodeOutput(raw)
	if err != nil {
		return PCIeEye{}, err
	}
	return decodePCIeEye(sectionOf(output, sectionPCIeEye)), nil
}

func decodeOutput(raw []byte) (map[string]json.RawMessage, error) {
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode mlxlink json: %w", err)
	}
	// Judged before the payload is even shaped: a failing mlxlink explains
	// itself here, and losing that message to a type error would leave the
	// operator with nothing to act on.
	if doc.Status != nil && doc.Status.Code != 0 {
		return nil, fmt.Errorf("mlxlink returned status %d: %s", doc.Status.Code, doc.Status.Message)
	}

	var result struct {
		Output map[string]json.RawMessage `json:"output"`
	}
	if len(doc.Result) > 0 {
		if err := json.Unmarshal(doc.Result, &result); err != nil {
			return nil, fmt.Errorf("decode mlxlink json: %w", err)
		}
	}
	if result.Output == nil {
		return nil, fmt.Errorf("decode mlxlink json: %w", errNoOutput)
	}
	return result.Output, nil
}

func decodeEye(sec section) Eye {
	values, ok := decodeEyeLaneValues(sec,
		"Initial FOM", "Last FOM", "Upper Grades", "Mid Grades", "Lower Grades")
	if !ok {
		return Eye{}
	}
	return Eye{
		InitialFOM: values[0],
		LastFOM:    values[1],
		UpperGrade: values[2],
		MidGrade:   values[3],
		LowerGrade: values[4],
	}
}

func decodePCIeEye(sec section) PCIeEye {
	values, ok := decodeEyeLaneValues(sec, "Initial FOM", "Last FOM")
	if !ok {
		return PCIeEye{}
	}
	return PCIeEye{InitialFOM: values[0], LastFOM: values[1]}
}

func decodeEyeLaneValues(sec section, fields ...string) ([][]LaneValue, bool) {
	if sec == nil {
		return nil, false
	}

	rawLanes, ok := rawValues(sec["Lane"])
	if !ok || len(rawLanes) == 0 {
		return nil, false
	}
	rawFamilies := make([][]string, len(fields))
	for i, field := range fields {
		rawFamilies[i], ok = rawValues(sec[field])
		if !ok || len(rawFamilies[i]) != len(rawLanes) {
			return nil, false
		}
	}

	lanes := make([]int, len(rawLanes))
	seen := make(map[int]struct{}, len(rawLanes))
	for i, rawLane := range rawLanes {
		lane, valid := parseNonnegativeInt(strings.TrimSpace(rawLane))
		if !valid {
			return nil, false
		}
		if _, duplicate := seen[lane]; duplicate {
			return nil, false
		}
		seen[lane] = struct{}{}
		lanes[i] = lane
	}

	decoded := make([][]LaneValue, len(fields))
	for i, lane := range lanes {
		laneValues := make([]float64, len(fields))
		validLane := true
		for family, rawFamily := range rawFamilies {
			value, err := strconv.ParseFloat(strings.TrimSpace(rawFamily[i]), 64)
			if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				validLane = false
				break
			}
			laneValues[family] = value
		}
		if !validLane {
			continue
		}
		for family, value := range laneValues {
			decoded[family] = append(decoded[family], LaneValue{Lane: lane, Value: value})
		}
	}
	for _, family := range decoded {
		sort.Slice(family, func(i, j int) bool { return family[i].Lane < family[j].Lane })
	}
	return decoded, true
}

func decodeFECHistogram(sec section) []FECHistogramBin {
	if sec == nil {
		return nil
	}

	header, ok := rawValues(sec["Header"])
	if !ok || len(header) != 2 || header[0] != "Range" || header[1] != "Occurrences" {
		return nil
	}

	binsByNumber := make(map[int]FECHistogramBin)
	for key, raw := range sec {
		if key == "Header" || !strings.HasPrefix(key, "Bin ") {
			continue
		}

		bin, ok := parseNonnegativeInt(strings.TrimPrefix(key, "Bin "))
		if !ok {
			return nil
		}
		if _, duplicate := binsByNumber[bin]; duplicate {
			return nil
		}

		values, ok := rawValues(raw)
		if !ok || len(values) != 2 {
			return nil
		}
		minimum, maximum, ok := parseFECRange(values[0])
		if !ok {
			return nil
		}
		occurrences, err := strconv.ParseUint(strings.TrimSpace(values[1]), 10, 64)
		if err != nil {
			return nil
		}
		binsByNumber[bin] = FECHistogramBin{
			Bin:           bin,
			ErrorCountMin: minimum,
			ErrorCountMax: maximum,
			Occurrences:   occurrences,
		}
	}

	if len(binsByNumber) == 0 {
		return nil
	}
	bins := make([]FECHistogramBin, 0, len(binsByNumber))
	for _, bin := range binsByNumber {
		bins = append(bins, bin)
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].Bin < bins[j].Bin })
	for i, bin := range bins {
		if bin.Bin != i {
			return nil
		}
	}
	return bins
}

func parseFECRange(raw string) (uint64, uint64, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 3 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return 0, 0, false
	}
	parts := strings.Split(raw[1:len(raw)-1], ":")
	if len(parts) < 1 || len(parts) > 2 {
		return 0, 0, false
	}
	minimum, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	maximum := minimum
	if len(parts) == 2 {
		maximum, err = strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || minimum > maximum {
			return 0, 0, false
		}
	}
	return minimum, maximum, true
}

func decodeSerDesTX(sec section) SerDesTX {
	if sec == nil {
		return SerDesTX{}
	}

	parameters, ok := rawValues(sec["Serdes TX parameters"])
	if !ok || len(parameters) == 0 {
		return SerDesTX{}
	}
	seenParameters := make(map[string]struct{}, len(parameters))
	for i := range parameters {
		parameters[i] = strings.TrimSpace(parameters[i])
		if _, duplicate := seenParameters[parameters[i]]; duplicate {
			return SerDesTX{}
		}
		seenParameters[parameters[i]] = struct{}{}
	}

	lanes := make(map[int][]string)
	for key, raw := range sec {
		if key == "Serdes TX parameters" {
			continue
		}
		if !strings.HasPrefix(key, "Lane ") {
			continue
		}
		lane, ok := parseNonnegativeInt(strings.TrimPrefix(key, "Lane "))
		if !ok {
			return SerDesTX{}
		}
		if _, duplicate := lanes[lane]; duplicate {
			return SerDesTX{}
		}
		values, ok := rawValues(raw)
		if !ok || len(values) != len(parameters) {
			return SerDesTX{}
		}
		lanes[lane] = values
	}

	var decoded SerDesTX
	for lane, values := range lanes {
		knownValues := make(map[string]float64)
		validLane := true
		for i, parameter := range parameters {
			if _, known := serDesParameter(parameter); !known {
				continue
			}
			value, err := strconv.ParseFloat(strings.TrimSpace(values[i]), 64)
			if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				validLane = false
				break
			}
			knownValues[parameter] = value
		}
		if !validLane {
			continue
		}
		for parameter, value := range knownValues {
			tap, _ := serDesParameter(parameter)
			if parameter == "drv_amp" {
				decoded.DriveAmplitude = append(decoded.DriveAmplitude, LaneValue{Lane: lane, Value: value})
				continue
			}
			decoded.FIRCoefficients = append(decoded.FIRCoefficients, SerDesFIRCoefficient{
				Lane: lane, Tap: tap, Value: value,
			})
		}
	}
	sort.SliceStable(decoded.FIRCoefficients, func(i, j int) bool {
		if decoded.FIRCoefficients[i].Lane != decoded.FIRCoefficients[j].Lane {
			return decoded.FIRCoefficients[i].Lane < decoded.FIRCoefficients[j].Lane
		}
		return decoded.FIRCoefficients[i].Tap < decoded.FIRCoefficients[j].Tap
	})
	sort.SliceStable(decoded.DriveAmplitude, func(i, j int) bool {
		return decoded.DriveAmplitude[i].Lane < decoded.DriveAmplitude[j].Lane
	})
	return decoded
}

func parseNonnegativeInt(raw string) (int, bool) {
	value, err := strconv.ParseUint(raw, 10, strconv.IntSize-1)
	if err != nil {
		return 0, false
	}
	return int(value), true
}

func serDesParameter(parameter string) (string, bool) {
	switch parameter {
	case "fir_pre3":
		return "pre3", true
	case "fir_pre2":
		return "pre2", true
	case "fir_pre1":
		return "pre1", true
	case "fir_main":
		return "main", true
	case "fir_post1":
		return "post1", true
	case "drv_amp":
		return "", true
	default:
		return "", false
	}
}

func rawValues(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var container struct {
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(raw, &container); err != nil || container.Values == nil {
		return nil, false
	}
	return container.Values, true
}

func decodeLink(sec section) LinkInfo {
	return LinkInfo{
		State:           stringField(sec, fieldState),
		PhysicalState:   stringField(sec, fieldPhysicalState),
		Speed:           stringField(sec, fieldSpeed),
		Width:           stringField(sec, fieldWidth),
		FEC:             stringField(sec, fieldFEC),
		AutoNegotiation: stringField(sec, fieldAutoNegotiation),
	}
}

func decodeCounters(sec section) Counters {
	return Counters{
		EffectivePhysicalErrors: parseFloatSafe(stringField(sec, fieldEffectivePhysicalErrors)),
		LinkDown:                parseFloatSafe(stringField(sec, fieldLinkDown)),
		LinkErrorRecovery:       parseFloatSafe(stringField(sec, fieldLinkErrorRecovery)),
		EffectiveBER:            parseFloatSafe(stringField(sec, fieldEffectiveBER)),
		RawBER:                  parseFloatSafe(stringField(sec, fieldRawBER)),

		RawPhysicalErrorsLane: laneValues(valuesField(sec, fieldRawErrorsPerLane), 1),
		RawBERLane:            laneValues(valuesField(sec, fieldRawBERPerLane), 1),
	}
}

func decodeModule(sec section) Module {
	return Module{
		TemperatureCelsius: parseFloatSafe(stringField(sec, fieldTemperature)),
		VoltageVolts:       divideValue(parseFloatSafe(stringField(sec, fieldVoltage)), millisPerUnit),

		BiasCurrentAmperes: commaLaneValues(stringField(sec, fieldBiasCurrent), millisPerUnit),
		RxPowerDBm:         commaLaneValues(stringField(sec, fieldRxPower), 1),
		TxPowerDBm:         commaLaneValues(stringField(sec, fieldTxPower), 1),

		ModuleFWFault:   parseFlag(stringField(sec, fieldModuleFWFault)),
		DatapathFWFault: parseFlag(stringField(sec, fieldDatapathFWFault)),

		TxFault:        laneFlags(valuesField(sec, fieldTxFault)),
		TxLOS:          laneFlags(valuesField(sec, fieldTxLOS)),
		RxLOS:          laneFlags(valuesField(sec, fieldRxLOS)),
		TxCDRLOL:       laneFlags(valuesField(sec, fieldTxCDRLOL)),
		RxCDRLOL:       laneFlags(valuesField(sec, fieldRxCDRLOL)),
		DatapathActive: laneStates(valuesField(sec, fieldDatapathState), datapathActivated),

		Info: ModuleInfo{
			Identifier:            stringField(sec, fieldIdentifier),
			Vendor:                stringField(sec, fieldVendor),
			PartNumber:            stringField(sec, fieldPartNumber),
			SerialNumber:          stringField(sec, fieldSerialNumber),
			Revision:              stringField(sec, fieldRevision),
			FirmwareVersion:       stringField(sec, fieldFirmwareVersion),
			ActiveHostCompliance:  stringField(sec, fieldActiveHostCompliance),
			ActiveMediaCompliance: stringField(sec, fieldActiveMediaCompliance),
			CableType:             stringField(sec, fieldCableType),
		},
	}
}

// sectionOf resolves a section by its canonical name. A section that is absent
// or not an object yields a nil section, which reads as all fields missing.
func sectionOf(output map[string]json.RawMessage, canonical string) section {
	for _, key := range fieldAliases[canonical] {
		raw, ok := output[key]
		if !ok {
			continue
		}
		var sec section
		if err := json.Unmarshal(raw, &sec); err != nil {
			return nil
		}
		return sec
	}
	return nil
}

func lookupField(sec section, canonical string) (json.RawMessage, bool) {
	for _, key := range fieldAliases[canonical] {
		if raw, ok := sec[key]; ok {
			return raw, true
		}
	}
	return nil, false
}

// stringField reads a scalar field. A value mlxlink reports as an object (the
// per lane form) reads as empty here rather than as an error.
func stringField(sec section, canonical string) string {
	raw, ok := lookupField(sec, canonical)
	if !ok {
		return ""
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// valuesField reads the per lane form, {"values": ["0", "0", ...]}.
func valuesField(sec section, canonical string) []string {
	raw, ok := lookupField(sec, canonical)
	if !ok {
		return nil
	}

	var container struct {
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(raw, &container); err != nil {
		return nil
	}
	return container.Values
}

// laneValues converts per lane readings, using the position in the list as the
// lane number. Unreadable entries, "N/A" among them, are left out entirely so
// that lane carries no sample instead of a made up one.
func laneValues(values []string, divisor float64) []LaneValue {
	lanes := make([]LaneValue, 0, len(values))
	for i, value := range values {
		parsed := parseFloatSafe(value)
		if !parsed.Valid {
			continue
		}
		lanes = append(lanes, LaneValue{Lane: i, Value: parsed.Float / divisor})
	}
	if len(lanes) == 0 {
		return nil
	}
	return lanes
}

// parseFlag reads a flag field, which mlxlink reports as "0" or "1". Any other
// text, a number outside that contract included, is unreadable: these feed
// gauges that may only ever be 0 or 1, and a "2" would be exported verbatim.
func parseFlag(s string) Value {
	switch strings.TrimSpace(s) {
	case "0":
		return Value{Float: 0, Valid: true}
	case "1":
		return Value{Float: 1, Valid: true}
	default:
		return Value{}
	}
}

// laneFlags reads a per lane 0/1 family, all of it or none of it. Dropping only
// the unreadable lanes would leave a family whose lane numbers no longer line
// up with the ones its neighbouring families report.
func laneFlags(values []string) []LaneValue {
	if len(values) == 0 {
		return nil
	}

	lanes := make([]LaneValue, 0, len(values))
	for i, value := range values {
		flag := parseFlag(value)
		if !flag.Valid {
			return nil
		}
		lanes = append(lanes, LaneValue{Lane: i, Value: flag.Float})
	}
	return lanes
}

// laneStates turns a per lane state into a 0/1 gauge, all of it or none of it.
// The comparison is exact: only the literal active state reads as active, so a
// state this decoder has not seen reads as inactive rather than as activity.
// A lane that carries no state at all makes the family unreadable instead,
// because "unknown" is not the same claim as "not active".
func laneStates(values []string, active string) []LaneValue {
	if len(values) == 0 {
		return nil
	}

	lanes := make([]LaneValue, 0, len(values))
	for i, value := range values {
		state := strings.TrimSpace(value)
		if state == "" || strings.EqualFold(state, "n/a") {
			return nil
		}
		flag := 0.0
		if state == active {
			flag = 1
		}
		lanes = append(lanes, LaneValue{Lane: i, Value: flag})
	}
	return lanes
}

// commaLaneValues reads the comma separated per lane form mlxlink uses for
// analog measurements, for example "265.504,265.504,248.416,248.416 [40..480]".
func commaLaneValues(raw string, divisor float64) []LaneValue {
	trimmed := trimRangeSuffix(raw)
	if trimmed == "" {
		return nil
	}
	return laneValues(strings.Split(trimmed, ","), divisor)
}

// trimRangeSuffix removes the bracketed range mlxlink appends to measured
// values, the " [40..480]" of "265.504,265.504,248.416,248.416 [40..480]".
func trimRangeSuffix(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, "]") {
		return s
	}

	open := strings.LastIndex(s, "[")
	if open <= 0 {
		return s
	}
	return strings.TrimSpace(s[:open])
}

// divideValue applies a unit conversion, keeping an invalid value invalid.
func divideValue(value Value, divisor float64) Value {
	if !value.Valid {
		return Value{}
	}
	return Value{Float: value.Float / divisor, Valid: true}
}

// parseFloatSafe converts an mlxlink scalar into a Value. Anything it cannot
// read with confidence yields Valid=false so the sample is dropped instead of
// exported as a fabricated number. Non-finite results are treated the same way:
// Prometheus must never receive Inf or NaN from us.
func parseFloatSafe(s string) Value {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || strings.EqualFold(trimmed, "n/a") {
		return Value{}
	}

	f, err := strconv.ParseFloat(trimmed, 64)
	if err == nil {
		return finiteValue(f)
	}
	// Out of float64 range in either direction. Neither the saturated result
	// nor a shortened prefix would be the number that was measured, so the
	// sample is dropped rather than reported off by orders of magnitude.
	if errors.Is(err, strconv.ErrRange) {
		return Value{}
	}

	// Hexadecimal notation must not fall through to the prefix scan below,
	// which would silently read "0x1f" as 0.
	if hasHexPrefix(trimmed) {
		return Value{}
	}

	// mlxlink appends units and ranges ("3.3V", "61 [-10..80]"): read the
	// leading number and ignore the suffix, which does not change the value.
	prefix, ok := leadingNumber(trimmed)
	if !ok {
		return Value{}
	}
	f, err = strconv.ParseFloat(prefix, 64)
	if err != nil {
		return Value{}
	}
	return finiteValue(f)
}

func finiteValue(f float64) Value {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return Value{}
	}
	return Value{Float: f, Valid: true}
}

func hasHexPrefix(s string) bool {
	s = strings.TrimLeft(s, "+-")
	return len(s) > 1 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
}

// leadingNumber returns the longest prefix of s that is a well formed float
// literal, in a single pass. It only engages on values that start like a
// number, so words are never mined for digits.
//
// An exponent marker with no digits after it ("1e", "1E-") makes the whole
// input unreadable instead of decaying to its mantissa: truncated output like
// "1e-255" cut short would otherwise be reported as 1.
func leadingNumber(s string) (prefix string, ok bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}

	digits := 0
	for i < len(s) && isDigit(s[i]) {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
			digits++
		}
	}
	if digits == 0 {
		return "", false
	}

	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		expDigits := 0
		for j < len(s) && isDigit(s[j]) {
			j++
			expDigits++
		}
		if expDigits == 0 {
			return "", false
		}
		i = j
	}
	return s[:i], true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
