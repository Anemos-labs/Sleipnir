package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// hang is the most a test waits for a tail to answer: a guard against a hang, never a timing.
const hang = 2 * time.Minute

// tailRig drives a Tail with a ticker the test owns and waits on its OnPoll as a barrier: there is no sleeping and no clock in it.
type tailRig struct {
	t      *testing.T
	path   string
	tick   chan time.Time
	polled chan PollInfo
	done   chan error
	cancel context.CancelFunc

	mu     sync.Mutex
	got    []events.Event // what was delivered since the last reset
	all    []events.Event // everything that was ever delivered, in order
	resets int
	last   PollInfo
	failOn func(events.Event) error
}

func startTail(t *testing.T, path string, opt TailOptions) *tailRig {
	t.Helper()
	r := &tailRig{t: t, path: path, tick: make(chan time.Time), polled: make(chan PollInfo), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	opt.Tick = r.tick
	userReset := opt.OnReset
	opt.OnReset = func() {
		r.mu.Lock()
		r.resets++
		r.got = nil // what was delivered before is void
		r.mu.Unlock()
		if userReset != nil {
			userReset()
		}
	}
	opt.OnPoll = func(p PollInfo) {
		r.mu.Lock()
		r.last = p
		r.mu.Unlock()
		select {
		case r.polled <- p:
		case <-ctx.Done():
		}
	}
	go func() {
		r.done <- Tail(ctx, path, func(e events.Event) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.failOn != nil {
				if err := r.failOn(e); err != nil {
					return err
				}
			}
			r.got = append(r.got, e)
			r.all = append(r.all, e)
			return nil
		}, opt)
	}()
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

// firstPoll waits for the poll that Tail makes before it waits for any tick.
func (r *tailRig) firstPoll() PollInfo {
	r.t.Helper()
	select {
	case p := <-r.polled:
		return p
	case <-time.After(hang):
		r.t.Fatal("the tail never polled")
		return PollInfo{}
	}
}

// step is one tick and the poll it causes.
func (r *tailRig) step() PollInfo {
	r.t.Helper()
	select {
	case r.tick <- time.Time{}:
	case <-time.After(hang):
		r.t.Fatal("the tail did not take a tick")
	}
	select {
	case p := <-r.polled:
		return p
	case <-time.After(hang):
		r.t.Fatal("the tail did not answer a tick")
		return PollInfo{}
	}
}

func (r *tailRig) seqs() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return seqList(r.got)
}

// everything is the seqs of every event delivered, resets or not.
func (r *tailRig) everything() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return seqList(r.all)
}

func seqList(evs []events.Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, fmt.Sprint(e.Seq))
	}
	return strings.Join(s, ",")
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func evAt(seq uint64) events.Event {
	return events.Event{Seq: seq, TS: statetest.Epoch.Add(time.Duration(seq) * time.Second), Session: "t", Agent: "a", Type: events.TypeToolCall,
		Data: []byte(fmt.Sprintf(`{"id":"c%d","name":"bash"}`, seq))}
}

func TestTailDeliversWhatIsThereAndWhatIsAppendedInOrder(t *testing.T) {
	path := writeLog(t, lineOf(evAt(1))+lineOf(evAt(2)))
	r := startTail(t, path, TailOptions{})
	if p := r.firstPoll(); p.Delivered != 2 || p.Total != 2 || p.Pending != 0 || p.Corrupt != 0 {
		t.Errorf("first poll %+v", p)
	}
	if got := r.seqs(); got != "1,2" {
		t.Fatalf("%s", got)
	}
	if p := r.step(); p.Delivered != 0 {
		t.Errorf("nothing new: %+v", p)
	}
	appendTo(t, path, lineOf(evAt(3))+lineOf(evAt(4))+lineOf(evAt(5)))
	if p := r.step(); p.Delivered != 3 || p.Total != 5 {
		t.Errorf("%+v", p)
	}
	appendTo(t, path, lineOf(evAt(6)))
	r.step()
	if got := r.seqs(); got != "1,2,3,4,5,6" {
		t.Errorf("%s", got)
	}
}

