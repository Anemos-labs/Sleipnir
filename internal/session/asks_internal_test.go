package session

import (
	"context"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// trackAsks tells the swarm which worker is waiting for the person (Swarm.Asking), and must change nothing else about the question: a
// session without a prompter has nobody to ask, and what the prompter answers is what the engine hears.
func TestTrackAsksChangesNothingAboutTheQuestion(t *testing.T) {
	s := &Session{}
	if s.trackAsks(nil) != nil {
		t.Error("no prompter must stay no prompter: the engine refuses what it has nobody to ask")
	}
	var seen perm.Request
	next := func(ctx context.Context, r perm.Request) perm.Decision {
		seen = r
		return perm.Decision{Allow: true, Reason: "the person said yes"}
	}
	req := perm.Request{Agent: "be-1", Tool: "bash", Summary: "Run go test"}
	got := s.trackAsks(next)(context.Background(), req)
	if !got.Allow || got.Reason != "the person said yes" || seen.Summary != "Run go test" || seen.Agent != "be-1" {
		t.Fatalf("the question or its answer was changed: asked %+v, answered %+v", seen, got)
	}
}
