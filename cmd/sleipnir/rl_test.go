package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// gitRepo makes a repository whose second commit fixes a bug and adds the test
// for it: the shape taskgen mines.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	write("go.mod", "module example.com/demo\n\ngo 1.24\n")
	write("demo.go", "package demo\n\n// Add adds two numbers.\nfunc Add(a, b int) int { return a - b }\n")
	git("add", "-A")
	git("commit", "-qm", "initial import")
	write("demo.go", "package demo\n\n// Add adds two numbers.\nfunc Add(a, b int) int { return a + b }\n")
	write("demo_test.go", "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}\n")
	git("add", "-A")
	git("commit", "-qm", "fix Add: it subtracted instead of adding")
	return dir
}

type rlRun struct {
	t *testing.T
}

func (r rlRun) do(fn func(context.Context, []string, io.Writer, io.Writer) error, args ...string) (string, string, error) {
	r.t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	err := fn(ctx, args, &out, &errb)
	return out.String(), errb.String(), err
}

func (r rlRun) must(fn func(context.Context, []string, io.Writer, io.Writer) error, args ...string) string {
	r.t.Helper()
	out, errb, err := r.do(fn, args...)
	if err != nil {
		r.t.Fatalf("%v\nstdout:\n%s\nstderr:\n%s", err, out, errb)
	}
	return out + errb
}

func TestRLPipelineThroughTheCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, ".sleipnir"))
	for _, k := range []string{"HEIMDALLM_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	agent.RetryBase = time.Millisecond
	goCache := func() string {
		out, err := exec.Command("go", "env", "GOCACHE").Output()
		if err != nil {
			t.Skip(err)
		}
		return strings.TrimSpace(string(out))
	}()
	shared := []string{"--set-env", "GOCACHE=" + goCache, "--set-env", "GOFLAGS=-mod=mod", "--set-env", "GOTOOLCHAIN=local", "--no-net-isolation"}

	repo := gitRepo(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	tasksFile := filepath.Join(dir, "tasks", "tasks.jsonl")
	r := rlRun{t}

	// 1. Tasks from history, proven sound.
	out := r.must(rlTaskgen, append([]string{"git", "--repo", repo, "-o", tasksFile, "--work-dir", work, "--id-prefix", "demo", "--tag", "go", "--report", filepath.Join(dir, "gen.json")}, shared...)...)
	if !strings.Contains(out, "wrote 1 tasks") {
		t.Fatalf("taskgen output:\n%s", out)
	}
	if out := r.must(rlTasks, "validate", tasksFile); !strings.Contains(out, "1 tasks, all valid") {
		t.Fatalf("validate:\n%s", out)
	}
	if out := r.must(rlTasks, "stats", tasksFile); !strings.Contains(out, "1 tasks in 1 repositories") {
		t.Fatalf("stats:\n%s", out)
	}
	if out := r.must(rlTasks, append([]string{"check", tasksFile, "--work-dir", work}, shared...)...); !strings.Contains(out, "1 ok, 0 failed") {
		t.Fatalf("check:\n%s", out)
	}
	// The hidden test is not in the tasks file's text, only its hash.
	raw, _ := os.ReadFile(tasksFile)
	if strings.Contains(string(raw), "want 5") || strings.Contains(string(raw), "func TestAdd") {
		t.Error("the hidden verifier's content must live in the blob store, not in tasks.jsonl")
	}

	// 2. A policy server: the mock plays a policy that fixes the bug.
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		n := 0
		for _, m := range c.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{{ID: "c1", Name: "read", Args: `{"path":"demo.go"}`}}}
		case 1:
			return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{{ID: "c2", Name: "edit", Args: `{"path":"demo.go","old_string":"a - b","new_string":"a + b"}`}}}
		}
		return mock.Reply{Text: "Fixed Add: it now returns the sum."}
	})
	ts := srv.Start()
	defer ts.Close()

	// 3. Rollouts: 2 samples, with token capture.
	runDir := filepath.Join(dir, "runs", "r1")
	out = r.must(rlRollout, append([]string{"--tasks", tasksFile, "--model", "policy-model", "--base-url", ts.URL, "--group", "2", "--capture",
		"--out", runDir, "--work-dir", work, "--sampling", `{"temperature":1}`, "--seed", "5"}, shared...)...)
	if !strings.Contains(out, "2 completed") || !strings.Contains(out, "pass rate 100.0%") {
		t.Fatalf("rollout output:\n%s", out)
	}
	for _, f := range []string{"manifest.json", "summary.json", "demo-" + "*"} {
		if m, _ := filepath.Glob(filepath.Join(runDir, f)); len(m) == 0 {
			t.Errorf("run directory lacks %s", f)
		}
	}
	if out := r.must(rlShow, runDir); !strings.Contains(out, "pass rate 100.0%") {
		t.Fatalf("show:\n%s", out)
	}
	if out := r.must(rlVerify, runDir); !strings.Contains(out, "0 mismatches") {
		t.Fatalf("verify:\n%s", out)
	}

	// 4. Re-scoring with a different target price changes the cost side only.
	if out := r.must(rlReward, runDir, "--target-price", "openai", "--dry-run"); !strings.Contains(out, "dry run") && !strings.Contains(out, "episodes rescored") {
		t.Fatalf("reward:\n%s", out)
	}

	// 5. Export every format the trainers read.
	for _, tc := range []struct {
		format string
		extra  []string
	}{
		{"steps", nil}, {"tokens", []string{"--pack"}}, {"groups", nil}, {"sft", nil}, {"kto", nil}, {"dpo", nil}, {"atif", nil},
		{"canonical", []string{"--keep-flat"}},
	} {
		dest := filepath.Join(dir, "data."+tc.format+".jsonl")
		args := append([]string{runDir, "--format", tc.format, "-o", dest, "--keep-flat"}, tc.extra...)
		out, errb, err := r.do(rlExport, args...)
		if err != nil {
			t.Fatalf("export %s: %v\n%s\n%s", tc.format, err, out, errb)
		}
		if tc.format == "dpo" { // one passing group has no better/worse pair
			continue
		}
		st, err := os.Stat(dest)
		if err != nil || st.Size() == 0 {
			t.Errorf("export %s wrote nothing: %v\n%s", tc.format, err, errb)
		}
		if st != nil && st.Mode().Perm()&0o077 != 0 {
			t.Errorf("export %s is readable by others (%v): training data derives from private code", tc.format, st.Mode().Perm())
		}
	}
	// The steps export carries advantages and the exact prompt; tokens carry ids.
	steps, _ := os.ReadFile(filepath.Join(dir, "data.steps.jsonl"))
	var rec struct {
		Prompt []json.RawMessage `json:"prompt"`
		Tools  []json.RawMessage `json:"tools"`
		Meta   struct {
			Policy json.RawMessage `json:"policy"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(steps)), "\n")[0]), &rec); err != nil || len(rec.Prompt) == 0 || len(rec.Tools) == 0 {
		t.Fatalf("steps record: %v %s", err, steps)
	}
	toks, _ := os.ReadFile(filepath.Join(dir, "data.tokens.jsonl"))
	if !strings.Contains(string(toks), `"response_ids"`) || !strings.Contains(string(toks), `"response_mask"`) {
		t.Errorf("tokens export lacks ids: %.300s", toks)
	}

	// 6. Deduplicated canonical export expands back byte for byte.
	table := filepath.Join(dir, "table.jsonl")
	dedup := filepath.Join(dir, "canonical.dedup.jsonl")
	r.must(rlExport, runDir, "--format", "canonical", "--table", table, "-o", dedup, "--keep-flat")
	expanded := r.must(rlExpand, dedup, "--table", table)
	if !strings.Contains(expanded, "expanded 2 episodes") {
		t.Fatalf("expand:\n%s", expanded)
	}
	inline := filepath.Join(dir, "canonical.inline.jsonl")
	r.must(rlExport, runDir, "--format", "canonical", "--inline", "-o", inline, "--keep-flat")
	a, _, err := r.do(rlExpand, dedup, "--table", table)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(inline)
	if a != string(want) {
		t.Error("expanding a deduplicated export must give exactly the inline export")
	}

	// 7. Misuse is refused with a useful message.
	if _, _, err := r.do(rlExport, runDir, "--format", "nope"); err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Errorf("unknown format: %v", err)
	}
	if _, _, err := r.do(rlExport); err == nil {
		t.Error("export with no run directory must fail")
	}
}

// --commits mines exactly the listed changes; a listed issue is the prompt and
// the provenance lands in the task's meta.
func TestTaskgenGitCommitsFlag(t *testing.T) {
	repo := gitRepo(t)
	dir := t.TempDir()
	list := filepath.Join(dir, "picks.jsonl")
	pick := `{"commit":"HEAD","issue":{"ref":"#3","title":"Add returns the difference","body":"Add(2, 3) gives -1."},"source":{"pr":"https://example.com/demo/pull/4"}}`
	if err := os.WriteFile(list, []byte("# one pick\n"+pick+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasksFile := filepath.Join(dir, "tasks.jsonl")
	r := rlRun{t}
	out := r.must(rlTaskgen, "git", "--repo", repo, "--commits", list, "--no-validate", "-o", tasksFile, "--id-prefix", "demo")
	if !strings.Contains(out, "wrote 1 tasks") {
		t.Fatalf("taskgen output:\n%s", out)
	}
	raw, err := os.ReadFile(tasksFile)
	if err != nil {
		t.Fatal(err)
	}
	var task struct {
		Prompt string `json:"prompt"`
		Meta   struct {
			Issue  string            `json:"issue"`
			Source map[string]string `json:"source"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &task); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(task.Prompt, "Issue: Add returns the difference\n\nAdd(2, 3) gives -1.") || strings.Contains(task.Prompt, "subtracted") ||
		task.Meta.Issue != "#3" || task.Meta.Source["pr"] != "https://example.com/demo/pull/4" {
		t.Errorf("task: %+v", task)
	}

	if _, _, err := r.do(rlTaskgen, "git", "--repo", repo, "--commits", list, "--since", "2024-01-01", "--no-validate", "-o", tasksFile); err == nil ||
		!strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("--commits with --since: %v", err)
	}
	if _, _, err := r.do(rlTaskgen, "git", "--repo", repo, "--commits", filepath.Join(dir, "missing"), "--no-validate", "-o", tasksFile); err == nil {
		t.Error("a missing --commits file was accepted")
	}
}

