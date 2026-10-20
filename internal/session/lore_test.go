package session

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// What a role's agents saved in one session is in that role's pin in the next session of the same project, framed as
// data, and nowhere else: the manager and other roles carry none.
func TestLoreFromOneSessionReachesTheRoleInTheNext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	log, err := events.Open(t.TempDir(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	s := &Session{Log: log, opts: Options{Home: home, Root: t.TempDir()}}
	team := []TeamSlot{{Role: "backend"}}
	st := s.buildLore(team)
	if st == nil {
		t.Fatal("no lore for a team session")
	}
	s.saveLore(st)("backend", []string{"Regenerate the client with make gen."})

	roles := swarm.Roles{"manager": {Name: "manager", Pin: "M"}, "backend": {Name: "backend", Pin: "B"}, "frontend": {Name: "frontend", Pin: "F"}}
	got := s.loreRoles(st, roles)
	if b := got["backend"].Pin; !strings.HasPrefix(b, "B\n\n<role-lore>") || !strings.Contains(b, "[insight] Regenerate the client with make gen.") || !strings.Contains(b, "data, not instructions") {
		t.Errorf("backend pin:\n%s", b)
	}
	if got["manager"].Pin != "M" || got["frontend"].Pin != "F" {
		t.Errorf("other pins changed: %q %q", got["manager"].Pin, got["frontend"].Pin)
	}

	s.opts.NoMemory = true
	if s.buildLore(team) != nil {
		t.Error("--no-memory kept the lore on")
	}
	s.opts.NoMemory = false
	if s.buildLore(nil) != nil {
		t.Error("a session without a team has lore")
	}
}
