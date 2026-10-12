package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
