package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// FuzzTail appends any bytes to a log that a Tail is following, piece by piece (a poll after each), and then reads the same file
// whole with events.Scan. Tail is a reader of the log like Scan and must agree with it about what is an event:
//
//   - when the pieces end at line boundaries (what a writer that appends whole lines shows a reader) the two deliver exactly the
//     same events in the same order, and skip the same number of damaged lines;
//   - when they are cut anywhere, everything Scan delivers the tail delivers too, in order, each line once, and it skips no more
//     lines than Scan. (It may deliver more: an event is delivered when its closing brace arrives, without waiting for its newline,
//     and a line that turns out to have more after it is damage to Scan. No writer of the log does that.)
//
// Every step is a tick the test sends and the poll it waits for, so nothing in it is timed.
func FuzzTail(f *testing.F) {
	good := logOf(handBuiltSession()[:14])
	f.Add([]byte(good), uint8(7), true)
	f.Add([]byte(good), uint8(1), false)
	f.Add([]byte(good+`{"seq":99,"type":"model.request","data":{"req"`), uint8(3), false)
	f.Add([]byte("\n\n{}\n[]\nnot json\n"+lineOf(evAt(1))), uint8(2), true)
	f.Add([]byte(lineOf(evAt(1))+`{"seq":9007199254740993,"type":"x"}`+"\n"+lineOf(evAt(2))), uint8(5), true)
	f.Add([]byte(`{"seq":1,"type":"x"} trailing`+"\n"+lineOf(evAt(2))), uint8(1), false)
	f.Add([]byte("\x00\xff{\"seq\":1}\r\n{\"seq\":2,\"ts\":\"x\"}\n"), uint8(4), false)
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte, piece uint8, aligned bool) {
		if len(data) > 4096 {
			data = data[:4096] // a step per piece: keep an input cheap
		}
		path := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		r := startTail(t, path, TailOptions{})
		r.firstPoll()
		n := int(piece)%61 + 1
		var pieces [][]byte
		if aligned {
			// n lines at a time; a last line with no newline comes alone, at the end.
			lines := bytes.SplitAfter(data, []byte("\n"))
			for len(lines) > 0 {
				k := min(n, len(lines))
				pieces = append(pieces, bytes.Join(lines[:k], nil))
				lines = lines[k:]
			}
		} else {
			for rest := data; len(rest) > 0; {
				k := min(n, len(rest))
				pieces = append(pieces, rest[:k])
				rest = rest[k:]
			}
		}
		for _, p := range pieces {
			if len(p) > 0 {
				appendTo(t, path, string(p))
			}
			r.step()
		}
		last := r.step()

		var scanned []events.Event
		err := events.Scan(path, func(e events.Event) error { scanned = append(scanned, e); return nil })
		scanBad := 0
		var ce *events.CorruptError
		if errors.As(err, &ce) {
			scanBad = ce.Lines
		} else if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		r.mu.Lock()
		got := append([]events.Event(nil), r.all...)
		resets := r.resets
		r.mu.Unlock()
		if resets != 0 {
			t.Fatalf("a log that only grew was taken for another one %d times", resets)
		}
		if aligned {
			if !sameEvents(got, scanned) || last.Corrupt != scanBad {
				t.Fatalf("Scan delivers %d events and skips %d lines, the tail %d and %d (whole lines appended %d at a time)", len(scanned), scanBad, len(got), last.Corrupt, n)
			}
			return
		}
		// Scan's events are among the tail's, in order.
		at := 0
		for _, want := range scanned {
			for at < len(got) && !reflect.DeepEqual(got[at], want) {
				at++
			}
			if at == len(got) {
				t.Fatalf("Scan delivers %d events, and the tail's %d do not hold them all in order (the log was cut in pieces of %d)", len(scanned), len(got), n)
			}
			at++
		}
		if last.Corrupt > scanBad {
			t.Fatalf("the tail skips %d lines as damage and Scan only %d", last.Corrupt, scanBad)
		}
	})
}

func sameEvents(a, b []events.Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
