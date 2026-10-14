package swarm

import (
	"strings"
	"testing"
)

// The manager and the architect give a user interface to the frontend role and the unit tests beside code to its implementer, and
// the tester works in test directories of its own. In a 32-worker run the manager gave the operator dashboard to a backend worker
// while eight frontend workers stayed idle, and the tester pool re-created the same test tasks three times because their scopes
// reached into packages the backend leased.
func TestThePinsRouteTheUIToTheFrontendAndTestsToTheirOwners(t *testing.T) {
	rs := BuiltinRoles()
	for role, wants := range map[string][]string{
		"manager":   {"is the frontend's whenever the team has one", "written by the implementer of that code", "test directories of their own"},
		"architect": {"is the frontend's whenever the team has one", "implementers own the unit tests", "tester owns test directories"},
		"tester":    {"test directories of your own", "belong to its implementer"},
	} {
		for _, w := range wants {
			if !strings.Contains(rs[role].Pin, w) {
				t.Errorf("the %s pin does not say %q", role, w)
			}
		}
	}
}
