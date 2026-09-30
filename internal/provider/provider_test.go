package provider

import (
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	const max = 90 * time.Second
	for _, c := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"soon", 0, false},
		{"0", 0, true},
		{"-5", 0, true},
		{"3", 3 * time.Second, true},
		{" 3 ", 3 * time.Second, true},
		{"1.5", 1500 * time.Millisecond, true},
		{"90", max, true},
		{"3000000000", max, true},
		{"9223372036854775807", max, true},
		{"99999999999999999999", max, true},
		{"NaN", 0, false},
		{"Inf", max, true},
		{now.Add(30 * time.Second).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), 30 * time.Second, true},
		{now.Add(-time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), 0, true},
		{now.Add(48 * time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), max, true},
	} {
		d, ok := ParseRetryAfter(c.in, now, max)
		if d != c.want || ok != c.ok {
			t.Errorf("ParseRetryAfter(%q) = %v, %v; want %v, %v", c.in, d, ok, c.want, c.ok)
		}
	}
	if d, _ := ParseRetryAfter("1000000", now, 0); d != DefaultMaxRetryAfter {
		t.Errorf("the default cap: %v", d)
	}
}
