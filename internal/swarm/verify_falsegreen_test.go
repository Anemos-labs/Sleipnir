package swarm

import (
	"strings"
	"testing"
)

// The refusal names the one command that clears it: removing the link alone, since a command that also writes through it is
// judged against what it reaches.
func TestFalseGreenNamesTheCommandThatRemovesTheLink(t *testing.T) {
	r := falseGreen([]string{"web/node_modules -> /src/app/web/node_modules", ".venv -> /src/app/.venv"})
	if r.code != -1 || !r.ran {
		t.Fatalf("verdict = %+v, want a refusal", r)
	}
	for _, want := range []string{"(rm web/node_modules)", "in a command of its own"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("message lacks %q:\n%s", want, r.out)
		}
	}
}