func TestTailHoldsBackATornLineUntilItIsWhole(t *testing.T) {
	path := writeLog(t, "")
	r := startTail(t, path, TailOptions{})
	r.firstPoll()
	line := lineOf(evAt(1))
	cut := len(line) / 2
	appendTo(t, path, line[:cut])
	if p := r.step(); p.Delivered != 0 || p.Pending != cut || r.seqs() != "" {
		t.Errorf("a half line must not be delivered: %+v %q", p, r.seqs())
	}
	appendTo(t, path, line[cut:len(line)-5])
	if p := r.step(); p.Delivered != 0 || p.Pending != len(line)-5 {
		t.Errorf("still not whole: %+v", p)
	}
	appendTo(t, path, line[len(line)-5:]+lineOf(evAt(2)))
	if p := r.step(); p.Delivered != 2 || p.Pending != 0 || p.Corrupt != 0 {
		t.Errorf("%+v", p)
	}
	r.mu.Lock()
	first := r.got[0]
	r.mu.Unlock()
	if first.Seq != 1 || string(first.Data) != string(evAt(1).Data) {
		t.Errorf("the event that was torn arrived intact: %+v", first)
	}
}

func TestTailDeliversAWholeEventAtOnceAndItsNewlineIsNotAnotherLine(t *testing.T) {
	path := writeLog(t, "")
	r := startTail(t, path, TailOptions{})
	r.firstPoll()
	line := lineOf(evAt(1))
	appendTo(t, path, line[:len(line)-1]) // the event, without its newline yet
	if p := r.step(); p.Delivered != 1 || p.Pending != 0 || r.seqs() != "1" {
		t.Errorf("a line that is a whole event is delivered without waiting: %+v", p)
	}
	if p := r.step(); p.Delivered != 0 {
		t.Errorf("once only: %+v", p)
	}
	appendTo(t, path, "\n")
	if p := r.step(); p.Delivered != 0 || p.Corrupt != 0 || r.seqs() != "1" {
		t.Errorf("the newline of a delivered line is not a corrupt empty line: %+v", p)
	}
	appendTo(t, path, lineOf(evAt(2)))
	if p := r.step(); p.Delivered != 1 || r.seqs() != "1,2" {
		t.Errorf("%+v", p)
	}
}

func TestTailSkipsWhatIsNotAnEventAndCountsIt(t *testing.T) {
	path := writeLog(t, "")
	r := startTail(t, path, TailOptions{})
	r.firstPoll()
	appendTo(t, path, lineOf(evAt(1))+"not json\n"+`{"seq":0,"type":"x"}`+"\n\n"+lineOf(evAt(2)))
	p := r.step()
	if p.Delivered != 2 || p.Corrupt != 3 || r.seqs() != "1,2" {
		t.Errorf("%+v %s", p, r.seqs())
	}
}

