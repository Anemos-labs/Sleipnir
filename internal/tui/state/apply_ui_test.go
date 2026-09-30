package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// lane returns the last n cells of an agent's lane as digit strings.
func lane(t testing.TB, sn *Snapshot, id string, n int) (levels, marks string) {
	t.Helper()
	r := rowOf(t, sn, id)
	if len(r.Levels) != ActivitySeconds || len(r.Marks) != ActivitySeconds {
		t.Fatalf("lane has %d levels and %d marks, want %d", len(r.Levels), len(r.Marks), ActivitySeconds)
	}
	lv, _ := r.Levels.MarshalJSON()
	mk, _ := r.Marks.MarshalJSON()
	l, m := strings.Trim(string(lv), `"`), strings.Trim(string(mk), `"`)
	return l[len(l)-n:], m[len(m)-n:]
}

func TestActivityLevelsAreTheFractionOfEachSecondThatSomethingWasInFlight(t *testing.T) {
	S := statetest.Epoch
	t.Run("a tool call across three seconds", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S.Add(ms(750))).Call("w", "c1", "bash", map[string]any{"command": "x"}))
		apply(t, st, b.At(S.Add(ms(2250))).Result("w", "c1", "bash", false, 1500))
		lv, _ := lane(t, st.Snapshot(), "w", 4)
		// 250 ms of the first second (2 eighths), the whole second (8), 250 ms of the third (2); nothing before.
		if lv != "0282" {
			t.Errorf("levels %s", lv)
		}
		if end := st.Snapshot().Activity.End; !end.Equal(S.Add(2 * time.Second)) {
			t.Errorf("End = %v: the second of the clock", end)
		}
	})
	t.Run("a model request", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S).Request("w", "w.1", "m", "pk", secShared()))
		apply(t, st, b.At(S.Add(ms(500))).Response("w", "w.1", "m", 1, 0, 0, 1, 0))
		if lv, _ := lane(t, st.Snapshot(), "w", 2); lv != "04" {
			t.Errorf("levels %s", lv)
		}
	})
	t.Run("overlapping spans count once", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S).Request("w", "w.1", "m", "pk", secShared()))
		apply(t, st, b.At(S.Add(ms(500))).Call("w", "c1", "bash", map[string]any{}))
		apply(t, st, b.At(S.Add(time.Second)).Response("w", "w.1", "m", 1, 0, 0, 1, 0))
		apply(t, st, b.At(S.Add(ms(1500))).Result("w", "c1", "bash", false, 1000))
		// the union is 0 to 1.5 s: a whole second, then half of the next
		if lv, _ := lane(t, st.Snapshot(), "w", 2); lv != "84" {
			t.Errorf("levels %s", lv)
		}
	})
	t.Run("a call that returned at once still shows", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S).Call("w", "c1", "read", map[string]any{"path": "x"}))
		apply(t, st, b.Result("w", "c1", "read", false, 0))
		if lv, _ := lane(t, st.Snapshot(), "w", 1); lv != "1" {
			t.Errorf("levels %s: any activity is at least one eighth", lv)
		}
	})
	t.Run("an open interval is credited up to now", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S.Add(ms(500))).Call("w", "c1", "wait", map[string]any{}))
		if lv, _ := lane(t, st.Snapshot(), "w", 1); lv != "1" {
			t.Errorf("at the clock the call has just begun: %s", lv)
		}
		sn := st.SnapshotAt(S.Add(5*time.Second + ms(250)))
		if lv, _ := lane(t, sn, "w", 6); lv != "488882" {
			t.Errorf("levels %s", lv)
		}
		if sn.Now.Sub(S) != 5*time.Second+ms(250) || !sn.Clock.Equal(S.Add(ms(500))) {
			t.Errorf("Now %v Clock %v", sn.Now, sn.Clock)
		}
		// A now before the clock is not honoured: an event that was applied is not hidden.
		if lv, _ := lane(t, st.SnapshotAt(S.Add(-time.Hour)), "w", 1); lv != "1" {
			t.Errorf("levels %s", lv)
		}
		// Asking did not change anything.
		if lv, _ := lane(t, st.Snapshot(), "w", 1); lv != "1" {
			t.Errorf("levels after asking %s", lv)
		}
	})
	t.Run("the end of a run closes what is open", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S).Call("w", "c1", "bash", map[string]any{}))
		apply(t, st, b.At(S.Add(ms(500))).Emit("w", events.TypeAgentState, map[string]any{"id": "w", "state": "idle"}))
		sn := st.SnapshotAt(S.Add(time.Minute))
		lv, _ := lane(t, sn, "w", 61)
		if lv[0] != '4' || strings.Trim(lv[1:], "0") != "" {
			t.Errorf("busy for half a second and no longer: %s", lv)
		}
		if a := agentOf(t, sn, "w"); a.OpenTools != 0 || a.Status != StatusIdle {
			t.Errorf("%+v", a)
		}
	})
	t.Run("only the last window is kept", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S).Call("w", "c1", "bash", map[string]any{}))
		apply(t, st, b.At(S.Add(time.Second)).Result("w", "c1", "bash", false, 1000))
		apply(t, st, b.At(S.Add(150*time.Second)).Call("w", "c2", "bash", map[string]any{}))
		apply(t, st, b.At(S.Add(151*time.Second)).Result("w", "c2", "bash", false, 1000))
		lv, _ := lane(t, st.Snapshot(), "w", ActivitySeconds)
		if strings.Count(lv, "8") != 1 || strings.Count(lv, "0") != ActivitySeconds-1 || lv[ActivitySeconds-2] != '8' {
			t.Errorf("levels %s: the call 150 s ago is in the window, the one 151 s ago is not", lv)
		}
		// The cell of an older second that an event finds taken by a newer one is not overwritten.
		apply(t, st, b.At(S.Add(30*time.Second)).Call("w", "c3", "bash", map[string]any{}))
		apply(t, st, b.At(S.Add(31*time.Second)).Result("w", "c3", "bash", false, 1000))
		if lv2, _ := lane(t, st.Snapshot(), "w", ActivitySeconds); lv2 != lv && strings.Count(lv2, "8") != 1 {
			t.Errorf("an event older than the window changed it: %s", lv2)
		}
	})
	t.Run("a span longer than the window fills it", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.At(S).Call("w", "c1", "bash", map[string]any{}))
		apply(t, st, b.At(S.Add(time.Hour)).Result("w", "c1", "bash", false, 3600000))
		lv, _ := lane(t, st.Snapshot(), "w", ActivitySeconds)
		// the hour ends at the very start of the clock's own second, which has nothing in it
		if lv != strings.Repeat("8", ActivitySeconds-1)+"0" {
			t.Errorf("levels %s", lv)
		}
	})
}

