package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// rebuildLibF is the value util.F<k> gives for x in the generated library.
func rebuildLibF(k, x int) int {
	switch {
	case x < 0:
		return -x + k
	case x == 0:
		return k
	case x > 100:
		return x - k
	}
	return x*2 + k
}

// rebuildLibG is the value core.G<k> gives for x in a library of n functions.
func rebuildLibG(k, x, n int) int {
	a, b := rebuildLibF(k, x), rebuildLibF((k+1)%n, x+1)
	if a > b {
		return a - b
	}
	if a < b {
		return b - a
	}
	return a + b + k
}

// writeRebuildLib writes a Go library big enough for the production gates of rl taskgen rebuild: util and core (which
// imports util), n functions each, with a test of each function.
func writeRebuildLib(t *testing.T, dir string, n int) {
	t.Helper()
	var util, utilTest, core, coreTest strings.Builder
	util.WriteString("// Package util maps numbers through fixed tables.\npackage util\n")
	utilTest.WriteString("package util\n\nimport \"testing\"\n")
	core.WriteString("// Package core combines util's tables.\npackage core\n\nimport \"example.com/lib/util\"\n")
	coreTest.WriteString("package core_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/lib/core\"\n)\n")
	for k := 0; k < n; k++ {
		fmt.Fprintf(&util, "\n// F%d maps x through table %d.\nfunc F%d(x int) int {\n\tswitch {\n\tcase x < 0:\n\t\treturn -x + %d\n\tcase x == 0:\n\t\treturn %d\n\tcase x > 100:\n\t\treturn x - %d\n\tdefault:\n\t\treturn x*2 + %d\n\t}\n}\n", k, k, k, k, k, k, k)
		fmt.Fprintf(&utilTest, "\nfunc TestF%d(t *testing.T) {\n\tif got := F%d(3); got != %d {\n\t\tt.Fatal(got)\n\t}\n}\n", k, k, rebuildLibF(k, 3))
		fmt.Fprintf(&core, "\n// G%d combines util.F%d and util.F%d.\nfunc G%d(x int) int {\n\ta := util.F%d(x)\n\tb := util.F%d(x + 1)\n\tif a > b {\n\t\treturn a - b\n\t}\n\tif a < b {\n\t\treturn b - a\n\t}\n\treturn a + b + %d\n}\n", k, k, (k+1)%n, k, k, (k+1)%n, k)
		fmt.Fprintf(&coreTest, "\nfunc TestG%d(t *testing.T) {\n\tif got := core.G%d(5); got != %d {\n\t\tt.Fatal(got)\n\t}\n}\n", k, k, rebuildLibG(k, 5, n))
	}
	for name, body := range map[string]string{
		"go.mod": "module example.com/lib\n\ngo 1.21\n", "README.md": "# lib\n\nTables of numbers.\n",
		"util/util.go": util.String(), "util/util_test.go": utilTest.String(), "core/core.go": core.String(), "core/core_test.go": coreTest.String(),
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTaskgenRebuildThroughTheCLI: rl taskgen rebuild makes a task of a library that meets the production gates
// (2,000 lines, 150 tests), and rl tasks check accepts it: the start scores 0 and the reference 1.
func TestTaskgenRebuildThroughTheCLI(t *testing.T) {
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
	repo := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	writeRebuildLib(t, repo, 95)
	git("add", "-A")
	git("commit", "-qm", "the library")

	gocache := t.TempDir()
	if b, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil && strings.TrimSpace(string(b)) != "" && strings.TrimSpace(string(b)) != "off" {
		gocache = strings.TrimSpace(string(b))
	}
	out := t.TempDir()
	tasksFile := filepath.Join(out, "tasks.jsonl")
	rig := []string{"--work-dir", filepath.Join(t.TempDir(), "work"), "--no-net-isolation", "--set-env", "GOCACHE=" + gocache}

	// Too small a scope is rejected, with the reason.
	if stdout, _, err := (rlRun{t}).do(rlTaskgen, append([]string{"rebuild", "--scope", "util/...", "-o", tasksFile, "--tier", "spec", repo}, rig...)...); err == nil ||
		!strings.Contains(stdout, "rejected: too few in-scope lines") {
		t.Fatalf("a scope under the gate: %v\n%s", err, stdout)
	}
	if _, err := os.Stat(tasksFile); err == nil {
		t.Fatal("a rejected scope wrote a tasks file")
	}
	if _, _, err := (rlRun{t}).do(rlTaskgen, "rebuild", repo); err == nil {
		t.Fatal("rebuild without --scope was accepted")
	}

	report := filepath.Join(out, "report.json")
	got := (rlRun{t}).must(rlTaskgen, append([]string{"rebuild", "--scope", "util,core", "-o", tasksFile, "--id-prefix", "lib", "--repeats", "2",
		"--report", report, repo}, rig...)...)
	for _, want := range []string{
		"wrote 1 rebuild task to " + tasksFile, "go, tier stubs, 2 packages, 2188 lines, 2 test files hidden, 3 agents",
		"units, in dependency order: example.com/lib/util (weight 1141, 95 tests), example.com/lib/core (weight 1047, 95 tests)",
		"tests: 190 scored, 0 dropped", "self-check: the start scores 0, the reference 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	tasks, err := env.LoadTasks(tasksFile)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("%v %d", err, len(tasks))
	}
	task := tasks[0]
	if task.Repo.Path != filepath.Join(out, "starts", task.ID+".git") || task.Team.Agents != 3 {
		t.Errorf("repo %+v, team %+v", task.Repo, task.Team)
	}
	var rep struct {
		Tests int `json:"tests"`
	}
	if b, err := os.ReadFile(report); err != nil || json.Unmarshal(b, &rep) != nil || rep.Tests != 190 {
		t.Errorf("report: %v %s", err, b)
	}

	checkReport := filepath.Join(out, "check.json")
	(rlRun{t}).must(rlTasks, append([]string{"check", tasksFile, "--report", checkReport}, rig...)...)
	var verdicts []struct {
		ID            string
		OK            bool
		Reason        string
		BaselineScore *float64 `json:"baseline_score"`
	}
	b, err := os.ReadFile(checkReport)
	if err != nil || json.Unmarshal(b, &verdicts) != nil || len(verdicts) != 1 {
		t.Fatalf("check report: %v %s", err, b)
	}
	if v := verdicts[0]; !v.OK || v.ID != task.ID || v.BaselineScore == nil || *v.BaselineScore != 0 {
		t.Fatalf("verdict: %+v", v)
	}
}
