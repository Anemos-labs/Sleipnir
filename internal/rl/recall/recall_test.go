package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/env"
)

// repo builds a project with n Go packages, each defining a distinctive constant.
func repo(t *testing.T, n int) string {
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
	write("go.mod", "module example.com/big\n\ngo 1.24\n")
	for i := 1; i <= n; i++ {
		write(fmt.Sprintf("pkg%02d/pkg%02d.go", i, i), fmt.Sprintf(
			"// Package pkg%02d is the %dth package.\npackage pkg%02d\n\nconst MaxRetries%02d = %d\n\nconst Banner%02d = \"welcome-%02d-%dx\"\n\n// Shared is defined in every package.\nconst Limit = 100\n\nfunc F%02d() int { return MaxRetries%02d }\n",
			i, i, i, i, 1000+i*7, i, i, i*13, i, i))
	}
	write("pkg01/pkg01_test.go", "package pkg01\n\nconst TestOnlyConst = 424242\n")
	write("vendor/x/x.go", "package x\n\nconst VendorConst = 909090\n")
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "init")
	return dir
}

func newGit(t *testing.T) *env.Git {
	t.Helper()
	g, err := env.NewGit(env.GitOptions{Scratch: t.TempDir()})
	if err != nil {
		t.Skip(err)
	}
	return g
}

func TestGenerateBuildsSoundDeterministicTasks(t *testing.T) {
	dir := repo(t, 20)
	g := newGit(t)
	gen := func(seed int64) []rl.Task {
		tasks, err := Generate(context.Background(), dir, Options{Git: g, Max: 5, Files: 6, Seed: seed, Tags: []string{"demo"}})
		if err != nil {
			t.Fatal(err)
		}
		return tasks
	}
	a, b := gen(1), gen(1)
	if len(a) != 5 {
		t.Fatalf("%d tasks", len(a))
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("the same seed must give the same tasks")
	}
	if c := gen(2); func() bool { jc, _ := json.Marshal(c); return string(jc) == string(ja) }() {
		t.Error("a different seed should select different facts or fillers")
	}
	if err := env.ValidateTasks(a); err != nil {
		t.Fatal(err)
	}
	for _, tk := range a {
		if tk.Kind != rl.TaskRecall || tk.Verifier.Cmd != "" || tk.Budget.ContextWindow != 12000 {
			t.Errorf("task shape: %+v", tk)
		}
		var meta Meta
		if err := json.Unmarshal(tk.Meta, &meta); err != nil {
			t.Fatal(err)
		}
		exp, err := env.ParseExpect(tk.Verifier.Expect)
		if err != nil || len(exp.Contains) != 1 {
			t.Fatalf("expect: %v %+v", err, exp)
		}
		answer := strings.TrimPrefix(exp.Contains[0], "ANSWER: ")
		// The answer is in the landmark file of the repository, and only there.
		src, err := os.ReadFile(filepath.Join(dir, meta.Landmark))
		if err != nil || !strings.Contains(string(src), meta.Name) || !strings.Contains(string(src), answer) {
			t.Errorf("%s: the answer %q is not in %s (%v)", tk.ID, answer, meta.Landmark, err)
		}
		// The prompt asks the question but never states the answer.
		if strings.Contains(tk.Prompt, answer) {
			t.Errorf("%s: the prompt leaks the answer %q:\n%s", tk.ID, answer, tk.Prompt)
		}
		if !strings.Contains(tk.Prompt, meta.Landmark) || !strings.Contains(tk.Prompt, meta.Name) {
			t.Errorf("%s: the prompt does not name the landmark and the constant:\n%s", tk.ID, tk.Prompt)
		}
		if len(meta.Fillers) != 6 {
			t.Errorf("%s: %d fillers", tk.ID, len(meta.Fillers))
		}
		for _, f := range meta.Fillers {
			if f == meta.Landmark || strings.Contains(f, "vendor") || strings.HasSuffix(f, "_test.go") {
				t.Errorf("%s: bad filler %s", tk.ID, f)
			}
			if !strings.Contains(tk.Prompt, f) {
				t.Errorf("%s: filler %s missing from the prompt", tk.ID, f)
			}
		}
		// Not a constant shared by many files: "Limit = 100" appears everywhere.
		if meta.Name == "Limit" {
			t.Errorf("%s asks about a constant defined in every file", tk.ID)
		}
		if !strings.Contains(strings.Join(tk.Tags, ","), "demo") || !strings.Contains(strings.Join(tk.Tags, ","), "recall") {
			t.Errorf("tags: %v", tk.Tags)
		}
	}
	// One task per landmark.
	seen := map[string]bool{}
	for _, tk := range a {
		var m Meta
		_ = json.Unmarshal(tk.Meta, &m)
		if seen[m.Landmark] {
			t.Errorf("landmark %s used twice", m.Landmark)
		}
		seen[m.Landmark] = true
	}
}

func TestGenerateRefusesWhatCannotBeAskedFairly(t *testing.T) {
	g := newGit(t)
	// Too few files for the requested reading.
	if _, err := Generate(context.Background(), repo(t, 4), Options{Git: g, Files: 12}); err == nil || !strings.Contains(err.Error(), "suitable source files") {
		t.Errorf("too small a repository: %v", err)
	}
	if _, err := Generate(context.Background(), t.TempDir(), Options{Git: g}); err == nil {
		t.Error("a non-repository must be refused")
	}
	if _, err := Generate(context.Background(), ".", Options{}); err == nil {
		t.Error("Git is required")
	}
}

func TestAnswersMustBeDistinctive(t *testing.T) {
	// Every package defines the same constants: nothing can be asked.
	dir := t.TempDir()
	for i := 1; i <= 8; i++ {
		p := filepath.Join(dir, fmt.Sprintf("p%d", i), "x.go")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(fmt.Sprintf("package p%d\n\nconst Limit = 100\nconst Name = \"same-everywhere\"\n", i)), 0o644)
	}
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.24\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "x"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git: %v %s", err, out)
		}
	}
	if _, err := Generate(context.Background(), dir, Options{Git: newGit(t), Files: 3}); err == nil || !strings.Contains(err.Error(), "distinctive") {
		t.Fatalf("constants that appear in many files cannot be a memory test: %v", err)
	}
}
