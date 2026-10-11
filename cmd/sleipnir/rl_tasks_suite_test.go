package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/env/taskgen"
)

// TestRLTasksCheckAcceptsASuiteTask: a task whose verifier is taskgen's whole-suite scorer, with units chosen from
// recorded runs, is sound for `rl tasks check`: the start scores 0 and the reference solution exactly 1.
func TestRLTasksCheckAcceptsASuiteTask(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test through the scorer several times")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the suite scorer needs POSIX process groups")
	}
	for _, tool := range []string{"git", "go", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("go.mod", "module example.com/suite\n\ngo 1.21\n")
	write("calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Mul(a, b int) int { return a * b }\n")
	write("calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal()\n\t}\n}\n\n"+
		"func TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal()\n\t}\n}\n")
	git("add", "-A")
	git("commit", "-qm", "reference")
	write("calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Mul(a, b int) int { return a + b }\n")
	git("commit", "-qam", "start")
	gold := []byte(git("diff", "HEAD", "HEAD~1") + "\n")
	start := git("rev-parse", "HEAD")

	out := t.TempDir()
	store, err := events.NewDirBlobs(filepath.Join(out, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	goldHash, err := store.Put(gold)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]string{"gold_blob": string(goldHash)})
	task := rl.Task{
		ID: "suite-check", Kind: rl.TaskFix, Prompt: "Mul adds instead of multiplying.", Meta: meta,
		Repo: rl.RepoSpec{Path: dir, Commit: start}, Verifier: rl.Verifier{Protected: []string{"*_test.go"}},
	}
	spec := taskgen.SuiteSpec{Commands: []taskgen.SuiteCommand{taskgen.GoSuiteCommand("")}, DeadlineS: 300}
	if err := taskgen.WriteSuite(&task, spec, store); err != nil {
		t.Fatal(err)
	}
	gocache := t.TempDir()
	if b, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil && strings.TrimSpace(string(b)) != "" && strings.TrimSpace(string(b)) != "off" {
		gocache = strings.TrimSpace(string(b))
	}
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root: filepath.Join(t.TempDir(), "work"), DisableNetIsolation: true, FailureTTL: -1, SetupTimeout: 2 * time.Minute,
		SetEnv: map[string]string{"GOCACHE": gocache},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	rec, err := taskgen.RecordSuite(context.Background(), ws, task, gold, 1, store)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := taskgen.BuildUnits(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	sel.Apply(&spec)
	if err := taskgen.WriteSuite(&task, spec, store); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(out, "tasks.jsonl")
	if err := env.WriteTasks(file, []rl.Task{task}); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(out, "report.json")
	(rlRun{t}).must(rlTasks, "check", file, "--work-dir", filepath.Join(t.TempDir(), "check"), "--no-net-isolation",
		"--set-env", "GOCACHE="+gocache, "--verify-repeats", "2", "--report", report)
	var verdicts []struct {
		ID            string
		OK            bool
		Reason        string
		BaselineScore *float64 `json:"baseline_score"`
	}
	b, err := os.ReadFile(report)
	if err != nil || json.Unmarshal(b, &verdicts) != nil || len(verdicts) != 1 {
		t.Fatalf("report: %v %s", err, b)
	}
	if v := verdicts[0]; !v.OK || v.ID != task.ID || (v.BaselineScore != nil && *v.BaselineScore != 0) {
		t.Fatalf("verdict: %+v\n%s", v, b)
	}
}
