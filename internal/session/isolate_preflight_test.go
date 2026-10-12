package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// The preflight refuses, with the session's own reasons, every combination of the isolation settings that the session would refuse as it
// starts, and lets through the ones it would accept. It reads the repository and changes nothing in it.
func TestPreflightIsolation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	repo := isoRepo(t)
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	git(t, empty, "init", "-q")
	sub := filepath.Join(repo, "sub")

	pf := func(p session.IsolationPreflight) string {
		p.Home = home
		if err := session.PreflightIsolation(ctx, p); err != nil {
			return err.Error()
		}
		return ""
	}
	for _, tc := range []struct {
		name string
		p    session.IsolationPreflight
		want string // "" passes
	}{
		{"a single agent", session.IsolationPreflight{Cwd: plain}, ""},
		{"a team without isolation", session.IsolationPreflight{Cwd: plain, Swarm: true, Isolation: "none"}, ""},
		{"worktree for a single agent", session.IsolationPreflight{Cwd: repo, Isolation: "worktree"}, "isolation applies to swarm sessions"},
		{"commit without worktree", session.IsolationPreflight{Cwd: repo, Swarm: true, Isolation: "none", Commit: true}, "commit needs worktree isolation"},
		{"worktree outside a repository", session.IsolationPreflight{Cwd: plain, Swarm: true, Isolation: "worktree"}, "needs a git repository"},
		{"worktree in a repository with no commit", session.IsolationPreflight{Cwd: empty, Swarm: true, Isolation: "worktree"}, "has none yet"},
		{"worktree below the repository's root", session.IsolationPreflight{Cwd: sub, Root: sub, Swarm: true, Isolation: "worktree"}, "the repository's root"},
		{"worktree at the root", session.IsolationPreflight{Cwd: repo, Swarm: true, Isolation: "worktree"}, ""},
		{"commit on a clean branch", session.IsolationPreflight{Cwd: repo, Swarm: true, Isolation: "worktree", Commit: true}, ""},
		{"a folder that becomes a repository", session.IsolationPreflight{Cwd: plain, Swarm: true, Isolation: "worktree", Commit: true, NewRepo: true}, ""},
		{"a folder that becomes a repository, for one agent", session.IsolationPreflight{Cwd: plain, Isolation: "worktree", NewRepo: true}, "isolation applies to swarm sessions"},
		{"an isolation that is not one", session.IsolationPreflight{Cwd: repo, Swarm: true, Isolation: "chroot"}, "want"},
	} {
		got := pf(tc.p)
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	// commit needs a clean checkout on a branch
	if err := os.WriteFile(filepath.Join(repo, "list.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pf(session.IsolationPreflight{Cwd: repo, Swarm: true, Isolation: "worktree", Commit: true}); !strings.Contains(got, "clean checkout") {
		t.Errorf("a dirty checkout: %q", got)
	}
	if got := pf(session.IsolationPreflight{Cwd: repo, Swarm: true, Isolation: "worktree"}); got != "" {
		t.Errorf("a dirty checkout without commit: %q", got)
	}
	git(t, repo, "checkout", "-q", "--", "list.txt")
	git(t, repo, "checkout", "-q", "--detach")
	if got := pf(session.IsolationPreflight{Cwd: repo, Swarm: true, Isolation: "worktree", Commit: true}); !strings.Contains(got, "detached") {
		t.Errorf("a detached HEAD: %q", got)
	}
	if st := git(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the preflight changed the checkout: %q", st)
	}
}

// ProbeRepo says what the New session dialog needs of a repository: commits, branch, cleanliness, and a bare repository for what it is.
func TestProbeRepo(t *testing.T) {
	ctx := context.Background()
	repo := isoRepo(t)
	p := session.ProbeRepo(ctx, repo)
	if !p.Probed || p.Bare || !p.HasCommits || p.Branch == "" || p.Detached || !p.Clean {
		t.Errorf("a clean repository: %+v", p)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "checkout", "-q", "--detach")
	if p := session.ProbeRepo(ctx, repo); !p.Probed || p.Clean || !p.Detached {
		t.Errorf("an untracked file on a detached HEAD: %+v", p)
	}
	empty := t.TempDir()
	git(t, empty, "init", "-q")
	if p := session.ProbeRepo(ctx, empty); !p.Probed || p.HasCommits {
		t.Errorf("no commit yet: %+v", p)
	}
	bare := t.TempDir()
	git(t, bare, "init", "-q", "--bare")
	if p := session.ProbeRepo(ctx, bare); !p.Probed || !p.Bare {
		t.Errorf("a bare repository: %+v", p)
	}
	if p := session.ProbeRepo(ctx, t.TempDir()); p.Probed {
		t.Errorf("not a repository: %+v", p)
	}
}
