package perm

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// A loop variable that arithmetic or an assigning parameter expansion may change is not bound to the loop's words: the command after
// the change would be judged for a file it does not open.
func TestLoopVariablesChangedByArithmeticOrExpansionAreNotBound(t *testing.T) {
	for _, cmd := range []string{
		`for f in README.md; do : $((f=7)); cat "$f"; done`,
		`for f in README.md; do unset f; : ${f:=/etc/shadow}; cat "$f"; done`,
		`for f in README.md; do : ${f=/etc/shadow}; cat "$f"; done`,
		"for f in README.md; do : <<EOF\n${f:=$HOME/.ssh/id_rsa}\nEOF\ncat \"$f\"; done", // assigned in a heredoc body
	} {
		an := shellparse.Parse(cmd)
		if !an.Parsed {
			t.Fatalf("%q did not parse", cmd)
		}
		for _, s := range bindLoopVars(cmd, an.Commands) {
			if s.Program == "cat" && !strings.Contains(strings.Join(s.Args, " "), "$f") {
				t.Errorf("%q: cat was bound to %v", cmd, s.Args)
			}
		}
	}
	cmd := `for f in README.md go.mod; do cat "$f"; done`
	an := shellparse.Parse(cmd)
	bound := 0
	for _, s := range bindLoopVars(cmd, an.Commands) {
		if s.Program == "cat" && !strings.Contains(strings.Join(s.Args, " "), "$f") {
			bound++
		}
	}
	if bound != 2 {
		t.Errorf("a plain loop was bound %d times, want 2", bound)
	}
}
