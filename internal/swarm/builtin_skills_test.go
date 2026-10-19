package swarm

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/skills/builtin"
)

// TestRolePinsNameOnlyBuiltinSkills keeps the role pins and the built-in skills in step: every skill a pin or the
// constitution tells an agent to load exists, and the skills meant to be used by default are named by at least one.
func TestRolePinsNameOnlyBuiltinSkills(t *testing.T) {
	known := map[string]bool{}
	for _, n := range builtin.Names() {
		known[n] = true
	}
	used := map[string]bool{}
	texts := map[string]string{"constitution": agent.Constitution(agent.ConstitutionOpts{Swarm: true})}
	for n, r := range BuiltinRoles() {
		texts[n] = r.Pin
	}
	for who, text := range texts {
		for _, sentence := range strings.Split(text, ".") {
			i := strings.Index(strings.ToLower(sentence), "load ")
			if i < 0 || !strings.Contains(sentence, "skill") {
				continue
			}
			for _, w := range strings.FieldsFunc(sentence[i:], func(r rune) bool { return !(r == '-' || r >= 'a' && r <= 'z') }) {
				if known[w] {
					used[w] = true
				} else if strings.Contains(w, "-") {
					t.Errorf("%s names a skill %q that is not built in", who, w)
				}
			}
		}
	}
	for _, n := range []string{"engineering-rigor", "diagnose", "tdd", "codebase-design", "spec", "code-review", "prototype", "plain-writing"} {
		if !used[n] {
			t.Errorf("no role pin or the constitution tells an agent to load the built-in skill %q", n)
		}
	}
}
