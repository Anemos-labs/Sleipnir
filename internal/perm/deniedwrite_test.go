package perm

import (
	"path/filepath"
	"testing"
)

// DeniedWrite answers for deny rules alone: a question or a built-in protection is not a refusal of what the harness applies.
func TestDeniedWrite(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	guarded := filepath.Join(other, "secret.txt")
	e, err := NewEngine(Config{Mode: ModeDefault, Root: root, Home: t.TempDir(),
		Deny: []string{"Edit(" + guarded + ")", "Edit(./locked/**)", "Read(./private/**)"}, Ask: []string{"Edit(./asked/**)"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		paths  []string
		denied bool
	}{
		{"a file an absolute rule names", []string{guarded}, true},
		{"one denied among others", []string{filepath.Join(root, "a.go"), filepath.Join(root, "locked", "b.go")}, true},
		{"nothing denied", []string{filepath.Join(root, "a.go")}, false},
		{"a read rule is not about writing", []string{filepath.Join(root, "private", "a")}, false},
		{"an ask rule asks nobody here", []string{filepath.Join(root, "asked", "a")}, false},
		{"a protection is not a rule", []string{filepath.Join(root, ".env")}, false},
	} {
		if got := e.DeniedWrite(tc.paths); (got != "") != tc.denied {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
}
