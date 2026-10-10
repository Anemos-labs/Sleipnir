package translate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// marksLog is the log of a worker that answers 40 main requests, with a compactor's request between some of them. Before request 8 it
// took a new shared prefix, before 17 its thinking was dropped, before 26 both, before 33 its spine was rewritten (a layer commit
// that marks nothing), and before 37 a new epoch with a compactor's request in between.
func marksLog() (evs []events.Event, want []string) {
	b := newLog(t0)
	b.add(time.Second, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work"})
	b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1", "model": "m1"})
	for i := 1; i <= 40; i++ {
		mark := ""
		switch i {
		case 8:
			b.add(time.Millisecond, "be-1", "layer.commit", map[string]any{"scope": "shared-sync", "reason": "the agent took the new prefix"})
			mark = "epoch"
		case 17:
			b.add(time.Millisecond, "be-1", "layer.commit", map[string]any{"scope": "thinking-strip", "reason": "provider rejected a thinking block"})
			mark = "rebase"
		case 26:
			b.add(time.Millisecond, "be-1", "layer.commit", map[string]any{"scope": "shared-sync", "reason": "again"})
			b.add(time.Millisecond, "be-1", "layer.commit", map[string]any{"scope": "thinking-strip", "reason": "again"})
			mark = "epoch"
		case 33:
			b.add(time.Millisecond, "be-1", "layer.commit", map[string]any{"scope": "agent", "spine": "s1", "spine_version": 2})
		case 37:
			b.add(time.Millisecond, "be-1", "layer.commit", map[string]any{"scope": "shared-sync", "reason": "a third time"})
			side := "side" + strconv.Itoa(i)
			b.add(time.Millisecond, "be-1", "model.request", map[string]any{"req": side, "kind": "compactor", "model": "m1"})
			b.add(time.Millisecond, "be-1", "model.response", map[string]any{"req": side, "usage": map[string]any{"input_tokens": 50, "output_tokens": 5}, "stop": "end_turn"})
			mark = "epoch"
		}
		req := "r" + strconv.Itoa(i)
		b.add(time.Millisecond, "be-1", "model.request", request(req))
		b.add(time.Millisecond, "be-1", "model.response", response(req, 1000))
		want = append(want, mark)
	}
	return b.evs, want
}

// marksOf are the marks of the req events of an agent, in order.
func marksOf(evs []map[string]any, id string) []string {
	var out []string
	for _, e := range ofKind(evs, "req") {
		if e["id"] == id {
			m, _ := e["mark"].(string)
			out = append(out, m)
		}
	}
	return out
}

// An agent that took a new shared prefix, or whose thinking blocks were dropped, has the mark on the request that follows (the one the
// terminal draws it above on the hit-ratio line); other layer commits and a compactor's requests mark nothing and take no place.
func TestEpochMarksAreOnTheRequestsTheyAffect(t *testing.T) {
	evs, want := marksLog()
	for _, logOnly := range []bool{false, true} {
		h := translateLog(t, evs, "/work", logOnly)
		checkStream(t, h.raws())
		if got := marksOf(h.decoded(), "be-1"); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("logOnly %v: the marks of the requests are\n%q\nwant\n%q", logOnly, got, want)
		}
	}
}

// The marks are in the history of a resumed session and in a replay of the recorded log.
func TestEpochMarksAreInTheHistoryAndTheReplay(t *testing.T) {
	evs, want := marksLog()
	dir := t.TempDir()
	var buf []byte
	for _, e := range evs {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(append(buf, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	raws, err := Replay(context.Background(), dir, Config{Tab: "r", Root: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if got := marksOf(decodeAll(t, raws), "be-1"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the replay's marks are\n%q\nwant\n%q", got, want)
	}

	h := newHarness(t, Config{Root: "/work"})
	h.set(t0.Add(time.Hour))
	log := openLog(t, dir, "s1", h.now)
	detach := h.tr.Attach(log, dir)
	defer detach()
	hist := h.decoded()
	for _, e := range ofKind(hist, "req") {
		if e["hist"] != true {
			t.Fatalf("a req of the history is not hist: %v", e)
		}
	}
	if got := marksOf(hist, "be-1"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the history's marks are\n%q\nwant\n%q", got, want)
	}
}

// A journal that evicts keeps the marks of the series it summarizes: keyframe ⧺ retained events reduces to what the whole stream does.
func TestEpochMarksSurviveTheKeyframe(t *testing.T) {
	evs, want := marksLog()
	run := func(lim Limits) *harness {
		h := newHarness(t, Config{Root: "/work", StartedAt: evs[0].TS, Limits: lim})
		h.feed(evs...)
		h.advance(3 * time.Second)
		return h
	}
	full, small := run(Limits{}), run(Limits{JournalEvents: 40, JournalBytes: 64 << 10})
	kf, retained, _, _ := small.tr.Journal()
	if small.tr.j.evicted == 0 {
		t.Fatal("nothing was evicted")
	}
	_, all, _, _ := full.tr.Journal()
	got, wantView := reduce(t, kf, retained), reduce(t, all)
	if a, b := view(t, got), view(t, wantView); a != b {
		t.Fatalf("keyframe ⧺ retained differs from the whole stream:\n%s", firstDiff(b, a))
	}
	if marks := wantView.agents["be-1"].marks; strings.Join(marks, ",") != strings.Join(want, ",") {
		t.Errorf("the mirror's marks are\n%q\nwant\n%q", marks, want)
	}
	// the mark of an old request is still on its bar after the events that carried it left the journal
	if !containsRaw(kf, `"hist":true,"mark":"epoch"`) || !containsRaw(kf, `"hist":true,"mark":"rebase"`) {
		t.Error("the keyframe does not carry the marks of the requests it summarizes")
	}
}