func TestActivityMarkersAndShape(t *testing.T) {
	S := statetest.Epoch
	b := newB()
	st := New()
	apply(t, st, b.At(S).Spawn("mgr", "manager", "", ""))
	apply(t, st, b.Spawn("be-1", "backend", "T1", "mgr"))
	apply(t, st, b.At(S.Add(ms(100))).Emit("be-1", events.TypeMailSend, map[string]any{"id": "m1", "from": "be-1", "to": "mgr", "text": "hi"}))
	apply(t, st, b.At(S.Add(ms(200))).Emit("mgr", events.TypeMailDeliver, map[string]any{"id": "m1", "from": "be-1"}))
	apply(t, st, b.At(S.Add(ms(1100))).Emit("be-1", events.TypeCompactCommit, map[string]any{"reason": "emergency: x", "snap_tokens": 10}))
	apply(t, st, b.At(S.Add(ms(2100))).Emit("be-1", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "n"}))
	apply(t, st, b.At(S.Add(ms(2200))).Emit("be-1", events.TypeCacheAnomaly, map[string]any{"kind": "drift"}))
	sn := st.Snapshot()
	// second 0: mail (1); second 1: compaction (2); second 2: stuck and anomaly (4+8 = c)
	if _, mk := lane(t, sn, "be-1", 3); mk != "12c" {
		t.Errorf("be-1 marks %s", mk)
	}
	if _, mk := lane(t, sn, "mgr", 3); mk != "100" {
		t.Errorf("mgr marks %s: the delivery marks the recipient's lane", mk)
	}
	if len(sn.Activity.Rows) != 2 || sn.Activity.Rows[0].Agent != "mgr" || sn.Activity.Rows[1].Agent != "be-1" {
		t.Errorf("rows are in display order, one per agent: %v", sn.Activity.Rows)
	}
	// It marshals as strings of digits, one character for each second.
	var raw struct {
		Activity struct {
			Rows []struct {
				Agent, Levels, Marks string
			}
		}
	}
	bs, err := json.Marshal(sn)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bs, &raw); err != nil {
		t.Fatal(err)
	}
	if r := raw.Activity.Rows[1]; len(r.Levels) != ActivitySeconds || len(r.Marks) != ActivitySeconds || !strings.HasSuffix(r.Marks, "12c") {
		t.Errorf("json lane %q %q", r.Levels, r.Marks)
	}
	if (Activity{}).Rows != nil || (New()).Snapshot().Activity.End != (time.Time{}) {
		t.Error("an empty state has no activity")
	}
}

