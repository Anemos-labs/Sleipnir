package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// openLog opens a session log in dir whose clock the test sets.
func openLog(t *testing.T, dir, session string, clock func() time.Time) *events.Log {
	t.Helper()
	l, err := events.Open(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	l.SetClock(clock)
	t.Cleanup(func() { l.Close() })
	return l
}

// takeAttach is the subscription Attach handed the translator's goroutine (the harness has none: the test reads it).
func takeAttach(t *testing.T, tr *Translator) attachReq {
	t.Helper()
	select {
	case a := <-tr.attach:
		return a
	default:
		t.Fatal("Attach handed over no subscription")
	}
	return attachReq{}
}

// A session resumed with a log of an earlier run gets that run's history at t 0, each event with its real time, then the state the
// whole log folds to (token tables and ratio series of every agent, its tasks), then the resumed row; the new run's events follow
// with their own times.
func TestHistoryOfResumedSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), statetest.DemoLog(), 0o600); err != nil {
		t.Fatal(err)
	}
	old := statetest.DemoEvents()
	resumedAt := old[len(old)-1].TS.Add(time.Hour)
	h := newHarness(t, Config{Root: "/work/handbook"})
	h.set(resumedAt)
	log := openLog(t, dir, old[0].Session, h.now)
	detach := h.tr.Attach(log, dir)
	defer detach()
	sub := takeAttach(t, h.tr)

	evs := h.decoded()
	var resumedRow int = -1
	for i, e := range evs {
		if e["t"] != 0.0 {
			t.Fatalf("event %d of the history is not at t 0: %v", i, e)
		}
		if e["k"] == "say" && e["who"] == "sys" && strings.HasPrefix(e["text"].(string), "↺ resumed 20260930-205228-76a40e · 2026-09-30 20:52") {
			resumedRow = i
		}
	}
	if resumedRow != len(evs)-1 {
		t.Fatalf("the resumed row is not last (%d of %d)", resumedRow, len(evs))
	}
	says := ofKind(evs, "say")
	if says[0]["who"] != "you" || says[0]["at"] == nil || says[1]["who"] != "mgr" || says[1]["stream"] != false {
		t.Fatalf("the history's first rows: %v", says[:2])
	}
	if rows := ofKind(evs, "tool"); len(rows) < 20 || rows[0]["at"] == nil || rows[0]["out"] == "" {
		t.Fatalf("history tool rows: %d, first %v", len(rows), rows[0])
	}
	// the folded state: every agent's token table equals a fold of the log
	st, err := state.Fold(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	uses := map[string]map[string]any{}
	reqs := map[string]int{}
	for _, e := range evs {
		switch e["k"] {
		case "use":
			uses[e["id"].(string)] = e
		case "req":
			if e["hist"] != true {
				t.Fatalf("a req of the history is not hist: %v", e)
			}
			reqs[e["id"].(string)]++
		}
	}
	for _, a := range st.Agents() {
		u := uses[uiID(a.ID)]
		if u == nil || u["rd"] != float64(a.Tokens.CacheRead) || u["out"] != float64(a.Tokens.Output) || reqs[uiID(a.ID)] != len(a.Hits.Ratios) {
			t.Fatalf("%s: use %v, %d ratios; the fold says %+v, %d", a.ID, u, reqs[uiID(a.ID)], a.Tokens, len(a.Hits.Ratios))
		}
	}
	if n := len(ofKind(evs, "task")); n != 7 {
		t.Fatalf("%d task events for the 7 tasks", n)
	}
	// the new run
	h.set(resumedAt.Add(5 * time.Second))
	log.Emit("mgr", events.TypeModelRequest, request("r100"))
	h.set(resumedAt.Add(6 * time.Second))
	log.Emit("mgr", events.TypeModelResponse, response("r100", 3000))
	for i := 0; i < 2; i++ {
		e := <-sub.ch
		h.tr.mu.Lock()
		h.tr.followEvent(e)
		h.tr.mu.Unlock()
	}
	live := h.decoded()[len(evs):]
	if len(live) == 0 || live[len(live)-1]["t"].(float64) < 5 {
		t.Fatalf("the new run's events: %v", live)
	}
	for _, e := range live {
		if e["at"] != nil {
			t.Fatalf("a live event has at: %v", e)
		}
	}
}

