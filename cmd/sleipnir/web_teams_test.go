package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/teamfile"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The saved teams of the page: the running team as it would be saved, saved for the project, listed, refused when the name is
// taken, checked when pasted, seen by the chat's /team, and removed; a single agent has no team to save.
func TestSavedTeamRoutes(t *testing.T) {
	project := webWorld(t)
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	solo := r.ready("")
	if code, b := r.do("GET", "/api/sessions/"+solo.ID+"/team", nil); code != 409 || !strings.Contains(string(b), "single") {
		t.Errorf("a single agent's team = %d %s, want 409 single", code, b)
	}
	tab := r.startTeam(project, nil)

	var live struct {
		Team teamfile.File
		Dir  string
	}
	r.json("GET", "/api/sessions/"+tab+"/team", nil, 200, &live)
	if len(live.Team.Slots) != 8 || live.Team.Models["manager"] == "" || live.Dir != project {
		t.Fatalf("the running team = %+v in %q", live.Team, live.Dir)
	}
	for _, s := range live.Team.Slots {
		if role, model, ok := strings.Cut(s, "="); !ok || role == "" || model == "" {
			t.Errorf("slot %q names no model: a saved team says what each slot runs on", s)
		}
	}

	put := map[string]any{"team": live.Team, "scope": "project", "dir": project}
	r.json("PUT", "/api/teams/eight", put, 200, nil)
	if code, b := r.do("PUT", "/api/teams/eight", put); code != 409 || !strings.Contains(string(b), "exists") {
		t.Errorf("a second save = %d %s, want 409 exists", code, b)
	}
	put["overwrite"] = true
	r.json("PUT", "/api/teams/eight", put, 200, nil)
	if code, _ := r.do("PUT", "/api/teams/..x", put); code != 422 {
		t.Errorf("a bad name = %d, want 422", code)
	}
	if _, err := os.Stat(filepath.Join(project, ".sleipnir", "teams", "eight.json")); err != nil {
		t.Errorf("the project's team file: %v", err)
	}

	var list struct {
		Teams   []teamfile.Entry
		Project string
		User    string
	}
	r.json("GET", "/api/teams?dir="+project, nil, 200, &list)
	if len(list.Teams) != 1 || list.Teams[0].Name != "eight" || list.Teams[0].Scope != "project" || list.Teams[0].File == nil || len(list.Teams[0].File.Slots) != 8 {
		t.Errorf("the list = %+v", list.Teams)
	}
	if list.Project != filepath.Join(project, ".sleipnir", "teams") || list.User == "" {
		t.Errorf("the directories = %q, %q", list.Project, list.User)
	}

	stranger := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(filepath.Join(stranger, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, b := r.do("PUT", "/api/teams/eight", map[string]any{"team": live.Team, "scope": "project", "dir": stranger}); code != 422 || !strings.Contains(string(b), "not a project this server knows") {
		t.Errorf("a save into a folder the server does not know = %d %s, want 422", code, b)
	}
	r.json("GET", "/api/teams?dir="+stranger, nil, 200, &list)
	if len(list.Teams) != 0 || list.Project != "" {
		t.Errorf("a folder the server does not know is looked at: %+v in %q", list.Teams, list.Project)
	}
	if _, err := os.Stat(filepath.Join(stranger, ".sleipnir")); !os.IsNotExist(err) {
		t.Errorf("the unknown folder was written to: %v", err)
	}

	var checked struct{ Team teamfile.File }
	r.json("POST", "/api/teams/check", map[string]string{"text": `{"slots":["backend=p/b","backend","tester"],"models":{"judge":"p/j"}}`}, 200, &checked)
	if len(checked.Team.Slots) != 3 || checked.Team.Models["judge"] != "p/j" {
		t.Errorf("checked = %+v", checked.Team)
	}
	for _, bad := range []string{`{"slots":["manager=p/m"]}`, `{"slots":[]}`, `{"slots":["backend"],"extra":1}`, `not json`} {
		if code, b := r.do("POST", "/api/teams/check", map[string]string{"text": bad}); code != 422 {
			t.Errorf("check %s = %d %s, want 422", bad, code, b)
		}
	}

	var res wire.CommandResult
	r.json("POST", "/api/sessions/"+tab+"/command", wire.CommandRequest{Line: "/team"}, 200, &res)
	if !strings.Contains(res.Output, "eight") || !strings.Contains(res.Output, "8 workers") {
		t.Errorf("/team = %q", res.Output)
	}

	r.json("DELETE", "/api/teams/eight?scope=project&dir="+project, nil, 200, nil)
	if code, _ := r.do("DELETE", "/api/teams/eight?scope=project&dir="+project, nil); code != 404 {
		t.Errorf("a second remove = %d, want 404", code)
	}
}

// A session the New session dialog starts with a saved team's name starts as that team: --team NAME brings its slots and models.
func TestNewSessionStartsAsTheSavedTeam(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	f := &teamfile.File{Slots: []string{"architect", "scout", "backend", "backend", "frontend", "devops", "tester", "docs", "reviewer"}}
	if _, err := teamfile.Save(project, "", teamfile.ScopeProject, "nine", f, false); err != nil {
		t.Fatal(err)
	}
	nine := 9
	slots, _ := f.Team()
	team := make([]wire.TeamSlot, len(slots))
	for i, s := range slots {
		team[i] = wire.TeamSlot{Role: s.Role, Model: s.Model}
	}
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &nine, Team: team, TeamName: "nine"}, 201, &created)
	r.ready(created.Tab.ID)
	s := r.h.tab(created.Tab.ID).session()
	if s.Swarm == nil || len(s.Team()) != 9 || s.Swarm.MaxWorkers() != 9 {
		t.Fatalf("the session = swarm %v, team %+v; want the saved team of nine", s.Swarm != nil && s.Swarm.MaxWorkers() == 9, s.Team())
	}
}
