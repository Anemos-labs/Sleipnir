package state

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// reader is a goroutine that does what a renderer does: take a snapshot (or ask for a piece of the State), look at it, and keep it
// for a while. It checks what must hold of every snapshot whatever moment it was taken at, and then writes over its own copy: a
// snapshot that shared memory with the State would be written to while the State folds, which the race detector sees.
type reader struct {
	snaps atomic.Int64
	wg    *sync.WaitGroup
}

func (r *reader) run(t *testing.T, st *State, id int, start <-chan struct{}, stop <-chan struct{}, exactTokens bool) {
	defer r.wg.Done()
	<-start
	var lastSeq uint64
	var lastEvents int
	for {
		select {
		case <-stop:
			return
		default:
		}
		var sn *Snapshot
		switch id % 3 {
		case 0:
			sn = st.Snapshot()
		case 1:
			sn = st.SnapshotAt(st.Clock().Add(time.Duration(id) * time.Second))
		default:
			sn = &Snapshot{Agents: st.Agents(), Stats: st.Stats(), Seq: st.LastSeq(), Now: st.Clock()}
			_ = st.Main()
			_ = st.TTLRemaining(st.Clock())
			sn.Board = st.Snapshot().Board
		}
		if sn.Seq < lastSeq || sn.Stats.Events < lastEvents {
			t.Errorf("reader %d: time went backwards: seq %d after %d, %d events after %d", id, sn.Seq, lastSeq, sn.Stats.Events, lastEvents)
			return
		}
		lastSeq, lastEvents = sn.Seq, sn.Stats.Events
		if len(sn.Agents) > MaxAgents || len(sn.Feed) > FeedCap {
			t.Errorf("reader %d: %d agents, %d feed lines", id, len(sn.Agents), len(sn.Feed))
			return
		}
		var sum Tokens
		for _, a := range sn.Agents {
			if a.Hits.Len() != a.Requests {
				t.Errorf("reader %d: %s has %d requests and a history of %d: a snapshot is one moment of the State, not two", id, a.ID, a.Requests, a.Hits.Len())
				return
			}
			addTokens(&sum, a.Tokens)
		}
		if exactTokens && id%3 != 2 && sum != sn.Totals.Tokens {
			t.Errorf("reader %d: the agents' tokens %+v are not the total %+v", id, sum, sn.Totals.Tokens)
			return
		}
		if c := sn.Board.Counts; len(sn.Board.Tasks) != c.Todo+c.Running+c.Verifying+c.Merged+c.Failed {
			t.Errorf("reader %d: %d tasks in the columns %+v", id, len(sn.Board.Tasks), c)
			return
		}
		scribble(sn)
		r.snaps.Add(1)
	}
}

