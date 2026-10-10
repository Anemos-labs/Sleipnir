package translate

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// mailmanLog is a team whose harness mailman (a service agent, in no roster) makes two requests, before the manager and a worker make
// six between them.
func mailmanLog() []events.Event {
	b := newLog(t0)
	const gap = 20 * time.Millisecond
	b.add(0, "", "session.start", map[string]any{"swarm": true, "model": "m1", "root": "/work", "mailman": true})
	b.add(gap, "swarm", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(gap, "swarm", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "model": "m1", "by": "mgr", "parent": "mgr"})
	b.add(gap, "swarm", "agent.spawn", map[string]any{"id": "mm-1", "role": "mailman", "service": true, "model": "m1"})
	b.add(gap, "mm-1", "model.request", request("m1"))
	b.add(gap, "mm-1", "model.response", response("m1", 0))
	b.add(gap, "mm-1", "model.request", request("m2"))
	b.add(gap, "mm-1", "model.response", response("m2", 50))
	for i, agent := range []string{"mgr", "be-1", "mgr", "be-1", "mgr", "be-1"} {
		req := "r" + jsNum(float64(i+1))
		b.add(gap, agent, "model.request", request(req))
		b.add(gap, agent, "model.response", response(req, 100*(i+1)))
	}
	b.add(gap, "", "session.end", map[string]any{"reason": "other"})
	return b.evs
}

// totalsOf is what the page adds up from a stream: every agent's last use event and the last svc event.
func totalsOf(evs []map[string]any) (prompt, out, cost float64) {
	last := map[string]map[string]any{}
	for _, e := range ofKind(evs, "use") {
		last[e["id"].(string)] = e
	}
	if svc := ofKind(evs, "svc"); len(svc) > 0 {
		last["\x00svc"] = svc[len(svc)-1]
	}
	for _, u := range last {
		prompt += u["rd"].(float64) + u["un"].(float64)
		out += u["out"].(float64)
		cost += u["cost"].(float64)
	}
	return prompt, out, cost
}

// The requests of a service agent are part of what the run used and cost, though it has no row: the translator sends their sum as an
// svc event with absolute values, once for each change; the page's totals (every agent's use and the svc) then equal the State's
// totals of the log, which the terminal shows. No event names the service agent and the roster does not have it.
func TestServiceAgentsUseIsSentAsOneTotal(t *testing.T) {
	log := mailmanLog()
	for _, logOnly := range []bool{false, true} {
		h := translateLog(t, log, "/work", logOnly)
		evs := h.decoded()
		svc := ofKind(evs, "svc")
		if len(svc) != 2 {
			t.Fatalf("logOnly=%v: %d svc events for two requests of the mailman: %v", logOnly, len(svc), svc)
		}
		last := svc[1]
		if last["un"] != 1000.0 || last["rd"] != 50.0 || last["out"] != 200.0 || math.Abs(last["cost"].(float64)-0.02) > 1e-9 {
			t.Errorf("logOnly=%v: the total of the mailman's requests: %v", logOnly, last)
		}
		if last["id"] != nil || svc[0]["un"] != 500.0 {
			t.Errorf("logOnly=%v: an svc event names no agent and each replaces the last: %v", logOnly, svc)
		}
		for _, e := range evs {
			if e["id"] == "mm-1" || e["ag"] == "mm-1" {
				t.Errorf("logOnly=%v: an event names the mailman: %v", logOnly, e)
			}
		}
		for _, r := range h.tr.Roster() {
			if r.ID == "mm-1" {
				t.Errorf("logOnly=%v: the mailman is in the roster", logOnly)
			}
		}
		st := state.New()
		st.ApplyAll(log)
		tot := st.Totals()
		prompt, out, cost := totalsOf(evs)
		if prompt != float64(tot.Tokens.Prompt()) || out != float64(tot.Tokens.Output) || math.Abs(cost-tot.CostUSD) > 1e-9 {
			t.Errorf("logOnly=%v: the stream adds up to %v prompt, %v output, $%v; the State's totals are %d, %d, $%v", logOnly, prompt, out, cost, tot.Tokens.Prompt(), tot.Tokens.Output, tot.CostUSD)
		}
	}
}

// The snapshot of a journal that evicted the svc event keeps what the service agents used: the keyframe holds it, and the page that
// reduces keyframe and retained events ends where one that reduced every event would.
func TestServiceAgentsUseSurvivesTheJournalsEviction(t *testing.T) {
	log := mailmanLog()
	run := func(lim Limits) *harness {
		h := newHarness(t, Config{Root: "/work", StartedAt: log[0].TS, Limits: lim})
		h.tr.d.logOnly, h.tr.d.exactPend = true, true
		h.feed(log...)
		h.advance(3 * time.Second)
		return h
	}
	full, small := run(Limits{}), run(Limits{JournalEvents: 8})
	kf, retained, _, _ := small.tr.Journal()
	_, all, _, _ := full.tr.Journal()
	if len(ofKind(decodeAll(t, retained), "svc")) != 0 || len(ofKind(decodeAll(t, kf), "svc")) != 1 {
		t.Fatalf("the svc events were not all evicted into the keyframe: retained %d, keyframe %d", len(ofKind(decodeAll(t, retained), "svc")), len(ofKind(decodeAll(t, kf), "svc")))
	}
	if got, want := view(t, reduce(t, kf, retained)), view(t, reduce(t, all)); got != want {
		t.Fatalf("keyframe ⧺ retained differs from the whole stream:\n%s", firstDiff(want, got))
	}
}

// A resumed session's history carries the service agents' use too: the state the whole log folds to is sent once, at the end of the
// history, with the other agents' token tables.
func TestServiceAgentsUseIsInTheHistoryOfAResumedSession(t *testing.T) {
	log := mailmanLog()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), encodeLog(log), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Config{Root: "/work"})
	h.set(log[len(log)-1].TS.Add(time.Hour))
	l := openLog(t, dir, log[0].Session, h.now)
	detach := h.tr.Attach(l, dir)
	defer detach()
	evs := h.decoded()
	svc := ofKind(evs, "svc")
	if len(svc) != 1 || svc[0]["t"] != 0.0 || svc[0]["un"] != 1000.0 || svc[0]["out"] != 200.0 {
		t.Fatalf("the history's svc events: %v", svc)
	}
	st := state.New()
	st.ApplyAll(log)
	tot := st.Totals()
	prompt, out, cost := totalsOf(evs)
	if prompt != float64(tot.Tokens.Prompt()) || out != float64(tot.Tokens.Output) || math.Abs(cost-tot.CostUSD) > 1e-9 {
		t.Errorf("the history adds up to %v prompt, %v output, $%v; the State's totals are %d, %d, $%v", prompt, out, cost, tot.Tokens.Prompt(), tot.Tokens.Output, tot.CostUSD)
	}
}

// An svc event is coalescable with one key for the tab: a page that falls behind needs only the newest.
func TestServiceUseFrameIsCoalescable(t *testing.T) {
	h := translateLog(t, mailmanLog(), "/work", true)
	n := 0
	for _, f := range h.frames {
		if f.Type != "ev" || !contains(string(f.Data.(wire.EvFrame).Ev), `"k":"svc"`) {
			continue
		}
		n++
		if f.Critical || !f.Coalescable || f.Key != "svc/t1" {
			t.Errorf("svc frame: %+v", f)
		}
	}
	if n == 0 {
		t.Fatal("no svc frame")
	}
}
