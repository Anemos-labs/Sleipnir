package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// args is a tolerant view of one tool call's JSON object.
//
// Models are imperfect at emitting arguments: numbers arrive as "30", flags as
// "true", stray keys sneak in. Bouncing those calls costs a whole request (the
// scarce resource), so the decoder coerces what is unambiguous, ignores what it
// does not know, and reserves errors for things that would change meaning. Every
// error names the field so the model can fix the call in one shot.
type args struct {
	m map[string]json.RawMessage
}

func parseArgs(raw json.RawMessage) (*args, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return &args{m: map[string]json.RawMessage{}}, nil
	}
	if raw[0] != '{' {
		return nil, fmt.Errorf("input must be a JSON object such as {\"command\": \"ls\"}")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("input is not valid JSON: %v", shortErr(err))
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return &args{m: m}, nil
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// present returns the raw value of name unless it is absent or null.
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

// unknownKeys lists keys that are not in known, sorted, so a missing-field
// error can point at a probable typo ("cmd" for "command").
func (a *args) unknownKeys(known ...string) []string {
	var out []string
	for k := range a.m {
		found := false
		for _, n := range known {
			if k == n {
				found = true
				break
			}
		}
		if !found {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (a *args) missing(name string, known ...string) error {
	if u := a.unknownKeys(known...); len(u) > 0 {
		quoted := make([]string, len(u))
		for i, k := range u {
			quoted[i] = strconv.Quote(k)
			if len(quoted[i]) > 40 {
				quoted[i] = quoted[i][:40] + "…"
			}
		}
		return fmt.Errorf("missing required field %q (got unknown field(s) %s)", name, strings.Join(quoted, ", "))
	}
	return fmt.Errorf("missing required field %q", name)
}

// str reads an optional string field.
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

// num reads an optional number, accepting a numeric string.
func (a *args) num(name string) (float64, bool, error) {
	v, ok := a.present(name)
	if !ok {
		return 0, false, nil
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		var s string
		if json.Unmarshal(v, &s) == nil {
			if p, perr := strconv.ParseFloat(strings.TrimSpace(s), 64); perr == nil {
				f = p
				goto check
			}
		}
		return 0, true, fmt.Errorf("field %q must be a number, got %s", name, describe(v))
	}
check:
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, true, fmt.Errorf("field %q must be a finite number", name)
	}
	return f, true, nil
}

// boolean reads an optional flag, accepting "true"/"false" strings and 0/1.
func (a *args) boolean(name string) (bool, bool, error) {
	v, ok := a.present(name)
	if !ok {
		return false, false, nil
	}
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		return b, true, nil
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1":
			return true, true, nil
		case "false", "no", "0", "":
			return false, true, nil
		}
	}
	var f float64
	if json.Unmarshal(v, &f) == nil && (f == 0 || f == 1) {
		return f == 1, true, nil
	}
	return false, true, fmt.Errorf("field %q must be true or false, got %s", name, describe(v))
}

// describe renders a raw JSON value for an error message without echoing
// arbitrarily large input back to the model.
func describe(v json.RawMessage) string {
	s := string(bytes.TrimSpace(v))
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}
