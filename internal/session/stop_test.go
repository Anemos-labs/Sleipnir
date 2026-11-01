package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A message that asks the team to stop is not a turn of the manager's: Run stops the team (no model is asked: the endpoint here
// answers nothing), says what it stopped, and a standing goal pauses instead of sending the manager on.
func TestRunStopsTheTeamOnAStopRequest(t *testing.T) {
	repo := newRepo(t)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: "http://127.0.0.1:9/v1"}}
	s, err := session.New(context.Background(), session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Offline: true, Interactive: true,
		Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Swarm: true, Workers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Swarm.StartManager(); err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(context.Background(), "STOP ALL TASKS")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !strings.HasPrefix(res.Stopped, "team stopped:") || res.Steps != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !s.Swarm.Stopped() {
		t.Fatal("the team is not stopped")
	}
	g := &goal.State{Objective: "build the API", Max: 5}
	note, next, _ := s.GoalJudged(context.Background(), g, nil)
	if next != "" || g.Paused != "you stopped the team" || !strings.Contains(note, "goal paused") {
		t.Fatalf("goal after a stop: note %q, next %q, paused %q", note, next, g.Paused)
	}
}
