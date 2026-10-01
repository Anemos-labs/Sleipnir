package swarm

import (
	"context"
	"fmt"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
)

// The board is read by every agent at every step (its snapshot is the <live> block they are shown) and written by each as it claims,
// updates and finishes tasks; the governor admits every model request of the swarm. Fifty agents is the size the design is for.

func benchBoard(b *testing.B, tasks int) *Board {
	b.Helper()
	bd := NewBoard(events.Discard{})
	for i := 0; i < tasks; i++ {
		if _, err := bd.CreateTask("mgr", TaskSpec{Title: fmt.Sprintf("Write the summary of topic %d", i), Role: "docs", Files: []string{fmt.Sprintf("summaries/topic-%d.md", i)}}); err != nil {
			b.Fatal(err)
		}
	}
	return bd
}

// BenchmarkBoardSnapshot is what every agent pays to look at the board: a load of an immutable snapshot.
func BenchmarkBoardSnapshot(b *testing.B) {
	bd := benchBoard(b, 50)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if s := bd.Snapshot(); len(s.Tasks) != 50 {
				b.Fatal("the board lost tasks")
			}
		}
	})
}

// BenchmarkBoardClaimUpdate is the write path: an agent claims a task and keeps its status line current, on a board of 50 (a fresh board
// every 50 claims, so that every claim is one that succeeds).
func BenchmarkBoardClaimUpdate(b *testing.B) {
	var bd *Board
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if i%50 == 0 {
			b.StopTimer()
			bd = benchBoard(b, 50)
			b.StartTimer()
		}
		id := fmt.Sprintf("T%d", i%50+1)
		if err := bd.Claim("w-1", id); err != nil {
			b.Fatal(err)
		}
		if err := bd.Update("w-1", id, "reading the topic"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGovernorAcquire is the admission of one request with nothing waiting: the governor's own cost on every call of every agent.
func BenchmarkGovernorAcquire(b *testing.B) {
	g := NewGovernor(GovernorConfig{})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rel, err := g.Acquire(ctx, 1)
		if err != nil {
			b.Fatal(err)
		}
		rel(nil, nil)
	}
}