func TestTailStartsAgainWhenTheLogIsTruncatedOrReplaced(t *testing.T) {
	t.Run("truncated", func(t *testing.T) {
		path := writeLog(t, lineOf(evAt(1))+lineOf(evAt(2))+lineOf(evAt(3)))
		r := startTail(t, path, TailOptions{})
		r.firstPoll()
		if err := os.WriteFile(path, []byte(lineOf(evAt(10))), 0o600); err != nil { // shorter: a new log in place of the old
			t.Fatal(err)
		}
		p := r.step()
		r.mu.Lock()
		resets, got := r.resets, len(r.got)
		r.mu.Unlock()
		if resets != 1 || p.Resets != 1 || got != 1 || p.Total != 1 || r.seqs() != "10" {
			t.Errorf("resets %d, %+v, %s", resets, p, r.seqs())
		}
		appendTo(t, path, lineOf(evAt(11)))
		r.step()
		if r.seqs() != "10,11" {
			t.Errorf("%s", r.seqs())
		}
	})
	t.Run("replaced by another file", func(t *testing.T) {
		path := writeLog(t, lineOf(evAt(1))+lineOf(evAt(2)))
		r := startTail(t, path, TailOptions{})
		r.firstPoll()
		// The old file gets one more event after it was moved away, and the new file starts at the top of a longer history.
		old := path + ".1"
		if err := os.Rename(path, old); err != nil {
			t.Skipf("cannot rename an open file here: %v", err)
		}
		appendTo(t, old, lineOf(evAt(3)))
		appendTo(t, path, lineOf(evAt(1))+lineOf(evAt(2))+lineOf(evAt(3))+lineOf(evAt(4)))
		p := r.step()
		r.mu.Lock()
		resets := r.resets
		r.mu.Unlock()
		if resets != 1 || p.Resets != 1 || r.seqs() != "1,2,3,4" {
			t.Errorf("resets %d %+v %s", resets, p, r.seqs())
		}
		if got := r.everything(); got != "1,2,3,1,2,3,4" {
			t.Errorf("the old file is read to its end before the new one is started: %s", got)
		}
	})
	t.Run("truncated and grown past where it was", func(t *testing.T) {
		// Its size alone would say that it only grew; its bytes say that it is another log.
		path := writeLog(t, lineOf(evAt(1))+lineOf(evAt(2)))
		r := startTail(t, path, TailOptions{})
		r.firstPoll()
		if err := os.WriteFile(path, []byte(lineOf(evAt(10))+lineOf(evAt(11))+lineOf(evAt(12))+lineOf(evAt(13))), 0o600); err != nil {
			t.Fatal(err)
		}
		p := r.step()
		r.mu.Lock()
		resets := r.resets
		r.mu.Unlock()
		if resets != 1 || p.Resets != 1 || p.Total != 4 || r.seqs() != "10,11,12,13" {
			t.Errorf("resets %d %+v %s", resets, p, r.seqs())
		}
	})
	t.Run("rewritten in place to the very same length", func(t *testing.T) {
		path := writeLog(t, lineOf(evAt(1))+lineOf(evAt(2)))
		r := startTail(t, path, TailOptions{})
		r.firstPoll()
		if err := os.WriteFile(path, []byte(lineOf(evAt(3))+lineOf(evAt(4))), 0o600); err != nil {
			t.Fatal(err)
		}
		p := r.step()
		if p.Resets != 1 || r.seqs() != "3,4" {
			t.Errorf("%+v %s", p, r.seqs())
		}
	})
	t.Run("the writer of a resumed log removed a torn tail", func(t *testing.T) {
		// events.Open truncates a final line that a crash cut short before it appends. That is not a new log: nothing is reset.
		whole := lineOf(evAt(1)) + lineOf(evAt(2))
		torn := lineOf(evAt(3))[:20]
		path := writeLog(t, whole+torn)
		r := startTail(t, path, TailOptions{})
		if p := r.firstPoll(); p.Delivered != 2 || p.Pending != len(torn) {
			t.Fatalf("%+v", p)
		}
		if err := os.Truncate(path, int64(len(whole))); err != nil {
			t.Fatal(err)
		}
		appendTo(t, path, lineOf(evAt(3))+lineOf(evAt(4)))
		p := r.step()
		r.mu.Lock()
		resets := r.resets
		r.mu.Unlock()
		if resets != 0 || p.Resets != 0 || r.seqs() != "1,2,3,4" || p.Pending != 0 {
			t.Errorf("resets %d %+v %s", resets, p, r.seqs())
		}
	})
}

