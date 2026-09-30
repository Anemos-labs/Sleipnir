package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// fuzzPrefix is the session an event that the fuzzer makes falls into: the hand-built swarm up to its busiest moment, so that a
// fuzzed event finds agents, tasks, leases, merge entries, open tools and requests for its handler to work on (a handler that has
// nothing to look up is a handler that is barely exercised).
func fuzzPrefix() []events.Event {
	evs := handBuiltSession()
	for i, e := range evs {
		if e.Seq > 62 {
			return evs[:i]
		}
	}
	return evs
}

// FuzzApply folds one event of any type, agent, payload, seq and time into a State that already holds a swarm. Whatever it is, Apply
// must not panic (the barrier counts a panic: it is a bug), the snapshot must marshal, and every table must be inside its cap.
func FuzzApply(f *testing.F) {
	seen := map[string]int{}
	for _, e := range append(statetest.DemoEvents(), handBuiltSession()...) {
		if seen[e.Type] >= 2 {
			continue // a couple of each type seeds it; the fuzzer makes the rest
		}
		seen[e.Type]++
		f.Add(e.Type, e.Agent, string(e.Data), e.Seq, e.TS.UnixNano())
	}
	for _, g := range garbage {
		for _, typ := range []string{events.TypeModelRequest, events.TypeBoardOp, events.TypeMergeQueued, events.TypeLease, events.TypeCompactCommit, events.TypeMailSend} {
			f.Add(typ, "be-1", g, uint64(0), int64(0))
		}
	}
	prefix := fuzzPrefix()
	f.Fuzz(func(t *testing.T, typ, agent, data string, seq uint64, ts int64) {
		st := New()
		st.ApplyAll(prefix)
		if p := st.Stats().Panics; p != 0 {
			t.Fatalf("the prefix panicked: %s", st.Stats().LastPanic)
		}
		e := events.Event{Seq: seq, TS: time.Unix(0, ts).UTC(), Session: "s", Agent: agent, Type: typ, Data: json.RawMessage(data)}
		st.Apply(e)
		st.Apply(e) // the same again: a duplicate, or (without a seq) a second time
		sn := st.Snapshot()
		if s := st.Stats(); s.Panics != 0 {
			t.Fatalf("panic: %s", s.LastPanic)
		}
		if _, err := json.Marshal(sn); err != nil {
			t.Fatalf("the snapshot does not marshal: %v", err)
		}
		_ = st.SnapshotAt(sn.Clock.Add(time.Hour))
		checkBounds(t, st)
	})
}

// FuzzFold reads a log of any bytes: the loader must return (an error for a file it cannot read, nothing for damage it can skip),
// never panic, and what it makes of it must marshal.
func FuzzFold(f *testing.F) {
	good := logOf(handBuiltSession()[:30])
	f.Add([]byte(good))
	f.Add([]byte(good[:len(good)/2]))
	f.Add([]byte(good + `{"seq":99,"type":"model.request","data":{"req"`))
	f.Add([]byte(good + "\x00\xff garbage\n{}\n[]\n"))
	f.Add([]byte(""))
	f.Add([]byte("\n\n\n"))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := Fold(p)
		if err != nil {
			return // a log that cannot be read is an error, and says so
		}
		if s := st.Stats(); s.Panics != 0 {
			t.Fatalf("panic: %s", s.LastPanic)
		}
		if _, err := json.Marshal(st.Snapshot()); err != nil {
			t.Fatalf("the snapshot does not marshal: %v", err)
		}
		checkBounds(t, st)
	})
}
