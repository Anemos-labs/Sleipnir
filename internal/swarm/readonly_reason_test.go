package swarm

import (
	"strings"
	"testing"
)

// A read-only role's refusal says what the role does instead. The plan profile's own words ("Present a plan instead of acting") sent
// a reviewer in a trial to write a plan when it had asked to run the tests.
func TestAReadOnlyRolesRefusalSaysWhatItDoesInstead(t *testing.T) {
	got := readOnlyRoleReason("reviewer", "role reviewer: plan mode (read-only): touch x is not a read-only command. Present a plan instead of acting")
	for _, want := range []string{"the reviewer role is read-only: touch x is not a read-only command", "node --test", "reports what should change"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "Present a plan") {
		t.Errorf("still tells the reviewer to present a plan: %q", got)
	}
	for _, other := range []string{
		"plan mode (read-only): touch x is not a read-only command. Present a plan instead of acting", // the session is planning
		"role reviewer: denied by rule Bash(rm:*)",
	} {
		if got := readOnlyRoleReason("reviewer", other); got != other {
			t.Errorf("%q was rewritten to %q", other, got)
		}
	}
}
