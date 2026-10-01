package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// writeLog writes events.jsonl as the log does and returns its path.
func writeLog(t testing.TB, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFoldReadsALogTheWayScanDoes(t *testing.T) {
	evs := handBuiltSession()
	good := logOf(evs)
	t.Run("a clean log", func(t *testing.T) {
		st, err := Fold(writeLog(t, good))
		if err != nil {
			t.Fatal(err)
		}
		if want := snapJSON(fold(t, evs...)); snapJSON(st) != want {
			t.Error("Fold and Apply disagree")
		}
		if st.Stats().Events != len(evs) {
			t.Errorf("events %d", st.Stats().Events)
		}
	})
	t.Run("a torn last line, a corrupt line, unknown fields", func(t *testing.T) {
		lines := strings.SplitAfter(good, "\n")
		half := lines[len(lines)-2][:len(lines[len(lines)-2])/2] // the last event, cut short by a crash
		var sb strings.Builder
		for i, l := range lines[:len(lines)-2] {
			if i == 3 {
				sb.WriteString("this line is not an event\n")
				sb.WriteString(`{"seq":0,"type":"x"}` + "\n") // no seq: not an event either
				sb.WriteString(`{"seq":9999,"ts":"2026-01-02T03:04:05Z","type":"future.thing","added_field":{"a":1},"data":{"also":"new"}}` + "\n")
			}
			sb.WriteString(l)
		}
		sb.WriteString(half)
		st, err := Fold(writeLog(t, sb.String()))
		if err != nil {
			t.Fatalf("damage is not an error: %v", err)
		}
		s := st.Stats()
		if s.Corrupt != 2 {
			t.Errorf("corrupt %d, want 2", s.Corrupt)
		}
		if s.Unknown != 1 || s.UnknownTypes["future.thing"] != 1 {
			t.Errorf("unknown %d %v", s.Unknown, s.UnknownTypes)
		}
		// Seq 9999 of the extra event is above all the others, so they would all be stale after it: the test put it in the middle.
		// What matters: the state holds the events up to it.
		if s.Events == 0 {
			t.Error("nothing applied")
		}
	})
	t.Run("a last line that is a whole event without its newline", func(t *testing.T) {
		st, err := Fold(writeLog(t, strings.TrimSuffix(good, "\n")))
		if err != nil {
			t.Fatal(err)
		}
		if st.Stats().Events != len(evs) {
			t.Errorf("the last event was not applied: %d of %d", st.Stats().Events, len(evs))
		}
	})
	t.Run("an empty log", func(t *testing.T) {
		st, err := Fold(writeLog(t, ""))
		if err != nil || st == nil || len(st.Snapshot().Agents) != 0 {
			t.Errorf("%v %v", st, err)
		}
	})
	t.Run("a log that is not there", func(t *testing.T) {
		st, err := Fold(filepath.Join(t.TempDir(), "nope.jsonl"))
		if st != nil || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%v %v", st, err)
		}
	})
	t.Run("until a seq", func(t *testing.T) {
		st, err := FoldUntil(writeLog(t, good), 40)
		if err != nil {
			t.Fatal(err)
		}
		want := fold(t, evs[:40]...)
		if snapJSON(st) != snapJSON(want) || st.LastSeq() != 40 {
			t.Errorf("FoldUntil(40): last seq %d", st.LastSeq())
		}
	})
	t.Run("into a state that has some of it already", func(t *testing.T) {
		p := writeLog(t, good)
		st := fold(t, evs[:30]...)
		if err := FoldInto(st, p, 0); err != nil {
			t.Fatal(err)
		}
		// The thirty events already folded come round again and are stale; everything else is what one pass gives.
		if got := st.Stats().Stale; got != 30 {
			t.Errorf("stale %d, want the 30 events folded twice", got)
		}
		a, b := st.Snapshot(), fold(t, evs...).Snapshot()
		a.Stats.Stale, b.Stats.Stale = 0, 0
		if js(a) != js(b) {
			t.Errorf("folding a log into a state that has a prefix of it differs from folding it once:\n%s", firstDiff(js(b), js(a)))
		}
	})
}

// replayLog is five events: two at the start, one a second later, one after half a second more, one ten seconds in.
func replayLog(t testing.TB) (string, []events.Event) {
	b := statetest.NewBuilder()
	S := statetest.Epoch
	at := func(d time.Duration) *statetest.Builder { return b.At(S.Add(d)) }
	at(0).Spawn("a", "backend", "T1", "mgr")
	at(0).Request("a", "a.1", "m", "pk", secShared())
	at(sec(1)).Response("a", "a.1", "m", 100, 900, 0, 10, 0.01)
	at(sec(1)+ms(500)).Call("a", "c1", "bash", map[string]any{"command": "x"})
	at(sec(10)).Result("a", "c1", "bash", false, 8500)
	evs := b.Events()
	return writeLog(t, logOf(evs)), evs
}