// scribble writes over everything in the snapshot that a State might be sharing with it.
func scribble(sn *Snapshot) {
	for i := range sn.Agents {
		a := &sn.Agents[i]
		for j := range a.Hits.Ratios {
			a.Hits.Ratios[j] = -1
		}
		for j := range a.Hits.Marks {
			a.Hits.Marks[j].Text = "scribble"
		}
		for j := range a.Stack.Sections {
			a.Stack.Sections[j].Tokens = -1
		}
		for j := range a.Compacts {
			a.Compacts[j].Before = -1
		}
		for j := range a.Anomalies {
			a.Anomalies[j].Kind = "scribble"
		}
		for j := range a.Leases {
			a.Leases[j] = "scribble"
		}
		for j := range a.Scope {
			a.Scope[j] = "scribble"
		}
		a.Stack.SystemHashes = append(a.Stack.SystemHashes[:0], "scribble")
	}
	for i := range sn.Feed {
		sn.Feed[i].Text = "scribble"
	}
	for i := range sn.Board.Tasks {
		sn.Board.Tasks[i].Title = "scribble"
		for j := range sn.Board.Tasks[i].Files {
			sn.Board.Tasks[i].Files[j] = "scribble"
		}
		for j := range sn.Board.Tasks[i].Deps {
			sn.Board.Tasks[i].Deps[j] = "scribble"
		}
	}
	for i := range sn.Board.Alerts {
		sn.Board.Alerts[i].Text = "scribble"
	}
	for i := range sn.Mail.Recent {
		sn.Mail.Recent[i].Summary = "scribble"
		for j := range sn.Mail.Recent[i].Origins {
			sn.Mail.Recent[i].Origins[j] = "scribble"
		}
	}
	for i := range sn.Merge.Recent {
		sn.Merge.Recent[i].Reason = "scribble"
		for j := range sn.Merge.Recent[i].Files {
			sn.Merge.Recent[i].Files[j] = "scribble"
		}
	}
	for i := range sn.Merge.Waiting {
		sn.Merge.Waiting[i].Reason = "scribble"
	}
	for i := range sn.Leases.Held {
		sn.Leases.Held[i].Path = "scribble"
	}
	for i := range sn.Perms.Pending {
		sn.Perms.Pending[i].Summary = "scribble"
		for j := range sn.Perms.Pending[i].Paths {
			sn.Perms.Pending[i].Paths[j] = "scribble"
		}
	}
	for i := range sn.Perms.Recent {
		sn.Perms.Recent[i].Reason = "scribble"
		for j := range sn.Perms.Recent[i].Ask.Paths {
			sn.Perms.Recent[i].Ask.Paths[j] = "scribble"
		}
	}
	for i := range sn.TTL {
		sn.TTL[i].Key = "scribble"
		for j := range sn.TTL[i].Agents {
			sn.TTL[i].Agents[j] = "scribble"
		}
	}
	for i := range sn.Prefixes {
		sn.Prefixes[i].Key = "scribble"
		for j := range sn.Prefixes[i].Agents {
			sn.Prefixes[i].Agents[j] = "scribble"
		}
	}
	for i := range sn.Anomalies {
		sn.Anomalies[i].Kind = "scribble"
	}
	for i := range sn.Models {
		sn.Models[i].ID = "scribble"
	}
	for i := range sn.Activity.Rows {
		for j := range sn.Activity.Rows[i].Levels {
			sn.Activity.Rows[i].Levels[j] = 8
		}
		for j := range sn.Activity.Rows[i].Marks {
			sn.Activity.Rows[i].Marks[j] = 15
		}
	}
	for k := range sn.Stats.UnknownTypes {
		sn.Stats.UnknownTypes[k] = -1
	}
}

// foldWhileRead folds the events in the test's goroutine, in batches, while readers take snapshots without pause; after each batch
// the writer waits until every reader has taken one more, so that the two are known to have overlapped and no reader is starved by
// the writer (neither is a timing: the wait is for a count). Then the State must be what a fold with nobody looking makes.
func foldWhileRead(t *testing.T, evs []events.Event, readers, batches int, exactTokens bool) {
	t.Helper()
	st := New()
	rs := make([]*reader, readers)
	var wg sync.WaitGroup
	start, stop := make(chan struct{}), make(chan struct{})
	for i := range rs {
		rs[i] = &reader{wg: &wg}
		wg.Add(1)
		go rs[i].run(t, st, i, start, stop, exactTokens)
	}
	close(start)
	deadline := time.After(2 * time.Minute) // a guard against a hang: a reader that died leaves the writer waiting
	batch := max(len(evs)/batches, 1)
	for len(evs) > 0 {
		n := min(batch, len(evs))
		before := make([]int64, readers)
		for i, r := range rs {
			before[i] = r.snaps.Load()
		}
		if len(evs)%2 == 0 {
			for _, e := range evs[:n] {
				st.Apply(e)
			}
		} else {
			st.ApplyAll(evs[:n])
		}
		evs = evs[n:]
		for i, r := range rs {
			for r.snaps.Load() == before[i] {
				select {
				case <-deadline:
					close(stop)
					wg.Wait()
					t.Fatalf("reader %d took no snapshot while the writer waited", i)
				default:
					runtime.Gosched() // let the readers run; nothing depends on how long that takes
				}
			}
		}
	}
	close(stop)
	wg.Wait()
	if p := st.Stats().Panics; p != 0 {
		t.Fatalf("%d panics: %s", p, st.Stats().LastPanic)
	}
	checkBounds(t, st)
}

