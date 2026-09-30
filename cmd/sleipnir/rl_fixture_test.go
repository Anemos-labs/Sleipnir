package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCLIFixture lays out one fixture whose verifier is a shell script, so the test needs no toolchain beyond sh and git.
func writeCLIFixture(t *testing.T, root, id string) {
	t.Helper()
	files := map[string]string{
		"task.json": `{"id":"` + id + `","kind":"fix","lang":"sh","difficulty":"easy","prompt":"add() in lib.sh must print the sum of its arguments.",` +
			`"verify":"sh check.sh","timeout_s":30,"protected":["check.sh"],"tags":["demo"],"budget":{"steps":10,"requests":20,"wall_s":60}}`,
		"start/lib.sh":    "add() { echo $(($1 - $2)); }\n",
		"hidden/check.sh": ". ./lib.sh\n[ \"$(add 2 3)\" = 5 ] && [ \"$(add 10 4)\" = 14 ]\n",
		"solution/lib.sh": "add() { echo $(($1 + $2)); }\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, id, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRLTaskgenFixtureThroughTheCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, ".sleipnir"))

	fixtures := t.TempDir()
	writeCLIFixture(t, fixtures, "demo-add")
	writeCLIFixture(t, fixtures, "demo-sum")
	if err := os.WriteFile(filepath.Join(fixtures, "README.md"), []byte("not a fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	tasksFile := filepath.Join(out, "tasks.jsonl")
	r := rlRun{t}

	got := r.must(rlTaskgen, "fixture", "--dir", fixtures, "-o", tasksFile, "--tag", "suite")
	if !strings.Contains(got, "wrote 2 tasks") {
		t.Fatalf("taskgen fixture output:\n%s", got)
	}
	for _, want := range []string{"demo-add", "demo-sum"} {
		if _, err := os.Stat(filepath.Join(out, "fixture-repos", want, ".git")); err != nil {
			t.Errorf("no repository for %s: %v", want, err)
		}
	}
	raw, err := os.ReadFile(tasksFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, bad := range []string{out, fixtures, "add 10 4"} {
		if strings.Contains(text, bad) {
			t.Errorf("tasks.jsonl must not contain %q (a machine path, or the hidden test's text)", bad)
		}
	}
	if !strings.Contains(text, `"path":"fixture-repos/demo-add"`) || !strings.Contains(text, `"suite"`) {
		t.Errorf("tasks.jsonl lacks the portable repository path or the --tag:\n%s", text)
	}
	if got := r.must(rlTasks, "validate", tasksFile); !strings.Contains(got, "2 tasks, all valid") {
		t.Fatalf("validate:\n%s", got)
	}

	// -id selects by wildcard.
	only := filepath.Join(out, "one.jsonl")
	if got := r.must(rlTaskgen, "fixture", "--dir", fixtures, "-o", only, "--id", "demo-a*"); !strings.Contains(got, "wrote 1 tasks") {
		t.Fatalf("--id output:\n%s", got)
	}

	// The proof of soundness runs from the directory that holds fixture-repos/, which is how a benchmark run finds them.
	t.Chdir(out)
	work := filepath.Join(t.TempDir(), "work")
	if got := r.must(rlTasks, "check", tasksFile, "--work-dir", work, "--no-net-isolation"); !strings.Contains(got, "2 ok, 0 failed") {
		t.Fatalf("check:\n%s", got)
	}

	// Refusals: nothing matches, no directory.
	if _, _, err := r.do(rlTaskgen, "fixture", "--dir", fixtures, "-o", filepath.Join(out, "none.jsonl"), "--id", "absent"); err == nil {
		t.Error("an --id that matches nothing must be an error")
	}
	if _, _, err := r.do(rlTaskgen, "fixture", "-o", filepath.Join(out, "x.jsonl")); err == nil {
		t.Error("a missing --dir must be an error")
	}
}
