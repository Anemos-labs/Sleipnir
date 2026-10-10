package translate

import (
	"encoding/json"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// reduce folds encoded events into a new mirror: the Go reduction of the page's world model.
func reduce(tb testing.TB, raws ...[]wire.Raw) *mirror {
	tb.Helper()
	m := newMirror()
	for _, rs := range raws {
		for _, r := range rs {
			e, err := decodeEvent(r)
			if err != nil {
				tb.Fatalf("an event the mirror cannot read: %v: %s", err, r)
			}
			m.fold(e)
		}
	}
	return m
}

// view is what of a mirror a page shows outside the transcript, as comparable JSON: every agent's state, tokens, layers and ratio
// series, the tasks and the order they merged in, the plan, the goal, the verdict, the checkpoints, the open questions, the mails,
// anomalies and compactions with their times, the warm clock, the governor, the queue, the alerts, the stalls, the mail counts and
// what the service agents used.
// The time of an agent's last request is left out: a keyframe gives the ratio series at its own t.
func view(tb testing.TB, m *mirror) string {
	tb.Helper()
	type agent struct {
		State  *StateX
		Use    *UseX
		Layers *wire.Layers
		Ratios []float64
		Marks  []string
	}
	agents := map[string]agent{}
	for id, a := range m.agents {
		ag := agent{Ratios: append([]float64{}, lastN(a.ratios, mirrorRatios)...), Marks: append([]string{}, lastN(a.marks, mirrorRatios)...)}
		if a.state != nil {
			c := *a.state
			c.Base = wire.Base{}
			ag.State = &c
		}
		if a.use != nil {
			c := *a.use
			c.Base = wire.Base{}
			ag.Use = &c
		}
		if a.layers != nil {
			c := *a.layers
			c.Base = wire.Base{}
			ag.Layers = &c
		}
		agents[id] = ag
	}
	tasks := map[string]TaskX{}
	for id, t := range m.tasks {
		c := *t
		c.Base = wire.Base{}
		if len(c.Deps) == 0 {
			c.Deps = nil
		}
		tasks[id] = c
	}
	strip := func(e wire.Event) any {
		if e == nil || reflect.ValueOf(e).IsNil() {
			return nil
		}
		b, _ := json.Marshal(e)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		delete(v, "seq")
		delete(v, "k")
		return v
	}
	stripT := func(e wire.Event) any {
		v := strip(e)
		if m, ok := v.(map[string]any); ok {
			delete(m, "t")
		}
		return v
	}
	var mails, anoms, comps, opens, ckpts, alerts, stalls []any
	for _, x := range lastN(m.mails, mirrorMails) {
		mails = append(mails, strip(x))
	}
	for _, x := range lastN(m.anoms, mirrorAnoms) {
		anoms = append(anoms, strip(x))
	}
	for _, x := range lastN(m.comps, mirrorAnoms) {
		comps = append(comps, strip(x))
	}
	for _, id := range m.qorder {
		opens = append(opens, strip(m.open[id]))
	}
	for _, id := range m.corder {
		ckpts = append(ckpts, strip(m.ckpts[id]))
	}
	for _, k := range m.alorder {
		alerts = append(alerts, stripT(m.alerts[k]))
	}
	for _, k := range m.sorder {
		stalls = append(stalls, stripT(m.stalls[k]))
	}
	out := map[string]any{"agents": agents, "tasks": tasks, "torder": m.torder, "merged": m.merged, "plan": stripT(m.plan),
		"goal": stripT(m.goal), "verdict": stripT(m.verdict), "ckpts": ckpts, "open": opens, "mails": mails, "anoms": anoms,
		"comps": comps, "warm": stripT(m.warm), "gov": stripT(m.gov), "queue": stripT(m.queue), "alerts": alerts, "stalls": stalls,
		"mailstat": stripT(m.mailstat), "svc": stripT(m.svc)}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// With small bounds the journal evicts; a reduction of keyframe ⧺ retained events then equals the reduction of every event, for
// everything but the transcript, and an open question that was evicted comes back in the keyframe. The same holds at every point
// Seek is asked for.
func TestJournalBoundsAndKeyframe(t *testing.T) {
	evs := shopEvents(t)
	run := func(lim Limits) *harness {
		h := newHarness(t, Config{Root: "/work/shop", StartedAt: evs[0].TS, Limits: lim})
		h.tr.Question(wire.Question{ID: "q_open", Agent: "fe-1", Cmd: "npm i", Kind: "command"})
		h.feed(evs...)
		h.advance(3 * time.Second)
		return h
	}
	full, small := run(Limits{}), run(Limits{JournalEvents: 60, JournalBytes: 64 << 10})

	kf, retained, seq, _ := small.tr.Journal()
	if len(retained) > 60 || small.tr.j.bytes > 64<<10 {
		t.Fatalf("the journal holds %d events, %d bytes", len(retained), small.tr.j.bytes)
	}
	if len(kf) == 0 || small.tr.j.evicted == 0 {
		t.Fatal("nothing was evicted")
	}
	for _, r := range kf {
		var h struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(r, &h)
		if h.Seq != 0 {
			t.Fatalf("a keyframe event has a seq: %s", r)
		}
	}
	_, all, fullSeq, _ := full.tr.Journal()
	if seq != fullSeq {
		t.Fatalf("seq %d, the unbounded journal's %d", seq, fullSeq)
	}
	if got, want := view(t, reduce(t, kf, retained)), view(t, reduce(t, all)); got != want {
		t.Fatalf("keyframe ⧺ retained differs from the whole stream:\n%s", firstDiff(want, got))
	}
	if !containsRaw(kf, `"id":"q_open"`) {
		t.Fatal("the open question is not in the keyframe")
	}
	// Seek: the state at a retained seq is its keyframe and the events up to it
	for _, at := range []uint64{seq - 50, seq - 10, seq} {
		skf, sevs, ok := small.tr.Seek(at)
		if !ok {
			t.Fatalf("seek %d refused", at)
		}
		var upTo []wire.Raw
		for _, r := range all {
			var h struct {
				Seq uint64 `json:"seq"`
			}
			_ = json.Unmarshal(r, &h)
			if h.Seq <= at {
				upTo = append(upTo, r)
			}
		}
		if got, want := view(t, reduce(t, skf, sevs)), view(t, reduce(t, upTo)); got != want {
			t.Fatalf("seek %d differs:\n%s", at, firstDiff(want, got))
		}
	}
	if _, _, ok := small.tr.Seek(1); ok {
		t.Fatal("a seek before the journal's front was answered")
	}
}

// containsRaw reports whether one of the encoded events contains s.
func containsRaw(raws []wire.Raw, s string) bool {
	for _, r := range raws {
		if json.Valid(r) && contains(string(r), s) {
			return true
		}
	}
	return false
}

// contains is strings.Contains.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// firstDiff shows the first line where two texts differ.
func firstDiff(want, got string) string {
	wl, gl := splitLines(want), splitLines(got)
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			lo, hi := max(i-6, 0), i+3
			ctx := func(ls []string) string {
				var b []byte
				for j := lo; j < hi && j < len(ls); j++ {
					b = append(b, ls[j]...)
					b = append(b, '\n')
				}
				return string(b)
			}
			return "line " + jsNum(float64(i+1)) + "\nwant:\n" + ctx(wl) + "got:\n" + ctx(gl)
		}
	}
	return "(same lines)"
}