func TestFeedIsABoundedRingOfShortCleanLines(t *testing.T) {
	b := newB()
	st := New()
	n := FeedCap + 57
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		apply(t, st, b.Call("w", id, "bash", map[string]any{"command": fmt.Sprintf("echo %d", i)}))
		b.Advance(ms(3))
		apply(t, st, b.Result("w", id, "bash", i%2 == 0, 3))
	}
	sn := st.Snapshot()
	if len(sn.Feed) != FeedCap {
		t.Fatalf("feed has %d lines", len(sn.Feed))
	}
	last := sn.Feed[len(sn.Feed)-1]
	if last.Text != fmt.Sprintf("bash echo %d", n-1) || last.Kind != FeedToolErr || last.Glyph != GlyphFail || last.Detail != "3 ms" || last.Agent != "w" || last.Seq != b.Seq() {
		t.Errorf("last line %+v", last)
	}
	if first := sn.Feed[0]; first.Seq >= last.Seq || first.Text != fmt.Sprintf("bash echo %d", n-FeedCap) {
		t.Errorf("first line %+v", first)
	}
	if ok := sn.Feed[len(sn.Feed)-2]; ok.Kind != FeedTool || ok.Glyph != GlyphOK {
		t.Errorf("a result that succeeded: %+v", ok)
	}

	// Text that comes from a model, a tool or a peer is made one short line with nothing in it that a terminal would act on.
	st2 := New()
	evil := "rm -rf /\x1b]52;c;Y2xpcGJvYXJk\x07\x1b[2J‮reversed​ zero\nwidth\r\n" + strings.Repeat("A", 500)
	apply(t, st2, b.Call("w", "e1", "bash", map[string]any{"command": evil}))
	apply(t, st2, b.Result("w", "e1", "bash", false, 1))
	apply(t, st2, b.Emit("w", events.TypeMailSend, map[string]any{"id": "m", "from": "w\x1b[31m", "to": "mgr\x00", "text": evil}))
	bs, _ := json.Marshal(st2.Snapshot())
	for _, bad := range []string{`\u001b`, `\u0007`, `\u0000`, `‮`, `​`, `\n`, `\r`} {
		if strings.Contains(string(bs), bad) {
			t.Errorf("the snapshot holds %s", bad)
		}
	}
	for _, l := range st2.Snapshot().Feed {
		if n := len([]rune(l.Text)); n > textLine {
			t.Errorf("a feed line of %d runes: %q", n, l.Text)
		}
	}
	if a := agentOf(t, st2.Snapshot(), "w"); len([]rune(a.ToolSummary)) > textShort {
		t.Errorf("summary %q", a.ToolSummary)
	}
}