func TestSnapshotsAreSafeWhileTheStateFolds(t *testing.T) {
	t.Run("the recorded demo", func(t *testing.T) {
		foldWhileRead(t, statetest.DemoEvents(), 4, 40, true)
	})
	t.Run("the hand built swarm", func(t *testing.T) {
		foldWhileRead(t, handBuiltSession(), 3, 40, false)
	})
	t.Run("a long generated log that fills every ring", func(t *testing.T) {
		n := 3000 // a snapshot of a full State is a megabyte, and each batch waits for one from every reader
		if testing.Short() {
			n = 1200
		}
		foldWhileRead(t, genEvents(n, 41), 2, 6, false)
	})
}

// What the readers saw does not change what the State is: folded with four goroutines looking, it is the State of a fold with
// nobody looking.
func TestAStateReadWhileItFoldsEndsUpTheSameAsOneThatWasNot(t *testing.T) {
	evs := statetest.DemoEvents()
	st := New()
	var wg sync.WaitGroup
	start, stop := make(chan struct{}), make(chan struct{})
	rs := make([]*reader, 4)
	for i := range rs {
		rs[i] = &reader{wg: &wg}
		wg.Add(1)
		go rs[i].run(t, st, i, start, stop, true)
	}
	close(start)
	for _, e := range evs {
		st.Apply(e)
	}
	close(stop)
	wg.Wait()
	if got, want := snapJSON(st), snapJSON(fold(t, evs...)); got != want {
		t.Fatalf("the readers changed the State:\n%s", firstDiff(want, got))
	}
}

// Reset while a renderer draws is what Follow does when a log is replaced: the snapshots taken across it are each one State or the
// other, never a mixture, and the State ends as a fresh fold of what came after.
func TestResetWhileSnapshotsAreTaken(t *testing.T) {
	evs := statetest.DemoEvents()
	st := New()
	var wg sync.WaitGroup
	start, stop := make(chan struct{}), make(chan struct{})
	rs := make([]*reader, 3)
	for i := range rs {
		rs[i] = &reader{wg: &wg}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				sn := st.Snapshot()
				for _, a := range sn.Agents {
					if a.Hits.Len() != a.Requests {
						t.Errorf("reader %d: %s has %d requests and a history of %d", i, a.ID, a.Requests, a.Hits.Len())
						return
					}
				}
				scribble(sn)
				rs[i].snaps.Add(1)
			}
		}(i)
	}
	close(start)
	const resets = 5
	for k := 0; k < resets; k++ {
		for _, e := range evs[:150] {
			st.Apply(e)
		}
		for _, r := range rs { // let each reader see the State that is about to be replaced
			for before := r.snaps.Load(); r.snaps.Load() == before; {
				runtime.Gosched()
			}
		}
		st.Reset()
	}
	for _, e := range evs {
		st.Apply(e)
	}
	close(stop)
	wg.Wait()
	want := fold(t, evs...).Snapshot()
	got := st.Snapshot()
	got.Stats.Resets = 0
	if compactJSON(got) != compactJSON(want) || st.Stats().Resets != resets {
		t.Errorf("after %d resets the State is not a fresh fold of what came after", resets)
	}
}

// Two goroutines that both apply events are not what the State is for (events have one order), but it must not fall over: nothing
// races, nothing panics, and every event is either applied or counted as stale.
func TestTwoWritersDoNotRaceOrPanic(t *testing.T) {
	evs := statetest.DemoEvents()
	st := New()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			if w == 0 {
				st.ApplyAll(evs)
				return
			}
			for _, e := range evs {
				st.Apply(e)
			}
		}(w)
	}
	close(start)
	wg.Wait()
	s := st.Stats()
	if s.Panics != 0 || s.Events+s.Stale != 2*len(evs) || s.Events < len(evs) {
		t.Errorf("stats %+v for 2 x %d events", s, len(evs))
	}
}
