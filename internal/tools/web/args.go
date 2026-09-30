package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// args is a tolerant view of one tool call's JSON object: numbers may arrive as
// numeric strings, stray keys are ignored, and every error names the field so
// the model can repair the call in one shot instead of guessing.
type args struct {
	m map[string]json.RawMessage
}

func parseArgs(raw json.RawMessage) (*args, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return &args{m: map[string]json.RawMessage{}}, nil
	}
	if raw[0] != '{' {
		return nil, fmt.Errorf("input must be a JSON object such as {\"url\": \"https://example.com\"}")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		msg := err.Error()
		if len(msg) > 160 {
			msg = msg[:160] + "…"
		}
		return nil, fmt.Errorf("input is not valid JSON: %s", msg)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return &args{m: m}, nil
}

func (a *args) present(name string) (json.RawMessage, bool) {
	v, ok := a.m[name]
	if !ok {
		return nil, false
	}
	if t := bytes.TrimSpace(v); len(t) == 0 || string(t) == "null" {
		return nil, false
	}
	return v, true
}

func (a *args) missing(name string, known ...string) error {
	var unknown []string
	for k := range a.m {
		found := false
		for _, n := range known {
			if k == n {
				found = true
				break
			}
		}
		if !found {
			q := strconv.Quote(k)
			if len(q) > 40 {
				q = q[:40] + "…"
			}
			unknown = append(unknown, q)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		return fmt.Errorf("missing required field %q (got unknown field(s) %s)", name, strings.Join(unknown, ", "))
	}
	return fmt.Errorf("missing required field %q", name)
}

func (a *args) str(name string) (string, bool, error) {
	v, ok := a.present(name)
	if !ok {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", true, fmt.Errorf("field %q must be a string, got %s", name, describe(v))
	}
	return s, true, nil
}

// integer reads an optional whole number, accepting numeric strings and
// truncating fractions ("20000.0" and 2e4 are both fine).
func (a *args) integer(name string) (int64, bool, error) {
	v, ok := a.present(name)
	if !ok {
		return 0, false, nil
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		var s string
		p, perr := 0.0, error(nil)
		if json.Unmarshal(v, &s) == nil {
			p, perr = strconv.ParseFloat(strings.TrimSpace(s), 64)
		} else {
			perr = err
		}
		if perr != nil {
			return 0, true, fmt.Errorf("field %q must be an integer, got %s", name, describe(v))
		}
		f = p
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, true, fmt.Errorf("field %q must be a finite integer", name)
	}
	switch {
	case f >= 9e18:
		return math.MaxInt64 / 2, true, nil
	case f <= -9e18:
		return math.MinInt64 / 2, true, nil
	}
	return int64(f), true, nil
}

func describe(v json.RawMessage) string {
	s := string(bytes.TrimSpace(v))
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}
