package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// The checkout's ignored files are the ones a worktree has no copy of: the permission engine may let a confined agent read them there.
func TestIgnoredCheckerFollowsTheCheckoutsGitignore(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".gitignore":          "game-data/\n*.log\n",
		"main.go":             "package main\n",
		"game-data/DTR/a.bin": "x",
		"run.log":             "x",
		"notes/untracked.txt": "x",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", ".gitignore", "main.go"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ignored := (&isoPlan{repo: repo}).ignoredChecker()
	slash := func(rel string) string { return filepath.ToSlash(filepath.Join(dir, rel)) }
	for rel, want := range map[string]bool{
		"game-data":             true,
		"game-data/DTR/a.bin":   true,
		"game-data/DTR/new.bin": true, // not there yet: still ignored, so a read of it finds nothing rather than being refused
		"run.log":               true,
		"main.go":               false,
		"notes/untracked.txt":   false, // untracked but not ignored: the trees have a copy
		".git/config":           false,
		".":                     false,
	} {
		for range 2 { // the second answer is the remembered one
			if got := ignored(slash(rel)); got != want {
				t.Errorf("ignored(%s) = %v, want %v", rel, got, want)
			}
		}
	}
	if ignored(filepath.ToSlash(filepath.Dir(dir))) {
		t.Error("a path outside the checkout counted as ignored")
	}
}
