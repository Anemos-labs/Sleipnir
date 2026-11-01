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

// TestRolesShareOneContract checks that the roles that build parts which talk to each other are told to build them against one
// machine-readable contract file and to check the assembled result end to end.
func TestRolesShareOneContract(t *testing.T) {
	rs := BuiltinRoles()
	for role, want := range map[string]string{
		"manager":   "one machine-readable contract file",
		"architect": "one machine-readable contract file",
		"backend":   "checks their responses against the file",
		"frontend":  "only through types or a client taken from it",
		"tester":    "starts the real built server",
	} {
		if !strings.Contains(rs[role].Pin, want) {
			t.Errorf("%s pin lacks %q", role, want)
		}
	}
}
