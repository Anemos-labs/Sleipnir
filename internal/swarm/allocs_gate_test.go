//go:build !race

package swarm

import (
	"context"
	"fmt"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Reading the board costs nothing but a load, and that is what lets every agent look at it at every step: a snapshot is immutable and
// shared. The write path and the governor's admission are held to what they are (with a fifth to spare): measured 45 and 5.
func TestBoardAndGovernorAllocationsAreHeld(t *testing.T) {
	bd := NewBoard(events.Discard{})
	for i := 0; i < 50; i++ {
		if _, err := bd.CreateTask("mgr", TaskSpec{Title: fmt.Sprintf("Write the summary of topic %d", i), Role: "docs", Files: []string{fmt.Sprintf("summaries/topic-%d.md", i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := testing.AllocsPerRun(100, func() { _ = bd.Snapshot() }); got != 0 {
		t.Errorf("reading the board allocates %.0f times: a snapshot is immutable and shared, a read is a load", got)
	}
	// every run claims a task of its own (a claim that is refused allocates for its error, which is not the path being held)
	ids := make([]string, 40)
	for k := range ids {
		ids[k] = fmt.Sprintf("T%d", k+1)
	}
	i := 0
	got := testing.AllocsPerRun(len(ids)-1, func() {
		id := ids[i]
		i++
		if err := bd.Claim("w-1", id); err != nil {
			t.Fatal(err)
		}
		if err := bd.Update("w-1", id, "reading the topic"); err != nil {
			t.Fatal(err)
		}
	})
	if got > 55 { // measured: 45 (a new immutable snapshot of the board, and what each of the two changes writes into the log)
		t.Errorf("a claim and an update allocate %.0f times, more than the 55 they are held to", got)
	}
	g := NewGovernor(GovernorConfig{})
	ctx := context.Background()
	if got := testing.AllocsPerRun(100, func() {
		rel, err := g.Acquire(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		rel(nil, nil)
	}); got > 7 { // measured: 5
		t.Errorf("admitting a request allocates %.0f times, more than the 7 it is held to", got)
	}
}
