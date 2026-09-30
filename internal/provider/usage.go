package provider

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Usage and cost figures arrive from the endpoint, and the dollar budgets of an
// agent and of a swarm are computed from them: "spend >= limit" is false for NaN,
// and a negative token count or cost subtracts from the total. Adapters therefore
// normalise what they read before anyone adds it up.

// MaxUsageTokens bounds one token counter of one response. It is far beyond any
// context window, and small enough that summing counters over millions of
// responses cannot overflow an int.
const MaxUsageTokens = math.MaxInt32

// MaxRequestCostUSD bounds the exact charge a gateway may report for one request.
// No single call costs a thousand dollars; a larger figure is a unit mix-up or an
// attack, and the price computed from the token counts is used instead.
const MaxRequestCostUSD = 1000.0

// ClampTokens returns n limited to 0..MaxUsageTokens: a negative counter is
// reported as zero, an absurd one is capped.
func ClampTokens(n int) int {
	switch {
	case n < 0:
		return 0
	case n > MaxUsageTokens:
		return MaxUsageTokens
	}
	return n
}

// ValidCost returns c when it can be believed as the exact charge of one request
// (finite, not negative, at most MaxRequestCostUSD) and nil otherwise, which tells
// the caller to price the request from its token counts. Zero is believed: free
// models exist.
func ValidCost(c *float64) *float64 {
	if c == nil {
		return nil
	}
	v := *c
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > MaxRequestCostUSD {
		return nil
	}
	return &v
}

// ParseTokenCount reads a token counter from a JSON value leniently: any number
// (fractions are cut, values beyond the int range saturate) or a numeric string.
// Anything else, a negative number and NaN included, is 0. It never fails, which
// is the point: a usage report whose counter cannot be decoded must not be dropped
// whole, because a dropped report reads as "free". Callers still ClampTokens the
// result.
func ParseTokenCount(b []byte) int {
	s := string(bytes.TrimSpace(b))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) { // ErrRange still yields ±Inf: saturate
		return 0
	}
	switch {
	case math.IsNaN(f) || f <= 0:
		return 0
	case f >= MaxUsageTokens:
		return MaxUsageTokens
	}
	return int(f)
}

// ParseCost reads a usage.cost member: nil unless it is a finite number (or a
// numeric string). Range and sign are judged by ValidCost.
func ParseCost(raw json.RawMessage) *float64 {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}
