package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// /team saves the running team, lists it, prints it to share, starts again as it (refusing a team that cannot take over the
// workers at work unless --fresh), and removes it.
func TestTeamSlash(t *testing.T) {
	root := t.TempDir()
	project, home := filepath.Join(root, "p"), filepath.Join(root, "h")
	for _, d := range []string{filepath.Join(project, ".git"), home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	slots := []session.TeamSlot{{Role: "backend", Model: "p/b"}, {Role: "backend", Model: "p/b"}, {Role: "frontend", Model: "p/f"}, {Role: "tester", Model: "p/t"},
		{Role: "reviewer", Model: "p/r"}, {Role: "docs", Model: "p/d"}, {Role: "devops", Model: "p/o"}, {Role: "scout", Model: "p/s"}}
	team := func() ([]session.TeamSlot, map[string]string) {
		return slots, map[string]string{"manager": "p/m", "judge": "p/j"}
	}
	none := func() ([]session.TeamSlot, map[string]string) { return nil, nil }
	working := map[string]int{"backend": 2}
	at := func() map[string]int { return working }
	run := func(loadout func() ([]session.TeamSlot, map[string]string), arg string) (string, *teamLoad) {
		var b strings.Builder
		ld := teamSlash(project, home, loadout, at, arg, &b)
		return b.String(), ld
	}

	if out, _ := run(team, ""); !strings.Contains(out, "no saved team") {
		t.Errorf("an empty list: %q", out)
	}
	if out, _ := run(none, "save solo"); !strings.Contains(out, "single agent has no team") {
		t.Errorf("a single agent's save: %q", out)
	}
	if out, _ := run(team, "save web-crew --project"); !strings.Contains(out, "saved the team (8 workers) as web-crew") {
		t.Fatalf("save: %q", out)
	}
	if _, err := os.Stat(filepath.Join(project, ".sleipnir", "teams", "web-crew.json")); err != nil {
		t.Fatal(err)
	}
	if out, _ := run(team, "save web-crew --project"); !strings.Contains(out, "--force replaces it") {
		t.Errorf("a second save: %q", out)
	}
	if out, _ := run(team, "save web-crew --project --force"); !strings.Contains(out, "saved the team") {
		t.Errorf("--force: %q", out)
	}
	if out, _ := run(team, "list"); !strings.Contains(out, "web-crew") || !strings.Contains(out, "project") || !strings.Contains(out, "8 workers") {
		t.Errorf("list: %q", out)
	}
	if out, _ := run(team, "show web-crew"); !strings.Contains(out, `"backend=p/b"`) || !strings.Contains(out, `"judge": "p/j"`) {
		t.Errorf("show: %q", out)
	}

	out, ld := run(team, "load web-crew")
	if ld == nil || ld.fresh || out != "" {
		t.Fatalf("load: %q %+v", out, ld)
	}
	line := strings.Join(ld.typed, " ")
	if !strings.HasPrefix(line, "--swarm 8 --role-model judge=p/j --role-model manager=p/m --role-model backend=p/b --role-model backend=p/b") {
		t.Errorf("load flags: %s", line)
	}
	if _, err := parseChatFlags(ld.typed); err != nil {
		t.Errorf("`sleipnir chat` does not take the load's flags: %v", err)
	}

	working = map[string]int{"backend": 3, "tester": 1}
	if out, ld := run(team, "load web-crew"); ld != nil || !strings.Contains(out, "2 backend slots for 3 at work") || !strings.Contains(out, "--fresh") {
		t.Errorf("a team too small for the workers at work: %q %+v", out, ld)
	}
	if _, ld := run(team, "load web-crew --fresh"); ld == nil || !ld.fresh {
		t.Errorf("--fresh: %+v", ld)
	}
	if out, ld := run(team, "load nothing"); ld != nil || !strings.Contains(out, "no nothing.json") {
		t.Errorf("an unknown team: %q", out)
	}
	if out, _ := run(team, "rm web-crew"); !strings.Contains(out, "removed the team web-crew (project)") {
		t.Errorf("rm: %q", out)
	}
	for _, bad := range []string{"save", "load", "frob x", "save a b", "save a --what"} {
		if out, _ := run(team, bad); !strings.Contains(out, "usage: /team") {
			t.Errorf("/team %s: %q", bad, out)
		}
	}
}