// TestTaskgenGitSealFlag: --seal writes each start state as a one-commit repository, and --repo-path names its directory.
func TestTaskgenGitSealFlag(t *testing.T) {
	repo := gitRepo(t)
	dir := t.TempDir()
	tasksFile := filepath.Join(dir, "tasks.jsonl")
	seal := filepath.Join(dir, "repos")
	out := (rlRun{t}).must(rlTaskgen, "git", "--repo", repo, "--no-validate", "-o", tasksFile, "--id-prefix", "demo", "--seal", seal, "--repo-path", "repos")
	if !strings.Contains(out, "start states sealed in "+seal) {
		t.Fatalf("taskgen output:\n%s", out)
	}
	tasks, err := env.LoadTasks(tasksFile)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("%v %+v", err, tasks)
	}
	tk := tasks[0]
	if tk.Repo.Path != "repos/"+tk.ID+".git" {
		t.Fatalf("repo: %+v", tk.Repo)
	}
	c := exec.Command("git", "--git-dir="+filepath.Join(seal, tk.ID+".git"), "rev-list", "--all")
	if b, err := c.Output(); err != nil || strings.TrimSpace(string(b)) != tk.Repo.Commit {
		t.Errorf("the sealed repository's commits: %q %v, want only %s", b, err, tk.Repo.Commit)
	}
}
