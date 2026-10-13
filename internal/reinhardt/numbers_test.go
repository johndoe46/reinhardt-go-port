package reinhardt

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

type namedInteger int64

func (namedInteger) String() string { return "not an integer" }

func TestNumberIntegerInputs(test *testing.T) {
	for _, scenario := range []struct {
		name  string
		value any
		want  int64
	}{
		{"absent", nil, 0},
		{"int", int(42), 42},
		{"int8", int8(-42), -42},
		{"int16", int16(-42), -42},
		{"int32", int32(-42), -42},
		{"int64", int64(42), 42},
		{"minimum", int64(math.MinInt64), math.MinInt64},
		{"maximum", int64(math.MaxInt64), math.MaxInt64},
		{"uint", uint(42), 42},
		{"uint8", uint8(42), 42},
		{"uint16", uint16(42), 42},
		{"uint32", uint32(42), 42},
		{"uint64", uint64(math.MaxInt64), math.MaxInt64},
		{"float32", float32(42), 42},
		{"float64", float64(42), 42},
		{"float minimum", float64(math.MinInt64), math.MinInt64},
		{"string", "-42", -42},
		{"JSON number", json.Number("9223372036854775807"), math.MaxInt64},
		{"named integer with Stringer", namedInteger(42), 42},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			got, err := number(scenario.value)
			if err != nil || got != scenario.want {
				test.Fatalf("got %d, want %d: %v", got, scenario.want, err)
			}
		})
	}
}

func TestNumberRejectsInvalidInputs(test *testing.T) {
	for _, scenario := range []struct {
		name  string
		value any
	}{
		{"empty string", ""},
		{"malformed string", "invalid"},
		{"string overflow", "9223372036854775808"},
		{"string underflow", "-9223372036854775809"},
		{"JSON fraction", json.Number("1.5")},
		{"JSON overflow", json.Number("9223372036854775808")},
		{"unsigned overflow", uint64(math.MaxUint64)},
		{"fraction", float64(1.5)},
		{"float overflow", float64(math.MaxInt64)},
		{"float underflow", math.Nextafter(-0x1p63, math.Inf(-1))},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
		{"boolean", true},
		{"slice", []int{42}},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			if _, err := number(scenario.value); err == nil {
				test.Fatal("invalid input accepted")
			}
			if got := (Row{"value": scenario.value}).N("value"); got != 0 {
				test.Fatalf("projection fallback=%d, want 0", got)
			}
		})
	}
}

func TestTemplateNumericConversions(test *testing.T) {
	templates, err := newTemplates()
	if err != nil {
		test.Fatal(err)
	}
	for _, scenario := range []struct {
		name, source string
		value        any
		want         string
		wantError    bool
	}{
		{"duration", `{{ humanDuration . }}`, int64(3661), "01:01:01", false},
		{"absent duration", `{{ humanDuration . }}`, nil, "00:00:00", false},
		{"cross-type equality", `{{ eq . 42 }}`, int64(42), "true", false},
		{"named equality", `{{ eq . 42 }}`, namedInteger(42), "true", false},
		{"cross-type membership", `{{ contains 42 . }}`, []int64{1, 42}, "true", false},
		{"invalid duration", `{{ humanDuration . }}`, "invalid", "", true},
		{"invalid greater left", `{{ gt . 0 }}`, "invalid", "", true},
		{"invalid greater right", `{{ gt 0 . }}`, "invalid", "", true},
		{"overflow equality", `{{ eq . 0 }}`, uint64(math.MaxUint64), "", true},
		{"overflow inequality", `{{ ne . 0 }}`, uint64(math.MaxUint64), "", true},
		{"overflow membership", `{{ contains 42 . }}`, []uint64{math.MaxUint64}, "", true},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			page, err := templates["reinhardt/index.html"].Clone()
			if err != nil {
				test.Fatal(err)
			}
			if _, err = page.New("numbers.html").Parse(scenario.source); err != nil {
				test.Fatal(err)
			}
			var output bytes.Buffer
			err = page.ExecuteTemplate(&output, "numbers.html", scenario.value)
			if (err != nil) != scenario.wantError || output.String() != scenario.want {
				test.Fatalf("output=%q, want %q; error=%v, want error=%v", output.String(), scenario.want, err, scenario.wantError)
			}
		})
	}
}
