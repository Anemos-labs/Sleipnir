package events

// Properties of the log under damage and replay, checked over every cut of a real
// log and over seeded random sequences of appends: what a crash can leave behind is
// repaired or reported, never silently lost, and what was written reads back the
// same however many times it is read.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// steppedClock is an injected clock: one second per reading from a fixed instant, so
// the bytes of a log are a function of what was emitted.
func steppedClock() func() time.Time {
	t := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

// sampleLog writes a log of varied events (kernel and agent events, causes, blob
// references, text with every kind of character JSON escapes, payloads of every JSON
// type, and none) and returns its bytes.
func sampleLog(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	l, err := Open(dir, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	l.SetClock(steppedClock())
	blobs, err := NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := blobs.Put([]byte("a blob body\nwith lines"))
	if err != nil {
		t.Fatal(err)
	}
	emit := func(agent, typ string, data any, opts ...Opt) uint64 {
		seq, err := l.Emit(agent, typ, data, opts...)
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	emit("", TypeSessionStart, map[string]any{"model": "m-1", "root": "/work/proj", "swarm": false})
	turn := emit("a1", TypeTurnAppend, map[string]any{"role": "user", "text": "héllo 世界 🐎 \"quoted\" \\ slash\nnew line\ttab \u2028 sep <tag> & amp"})
	call := emit("a1", TypeToolCall, map[string]any{"name": "read", "input": json.RawMessage(`{"path":"a.go","lines":[1,2,3]}`)}, Cause(turn))
	emit("a1", TypeToolResult, map[string]any{"blob": string(h), "bytes": 22, "is_error": false}, Cause(call))
	emit("a2", TypeModelResponse, map[string]any{"usage": map[string]any{"input_tokens": 12, "cost": 0.000123}, "hit_ratio": 0.5})
	emit("", TypeOutcome, nil)
	emit("a2", "custom", json.RawMessage(`"a bare string payload"`))
	emit("a2", "custom", json.RawMessage(`[1,[2,[3]],{"k":null}]`))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// logLine is one line of a log: where it starts, where its JSON ends (the newline is
// the next byte) and what it decodes to.
type logLine struct {
	start, contentEnd int
	ev                Event
}

func parseLines(t *testing.T, data []byte) []logLine {
	t.Helper()
	var out []logLine
	start := 0
	for start < len(data) {
		nl := bytes.IndexByte(data[start:], '\n')
		if nl < 0 {
			t.Fatalf("the sample log must end with a newline")
		}
		var e Event
		if err := json.Unmarshal(data[start:start+nl], &e); err != nil {
			t.Fatalf("line at %d: %v", start, err)
		}
		out = append(out, logLine{start, start + nl, e})
		start += nl + 1
	}
	return out
}

func scanAll(path string) ([]Event, error) {
	var evs []Event
	err := Scan(path, func(e Event) error {
		evs = append(evs, e)
		return nil
	})
	return evs, err
}

// A crash can cut the log at any byte, and some file systems then leave the rest of
// the extent as zeros. For every cut of a real log, with and without such a tail:
//
//   - reading it never fails and delivers every event that is whole, in order, and
//     nothing else (Scan ignores a torn tail: it is what a crash leaves);
//   - opening it repairs it, and says what it found: the torn bytes are counted in
//     Recovery, no line is reported as corrupt, and a final event that lost only its
//     newline is kept, not torn;
//   - appending then continues the sequence without a gap, and the result reads back as
//     the events that were whole followed by the new one.
//
// Closing a log syncs it, which is what makes a full open, append and close at every one
// of the log's bytes cost seconds (and more where a sync is slow). So every cut is read
// back (Scan) with and without a tail of zeros, and the pure scan that Open is built on
// (scanLog, below) is checked at every cut with every length of tail; the full cycle runs
// at every cut in the first lines, around every newline, where the cases change, and at
// every fifth byte elsewhere, a few at a time so that the syncs overlap.
func TestTruncatedLogAtEveryByteOffset(t *testing.T) {
	data := sampleLog(t)
	lines := parseLines(t, data)
	if len(lines) < 8 {
		t.Fatalf("sample log has %d lines", len(lines))
	}
	var cuts []cut
	for k := 0; k <= len(data); k++ {
		repair := k%5 == 0 || k <= lines[2].contentEnd
		for _, ln := range lines {
			if k >= ln.contentEnd-3 && k <= ln.contentEnd+4 {
				repair = true
			}
		}
		cuts = append(cuts, cut{k: k, repair: repair, second: k%4 == 0})
		cuts = append(cuts, cut{k: k, pad: 1 + k%5, repair: repair && k%3 == 0})
	}

	const workers = 4
	jobs := make(chan cut)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures []string
	)
	for w := 0; w < workers; w++ {
		workDir := t.TempDir()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if err := checkTruncation(workDir, data, lines, c); err != nil {
					mu.Lock()
					if len(failures) < 5 {
						failures = append(failures, err.Error())
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, c := range cuts {
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	for _, f := range failures {
		t.Error(f)
	}
}

// scanLog is what Open reads a log with. At every cut of the log, with every length of
// tail of zeros, it finds exactly the events that are whole, counts exactly the bytes
// that are torn, reports no corrupt line, and says where appending resumes.
func TestScanLogAtEveryCut(t *testing.T) {
	data := sampleLog(t)
	lines := parseLines(t, data)
	for k := 0; k <= len(data); k++ {
		for pad := 0; pad <= 6; pad++ {
			content := append(append([]byte(nil), data[:k]...), bytes.Repeat([]byte{0}, pad)...)
			sc, err := scanLog(bytes.NewReader(content))
			if err != nil {
				t.Fatalf("cut %d+%d: %v", k, pad, err)
			}
			keep, last, lost := cutState(lines, k, pad)
			if sc.size != int64(keep) || sc.TornBytes != int64(len(content)-keep) || sc.last != last || sc.needNewline != lost || sc.CorruptLines != 0 {
				t.Fatalf("cut %d+%d: scanLog = %+v, want size %d torn %d last %d newline %v", k, pad, sc, keep, len(content)-keep, last, lost)
			}
		}
	}
}

// cutState says what is left of a log cut at k with pad zeros after it: how many bytes
// stay, the last whole sequence number, and whether the final event lost its newline.
func cutState(lines []logLine, k, pad int) (keep int, last uint64, lostNewline bool) {
	for _, ln := range lines {
		switch {
		case ln.contentEnd < k: // its newline is there too
			keep, last, lostNewline = ln.contentEnd+1, ln.ev.Seq, false
		case ln.contentEnd == k && pad == 0: // a whole event that lost only its newline
			keep, last, lostNewline = ln.contentEnd, ln.ev.Seq, true
		}
	}
	return keep, last, lostNewline
}

// cut is one damaged copy of the sample log: the log cut at k, then pad zeros. Scan is
// always checked; repair adds opening, appending and reading it again, and second that
// a second open finds nothing left to repair.
type cut struct {
	k, pad         int
	repair, second bool
}

// checkTruncation checks one cut in a scratch directory under workDir and reports the
// first thing that is wrong with it. It is safe to run in several goroutines.
func checkTruncation(workDir string, data []byte, lines []logLine, c cut) (err error) {
	k, pad := c.k, c.pad
	defer func() {
		if err != nil {
			err = fmt.Errorf("cut at byte %d of %d, %d zeros after it: %w", k, len(data), pad, err)
		}
	}()
	content := append(append([]byte(nil), data[:k]...), bytes.Repeat([]byte{0}, pad)...)

	// What is whole, and what is torn, by the definition in the package comment.
	var whole []Event
	for _, ln := range lines {
		if ln.contentEnd < k || (ln.contentEnd == k && pad == 0) {
			whole = append(whole, ln.ev)
		}
	}
	keep, _, lostNewline := cutState(lines, k, pad)
	torn := len(content) - keep

	dir, err := os.MkdirTemp(workDir, "cut-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}

	got, err := scanAll(path)
	if err != nil {
		return fmt.Errorf("Scan: %w", err)
	}
	if !reflect.DeepEqual(got, whole) && !(len(got) == 0 && len(whole) == 0) {
		return fmt.Errorf("Scan delivered %d events, want the %d whole ones", len(got), len(whole))
	}
	if !c.repair {
		return nil
	}

	l, err := Open(dir, "sess-1")
	if err != nil {
		return fmt.Errorf("Open: %w", err)
	}
	if rec := l.Recovery(); rec != (Recovery{TornBytes: int64(torn)}) {
		l.Close()
		return fmt.Errorf("Recovery = %+v, want %d torn bytes and nothing else", rec, torn)
	}
	l.SetClock(steppedClock())
	seq, err := l.Emit("a", "after", map[string]int{"k": k})
	if err != nil {
		l.Close()
		return fmt.Errorf("Emit: %w", err)
	}
	opened := 0 // events Open wrote itself: a log with nothing whole starts again
	if len(whole) == 0 {
		opened = 1
	}
	if want := uint64(len(whole) + opened + 1); seq != want {
		l.Close()
		return fmt.Errorf("the appended event has seq %d, want %d", seq, want)
	}
	if err := l.Close(); err != nil {
		return fmt.Errorf("Close: %w", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	wantPrefix := content[:keep]
	if lostNewline {
		wantPrefix = append(append([]byte(nil), wantPrefix...), '\n')
	}
	if !bytes.HasPrefix(after, wantPrefix) {
		return errors.New("the repaired log does not start with the whole lines")
	}
	if after[len(after)-1] != '\n' {
		return errors.New("the log does not end with a newline")
	}
	got, err = scanAll(path)
	if err != nil {
		return fmt.Errorf("Scan after the repair: %w", err)
	}
	if len(got) != len(whole)+opened+1 {
		return fmt.Errorf("after the repair Scan delivers %d events, want %d", len(got), len(whole)+opened+1)
	}
	for i, e := range got {
		if e.Seq != uint64(i)+1 {
			return fmt.Errorf("event %d has seq %d: the sequence has a gap", i, e.Seq)
		}
	}
	if last := got[len(got)-1]; last.Type != "after" || !bytes.Equal(last.Data, []byte(fmt.Sprintf(`{"k":%d}`, k))) {
		return fmt.Errorf("the last event is %+v, not the appended one", last)
	}
	if opened == 0 && !reflect.DeepEqual(got[:len(whole)], whole) {
		return errors.New("an event that was whole before the repair changed")
	}
	if !c.second {
		return nil
	}
	// What Open repaired is repaired: a second open has nothing to report.
	l2, err := Open(dir, "sess-1")
	if err != nil {
		return fmt.Errorf("second Open: %w", err)
	}
	defer l2.Close()
	if rec := l2.Recovery(); rec != (Recovery{}) {
		return fmt.Errorf("a repaired log reported damage again: %+v", rec)
	}
	return nil
}

// replayState is what a consumer rebuilds from a log.
type replayState struct {
	Events []Event
	ByType map[string]int
	Blobs  map[core.Hash]int
}

// replay reads a log back as a consumer would: every event, counted by type, and every
// blob an event refers to, fetched and checked against its name.
func replay(t *testing.T, dir string) replayState {
	t.Helper()
	blobs, err := NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	st := replayState{ByType: map[string]int{}, Blobs: map[core.Hash]int{}}
	err = Scan(filepath.Join(dir, "events.jsonl"), func(e Event) error {
		st.Events = append(st.Events, e)
		st.ByType[e.Type]++
		var d struct {
			Blob string `json:"blob"`
		}
		if json.Unmarshal(e.Data, &d) == nil && d.Blob != "" {
			b, err := blobs.Get(core.Hash(d.Blob))
			if err != nil {
				return fmt.Errorf("event %d refers to a blob that does not read back: %w", e.Seq, err)
			}
			if core.HashBytes(b) != core.Hash(d.Blob) {
				return fmt.Errorf("blob %s reads back as other bytes", d.Blob)
			}
			st.Blobs[core.Hash(d.Blob)] = len(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return st
}

// randomRun performs a seeded random sequence of appends (events with causes and
// random payloads, blobs stored and referenced, repeated content, flushes, and
// closing and reopening the log in the middle) in a fresh directory, and returns the
// directory and the sequence numbers Emit handed out.
func randomRun(t *testing.T, seed int64) (string, []uint64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	dir := t.TempDir()
	open := func() *Log {
		l, err := Open(dir, "sess-r")
		if err != nil {
			t.Fatal(err)
		}
		l.SetClock(steppedClock())
		return l
	}
	l := open()
	blobs, err := NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	agents := []string{"", "a1", "a2", "mgr"}
	types := []string{TypeTurnAppend, TypeToolCall, TypeToolResult, TypeModelResponse, TypeBoardOp, TypeMailSend, "custom"}
	words := []string{"alpha", "世界", "🐎", "tab\there", "line\nbreak", "quote\"", "back\\slash", "<&>", "\u2028", "é", ""}
	var seqs []uint64
	var stored [][]byte
	var randValue func(depth int) any
	randValue = func(depth int) any {
		switch n := rng.Intn(7); {
		case n == 0:
			return rng.Intn(1 << 20)
		case n == 1:
			return rng.Float64()
		case n == 2:
			return words[rng.Intn(len(words))]
		case n == 3:
			return rng.Intn(2) == 0
		case n == 4 && depth < 3:
			arr := make([]any, rng.Intn(4))
			for i := range arr {
				arr[i] = randValue(depth + 1)
			}
			return arr
		case n == 5 && depth < 3:
			m := map[string]any{}
			for i := rng.Intn(4); i > 0; i-- {
				m[words[rng.Intn(len(words))]+fmt.Sprint(rng.Intn(5))] = randValue(depth + 1)
			}
			return m
		}
		return nil
	}
	for i := 0; i < 160; i++ {
		var data any = map[string]any{"v": randValue(0)}
		var opts []Opt
		if len(seqs) > 0 && rng.Intn(4) == 0 {
			opts = append(opts, Cause(seqs[rng.Intn(len(seqs))]))
		}
		switch r := rng.Intn(100); {
		case r < 25: // a blob, new or repeated, and the event that refers to it
			var body []byte
			if len(stored) > 0 && rng.Intn(3) == 0 {
				body = stored[rng.Intn(len(stored))]
			} else {
				body = make([]byte, rng.Intn(3000))
				rng.Read(body)
				stored = append(stored, body)
			}
			h, err := blobs.Put(body)
			if err != nil {
				t.Fatal(err)
			}
			data = map[string]any{"blob": string(h), "bytes": len(body)}
		case r < 30:
			if err := l.Flush(); err != nil {
				t.Fatal(err)
			}
			continue
		case r < 33: // resume: close, open again, carry on counting
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			l = open()
			continue
		}
		seq, err := l.Emit(agents[rng.Intn(len(agents))], types[rng.Intn(len(types))], data, opts...)
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, seqs
}

// normalised drops what a run cannot control: the timestamp of the events Open writes
// itself, which come from the wall clock before a clock can be injected.
func normalised(evs []Event) []Event {
	out := make([]Event, len(evs))
	for i, e := range evs {
		if e.Type == "log.open" || e.Type == TypeLogCorrupt {
			e.TS = time.Time{}
		}
		out[i] = e
	}
	return out
}

// The same sequence of appends, run twice in two directories, reads back as the same
// events (their order, sequence numbers, causes, timestamps and payloads), and every
// blob they refer to reads back as the bytes that were stored; reading one log twice
// gives one answer. Nothing a path, a process or a map's iteration order could change
// gets into what is replayed.
func TestReplayOfTheSameAppendsIsTheSameState(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 7, 42, 1009, 65537} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			dirA, seqsA := randomRun(t, seed)
			dirB, seqsB := randomRun(t, seed)
			if !reflect.DeepEqual(seqsA, seqsB) {
				t.Fatalf("the two runs handed out different sequence numbers")
			}
			for i, s := range seqsA {
				if i > 0 && s <= seqsA[i-1] {
					t.Fatalf("sequence numbers do not grow: %d after %d", s, seqsA[i-1])
				}
			}
			a1, a2, b := replay(t, dirA), replay(t, dirA), replay(t, dirB)
			if !reflect.DeepEqual(a1, a2) {
				t.Fatalf("reading the same log twice gave two different states")
			}
			if !reflect.DeepEqual(normalised(a1.Events), normalised(b.Events)) || !reflect.DeepEqual(a1.ByType, b.ByType) || !reflect.DeepEqual(a1.Blobs, b.Blobs) {
				t.Fatalf("two runs of the same appends replay differently: %d events / %d blobs vs %d events / %d blobs", len(a1.Events), len(a1.Blobs), len(b.Events), len(b.Blobs))
			}
			if len(a1.Events) < 100 || len(a1.Blobs) == 0 {
				t.Fatalf("the run is too small to mean anything: %d events, %d blobs", len(a1.Events), len(a1.Blobs))
			}
			// Every event Emit acknowledged is in the log, in that order, and the log is gapless.
			var got []uint64
			for _, e := range a1.Events {
				got = append(got, e.Seq)
			}
			for i, s := range got {
				if s != uint64(i)+1 {
					t.Fatalf("event %d has seq %d", i, s)
				}
			}
			for _, s := range seqsA {
				if s > uint64(len(got)) {
					t.Fatalf("Emit handed out seq %d but the log has %d events", s, len(got))
				}
			}
		})
	}
}

// Reopening a log that was written and closed cleanly finds nothing to repair and
// reads back the same events.
func TestReopeningACleanLogChangesNothing(t *testing.T) {
	dir, _ := randomRun(t, 11)
	before, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, "sess-r")
	if err != nil {
		t.Fatal(err)
	}
	if rec := l.Recovery(); rec != (Recovery{}) {
		t.Fatalf("Recovery = %+v", rec)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatalf("opening and closing a clean log changed it")
	}
}