// The history keeps its newest events within its bound and says how many earlier ones it does not show.
func TestHistoryIsBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), statetest.DemoLog(), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Config{Limits: Limits{History: 20}})
	h.set(time.Now())
	log := openLog(t, dir, "s", h.now)
	defer h.tr.Attach(log, dir)()
	takeAttach(t, h.tr)
	evs := h.decoded()
	if !strings.Contains(evs[0]["text"].(string), "earlier events of the history are not shown") {
		t.Fatalf("first row: %v", evs[0])
	}
	var withAt int
	for _, e := range evs {
		if e["at"] != nil {
			withAt++
		}
	}
	if withAt > 20 || withAt == 0 {
		t.Fatalf("%d history events kept, bound 20", withAt)
	}
}

// Events the subscription dropped are found by their seq and read back from the file.
func TestSeqGapRescan(t *testing.T) {
	dir := t.TempDir()
	clock := t0
	log := openLog(t, dir, "s", func() time.Time { return clock })
	h := newHarness(t, Config{StartedAt: t0})
	defer h.tr.Attach(log, dir)()
	sub := takeAttach(t, h.tr)
	for i := 0; i < 6; i++ {
		clock = clock.Add(time.Second)
		req := "r" + jsNum(float64(i))
		log.Emit("be-1", events.TypeModelRequest, request(req))
		log.Emit("be-1", events.TypeModelResponse, response(req, 100*(i+1)))
	}
	var got []events.Event
	for len(got) < 12 {
		got = append(got, <-sub.ch)
	}
	h.set(clock)
	h.tr.mu.Lock()
	h.tr.followEvent(got[0])
	h.tr.followEvent(got[1])
	h.tr.followEvent(got[11]) // events 3 to 11 were lost by the subscription
	h.tr.mu.Unlock()
	if n := len(ofKind(h.decoded(), "req")); n != 6 {
		t.Fatalf("%d req events for 6 answered requests", n)
	}
}

// With its goroutine, the translator follows an attached log until detached, and stops when closed.
func TestAttachFollowsTheLog(t *testing.T) {
	dir := t.TempDir()
	log := openLog(t, dir, "s", time.Now)
	tr := New(Config{Tab: "t"})
	defer tr.Close()
	detach := tr.Attach(log, dir)
	log.Emit("main", events.TypeModelRequest, request("r1"))
	log.Emit("main", events.TypeModelResponse, response("r1", 100))
	waitFor(t, func() bool {
		_, evs, _, _ := tr.Journal()
		return containsRaw(evs, `"k":"req"`)
	})
	if tr.StartedAt().IsZero() || tr.Now() < 0 {
		t.Fatal("no start after Attach")
	}
	if r := tr.Roster(); len(r) != 1 || r[0].ID != "mgr" {
		t.Fatalf("roster: %+v", r)
	}
	detach()
	tr.Close()
	tr.Close()
	tr.Emit(&wire.Final{}) // does nothing after Close
	tr.Sink().Text("main", "x")
}

// A log another process writes is followed by polling: its answers, tool rows and states come from the file as it grows.
func TestFollowAWatchedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	all := statetest.DemoLog()
	half := len(all) / 2
	for all[half] != '\n' {
		half++
	}
	if err := os.WriteFile(path, all[:half+1], 0o600); err != nil {
		t.Fatal(err)
	}
	tr := New(Config{Tab: "w", Root: "/work/handbook"})
	defer tr.Close()
	stop := tr.Follow(path)
	waitFor(t, func() bool {
		_, evs, _, _ := tr.Journal()
		return containsRaw(evs, `"k":"tool"`)
	})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(all[half+1:])
	f.Close()
	waitFor(t, func() bool {
		_, evs, _, _ := tr.Journal()
		return containsRaw(evs, `"k":"final"`)
	})
	stop()
	_, evs, _, _ := tr.Journal()
	checkStream(t, evs)
	if !containsRaw(evs, `"who":"you"`) || !containsRaw(evs, `"who":"mgr"`) {
		t.Fatal("a followed log shows no conversation")
	}
}

// waitFor polls cond for up to ten seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
