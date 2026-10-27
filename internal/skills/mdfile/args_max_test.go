package mdfile

import (
	"strings"
	"testing"
)

// A template that repeats a placeholder stops at the bound, before the output grows past it; within the bound it is Substitute.
func TestSubstituteMaxStopsAtTheBound(t *testing.T) {
	tmpl := strings.Repeat("$1 $ARGUMENTS ", 1000)
	arg := strings.Repeat("x", MaxArgsBytes-1)
	if _, used, err := SubstituteMax(tmpl, arg, ArgsText, 1<<20); err == nil || !used {
		t.Fatalf("err = %v, used = %v", err, used)
	}
	want, _ := Substitute("a $1 b $$1 $ c", "w", ArgsShell)
	if got, used, err := SubstituteMax("a $1 b $$1 $ c", "w", ArgsShell, len(want)); err != nil || !used || got != want {
		t.Fatalf("%q, %v, %v; want %q", got, used, err, want)
	}
	if _, _, err := SubstituteMax("a $1 b $$1 $ c", "w", ArgsShell, len(want)-1); err == nil {
		t.Fatal("one byte over the bound was allowed")
	}
	if _, _, err := SubstituteMax("no placeholder", "w", ArgsText, 3); err == nil {
		t.Fatal("a template over the bound was allowed")
	}
}
