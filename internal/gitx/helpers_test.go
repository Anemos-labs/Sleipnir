package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Fixtures are built with the real git binary under a hermetic environment
// (no system or global configuration, fixed identity and dates), so they are the
// same on every machine and never run anything from the developer's
// ~/.gitconfig. The code under test then opens them through gitx.

var fixtureClock atomic.Int64

func fixtureEnv(home string) []string {
	n := fixtureClock.Add(1)
	date := time.Unix(1_700_000_000+n*60, 0).UTC().Format("2006-01-02T15:04:05") + " +0000"
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date,
		// No background work. git starts `git maintenance run --auto` detached after a commit, and the worktree-prune task of
		// that removes a worktree entry that has no index yet and whose gitdir is not there: the half-made worktrees that some
		// tests build, a few milliseconds after the commit that preceded them (one run in sixteen on git 2.55). Every command
		// the product runs is hardened the same way (hardenedConfig).
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0",
		"GIT_CONFIG_KEY_2=gc.autoDetach", "GIT_CONFIG_VALUE_2=false",
	}
}

// rawGit runs git directly (no hardening) in dir. Only for building fixtures
// before any hostile configuration is installed.
func rawGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv(t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

// isolateHome points HOME at an empty temp directory so no test can read the
// developer's git configuration.
func isolateHome(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// writeFile creates path (and parents) with content.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newRepo creates a repository with one commit containing a.txt, b.txt and
// sub/c.txt, and returns its (symlink-resolved) directory.
func newRepo(t testing.TB) string {
	t.Helper()
	isolateHome(t)
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "init", "-q", "-b", "main", ".")
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "bravo\n")
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), "charlie\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

// open opens dir hermetically.
func openRepo(t testing.TB, dir string, opts ...Option) *Repo {
	t.Helper()
	r, err := Open(dir, append([]Option{WithHermeticConfig()}, opts...)...)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return r
}

func ctxT(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func skipWithoutUnix(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a unix shell")
	}
}

// rawGitMayFail runs git and returns its combined output whatever the exit status
// (merges that are meant to conflict).
func rawGitMayFail(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv(os.TempDir())
	out, _ := cmd.CombinedOutput()
	return string(out)
}
