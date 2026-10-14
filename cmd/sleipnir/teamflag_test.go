package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// --role-model has two uses: a role once with a model is that role's model on the default team; a role repeated, or bare, makes the
// values the team, one slot each, of at least 8. compactor and mailman only take a model. --swarm must agree with a team.
func TestRoleModelNamesModelsOrATeam(t *testing.T) {
	parse := func(vals ...string) (map[string]string, int, error) {
		f := &roleModelFlag{}
		for _, v := range vals {
			if err := f.Set(v); err != nil {
				return nil, 0, err
			}
		}
		m, team, err := f.split()
		return m, len(team), err
	}
	if m, n, err := parse("backend=a/b", "manager=c/d"); err != nil || n != 0 || m["backend"] != "a/b" || m["manager"] != "c/d" {
		t.Errorf("models only: %v %d %v", m, n, err)
	}
	eight := []string{"architect", "scout", "backend", "backend=a/b", "frontend", "devops", "tester", "reviewer", "mailman=x/y"}
	m, n, err := parse(eight...)
	if err != nil || n != 8 || m["mailman"] != "x/y" || m["backend"] != "" {
		t.Errorf("a team of eight with two backends and the mailman's model: %v %d %v", m, n, err)
	}
	if _, _, err := parse("backend", "backend", "tester"); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Errorf("a team of three: %v", err)
	}
	// the manager's model given beside a team is its model, not a ninth slot
	withMgr := append([]string{"manager=m/x"}, eight...)
	if m, n, err := parse(withMgr...); err != nil || n != 8 || m["manager"] != "m/x" || m["mailman"] != "x/y" {
		t.Errorf("a team of eight and the manager's model: %v %d %v", m, n, err)
	}
	if _, _, err := parse("manager"); err == nil || !strings.Contains(err.Error(), "takes a model") {
		t.Errorf("a bare manager: %v", err)
	}
	if _, _, err := parse("manager=a/b", "manager=c/d"); err == nil {
		t.Error("manager twice")
	}
	if _, _, err := parse("mailman"); err == nil || !strings.Contains(err.Error(), "takes a model") {
		t.Errorf("a bare mailman: %v", err)
	}
	if _, _, err := parse("compactor=a/b", "compactor=c/d"); err == nil {
		t.Error("compactor twice")
	}
	if _, _, err := parse("backend="); err == nil {
		t.Error("an empty model")
	}

	f, err := parseChatFlags(append([]string{"--swarm", "9"}, roleModelArgs(eight)...))
	if err == nil || !strings.Contains(err.Error(), "does not match the team") {
		t.Errorf("--swarm 9 against a team of 8: %+v %v", f, err)
	}
	f, err = parseChatFlags(roleModelArgs(eight))
	if err != nil || f.workers != 8 || len(f.options().Team) != 8 || !f.options().Swarm {
		t.Errorf("the team decides the size: %+v %v", f, err)
	}
	if _, err := parseChatFlags([]string{"--swarm", "3"}); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Errorf("--swarm 3: %v", err)
	}
	if f, err := parseChatFlags([]string{"--swarm", "0"}); err != nil || f.options().Swarm {
		t.Errorf("--swarm 0 is a single agent: %v", err)
	}
}

// roleModelArgs puts --role-model before each value.
func roleModelArgs(vals []string) []string {
	var out []string
	for _, v := range vals {
		out = append(out, "--role-model", v)
	}
	return out
}

// The New session request of a custom team with the manager's model keeps that model: argsFor writes it beside the team's slots
// (a worker role's model is its slots'), and the chat's parser reads it back as the manager's model, not a ninth slot.
func TestACustomTeamKeepsTheManagersModelThroughArgsFor(t *testing.T) {
	var team []wire.TeamSlot
	for _, role := range []string{"architect", "scout", "backend", "backend", "frontend", "devops", "tester", "reviewer"} {
		team = append(team, wire.TeamSlot{Role: role, Model: "mock/w"})
	}
	n := 8
	args := argsFor(wire.NewSessionRequest{Cwd: "/p", Model: "mock/m", Swarm: &n, Team: team,
		RoleModels: map[string]string{"manager": "mock/boss", "backend": "mock/dropped"}})
	var vals []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--role-model" {
			vals = append(vals, args[i+1])
		}
	}
	if !slices.Contains(vals, "manager=mock/boss") || slices.Contains(vals, "backend=mock/dropped") || len(vals) != 9 {
		t.Fatalf("--role-model values: %v", vals)
	}
	f, err := parseChatFlags(args)
	if err != nil {
		t.Fatal(err)
	}
	o := f.options()
	if o.RoleModels["manager"] != "mock/boss" || len(o.Team) != 8 || o.Workers != 8 {
		t.Fatalf("options: manager %q, team %d, workers %d", o.RoleModels["manager"], len(o.Team), o.Workers)
	}
}
