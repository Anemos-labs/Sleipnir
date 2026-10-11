package taskgen

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// A composite one of whose components changes a UI file gives the UI to the frontend
// role; one whose components change no UI file (a UI test file does not count: it is
// not part of the solution) owns nothing.
func TestCompositeGivesTheUIToTheFrontendRole(t *testing.T) {
	mk := func(id string, files ...string) rl.Task {
		meta, _ := json.Marshal(Meta{Commit: "c1", Files: files})
		return rl.Task{
			ID: id, Kind: rl.TaskFix, Repo: rl.RepoSpec{Path: "/repos/r", Commit: "c1"}, Prompt: "fix " + id,
			Verifier: rl.Verifier{Cmd: "go test ./" + id, Protected: []string{"*_test.go"}}, Meta: meta,
		}
	}
	ui, err := Composite([]rl.Task{mk("api", "api/server.go", "api/server_test.go"), mk("page", "web/app.css")}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ui) != 1 {
		t.Fatalf("got %d composites", len(ui))
	}
	team := ui[0].Team
	if !reflect.DeepEqual(team.Owns, map[string][]string{"frontend": rl.UIGlobs()}) {
		t.Fatalf("owns = %v, want the frontend role to own the UI globs", team.Owns)
	}
	if len(team.Roles) != 0 {
		t.Errorf("roles = %v: an empty list (every built-in role, frontend among them) must stay empty", team.Roles)
	}
	own, err := env.CompileOwnership(team.Owns)
	if err != nil {
		t.Fatal(err)
	}
	if got := own.OwnersOf("web/app.css"); !reflect.DeepEqual(got, []string{"frontend"}) {
		t.Errorf("web/app.css is owned by %v", got)
	}
	if got := own.OwnersOf("api/server.go"); got != nil {
		t.Errorf("api/server.go is owned by %v", got)
	}

	plain, err := Composite([]rl.Task{mk("api", "api/server.go"), mk("cli", "cmd/main.go", "web/app_test.go")}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 1 {
		t.Fatalf("got %d composites", len(plain))
	}
	if plain[0].Team.Owns != nil {
		t.Fatalf("a composite without UI work owns %v", plain[0].Team.Owns)
	}
}

func TestOwnUI(t *testing.T) {
	swarm := func(roles ...string) rl.Task {
		return rl.Task{Team: rl.Team{Mode: "swarm", Roles: roles, Owns: map[string][]string{"backend": {"api/"}}}}
	}

	// Roles listed without frontend: it joins them, and other owners stay.
	tk := swarm("scout", "backend")
	if !OwnUI(&tk, []string{"src/App.tsx", "go.mod"}) {
		t.Fatal("a change to src/App.tsx is UI work")
	}
	if want := []string{"scout", "backend", "frontend"}; !reflect.DeepEqual(tk.Team.Roles, want) {
		t.Errorf("roles = %v, want %v", tk.Team.Roles, want)
	}
	if !reflect.DeepEqual(tk.Team.Owns["backend"], []string{"api/"}) || !reflect.DeepEqual(tk.Team.Owns["frontend"], rl.UIGlobs()) {
		t.Errorf("owns = %v", tk.Team.Owns)
	}
	if err := env.ValidateTask(rl.Task{ID: "t", Kind: rl.TaskSwarm, Repo: rl.RepoSpec{Path: "/r", Commit: "c"}, Prompt: "p",
		Team: tk.Team, Verifier: rl.Verifier{Cmd: "true"}}); err != nil {
		t.Errorf("the owned task does not validate: %v", err)
	}

	// Frontend already among the roles: not added twice.
	tk = swarm("frontend", "backend")
	OwnUI(&tk, []string{"web/index.html"})
	if want := []string{"frontend", "backend"}; !reflect.DeepEqual(tk.Team.Roles, want) {
		t.Errorf("roles = %v, want %v", tk.Team.Roles, want)
	}

	// No UI work in the solution, or not a team: the task is left alone.
	for name, c := range map[string]struct {
		task  rl.Task
		files []string
	}{
		"backend only":  {swarm("backend"), []string{"api/server.go"}},
		"UI tests only": {swarm("backend"), []string{"api/server.go", "web/app_test.go"}},
		"single agent":  {rl.Task{Team: rl.Team{Mode: "single"}}, []string{"web/app.css"}},
	} {
		before := c.task.Team
		if OwnUI(&c.task, c.files) || !reflect.DeepEqual(c.task.Team, before) {
			t.Errorf("%s: changed the team to %+v", name, c.task.Team)
		}
	}
}
