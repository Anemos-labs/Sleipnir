package perm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A confined agent shares the repository's refs with the person's checkout, so moving a branch is not a file access its tree check
// sees. The manager of a live run moved the checkout's branch to the integration tip with git update-ref, and the person's files
// stayed behind: every merged change looked undone. Only the merge queue moves the checkout's branch and the run's branches.
func TestAConfinedAgentMovesOnlyItsOwnBranch(t *testing.T) {
	f := newFixture(t)
	trees := filepath.Join(f.base, "trees")
	mine := filepath.Join(trees, "be-1")
	gitdir := filepath.Join(f.root, ".git")
	wt := filepath.Join(gitdir, "worktrees", "be-1")
	for p, body := range map[string]string{
		filepath.Join(gitdir, "HEAD"):                   "ref: refs/heads/claude/work\n",
		filepath.Join(wt, "HEAD"):                       "ref: refs/heads/sleipnir/s1/be-1\n",
		filepath.Join(mine, ".git"):                     "gitdir: " + wt + "\n",
		filepath.Join(wt, "commondir"):                  "../..\n",
		filepath.Join(gitdir, "refs", "heads", "alias"): "ref: refs/heads/claude/work\n",
		filepath.Join(gitdir, "config"):                 "[remote \"origin\"]\n\turl = https://example.com/o/r.git\n[remote \"self\"]\n\turl = " + f.root + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e, err := NewEngine(Config{Root: f.root, Home: f.home, ExtraRoots: []string{trees}, Mode: ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	e.Confine("be-1", mine)
	cases := []struct {
		cmd     string
		refused bool
	}{
		{"git update-ref refs/heads/claude/work sleipnir/s1/_integration", true},
		{"git update-ref refs/heads/sleipnir/s1/_integration HEAD", true},
		{"git update-ref -d refs/heads/sleipnir/s1/be-2", true},
		{"git update-ref --stdin", true},
		{"git branch -f claude/work HEAD", true},
		{"git branch -f sleipnir/s1/_integration", true},
		{"git branch -D sleipnir/s1/be-2", true},
		{"git branch -m sleipnir/s1/be-1 mine", true},
		{"git branch -m renamed", true},
		{"git push . HEAD:claude/work", true},
		{"git push . +HEAD:refs/heads/sleipnir/s1/_integration", true},
		{"git fetch origin main:claude/work", true},
		{"git update-ref -m sync refs/heads/claude/work HEAD", true},
		{"git symbolic-ref -m why refs/heads/claude/work refs/heads/x", true},
		{"git push . --delete sleipnir/s1/be-2", true},
		{"git push . :claude/work", true},
		{"git -C sub update-ref refs/heads/claude/work HEAD", true},
		// The same repository named another way than ".", and the long spellings of a branch copy.
		{"git push " + f.root + " HEAD:claude/work", true},
		{"git push ./ HEAD:claude/work", true},
		{"git push file://" + f.root + " HEAD:refs/heads/sleipnir/s1/_integration", true},
		{"git push self HEAD:claude/work", true},
		{"git push upstream HEAD:claude/work", true}, // a remote whose URL the configuration does not give
		{"git branch --copy --force HEAD claude/work", true},
		{"git branch -C scratch sleipnir/s1/_integration", true},
		{"git branch --copy sleipnir/s1/be-1 sleipnir/s1/be-2", true},
		{"git update-ref refs/heads/alias HEAD", true}, // a symbolic ref pointing at the checkout's branch
		{"git symbolic-ref refs/heads/other refs/heads/claude/work", true},
		{"git symbolic-ref refs/tags/x refs/heads/sleipnir/s1/_integration", true},

		{"git update-ref refs/heads/sleipnir/s1/be-1 HEAD~1", false},
		{"git update-ref refs/tags/v1 HEAD", false},
		{"git update-ref --no-deref refs/heads/alias HEAD", false},
		{"git update-ref -m sync refs/heads/sleipnir/s1/be-1 HEAD", false},
		{"git push . --delete scratch", false},
		{"git branch scratch", false},
		{"git branch -f scratch HEAD", false},
		{"git branch -D scratch", false},
		{"git branch -c sleipnir/s1/be-1 backup", false},
		{"git push origin HEAD:feature", false},
		{"git push origin HEAD:claude/work", false}, // origin is on another machine
		{"git push git@example.com:o/r.git HEAD:claude/work", false},
		{"git branch --copy --force sleipnir/s1/be-1 backup", false},
		{"git fetch origin main:refs/remotes/origin/main", false},
		{"git fetch origin", false},
		{"git status", false},
		{"git log --oneline -3", false},
		// attaching the tree to a queued branch would make it the tree's own for every later command
		{"git checkout sleipnir/s1/_integration", true},
		{"git switch claude/work", true},
		{"git checkout -B sleipnir/s1/_integration", true},
		{"git switch -c sleipnir/s1/new", true},
		{"git checkout -bsleipnir/s1/new", true},
		{"git switch --orphan=sleipnir/s1/new", true},
		{"git checkout sleipnir/s1/be-1", false},
		{"git checkout -b scratch", false},
		{"git switch -c scratch sleipnir/s1/_integration", false}, // a new branch starting there moves nothing
		{"git checkout sleipnir/s1/_integration -- main.go", false},
		{"git checkout -- main.go", false},
		{"git checkout --detach sleipnir/s1/_integration", false},
		{"git switch --force-create=sleipnir/s1/_integration", true},
		{"git switch --create sleipnir/s1/new", true},
		{"git switch --force-c=sleipnir/s1/new", true}, // an abbreviation, as git accepts it
		{"git checkout --orph sleipnir/s1/new", true},
		{"git switch -- sleipnir/s1/_integration", true}, // for switch, -- only ends the options
		{"git switch --track origin/sleipnir/s1/new", true},
		{"git checkout -t origin/sleipnir/s1/new", true},
		{"git switch --track=direct origin/sleipnir/s1/new", true},
		{"git switch --track origin/scratch", false},
		{"git switch -c scratch --track origin/sleipnir/s1/new", false},
	}
	for _, tc := range cases {
		r := f.request(bash(tc.cmd))
		r.Agent, r.Cwd = "be-1", mine
		d := e.Check(bg, r)
		got := !d.Allow && strings.Contains(d.Reason, "isolation:")
		if got != tc.refused {
			t.Errorf("%s: refused=%v (%s), want %v", tc.cmd, got, d.Reason, tc.refused)
		}
	}
	// An agent with no tree of its own is not this check's business.
	r := f.request(bash("git update-ref refs/heads/claude/work HEAD"))
	r.Agent, r.Cwd = "solo", f.root
	if d := e.Check(bg, r); strings.Contains(d.Reason, "isolation:") {
		t.Errorf("an unconfined agent was refused: %s", d.Reason)
	}
}