func TestCleanMakesAnyTextOneSafeLine(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"", 10, ""},
		{"plain", 10, "plain"},
		{"  padded \t and\n\nspread  ", 40, "padded and spread"},
		{"a\x1b[31mred", 40, "a [31mred"},
		{"bell\x07here", 40, "bell here"},
		{"zero​width‮flip", 40, "zerowidthflip"},
		{"line sep", 40, "line sep"},
		{"toolongtoolong", 8, "toolong…"},
		{"ab cd ef", 3, "ab…"},
		{"ab cd ef", 1, "…"},
		{"日本語のテキスト", 4, "日本語…"},
		{"bad\xffbyte", 40, "bad�byte"},
		{"x", 0, ""},
		{"\x00\x01\x02", 10, ""},
	}
	for _, c := range cases {
		if got := clean(c.in, c.max); got != c.want {
			t.Errorf("clean(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
	if got := clean(strings.Repeat("é", 100), 10); len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("a cut ends in an ellipsis and is exactly max runes: %q", got)
	}
}

func TestFormattersAreExact(t *testing.T) {
	for in, want := range map[int64]string{-5: "0 ms", 0: "0 ms", 340: "340 ms", 999: "999 ms", 1000: "1.0 s", 1400: "1.4 s", 9949: "9.9 s", 10000: "10 s", 59999: "59 s", 60000: "1m00s", 125000: "2m05s", 3600000: "1h00m", 3720000: "1h02m"} {
		if got := fmtDur(in); got != want {
			t.Errorf("fmtDur(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1.0k", 2400: "2.4k", 9999: "10.0k", 10000: "10k", 31200: "31k", 999999: "999k", 1000000: "1.0M", 1200000: "1.2M"} {
		if got := fmtTok(in); got != want {
			t.Errorf("fmtTok(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "$0", 0.0004: "$0.0004", 0.31: "$0.31", 9.999: "$10.00", 12.4: "$12.4", 20: "$20.0"} {
		if got := fmtUSD(in); got != want {
			t.Errorf("fmtUSD(%v) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct{ a, b string }{{"be-2", "be-10"}, {"a", "b"}, {"w-9", "w-10"}, {"x1", "x01a"}, {"", "a"}, {"T2", "T10"}, {"a", "a1"}} {
		if !idLess(c.a, c.b) || idLess(c.b, c.a) {
			t.Errorf("idLess(%q, %q) should hold one way only", c.a, c.b)
		}
	}
	if idLess("a1", "a1") || idLess("", "") {
		t.Error("idLess is irreflexive")
	}
	// Two ids that differ only in leading zeros are still ordered.
	if idLess("a01", "a1") == idLess("a1", "a01") {
		t.Error("a total order")
	}
	if taskNum("T12") != 12 || taskNum("x") < 1<<30 || taskNum("T") < 1<<30 || taskNum("T1x") < 1<<30 {
		t.Error("taskNum")
	}
}

func TestRingKeepsTheNewestAndAddressesByAbsoluteIndex(t *testing.T) {
	r := newRing[int](3)
	for i := 0; i < 2; i++ {
		r.push(i)
	}
	if got := fmt.Sprint(r.slice()); got != "[0 1]" {
		t.Errorf("%s", got)
	}
	for i := 2; i < 8; i++ {
		r.push(i)
	}
	if got := fmt.Sprint(r.slice()); got != "[5 6 7]" || r.len() != 3 || r.total != 8 {
		t.Errorf("%s len %d total %d", got, r.len(), r.total)
	}
	if p, ok := r.byAbs(7); !ok || *p != 7 {
		t.Error("byAbs(7)")
	}
	if _, ok := r.byAbs(4); ok {
		t.Error("index 4 was pushed out")
	}
	if p, ok := r.byAbs(5); !ok || *p != 5 {
		t.Error("byAbs(5)")
	}
	if _, ok := r.byAbs(8); ok {
		t.Error("index 8 does not exist yet")
	}
	var zero ring[int]
	zero.push(1)
	if zero.len() != 0 || zero.slice() != nil {
		t.Error("a ring of capacity zero holds nothing")
	}
	r.reset()
	if r.len() != 0 || r.total != 0 {
		t.Error("reset")
	}
}
