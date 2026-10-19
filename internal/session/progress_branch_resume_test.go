package session

import (
	"context"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
)

// A resumed session reads back the branch it pushed to, so its progress keeps going to
// the same remote branch whatever has merged since; the goal names it otherwise.
func TestProgressBranchSurvivesAResume(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{Dir: dir, Log: log, Blobs: events.NewMemBlobs()}
	if b := s.lastProgressBranch(); b != "" {
		t.Fatalf("a new session has pushed to %q", b)
	}
	s.SaveGoal(goal.New("Add CSV export"))
	if got := s.goalObjective(); got != "Add CSV export" {
		t.Fatalf("objective %q", got)
	}
	git := func(ctx context.Context, args ...string) ([]byte, error) { return nil, nil }
	p := &progressPusher{git: git, remote: "origin", log: log, name: func(string) string {
		return progressBranch("sleipnir/20261019-015656-a1b2c3", s.goalObjective())
	}}
	p.Kick("aaa")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	p.Wait(ctx)
	cancel()
	log.Close()

	reopened, err := events.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed := &Session{Dir: dir, Log: reopened, Blobs: events.NewMemBlobs()}
	if b := resumed.lastProgressBranch(); b != "sleipnir/add-csv-export-a1b2c3" {
		t.Fatalf("resumed session pushes to %q", b)
	}
}
