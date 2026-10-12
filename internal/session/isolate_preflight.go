package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// IsolationPreflight is what PreflightIsolation checks: the folder a session would work in, its project root ("" finds it from Cwd as
// New does), the user's home, whether it is a team, the isolation asked for ("none", "worktree", or "" for ConfigIsolation) and Options.Commit.
// NewRepo says that the folder is made a git repository, with a first commit on a branch and nothing else in it, before the session
// starts: what a repository is asked for then holds, and only the combination of the settings is checked.
type IsolationPreflight struct {
	Cwd             string
	Root            string
	Home            string
	Swarm           bool
	Isolation       string
	ConfigIsolation string // effective configuration's isolation mode, used only without an override
	Commit          bool
	NewRepo         bool
}

// PreflightIsolation runs the checks a new session makes of its isolation before anything is started (the ones planIsolation makes
// of a session that resumes nothing), so that a combination that cannot start is refused with the session's own reason before a tab
// opens. It changes nothing: cleanliness is read from git status, where the session itself snapshots the work tree.
func PreflightIsolation(ctx context.Context, p IsolationPreflight) error {
	mode, explicit := config.IsolationNone, false
	switch v := strings.ToLower(strings.TrimSpace(p.Isolation)); v {
	case "":
		if p.ConfigIsolation != "" {
			mode = p.ConfigIsolation
		}
	case config.IsolationNone, "shared":
		explicit = true
	case config.IsolationWorktree:
		mode, explicit = config.IsolationWorktree, true
	default:
		return fmt.Errorf("isolation %q: want %q or %q", p.Isolation, config.IsolationNone, config.IsolationWorktree)
	}
	c := isolationCheck{mode: mode, explicit: explicit, swarm: p.Swarm, commit: p.Commit, home: p.Home, id: "preflight"}
	if p.NewRepo {
		_, err := c.modeCheck()
		return err
	}
	root := p.Root
	if root == "" {
		root, _ = config.FindRoot(p.Cwd)
	}
	if root == "" {
		root = p.Cwd
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	c.root = root
	_, _, err := checkIsolation(ctx, c, statusDirty)
	return err
}

// statusDirty reports whether the work tree has anything git status shows (tracked edits, staged changes, untracked files), without
// writing to the repository.
func statusDirty(ctx context.Context, repo *gitx.Repo) (bool, error) {
	st, err := repo.Status(ctx)
	if err != nil {
		return false, err
	}
	return !st.Clean(), nil
}

// RepoProbe is what a folder's git repository says about starting an isolated team in it: Bare (no work tree), HasCommits (a commit to
// start the trees from), Branch (the checked-out branch, "" when HEAD is detached: Detached), and Clean (nothing staged, modified or
// untracked). Probed is false when git could not be asked (no git, a timeout): the other fields then say nothing.
type RepoProbe struct {
	Probed     bool
	Bare       bool
	HasCommits bool
	Branch     string
	Detached   bool
	Clean      bool
}

// ProbeRepo asks git about the repository at dir, which the caller found to be one (its root, or a bare repository), and writes nothing.
func ProbeRepo(ctx context.Context, dir string) RepoProbe {
	repo, err := gitx.Open(dir)
	if err != nil {
		return RepoProbe{}
	}
	if repo.IsBare() {
		return RepoProbe{Probed: true, Bare: true}
	}
	st, err := repo.Status(ctx)
	if err != nil {
		return RepoProbe{}
	}
	return RepoProbe{Probed: true, HasCommits: st.Head != "", Branch: st.Branch, Detached: st.Detached || st.Branch == "", Clean: st.Clean()}
}
