package state

import (
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
)

// snapJSON is the State as a string, for comparing two States: the snapshot at the State's own clock, which is what every fold
// of the same events must agree on. (Compact: the snapshot of a full State is a megabyte, and indenting it costs more than the
// fold it checks.)
func snapJSON(st *State) string { return compactJSON(st.Snapshot()) }

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "unmarshalable: " + err.Error()
	}
	return string(b)
}

// foldChunked folds the events in chunks of random sizes, taking snapshots between them (a renderer doing its job must not change
// what the next event finds), and using Apply for some chunks and ApplyAll for the others.
func foldChunked(t testing.TB, evs []events.Event, seed int64) *State {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	st := New()
	for len(evs) > 0 {
		n := 1 + r.Intn(40)
		if r.Intn(10) == 0 {
			n = 1 + r.Intn(3000)
		}
		n = min(n, len(evs))
		chunk := evs[:n]
		evs = evs[n:]
		if r.Intn(2) == 0 {
			for _, e := range chunk {
				st.Apply(e)
			}
		} else {
			st.ApplyAll(chunk)
		}
		switch r.Intn(3) {
		case 0:
			_ = st.Snapshot()
		case 1:
			_ = st.SnapshotAt(st.Clock().Add(sec(r.Intn(500))))
		}
	}
	if p := st.Stats().Panics; p != 0 {
		t.Fatalf("%d panics: %s", p, st.Stats().LastPanic)
	}
	return st
}

// Folding the log in one go equals folding it in any chunking, with snapshots taken in between, equals folding it after a JSON round
// trip of each event, and a replay of it changes nothing.
func TestFoldIsTheSameWhateverTheChunkingAndTheRoundTrip(t *testing.T) {
	big := 12000 // enough to fill every ring and table, which a short log does not
	if testing.Short() {
		big = 4000
	}
	logs := map[string][]events.Event{
		"generated 3k":      genEvents(3000, 1),
		"generated big":     genEvents(big, 2),
		"small":             genEvents(120, 3),
		"richly hand built": handBuiltSession(),
	}
	for name, evs := range logs {
		t.Run(name, func(t *testing.T) {
			whole := fold(t, evs...)
			want := snapJSON(whole)
			seeds := int64(3)
			if len(evs) > 5000 {
				seeds = 1 // a long log is folded several times as it is; one chunking of it is enough
			}
			for seed := int64(1); seed <= seeds; seed++ {
				if got := snapJSON(foldChunked(t, evs, seed)); got != want {
					t.Fatalf("chunking %d gives another state:\n%s", seed, firstDiff(want, got))
				}
			}
			if got := snapJSON(fold(t, roundTrip(evs)...)); got != want {
				t.Fatalf("a JSON round trip of every event gives another state:\n%s", firstDiff(want, got))
			}
			// SnapshotAt an earlier moment never changes the State, and the State's own snapshot is the same as one taken at its clock.
			if got := compactJSON(whole.SnapshotAt(whole.Clock())); got != want {
				t.Fatal("Snapshot and SnapshotAt(clock) disagree")
			}
			_ = whole.SnapshotAt(whole.Clock().Add(-sec(3600)))
			if snapJSON(whole) != want {
				t.Fatal("asking for a snapshot changed the State")
			}
			// Feeding the same events again (a replay) changes nothing but the count of the ones it ignored.
			st := fold(t, evs...)
			apply(t, st, evs...)
			a, b := st.Snapshot(), whole.Snapshot()
			a.Stats.Stale = 0
			if compactJSON(a) != compactJSON(b) {
				t.Fatal("a replay of the same events changed the State")
			}
			if st.Stats().Stale == 0 {
				t.Fatal("the replay was not recognised")
			}
		})
	}
}

// firstDiff names where two long strings first differ.
func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := max(i-200, 0)
	return "want …" + a[lo:min(i+200, len(a))] + "\ngot  …" + b[lo:min(i+200, len(b))]
}

// Two States fed the same events hold the same data though they were built in different goroutines' order of snapshots: the
// fold is a function of the events alone. (A fold that depended on a map's iteration order would fail this, in time.)
func TestFoldIsDeterministicAcrossManyRuns(t *testing.T) {
	evs := genEvents(4000, 11)
	want := snapJSON(fold(t, evs...))
	for i := 0; i < 5; i++ {
		if got := snapJSON(fold(t, evs...)); got != want {
			t.Fatalf("run %d differs:\n%s", i, firstDiff(want, got))
		}
	}
}
