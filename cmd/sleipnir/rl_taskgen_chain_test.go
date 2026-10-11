package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// TestRLTaskgenChainThroughCheck: `rl taskgen chain` makes a chain of three merged changes, each adding a function and
// its test in a Go package of its own, into a swarm task scored per milestone, and `rl tasks check` accepts it: the
// start scores 0 and the reference solution exactly 1.
func TestRLTaskgenChainThroughCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("records go test through the scorer at four states")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the suite scorer needs POSIX process groups")
	}
	for _, tool := range []string{"git", "go", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		c.Dir = repo
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("go.mod", "module example.com/chain\n\ngo 1.21\n")
	write("calc.go", "package calc\n\n// Add returns a+b.\nfunc Add(a, b int) int { return a + b }\n")
	write("calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal()\n\t}\n}\n")
	git("add", "-A")
	git("commit", "-qm", "initial")
	var commits []string
	for _, fn := range []struct{ pkg, name, op string }{{"sub", "Sub", "-"}, {"mul", "Mul", "*"}, {"div", "Div", "/"}} {
		write(fn.pkg+"/"+fn.pkg+".go", "package "+fn.pkg+"\n\n// "+fn.name+" applies "+fn.op+".\nfunc "+fn.name+"(a, b int) int { return a "+fn.op+" b }\n")
		write(fn.pkg+"/"+fn.pkg+"_test.go", "package "+fn.pkg+"\n\nimport \"testing\"\n\nfunc Test"+fn.name+"(t *testing.T) {\n\tif "+fn.name+"(6, 2) == 0 {\n\t\tt.Fatal()\n\t}\n}\n")
		git("add", "-A")
		git("commit", "-qm", "Add "+fn.name)
		commits = append(commits, git("rev-parse", "HEAD"))
	}

	out := t.TempDir()
	chainsFile := filepath.Join(out, "chains.jsonl")
	line, _ := json.Marshal(map[string]any{"id": "arith", "commits": commits, "issues": []string{"Subtraction is missing."}})
	if err := os.WriteFile(chainsFile, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	gocache := t.TempDir()
	if b, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil && strings.TrimSpace(string(b)) != "" && strings.TrimSpace(string(b)) != "off" {
		gocache = strings.TrimSpace(string(b))
	}
	tasksFile := filepath.Join(out, "tasks.jsonl")
	r := rlRun{t}
	got := r.must(rlTaskgen, "chain", "--chains", chainsFile, "-o", tasksFile, "--id-prefix", "demo", "--repeats", "2", "--timeout", "5m",
		"--work-dir", filepath.Join(t.TempDir(), "gen"), "--no-net-isolation", "--set-env", "GOCACHE="+gocache, "--report", filepath.Join(out, "report.json"), repo)
	if !strings.Contains(got, "wrote 1 chain tasks") || !strings.Contains(got, "arith: self-check ok: scores 0.000 0.333 0.667 1.000") {
		t.Fatalf("taskgen chain output:\n%s", got)
	}
	tasks, err := env.LoadTasks(tasksFile)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("%v %+v", err, tasks)
	}
	if tk := tasks[0]; tk.ID != "demo-arith" || tk.Team.Mode != "swarm" || !strings.Contains(tk.Prompt, "Milestone 1:\nSubtraction is missing.\n\nMilestone 2:\nAdd Mul") {
		t.Fatalf("task: %s %+v\n%s", tk.ID, tk.Team, tk.Prompt)
	}

	report := filepath.Join(out, "check.json")
	r.must(rlTasks, "check", tasksFile, "--work-dir", filepath.Join(t.TempDir(), "check"), "--no-net-isolation",
		"--set-env", "GOCACHE="+gocache, "--verify-repeats", "2", "--report", report)
	var verdicts []struct {
		ID            string
		OK            bool
		BaselineScore *float64 `json:"baseline_score"`
	}
	b, err := os.ReadFile(report)
	if err != nil || json.Unmarshal(b, &verdicts) != nil || len(verdicts) != 1 {
		t.Fatalf("report: %v %s", err, b)
	}
	if v := verdicts[0]; !v.OK || v.ID != "demo-arith" || (v.BaselineScore != nil && *v.BaselineScore != 0) {
		t.Fatalf("verdict: %+v\n%s", v, b)
	}

	// Usage errors.
	if _, _, err := r.do(rlTaskgen, "chain", "--chains", chainsFile, "-o", tasksFile); err == nil {
		t.Error("no repository was accepted")
	}
	if _, _, err := r.do(rlTaskgen, "chain", "-o", tasksFile, repo); err == nil || !strings.Contains(err.Error(), "--chains") {
		t.Errorf("no --chains: %v", err)
	}
}
