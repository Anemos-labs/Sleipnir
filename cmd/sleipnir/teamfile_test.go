package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTeam(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// setTeam gives --team v and splits the values, where the team file is read.
func setTeam(v string) error {
	rf := &roleModelFlag{}
	if err := (&teamFlag{roles: rf}).Set(v); err != nil {
		return err
	}
	_, _, err := rf.split()
	return err
}

// A team file's slots are the team even when no role repeats, and its models are those of the roles that are not workers;
// --role-model values given with it add to them.
func TestTeamFileIsTheTeam(t *testing.T) {
	p := writeTeam(t, t.TempDir(), "eight.json", `{"description":"x","slots":["architect=p/a","scout","backend=p/b","frontend=p/c","devops","tester","docs","reviewer=p/d"],"models":{"manager":"p/m","judge":"p/j"}}`)
	rf := &roleModelFlag{}
	if err := (&teamFlag{roles: rf}).Set(p); err != nil {
		t.Fatal(err)
	}
	if err := rf.Set("compactor=p/k"); err != nil {
		t.Fatal(err)
	}
	models, slots, err := rf.split()
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 8 || slots[0].Role != "architect" || slots[0].Model != "p/a" || slots[1].Model != "" || slots[7].Model != "p/d" {
		t.Errorf("slots = %+v", slots)
	}
	if fmt.Sprint(models) != "map[compactor:p/k judge:p/j manager:p/m]" {
		t.Errorf("models = %v", models)
	}
}

func TestTeamFileIsFoundByName(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o700)
	writeTeam(t, filepath.Join(root, ".sleipnir", "teams"), "duo.json", `{"slots":["backend","backend","frontend","frontend","tester","tester","reviewer","docs"]}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	writeTeam(t, filepath.Join(home, ".sleipnir", "teams"), "mine.json", `{"slots":["backend"]}`)
	t.Chdir(root)
	rf := &roleModelFlag{}
	if err := (&teamFlag{roles: rf}).Set("duo"); err != nil {
		t.Fatal(err)
	}
	if _, slots, err := rf.split(); err != nil || len(slots) != 8 {
		t.Errorf("duo: %v %v", slots, err)
	}
	err := setTeam("nope")
	if err == nil || !strings.Contains(err.Error(), "teams there: duo, mine") {
		t.Errorf("a missing team: %v", err)
	}
	// a team below the minimum is refused like the same --role-model values
	rf = &roleModelFlag{}
	if err := (&teamFlag{roles: rf}).Set("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rf.split(); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Errorf("a one-slot team: %v", err)
	}
}

func TestTeamFileRefusals(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ body, want string }{
		{`{"slots":[]}`, "no slots"},
		{`{"slots":["backend"],"models":{"backend":"p/x"}}`, "goes in its slot"},
		{`{"slots":["manager=p/x"]}`, "not a worker"},
		{`{"slots":["backend"],"extra":1}`, "unknown field"},
		{`not json`, "want {"},
		{`{"slots":["backend"]} {"slots":["frontend"]}`, "more than one JSON value"},
		{`{"slots":["backend"]}}`, "more than one JSON value"},
		{`{"slots":["backend"]}]`, "more than one JSON value"},
	} {
		p := writeTeam(t, dir, "t.json", tc.body)
		err := setTeam(p)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.body, err, tc.want)
		}
	}
	if err := setTeam("../x"); err == nil {
		t.Error("a path-like name with no .json passed as a name")
	}
}

func TestTheExampleTeamFileLoads(t *testing.T) {
	rf := &roleModelFlag{}
	if err := (&teamFlag{roles: rf}).Set(filepath.Join("..", "..", "docs", "examples", "teams", "mixed.json")); err != nil {
		t.Fatal(err)
	}
	if _, slots, err := rf.split(); err != nil || len(slots) != 8 {
		t.Errorf("example: %v %v", slots, err)
	}
}

// A team named by NAME is looked for in the project of --cwd, wherever --cwd stands on the command line, and its values keep
// the place of --team among the --role-model values.
func TestTeamFileIsFoundInTheCwdProject(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o700)
	writeTeam(t, filepath.Join(root, ".sleipnir", "teams"), "duo.json", `{"slots":["backend","backend","frontend","frontend","tester","tester","reviewer","docs"]}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir()) // the command runs outside the project
	for _, args := range [][]string{
		{"--cwd", root, "--role-model", "scout", "--team", "duo", "--role-model", "docs"},
		{"--role-model", "scout", "--team", "duo", "--role-model", "docs", "--cwd", root},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		cwd := fs.String("cwd", "", "")
		rf := &roleModelFlag{}
		fs.Var(rf, "role-model", "")
		fs.Var(&teamFlag{roles: rf, cwd: cwd}, "team", "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		_, slots, err := rf.split()
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(slots) != 10 || slots[0].Role != "scout" || slots[1].Role != "backend" || slots[8].Role != "docs" || slots[9].Role != "docs" {
			t.Errorf("%v: slots = %+v", args, slots)
		}
	}
}
