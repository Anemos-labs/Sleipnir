package state

import (
	"testing"

	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// BenchmarkApply is the cost of folding a real session: the recorded demo, event by event. A live UI folds a few events a second
// and a replay at speed a few thousand; the fold has to be far from being the thing a frame waits for.
func BenchmarkApply(b *testing.B) {
	evs := statetest.DemoEvents()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := New()
		for _, e := range evs {
			st.Apply(e)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(evs)), "ns/event")
}

// BenchmarkSnapshot is the cost of one frame's copy of a State that is as full as it gets (every cap reached), which is what a
// renderer pays per frame before it draws anything.
func BenchmarkSnapshot(b *testing.B) {
	st := New()
	st.ApplyAll(genEvents(30_000, 5))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = st.Snapshot()
	}
}

// BenchmarkSnapshotOfADemo is the same for the state of an ordinary swarm: eight agents.
func BenchmarkSnapshotOfADemo(b *testing.B) {
	st := New()
	st.ApplyAll(statetest.DemoEvents())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = st.Snapshot()
	}
}
