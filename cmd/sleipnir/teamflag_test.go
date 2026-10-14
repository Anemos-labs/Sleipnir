package main

import (
	"strings"
	"testing"
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
