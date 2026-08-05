package mlxlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// errNotAnObject reports a document whose top level is not a JSON object, which
// mlxlink --json never produces.
var errNotAnObject = errors.New("top level value is not an object")

// fieldAliases maps a canonical field name to the mlxlink JSON keys that may
// carry it. Keeping every alias here makes key renames between MFT releases a
// one-line change instead of a hunt through the decoder.
//
// The table is intentionally empty: the real keys come from a captured MFT
// 4.34.1 response (Phase B). Guessing paths before the fixture exists would
// bake in assumptions nothing can verify.
var fieldAliases = map[string][]string{}

// Decode converts raw mlxlink JSON into a PortData.
//
// Only document validation is implemented so far. The mlxlink schema (section
// names, per-lane representation, unit suffixes) is unverified until a real
// capture lands, and an empty PortData exports nothing rather than exporting
// guesses.
//
// The returned error is a plain error on purpose: turning a failure into an
// ErrorReason is the caller's job, which keeps this package's parsing layer
// free of metric concerns.
//
// Phase B, once the fixture lands:
//   - fill fieldAliases and add golden tests over testdata/mlxlink/
//   - add per-lane parsing; its input type depends on whether lanes arrive as
//     an array or an object, and that shape also settles whether lane numbers
//     start at 0 or 1
//   - normalise units (mV to V, mA to A) here, so the collector only ever sees
//     base units
func Decode(raw []byte) (PortData, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return PortData{}, fmt.Errorf("decode mlxlink json: %w", err)
	}
	// A top level "null" unmarshals into a nil map without error.
	if root == nil {
		return PortData{}, fmt.Errorf("decode mlxlink json: %w", errNotAnObject)
	}

	return PortData{}, nil
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

	// mlxlink may append units ("3.3V", "1.5 mW"): read the leading number and
	// ignore the suffix, which does not change the value.
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
