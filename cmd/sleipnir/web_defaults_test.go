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
