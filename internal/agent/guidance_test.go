package agent_test

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
)

// The constitution teaches the habits whose absence made real runs fail: the user's names kept exactly, requirements checked before
// finishing, tests that actually ran (a missing runner set up in a project virtual environment, never system-wide), no weakened tests,
// and a report that shows its evidence. Every agent reads it, alone or in a team.
func TestConstitutionTeachesSpecificationAndVerification(t *testing.T) {
	for _, swarm := range []bool{false, true} {
		c := agent.Constitution(agent.ConstitutionOpts{Swarm: swarm})
		for _, want := range []string{
			"# The user's words are the specification",
			"exactly as the user wrote it",
			"check each one before you finish",
			"# Verify your work",
			"python3 -m venv .venv",
			".venv/bin/python -m pytest",
			"Never use sudo, pip install --user",
			"Never make a test pass by weakening it",
			"never describe tests as passing when they did not run",
			"# Examples",
			"# Finishing",
		} {
			if !strings.Contains(c, want) {
				t.Errorf("swarm=%v: the constitution does not say %q", swarm, want)
			}
		}
		if swarm && !strings.Contains(c, "If the tests could not run, say so there") {
			t.Error("a team's constitution does not make task done's result line carry the verification")
		}
	}
}

// A request to build something is a request for a product: the constitution makes production quality the default (complete,
// secure, designed, fast at scale, looked at), so nobody has to ask for it, while changes inside an existing project stay focused.
func TestConstitutionImpliesProductionQuality(t *testing.T) {
	for _, swarm := range []bool{false, true} {
		c := agent.Constitution(agent.ConstitutionOpts{Swarm: swarm})
		for _, want := range []string{
			"# Build to ship",
			"Production quality is implied",
			"Secure by default",
			"professionally designed product",
			"Fast at realistic scale",
			"render every screen in a real browser",
			"its stack and conventions win",
			"keep it that",
		} {
			if !strings.Contains(c, want) {
				t.Errorf("swarm=%v: the constitution does not say %q", swarm, want)
			}
		}
		if strings.Contains(c, "no more and no less") {
			t.Errorf("swarm=%v: the constitution still caps a build at the literal request", swarm)
		}
	}
}
