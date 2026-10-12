package env

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// RepoContains searches the task's starting state, ignoring case: a git repository at the task's commit (not what
// came after it), a plain directory, and nothing for a task whose repository is not on this machine.
func TestRepoContains(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("docs/SPEC.md", "Routes: GET /Accounts, GET /periods\n")
	start := r.commit("start")
	r.write("later.txt", "almanac.count\n")
	r.commit("later")
	g, err := NewGit(GitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := context.Background()
	task := rl.Task{ID: "t", Repo: rl.RepoSpec{Path: r.Dir, Commit: start}}
	for text, want := range map[string]bool{"accounts": true, "get /periods": true, "almanac.count": false, "statement": false} {
		if got, err := RepoContains(ctx, g, task, "", text); err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", text, got, err, want)
		}
	}
	// Relative to the base directory.
	rel := task
	rel.Repo.Path = filepath.Base(r.Dir)
	if got, err := RepoContains(ctx, g, rel, filepath.Dir(r.Dir), "accounts"); err != nil || !got {
		t.Errorf("a relative repository: %v %v", got, err)
	}
	// A plain directory.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spec.txt"), []byte("Berths.Open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := rl.Task{ID: "p", Repo: rl.RepoSpec{Path: dir}}
	if got, err := RepoContains(ctx, g, plain, "", "berths.open"); err != nil || !got {
		t.Errorf("a plain directory: %v %v", got, err)
	}
	if got, err := RepoContains(ctx, g, plain, "", "berths.close"); err != nil || got {
		t.Errorf("a plain directory, absent text: %v %v", got, err)
	}
	if _, err := RepoContains(ctx, g, rl.Task{ID: "u", Repo: rl.RepoSpec{URL: "https://example.com/r.git"}}, "", "x"); err == nil {
		t.Error("a repository that is not on this machine was searched")
	}
	// A checkout whose git metadata git cannot read is not searched as a plain directory: its files may hold what
	// came after the task's commit.
	broken := t.TempDir()
	for name, body := range map[string]string{".git": "gitdir: " + filepath.Join(broken, "gone") + "\n", "later.txt": "almanac.count\n"} {
		if err := os.WriteFile(filepath.Join(broken, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := RepoContains(ctx, g, rl.Task{ID: "b", Repo: rl.RepoSpec{Path: broken, Commit: start}}, "", "almanac.count"); err == nil {
		t.Errorf("a checkout git cannot read was searched as a directory: %v", got)
	}
	// A directory whose entries cannot be looked up (no search permission) is an error, not a plain directory.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		locked := t.TempDir()
		if err := os.WriteFile(filepath.Join(locked, "spec.txt"), []byte("almanac.count\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		if got, err := RepoContains(ctx, g, rl.Task{ID: "l", Repo: rl.RepoSpec{Path: locked}}, "", "almanac.count"); err == nil || !strings.Contains(err.Error(), "git metadata") {
			t.Errorf("a directory whose git metadata cannot be looked up: %v %v, want the lookup's error", got, err)
		}
	}
}
