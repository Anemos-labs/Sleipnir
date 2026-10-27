package settings

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/plugin"
)

// What a confirmation covers changes with everything the confirmed action runs or saves.
func TestNeedScopesBindWhatRuns(t *testing.T) {
	n := plugin.Need{ID: "rg", Commands: []string{"rg"}, VersionArgs: []string{"--version"}}
	base := locateScope(n, "/usr/bin/rg")
	m := n
	m.VersionArgs = []string{"--pre", "sh"}
	if locateScope(m, "/usr/bin/rg") == base {
		t.Error("other version arguments share the locate scope")
	}
	m = n
	m.Commands = []string{"rg", "git"}
	if locateScope(m, "/usr/bin/rg") == base {
		t.Error("other launcher names share the locate scope")
	}

	srv := plugin.Need{ID: "s", MCP: &plugin.NeedMCP{Name: "s", Command: "run", Args: []string{"a b"}}}
	ms := plugin.MethodStatus{ID: "add", Connect: `adds the "s" tool server (run a b) to your configuration`}
	other := srv
	other.MCP = &plugin.NeedMCP{Name: "s", Command: "run", Args: []string{"a", "b"}}
	if installScope(srv, ms) == installScope(other, ms) {
		t.Error("two server entries that read the same in words share the install scope")
	}
}

// A setup plan's scope keeps where each step and each argument ends: an argument holding the bytes a joined form would use as a
// delimiter cannot make one plan read as another, with a step added.
func TestInstallScopeKeepsSetupStepBoundaries(t *testing.T) {
	n := plugin.Need{ID: "x"}
	one := plugin.MethodStatus{ID: "m", Setup: [][]string{{"echo", "a\x01curl", "evil"}}}
	two := plugin.MethodStatus{ID: "m", Setup: [][]string{{"echo", "a"}, {"curl", "evil"}}}
	if installScope(n, one) == installScope(n, two) {
		t.Error("one setup step and two steps share the install scope")
	}
	three := plugin.MethodStatus{ID: "m", Setup: [][]string{{"echo", "a\x00curl"}}}
	four := plugin.MethodStatus{ID: "m", Setup: [][]string{{"echo", "a", "curl"}}}
	if installScope(n, three) == installScope(n, four) {
		t.Error("one argument and two share the install scope")
	}
}