// splitLines splits a text at newlines.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// A log delivered twice, or in pieces that overlap, translates to what it translates to once: events are applied by their seq.
func TestReplayIsIdempotent(t *testing.T) {
	evs := shopEvents(t)
	once := translateLog(t, evs, "/work/shop", false)
	twice := newHarness(t, Config{Root: "/work/shop", StartedAt: evs[0].TS})
	twice.feed(evs[:400]...)
	twice.feed(evs[:300]...)
	twice.feed(evs...)
	twice.advance(3 * time.Second)
	if a, b := string(lines(once.raws())), string(lines(twice.raws())); a != b {
		t.Fatalf("a replay changed the translation:\n%s", firstDiff(a, b))
	}
}

// Under a flood the journal keeps its bounds, and memory with it.
func TestJournalBoundsUnderAFlood(t *testing.T) {
	n := 1_000_000
	if raceEnabled {
		n = 100_000
	}
	j := newJournal(DefaultJournalEvents, DefaultJournalBytes)
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 1; i <= n; i++ {
		e := &wire.Req{ID: "be-" + jsNum(float64(i%40)), Ratio: 0.5, P: int64(i), O: 7}
		h := baseOf(e)
		h.K, h.T, h.Seq = "req", float64(i)/1000, uint64(i)
		raw, _ := json.Marshal(e)
		j.add(uint64(i), h.T, "req", raw, e)
		if j.n > DefaultJournalEvents+1 || j.bytes > DefaultJournalBytes {
			t.Fatalf("after %d events: %d retained, %d bytes", i, j.n, j.bytes)
		}
	}
	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > 64<<20 {
		t.Fatalf("the heap grew by %d MiB for a bounded journal", grew>>20)
	}
	if kf := j.keyframe(); len(kf) == 0 {
		t.Fatal("no keyframe after evicting")
	}
	if j.pbytes > maxKeyframeBytes {
		t.Fatalf("periodic keyframes hold %d bytes", j.pbytes)
	}
}
