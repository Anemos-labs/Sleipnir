package teamfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// world is a project (with .git) and a home directory, each empty.
func world(t *testing.T) (project, home string) {
	t.Helper()
	root := t.TempDir()
	project, home = filepath.Join(root, "project"), filepath.Join(root, "home")
	for _, d := range []string{filepath.Join(project, ".git"), home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return project, home
}

func TestParseRefusesWhatIsNoTeam(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"slots":[]}`, "no slots"},
		{`{"slots":["manager=p/m"]}`, "not a worker"},
		{`{"slots":["backend="]}`, "want role=model"},
		{`{"slots":["backend"],"models":{"backend":"p/b"}}`, "goes in its slot"},
		{`{"slots":["backend"],"models":{"judge":" "}}`, "no model"},
		{`{"slots":["backend"],"colour":"red"}`, "unknown field"},
		{`{"slots":["backend"]} {}`, "more than one"},
		{`not json`, "want {"},
		{`{"slots":["backend"],"description":"` + strings.Repeat("x", 501) + `"}`, "description"},
	} {
		if _, err := Parse([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%.60s: %v, want %q", tc.body, err, tc.want)
		}
	}
	f, err := Parse([]byte(`{"description":"d","slots":["backend=p/b","tester"],"models":{"manager":"p/m","mailman":"p/s"}}`))
	if err != nil || len(f.Slots) != 2 || f.Models["mailman"] != "p/s" {
		t.Fatalf("a team: %+v %v", f, err)
	}
}

// A team turned into a file and back is the same team, and its bytes are what is shared: stable, indented, one value.
func TestATeamSurvivesItsFile(t *testing.T) {
	slots := []session.TeamSlot{{Role: "backend", Model: "p/b"}, {Role: "backend"}, {Role: "tester", Model: "p/t"}}
	f := FromTeam(" two backends ", slots, map[string]string{"manager": "p/m", "backend": "p/x", "judge": ""})
	if f.Description != "two backends" || strings.Join(f.Slots, " ") != "backend=p/b backend tester=p/t" || len(f.Models) != 1 || f.Models["manager"] != "p/m" {
		t.Fatalf("file = %+v (a worker role's model goes in its slots, an empty model nowhere)", f)
	}
	b, err := f.Marshal()
	if err != nil || !strings.HasSuffix(string(b), "}\n") || !strings.Contains(string(b), "\n  \"slots\"") {
		t.Fatalf("bytes = %q %v", b, err)
	}
	back, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	got, models := back.Team()
	if len(got) != 3 || got[1] != (session.TeamSlot{Role: "backend"}) || got[2] != slots[2] || models["manager"] != "p/m" {
		t.Errorf("back = %+v %v", got, models)
	}
}

func TestSaveListFindRemove(t *testing.T) {
	project, home := world(t)
	f := &File{Slots: []string{"backend", "tester"}}
	p, err := Save(project, home, ScopeProject, "duo", f, false)
	if err != nil || p != filepath.Join(project, ".sleipnir", "teams", "duo.json") {
		t.Fatalf("save: %q %v", p, err)
	}
	if _, err := Save(project, home, ScopeProject, "duo", f, false); !errors.Is(err, ErrExists) {
		t.Errorf("a second save: %v, want ErrExists", err)
	}
	if _, err := Save(project, home, ScopeProject, "duo", &File{Slots: []string{"docs"}}, true); err != nil {
		t.Errorf("overwrite: %v", err)
	}
	if _, err := Save(project, home, "", "duo", f, false); err != nil {
		t.Errorf("the user's: %v", err)
	}
	if _, err := Save(project, home, ScopeUser, "solo", f, false); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "../x", "a b", ".hidden", "x.json", strings.Repeat("a", 65)} {
		if _, err := Save(project, home, ScopeUser, bad, f, false); err == nil {
			t.Errorf("name %q was taken", bad)
		}
	}
	if _, err := Save(t.TempDir(), home, ScopeProject, "x", f, false); err == nil || !strings.Contains(err.Error(), "not in a project") {
		t.Errorf("a project team outside a project: %v", err)
	}
	if _, err := Save(project, home, "team", "x", f, false); err == nil {
		t.Error("an unknown scope was taken")
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "teams", "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := List(project, home)
	var got []string
	for _, e := range l {
		s := e.Scope + ":" + e.Name
		if e.Shadowed {
			s += "(shadowed)"
		}
		if e.Err != "" {
			s += "(error)"
		}
		got = append(got, s)
	}
	if strings.Join(got, " ") != "project:duo user:broken(error) user:duo(shadowed) user:solo" {
		t.Errorf("list = %v", got)
	}
	if l[0].File == nil || l[0].File.Slots[0] != "docs" {
		t.Errorf("the project's duo = %+v (overwritten)", l[0].File)
	}

	if p, err := Find("duo", project, home); err != nil || !strings.HasPrefix(p, project) {
		t.Errorf("find duo: %q %v, want the project's", p, err)
	}
	if p, err := Find("solo", project, home); err != nil || !strings.HasPrefix(p, home) {
		t.Errorf("find solo: %q %v", p, err)
	}
	if _, err := Find("none", project, home); err == nil || !strings.Contains(err.Error(), "teams there: broken, duo, solo") {
		t.Errorf("find none: %v", err)
	}

	if err := Remove(project, home, ScopeProject, "duo"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(project, home, ScopeProject, "duo"); err == nil {
		t.Error("removed twice")
	}
	if l := List(project, home); len(l) != 3 || l[1].Shadowed {
		t.Errorf("after the remove: %+v", l)
	}
}
