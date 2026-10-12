package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// A team whose isolation nobody chose works in trees of its own in the root of a repository with a commit, and in a folder that the
// request makes a repository; elsewhere the session's own default (none) stands.
func TestATeamInARepositoryDefaultsToWorktrees(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	g, err := gitx.Init(ctx, repo, false)
	if err != nil {
		t.Skipf("git: %v", err)
	}
	empty := t.TempDir()
	if got := defaultIsolation(ctx, repo, false); got != "" {
		t.Errorf("a repository without a commit: %q", got)
	}
	if _, err := g.CommitEmpty(ctx, firstCommit, gitx.DefaultAuthor); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dir     string
		newRepo bool
		want    string
	}{
		{repo, false, config.IsolationWorktree},
		{sub, false, ""},
		{empty, false, ""},
		{empty, true, config.IsolationWorktree},
	} {
		if got := defaultIsolation(ctx, c.dir, c.newRepo); got != c.want {
			t.Errorf("%s (new repository %v): %q, want %q", c.dir, c.newRepo, got, c.want)
		}
	}
}

// A request that arrives while the session starts waits for the start instead of being refused, and is answered when the start
// ends (here: it failed, so there is no session); a closed tab answers at once.
func TestARequestWaitsForTheSessionToStart(t *testing.T) {
	tab := &webTab{starting: true}
	tab.cond = sync.NewCond(&tab.mu)
	go func() {
		time.Sleep(50 * time.Millisecond)
		tab.mu.Lock()
		tab.starting = false
		tab.cond.Broadcast()
		tab.mu.Unlock()
	}()
	start := time.Now()
	if s := tab.readySession(context.Background()); s != nil {
		t.Fatalf("a failed start gave a session")
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("the request did not wait for the start: %v", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	tab.mu.Lock()
	tab.starting = true
	tab.mu.Unlock()
	if s := tab.readySession(ctx); s != nil {
		t.Fatal("a cancelled wait gave a session")
	}
	if ctx.Err() == nil {
		t.Error("readySession returned before its context ended")
	}
}

// The New session dialog picks worktree isolation for a team in a repository unless someone named the isolation: the defaults say
// whether --isolation of sleipnir web or swarm.isolation of the configuration did, and the built-in "none" is no one's choice.
func TestTheDefaultsSayWhetherTheIsolationWasNamed(t *testing.T) {
	project := webWorld(t)
	home, _ := os.UserHomeDir()
	defaults := func(flag string) newSessionDefaults {
		return (&webHostImpl{d: webDefaults{Cwd: project, Isolation: flag}}).defaults()
	}
	if d := defaults(""); d.IsolationSet || d.Isolation != "none" {
		t.Errorf("nothing named: isolation %q, set %v", d.Isolation, d.IsolationSet)
	}
	if d := defaults("none"); !d.IsolationSet || d.Isolation != "none" {
		t.Errorf("--isolation none: isolation %q, set %v", d.Isolation, d.IsolationSet)
	}
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), []byte(`{"swarm":{"isolation":"none"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := defaults(""); !d.IsolationSet || d.Isolation != "none" {
		t.Errorf("swarm.isolation none in the person's file: isolation %q, set %v", d.Isolation, d.IsolationSet)
	}
}
