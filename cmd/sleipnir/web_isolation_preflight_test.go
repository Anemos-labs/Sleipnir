package main

import (
	"context"
	"os"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestWebPreflightLoadsTargetIsolation(t *testing.T) {
	project := webWorld(t)
	if err := os.WriteFile(config.ProjectConfigPath(project), []byte(`{"swarm":{"isolation":"worktree"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &webHostImpl{base: session.Options{Home: os.Getenv("HOME")}}
	if err := h.preflightIsolation(context.Background(), chatFlags{workers: 8}, project, dirPlan{}); err == nil {
		t.Fatal("configured worktree isolation must reject a team in a plain directory")
	}
	if err := h.preflightIsolation(context.Background(), chatFlags{workers: 8, isolation: "none"}, project, dirPlan{}); err != nil {
		t.Fatalf("explicit none must override target configuration: %v", err)
	}
	if err := h.preflightIsolation(context.Background(), chatFlags{}, project, dirPlan{}); err != nil {
		t.Fatalf("solo configured isolation should retain its plain-directory fallback: %v", err)
	}
}
