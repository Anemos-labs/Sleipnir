package provider

import (
	"encoding/json"
	"math"
	"testing"
)

func TestClampTokens(t *testing.T) {
	for in, want := range map[int]int{
		0: 0, 1: 1, 200_000: 200_000, MaxUsageTokens: MaxUsageTokens,
		-1: 0, -5_000_000: 0, math.MinInt64: 0,
		MaxUsageTokens + 1: MaxUsageTokens, math.MaxInt64: MaxUsageTokens,
	} {
		if got := ClampTokens(in); got != want {
			t.Errorf("ClampTokens(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestValidCost(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for name, tc := range map[string]struct {
		in   *float64
		want *float64
	}{
		"nil":            {nil, nil},
		"zero (free)":    {f(0), f(0)},
		"typical":        {f(0.0123), f(0.0123)},
		"the ceiling":    {f(MaxRequestCostUSD), f(MaxRequestCostUSD)},
		"negative":       {f(-1e6), nil},
		"tiny negative":  {f(-1e-12), nil},
		"negative zero":  {f(math.Copysign(0, -1)), f(0)}, // -0 compares equal to 0
		"NaN":            {f(math.NaN()), nil},
		"+Inf":           {f(math.Inf(1)), nil},
		"-Inf":           {f(math.Inf(-1)), nil},
		"past the limit": {f(MaxRequestCostUSD * 1.001), nil},
		"astronomical":   {f(1e300), nil},
	} {
		got := ValidCost(tc.in)
		switch {
		case (got == nil) != (tc.want == nil):
			t.Errorf("%s: ValidCost = %v, want %v", name, got, tc.want)
		case got != nil && *got != *tc.want:
			t.Errorf("%s: ValidCost = %v, want %v", name, *got, *tc.want)
		}
	}
	// The result is a copy: callers cannot reach back into the parsed frame.
	in := f(1)
	out := ValidCost(in)
	*in = 99
	if *out != 1 {
		t.Error("ValidCost aliased its input")
	}
}

func TestParseTokenCount(t *testing.T) {
	for in, want := range map[string]int{
		`0`: 0, `1`: 1, `1234`: 1234, ` 42 `: 42,
		`-1`: 0, `-5000000`: 0, `-0`: 0,
		`12.9`: 12, `0.5`: 0, `1e3`: 1000, `2.5e6`: 2_500_000,
		`2147483647`: MaxUsageTokens, `2147483648`: MaxUsageTokens, `9223372036854775807`: MaxUsageTokens,
		`99999999999999999999999`: MaxUsageTokens, `1e30`: MaxUsageTokens, `1e999`: MaxUsageTokens, `-1e999`: 0,
		`"57"`: 57, `" 58 "`: 58, `"-9"`: 0, `"1e30"`: MaxUsageTokens,
		`null`: 0, `true`: 0, `false`: 0, `"abc"`: 0, `""`: 0, ``: 0, `{}`: 0, `[]`: 0, `[5]`: 0, `"NaN"`: 0, `NaN`: 0,
		`Infinity`: MaxUsageTokens, `"Inf"`: MaxUsageTokens, `-Infinity`: 0,
	} {
		if got := ParseTokenCount([]byte(in)); got != want {
			t.Errorf("ParseTokenCount(%s) = %d, want %d", in, got, want)
		}
	}
}

func TestParseCost(t *testing.T) {
	for in, want := range map[string]float64{
		`0.0123`: 0.0123, `0`: 0, `1e-6`: 1e-6, `"0.5"`: 0.5, ` 3 `: 3, `-1e6`: -1e6, `1e300`: 1e300,
	} {
		got := ParseCost(json.RawMessage(in))
		if got == nil || *got != want {
			t.Errorf("ParseCost(%s) = %v, want %v", in, got, want)
		}
	}
	// What is not a finite number is nil: the request is priced from its tokens.
	for _, in := range []string{``, `null`, `"abc"`, `NaN`, `"NaN"`, `Infinity`, `"-Inf"`, `1e999`, `-1e999`, `{}`, `[]`, `true`} {
		if got := ParseCost(json.RawMessage(in)); got != nil {
			t.Errorf("ParseCost(%s) = %v, want nil", in, *got)
		}
	}
}
