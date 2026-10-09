package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// integrityTasks materialises one fixture through `rl taskgen fixture` with the given files (merged over the default
// shell fixture), lets mutate adjust the task, and returns the tasks file. The working directory becomes the output
// directory, where the task's repository path resolves.
func integrityTasks(t *testing.T, files map[string]string, mutate func(*rl.Task)) string {
	t.Helper()
	fixtures, out := t.TempDir(), t.TempDir()
	writeCLIFixture(t, fixtures, "demo")
	for name, body := range files {
		p := filepath.Join(fixtures, "demo", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(out, "tasks.jsonl")
	(rlRun{t}).must(rlTaskgen, "fixture", "--dir", fixtures, "-o", file)
	tasks, err := env.LoadTasks(file)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&tasks[0])
	}
	if err := env.WriteTasks(file, tasks); err != nil {
		t.Fatal(err)
	}
	t.Chdir(out)
	return file
}

// checkArgs are the flags every check below runs with.
func checkArgs(t *testing.T, file string, extra ...string) []string {
	return append([]string{"check", file, "--work-dir", filepath.Join(t.TempDir(), "work"), "--no-net-isolation"}, extra...)
}

// TestRLTasksCheckSkipsTasksWhoseToolsAreMissing: a task that requires a tool the commands' PATH lacks is skipped with its
// reason and counted, not failed, and the check still succeeds.
func TestRLTasksCheckSkipsTasksWhoseToolsAreMissing(t *testing.T) {
	file := integrityTasks(t, nil, func(task *rl.Task) { task.Requires = []string{"ruby"} })
	report := filepath.Join(t.TempDir(), "report.json")
	stdout, stderr, err := (rlRun{t}).do(rlTasks, checkArgs(t, file, "--set-env", "PATH="+t.TempDir(), "--report", report)...)
	if err != nil || !strings.Contains(stdout, "demo: skipped: missing ruby") || !strings.Contains(stdout, "1 skipped (1 for missing tools)") {
		t.Fatalf("err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	b, err := os.ReadFile(report)
	if err != nil || !strings.Contains(string(b), `"missing": [`) {
		t.Fatalf("report: %v %s", err, b)
	}
	if err := nothingCompleted("rl rollout", &env.Summary{Rollouts: 2, Skipped: 2}); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("a run whose every rollout was skipped must say so: %v", err)
	} else if _, temp := err.(*exitError); temp {
		t.Errorf("rerunning does not help a run that lacks tools: %v", err)
	}
}

// TestRLTasksCheckMutantsReportsWeakVerifiers: with --mutants a verifier that still passes when one solution file is left
// at its start content is reported weak, by file, as a warning: the check succeeds.
func TestRLTasksCheckMutantsReportsWeakVerifiers(t *testing.T) {
	file := integrityTasks(t, map[string]string{"start/value": "wrong\n", "solution/value": "right\n"}, nil)
	report := filepath.Join(t.TempDir(), "report.json")
	stdout, stderr, err := (rlRun{t}).do(rlTasks, checkArgs(t, file, "--mutants", "--report", report)...)
	if err != nil || !strings.Contains(stdout, "demo: ok (WEAK: the verifier still passes with value left at the start)") ||
		!strings.Contains(stdout, "1 weak") {
		t.Fatalf("err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if b, err := os.ReadFile(report); err != nil || !strings.Contains(string(b), `"weak": [`) {
		t.Fatalf("report: %v %s", err, b)
	}
	// Without the flag nothing extra runs and nothing is called weak.
	if stdout, _, err := (rlRun{t}).do(rlTasks, checkArgs(t, file)...); err != nil || strings.Contains(stdout, "WEAK") || strings.Contains(stdout, "weak") {
		t.Fatalf("without --mutants: %v\n%s", err, stdout)
	}
}

// TestRLTasksCheckCalibratesTheStartScore: a json-score verifier that gives the untouched start partial credit fails the
// check until the start's score is recorded, and --calibrate records it.
func TestRLTasksCheckCalibratesTheStartScore(t *testing.T) {
	file := integrityTasks(t, map[string]string{
		"hidden/check.sh": ". ./lib.sh\nif [ \"$(add 2 3)\" = 5 ]; then echo '{\"score\": 1}'; else echo '{\"score\": 0.5}'; fi\n",
	}, func(task *rl.Task) { task.Verifier.Pass = "json-score" })

	stdout, stderr, err := (rlRun{t}).do(rlTasks, checkArgs(t, file)...)
	if err == nil || !strings.Contains(stdout, "FAIL") || !strings.Contains(stdout, "baseline_score") {
		t.Fatalf("an unrecorded start score must fail the check: err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	calibrated := filepath.Join(filepath.Dir(file), "calibrated.jsonl")
	stdout, stderr, err = (rlRun{t}).do(rlTasks, checkArgs(t, file, "--calibrate", calibrated)...)
	if err != nil || !strings.Contains(stdout, "recalibrated") {
		t.Fatalf("--calibrate: err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	tasks, err := env.LoadTasks(calibrated)
	if err != nil || len(tasks) != 1 || tasks[0].Verifier.BaselineScore != 0.5 {
		t.Fatalf("calibrated tasks: %v %+v", err, tasks)
	}
	if stdout, stderr, err := (rlRun{t}).do(rlTasks, checkArgs(t, calibrated)...); err != nil || !strings.Contains(stdout, "ok (start scores 0.5)") {
		t.Fatalf("the calibrated task is sound: err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
}