// The options are optional: Tail and Follow with none poll on their own ticker, and a context that is done ends them, with its
// error, after the events that were in the log have been delivered.
func TestTailAndFollowNeedNoOptions(t *testing.T) {
	path := writeLog(t, lineOf(evAt(1))+lineOf(evAt(2)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen []uint64
	err := Tail(ctx, path, func(e events.Event) error {
		seen = append(seen, e.Seq)
		if e.Seq == 2 {
			cancel() // the first poll has delivered everything: stop before any tick
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(seen) != 2 {
		t.Errorf("Tail: %v, saw %v", err, seen)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	st := New()
	done := make(chan error, 1)
	go func() { done <- Follow(ctx2, path, st) }()
	for st.LastSeq() < 2 { // the first poll is not timed: wait for it by what it did
		select {
		case err := <-done:
			t.Fatalf("Follow ended early: %v", err)
		default:
			runtime.Gosched()
		}
	}
	cancel2()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Follow: %v", err)
		}
	case <-time.After(hang):
		t.Fatal("Follow did not end when its context was done")
	}
}

func TestTailWaitsForALogThatIsNotThereYetOrFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := Tail(context.Background(), path, func(events.Event) error { return nil }, TailOptions{Tick: make(chan time.Time)}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a log that is not there is an error by default: %v", err)
	}
	r := startTail(t, path, TailOptions{WaitForFile: true})
	if p := r.firstPoll(); p.Delivered != 0 || p.Offset != 0 {
		t.Errorf("%+v", p)
	}
	r.step()
	appendTo(t, path, lineOf(evAt(1)))
	if p := r.step(); p.Delivered != 1 || r.seqs() != "1" {
		t.Errorf("%+v", p)
	}
}

func TestTailEndsWhenTheContextIsDoneOrTheCallbackFails(t *testing.T) {
	path := writeLog(t, lineOf(evAt(1)))
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	polled := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Tail(ctx, path, func(events.Event) error { return nil }, TailOptions{Tick: tick, OnPoll: func(PollInfo) { polled <- struct{}{} }})
	}()
	select {
	case <-polled:
	case <-time.After(hang):
		t.Fatal("no poll")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err %v", err)
		}
	case <-time.After(hang):
		t.Fatal("Tail did not end")
	}
	// A context that is done before the start does no polling.
	if err := Tail(ctx, path, func(events.Event) error { t.Error("delivered"); return nil }, TailOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err %v", err)
	}
	// An error from the callback ends it, unchanged.
	boom := errors.New("boom")
	if err := Tail(context.Background(), path, func(events.Event) error { return boom }, TailOptions{Tick: make(chan time.Time)}); !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
	// And so does one from the delivery of an unfinished whole line.
	path2 := writeLog(t, strings.TrimSuffix(lineOf(evAt(1)), "\n"))
	if err := Tail(context.Background(), path2, func(events.Event) error { return boom }, TailOptions{Tick: make(chan time.Time)}); !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
}

func TestTailSkipsALineTooLongToBuffer(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 33 MiB line")
	}
	path := writeLog(t, lineOf(evAt(1)))
	r := startTail(t, path, TailOptions{})
	r.firstPoll()
	big := strings.Repeat("x", events.MaxEventBytes+1024)
	appendTo(t, path, `{"seq":2,"type":"x","data":"`+big+`"}`+"\n"+lineOf(evAt(3)))
	if p := r.step(); p.Corrupt != 1 || p.Delivered != 1 || r.seqs() != "1,3" {
		t.Errorf("%+v %s", p, r.seqs())
	}
}

// A real log, written by events.Log, followed while it grows, closed, resumed after a simulated crash, and followed on.
func TestTailFollowsARealLogThroughACrashAndAResume(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "live")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	st := New()
	r := startFollow(t, path, st)
	r.firstPoll()
	for i := 0; i < 5; i++ {
		log.Emit("a", events.TypeToolCall, map[string]any{"id": fmt.Sprint("c", i), "name": "bash", "input": map[string]any{"command": "x"}})
	}
	if err := log.Flush(); err != nil {
		t.Fatal(err)
	}
	r.step()
	if a := st.Snapshot().Totals.ToolCalls; a != 5 {
		t.Errorf("tool calls %d", a)
	}
	log.Close()
	// The process died in the middle of writing an event.
	appendTo(t, path, `{"seq":99,"ts":"2026-01-01T00:00:00Z","session":"live","agent":"a","type":"tool.call","da`)
	if p := r.step(); p.Pending == 0 {
		t.Errorf("the torn tail is pending: %+v", p)
	}
	log2, err := events.Open(dir, "live") // resumes: drops the torn tail and carries on counting
	if err != nil {
		t.Fatal(err)
	}
	log2.Emit("a", events.TypeToolCall, map[string]any{"id": "after", "name": "bash"})
	if err := log2.Close(); err != nil {
		t.Fatal(err)
	}
	p := r.step()
	if p.Resets != 0 || p.Pending != 0 || p.Corrupt != 0 {
		t.Errorf("a resumed log is not a new log: %+v", p)
	}
	if got := st.Snapshot().Totals.ToolCalls; got != 6 {
		t.Errorf("tool calls %d, want 6", got)
	}
	if st.Stats().Panics != 0 || st.Stats().Stale != 0 {
		t.Errorf("stats %+v", st.Stats())
	}
}