func TestReplayAppliesEventsWhenTheVirtualClockReachesThem(t *testing.T) {
	path, evs := replayLog(t)
	r, err := Replay(path, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	S := statetest.Epoch
	seqs := func(es []events.Event) string {
		var s []string
		for _, e := range es {
			s = append(s, fmt.Sprint(e.Seq))
		}
		return strings.Join(s, ",")
	}
	step := func(dt time.Duration, want string) {
		t.Helper()
		if got := seqs(r.Advance(dt)); got != want {
			t.Fatalf("Advance(%v) gave %q, want %q (elapsed %v)", dt, got, want, r.Elapsed())
		}
	}
	if r.Done() || !r.Now().Equal(S) || r.Elapsed() != 0 || r.Applied() != 0 {
		t.Errorf("at the start: done %v now %v elapsed %v applied %d", r.Done(), r.Now(), r.Elapsed(), r.Applied())
	}
	if len(r.State().Snapshot().Agents) != 0 {
		t.Error("nothing is applied before the clock moves")
	}
	step(0, "1,2")    // due at the start: the first event, and the one with the same timestamp
	step(ms(500), "") // 0.5 s: nothing yet
	step(ms(499), "") // 0.999 s
	step(ms(1), "3")  // 1.0 s: due exactly now
	step(ms(499), "") // 1.499 s
	step(ms(1), "4")  // 1.5 s
	step(sec(8), "")  // 9.5 s
	step(-sec(5), "") // a negative step is no step
	if r.Elapsed() != ms(9500) || !r.Now().Equal(S.Add(ms(9500))) || r.Applied() != 4 || r.Done() {
		t.Errorf("elapsed %v now %v applied %d done %v", r.Elapsed(), r.Now(), r.Applied(), r.Done())
	}
	if a, _ := r.State().Snapshot().Agent("a"); a.Status != StatusTool {
		t.Errorf("the state is as of the events played: %s", a.Status)
	}
	step(ms(500), "5")
	if !r.Done() || len(r.Advance(time.Hour)) != 0 {
		t.Error("the replay is over")
	}
	if snapJSON(r.State()) != snapJSON(fold(t, evs...)) {
		t.Error("a replay played to the end is the fold of the log")
	}
}

func TestReplaySpeedUntilAndDrain(t *testing.T) {
	path, evs := replayLog(t)
	t.Run("speed scales the virtual clock", func(t *testing.T) {
		r, err := Replay(path, ReplayOptions{Speed: 4})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if got := len(r.Advance(ms(250))); got != 3 { // a quarter of a second at four times is a second of the session
			t.Errorf("%d events due at 4x after 250 ms", got)
		}
		if r.Elapsed() != sec(1) {
			t.Errorf("elapsed %v", r.Elapsed())
		}
		if got := len(r.Advance(ms(125))); got != 1 { // another half second
			t.Errorf("%d events due", got)
		}
		if got := len(r.Advance(ms(2125))); got != 1 || !r.Done() { // and eight and a half more
			t.Errorf("%d events due, done %v", got, r.Done())
		}
	})
	t.Run("bad speeds mean 1", func(t *testing.T) {
		for _, sp := range []float64{0, -3, nanF(), infF()} {
			r, err := Replay(path, ReplayOptions{Speed: sp})
			if err != nil {
				t.Fatal(err)
			}
			r.Advance(0)
			if got := len(r.Advance(sec(1))); got != 1 {
				t.Errorf("speed %v: %d events due after one second", sp, got)
			}
			r.Close()
		}
	})
	t.Run("until a seq", func(t *testing.T) {
		st := New()
		r, err := Replay(path, ReplayOptions{Until: 3, State: st})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if r.State() != st {
			t.Error("the State given is the State fed")
		}
		got := r.Advance(time.Hour)
		if len(got) != 3 || got[2].Seq != 3 || !r.Done() || st.LastSeq() != 3 {
			t.Errorf("%d events, done %v, last seq %d", len(got), r.Done(), st.LastSeq())
		}
		if len(r.Drain()) != 0 {
			t.Error("Drain past Until")
		}
	})
	t.Run("drain jumps to the end", func(t *testing.T) {
		r, err := Replay(path, ReplayOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		r.Advance(ms(1000))
		rest := r.Drain()
		if len(rest) != 2 || !r.Done() || r.Elapsed() != sec(10) {
			t.Errorf("%d events, done %v, elapsed %v", len(rest), r.Done(), r.Elapsed())
		}
		if snapJSON(r.State()) != snapJSON(fold(t, evs...)) {
			t.Error("drained state differs")
		}
	})
	t.Run("close early", func(t *testing.T) {
		r, err := Replay(path, ReplayOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r.Advance(0)
		r.Close()
		r.Close()
		if len(r.Advance(time.Hour)) != 0 || !r.Done() {
			t.Error("a closed replay plays nothing")
		}
	})
	t.Run("a log that is not there", func(t *testing.T) {
		if r, err := Replay(filepath.Join(t.TempDir(), "x"), ReplayOptions{}); err == nil || r != nil {
			t.Error("expected an error")
		}
	})
	t.Run("an empty log", func(t *testing.T) {
		r, err := Replay(writeLog(t, ""), ReplayOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Done() || !r.Now().IsZero() || len(r.Advance(time.Hour)) != 0 {
			t.Error("an empty log has nothing to play")
		}
	})
}

func nanF() float64 { var z float64; return z / z }
func infF() float64 { var z float64; return 1 / z }

func TestReplayFramesAreTheHeadlessRecordersLoop(t *testing.T) {
	path, evs := replayLog(t)
	r, err := Replay(path, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var all []events.Event
	var idx []int
	var last Frame
	for f := range r.Frames(time.Second) {
		all = append(all, f.Events...)
		idx = append(idx, f.Index)
		last = f
	}
	if len(all) != len(evs) {
		t.Fatalf("%d events over the frames", len(all))
	}
	for i, e := range all {
		if e.Seq != evs[i].Seq {
			t.Fatalf("frame events are in order: %d", i)
		}
	}
	if len(idx) != 10 || idx[9] != 9 || !last.Now.Equal(statetest.Epoch.Add(sec(10))) {
		t.Errorf("%d frames, last %+v: one a second for ten seconds", len(idx), last)
	}
	// No step given: the default frame. The whole ten seconds is 300 of them.
	r2, _ := Replay(path, ReplayOptions{})
	defer r2.Close()
	n := 0
	for range r2.Frames(0) {
		n++
	}
	if n < 299 || n > 301 {
		t.Errorf("%d default frames", n)
	}
	// Stopping early is allowed.
	r3, _ := Replay(path, ReplayOptions{})
	defer r3.Close()
	for f := range r3.Frames(time.Second) {
		if f.Index == 2 {
			break
		}
	}
	if r3.Done() {
		t.Error("a loop that broke out has not finished the replay")
	}
}

func TestReplayIsTolerantOfOddTimestampsAndDamage(t *testing.T) {
	S := statetest.Epoch
	mk := func(seq uint64, ts time.Time) events.Event {
		return events.Event{Seq: seq, TS: ts, Agent: "a", Type: events.TypeToolCall, Data: []byte(fmt.Sprintf(`{"id":"c%d","name":"bash"}`, seq))}
	}
	// 1 at t0, 2 a second later, 3 with a clock that stepped back, 4 with no time at all, 5 five seconds on.
	evs := []events.Event{mk(1, S), mk(2, S.Add(sec(1))), mk(3, S.Add(-sec(30))), mk(4, time.Time{}), mk(5, S.Add(sec(5)))}
	content := logOf(evs[:2]) + "garbage line\n" + logOf(evs[2:]) + `{"seq":6,"ts":"2026`
	r, err := Replay(writeLog(t, content), ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got [][]uint64
	for f := range r.Frames(sec(1)) {
		var seqs []uint64
		for _, e := range f.Events {
			seqs = append(seqs, e.Seq)
		}
		got = append(got, seqs)
	}
	// Each step is a second. The event a second in is due in the first step, and so are the clock that went back and the event with
	// no time: they are due no earlier than the event before them, which is as early as they can be and stay in order.
	want := "[[1 2 3 4] [] [] [] [5]]"
	if fmt.Sprint(got) != want {
		t.Errorf("frames %v, want %v", got, want)
	}
	if r.State().Stats().Corrupt != 1 || r.Err() != nil {
		t.Errorf("corrupt %d err %v", r.State().Stats().Corrupt, r.Err())
	}
	// A whole day of silence between two events costs one Advance, not a day.
	quiet := logOf([]events.Event{mk(1, S), mk(2, S.Add(24*time.Hour))})
	rq, _ := Replay(writeLog(t, quiet), ReplayOptions{})
	defer rq.Close()
	rq.Advance(0)
	if len(rq.Advance(24*time.Hour)) != 1 || !rq.Done() {
		t.Error("a day of virtual time passes in one call")
	}
	// An enormous step saturates instead of overflowing.
	rs, _ := Replay(writeLog(t, quiet), ReplayOptions{Speed: 1e18})
	defer rs.Close()
	if len(rs.Advance(time.Hour*24*365)) != 2 || rs.Elapsed() <= 0 {
		t.Errorf("elapsed %v", rs.Elapsed())
	}
}
