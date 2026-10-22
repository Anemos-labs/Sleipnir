package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A single agent continued from a server whose defaults start isolated teams comes back as a single agent, without the team's
// isolation or commit, which a single-agent session refuses ("isolation applies to swarm sessions").
func TestChatArgsOfSingleAgentResumeDropsTeamIsolation(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	id := "20260101-000000-aaaaaa"
	l, err := events.Open(filepath.Join(home, ".sleipnir", "sessions", id), id)
	if err != nil {
		t.Fatal(err)
	}
	l.Emit("", events.TypeSessionStart, map[string]any{"swarm": false, "root": root})
	l.Emit("main", events.TypeAgentSnapshot, map[string]any{"blob": "x"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	d := webDefaults{Cwd: root, Workers: 8, Isolation: "worktree", Commit: true}
	got := chatArgsOf(d, id)
	if i := slices.Index(got, "--swarm"); i < 0 || got[i+1] != "0" {
		t.Errorf("args %q: want --swarm 0", got)
	}
	if slices.Contains(got, "--isolation") || slices.Contains(got, "--commit") {
		t.Errorf("args %q: a single agent takes no isolation or commit", got)
	}
	// a new session (no resume) keeps the server's team defaults
	if got := chatArgsOf(d, ""); !slices.Contains(got, "--isolation") || !slices.Contains(got, "--commit") {
		t.Errorf("args %q: a new team keeps its isolation", got)
	}
}