func startFollow(t *testing.T, path string, st *State) *tailRig {
	t.Helper()
	r := &tailRig{t: t, path: path, tick: make(chan time.Time), polled: make(chan PollInfo), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		r.done <- Follow(ctx, path, st, TailOptions{Tick: r.tick, OnPoll: func(p PollInfo) {
			select {
			case r.polled <- p:
			case <-ctx.Done():
			}
		}})
	}()
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

func TestFollowResetsItsStateWhenTheLogIsReplacedAndCountsDamage(t *testing.T) {
	first := handBuiltSession()
	path := writeLog(t, logOf(first[:30])+"damaged line\n")
	st := New()
	r := startFollow(t, path, st)
	if p := r.firstPoll(); p.Corrupt != 1 {
		t.Errorf("%+v", p)
	}
	if st.Stats().Events != 30 || st.Stats().Corrupt != 1 {
		t.Errorf("stats %+v", st.Stats())
	}
	// A shorter log takes its place; its seqs start again from 1, which the State would call duplicates had it not been reset.
	small := handBuiltSession()[:10]
	if err := os.WriteFile(path, []byte(logOf(small)), 0o600); err != nil {
		t.Fatal(err)
	}
	r.step()
	want := fold(t, small...)
	got := st.Snapshot()
	got.Stats.Resets = 0
	if js(got) != js(want.Snapshot()) {
		t.Errorf("the state after a reset is the fold of the new log")
	}
	if st.Stats().Resets != 1 && want.Stats().Resets != 0 {
		t.Errorf("stats %+v", st.Stats())
	}
}

// The recording of a real session, written to a file in pieces of any size (from one byte to a few kilobytes, so that most of them
// end in the middle of an event and some in the middle of a character), with a poll after each: however it is cut, what the
// follower holds at the end is what a fold of the whole file holds, nothing was delivered twice, and nothing in between was a
// damaged line.
func TestTailFollowsARealRecordingGrowingInPiecesOfAnySize(t *testing.T) {
	log := statetest.DemoLog()
	want := snapJSON(fold(t, statetest.DemoEvents()...))
	for _, seed := range []int64{1, 2, 3} {
		rng := rand.New(rand.NewSource(seed))
		path := writeLog(t, "")
		st := New()
		r := startFollow(t, path, st)
		r.firstPoll()
		var last PollInfo
		delivered := 0
		for pos := 0; pos < len(log); {
			n := min(1+rng.Intn(3000), len(log)-pos)
			if rng.Intn(4) == 0 {
				n = min(1+rng.Intn(8), len(log)-pos) // and some very short ones
			}
			appendTo(t, path, string(log[pos:pos+n]))
			pos += n
			last = r.step()
			delivered += last.Delivered
			if last.Corrupt != 0 || last.Resets != 0 {
				t.Fatalf("seed %d: after %d bytes %+v: a torn write is not a damaged line", seed, pos, last)
			}
		}
		if delivered != 312 || last.Total != 312 || last.Pending != 0 || int(last.Offset) != len(log) {
			t.Errorf("seed %d: %d events delivered, last poll %+v for %d bytes", seed, delivered, last, len(log))
		}
		if got := snapJSON(st); got != want {
			t.Errorf("seed %d: the followed state is not the folded one:\n%s", seed, firstDiff(want, got))
		}
		if s := st.Stats(); s.Stale != 0 || s.Panics != 0 || s.Corrupt != 0 {
			t.Errorf("seed %d: stats %+v", seed, s)
		}
	}
}
