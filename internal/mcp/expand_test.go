package mcp

import (
	"errors"
	"strings"
	"testing"
)

func TestExpandString(t *testing.T) {
	env := map[string]string{"A": "alpha", "B": "beta", "EMPTY": "", "PATHY": "/usr/bin:/bin", "DOLLAR": "$A"}
	tests := []struct {
		in   string
		want string
		err  string
	}{
		{"plain", "plain", ""},
		{"", "", ""},
		{"${A}", "alpha", ""},
		{"x${A}y${B}z", "xalphaybetaz", ""},
		{"${EMPTY}", "", ""},
		{"${MISSING:-def}", "def", ""},
		{"${EMPTY:-def}", "def", ""},
		{"${A:-def}", "alpha", ""},
		{"${MISSING:-}", "", ""},
		{"${MISSING:-a b c}", "a b c", ""},
		{"${MISSING:-${A}}", "alpha", ""},
		{"${MISSING:-${ALSO:-deep}}", "deep", ""},
		{"${MISSING:-x${A}y}", "xalphay", ""},
		{"$A", "$A", ""},
		{"$", "$", ""},
		{"cost: $5", "cost: $5", ""},
		{"$${A}", "${A}", ""},
		{"a$${b}c${A}", "a${b}calpha", ""},
		{"${DOLLAR}", "$A", ""}, // values are not expanded again
		{"${PATHY}", "/usr/bin:/bin", ""},
		{"${MISSING}", "", "variable MISSING is not set"},
		{"${MISSING:+x}", "", "invalid variable name"},
		{"${1BAD}", "", "invalid variable name"},
		{"${}", "", "invalid variable name"},
		{"${A", "", "unterminated"},
		{"${A:-${B}", "", "unterminated"},
		{"${A-b}", "", "invalid variable name"},
		{"${ A }", "", "invalid variable name"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := expandString(tt.in, env)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpandUnsetIsTyped(t *testing.T) {
	_, err := expandString("${NOPE}", nil)
	var u *UnsetVarError
	if !errors.As(err, &u) || u.Name != "NOPE" {
		t.Fatalf("got %v", err)
	}
}

func TestExpandBounds(t *testing.T) {
	// Deep nesting of defaults is refused, not recursed into without limit.
	deep := strings.Repeat("${A:-", 50) + "x" + strings.Repeat("}", 50)
	if _, err := expandString(deep, nil); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Errorf("deep nesting: %v", err)
	}
	// A huge expansion is refused.
	big := strings.Repeat("y", 40<<10)
	if _, err := expandString("${V}${V}", map[string]string{"V": big}); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("huge expansion: %v", err)
	}
	// A value containing "${" is inserted as text.
	got, err := expandString("${V}", map[string]string{"V": "${NOT_EXPANDED}"})
	if err != nil || got != "${NOT_EXPANDED}" {
		t.Errorf("values must not be re-expanded: %q %v", got, err)
	}
}

func TestEnvMap(t *testing.T) {
	m := EnvMap([]string{"A=1", "B=", "C=x=y", "=bad", "noequals"})
	if m["A"] != "1" || m["B"] != "" || m["C"] != "x=y" || len(m) != 3 {
		t.Errorf("EnvMap = %v", m)
	}
}

func FuzzExpandString(f *testing.F) {
	for _, s := range []string{"${A}", "${A:-${B}}", "$${", "${", "}", "${A:-", "$$$${{}}", "${A:-x}${B}"} {
		f.Add(s)
	}
	env := map[string]string{"A": "a", "B": "b"}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := expandString(s, env)
		if err == nil && len(out) > maxExpandedLen {
			t.Fatalf("unbounded output: %d bytes", len(out))
		}
		_ = refsIn(s)
	})
}
