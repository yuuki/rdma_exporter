package mlxlink

import (
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

func TestDecode_EmptyObject(t *testing.T) {
	t.Parallel()

	data, err := Decode([]byte(`{}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if !reflect.DeepEqual(data, PortData{}) {
		t.Fatalf("expected empty PortData, got %+v", data)
	}
}

func TestDecode_UnknownFieldsIgnored(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"result":{"output":{"Physical state":"LinkUp","lanes":[1,2,3]}},"status":0}`)

	data, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if !reflect.DeepEqual(data, PortData{}) {
		t.Fatalf("expected empty PortData until the schema is mapped, got %+v", data)
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

func TestFieldAliases_EmptyUntilFixtureArrives(t *testing.T) {
	t.Parallel()

	// Tripwire: the alias table stays empty until the MFT 4.34.1 fixture lands.
	// Filling it belongs together with golden tests over testdata/mlxlink/, so
	// this expectation is meant to be replaced in the same change.
	if len(fieldAliases) != 0 {
		t.Fatalf("expected no aliases before the fixture arrives, got %d", len(fieldAliases))
	}
}
