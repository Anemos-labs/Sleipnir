package swarm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A worker that asks the person for an approval and gets no answer is stopped by the watchdog like any worker that shows no sign of
// life (an attempt is counted: the next worker would ask the same), but the alert and the line the manager gets say what it was
// waiting for.
func TestAWorkerWaitingForAnAnswerIsStoppedWithTheQuestionNamed(t *testing.T) {
	clock := newFakeClock()
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	r := newClockRig(t, Config{MaxWriters: 4, StuckAfter: 10 * time.Minute, StuckGrace: time.Minute}, clock, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" {
			rvBlock(ctx, gate) // no sign of life: the question is what it is waiting for
		}
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker to reach the model", func() bool { return r.prov.callsFor(id) >= 1 })
	answered := r.sw.Asking(id, "Run go test in repo root [reads /workspace outside the workspace]")

	clock.Advance(11 * time.Minute)
	r.sw.superviseOnce(clock.Now())
	as := r.sw.Board.Snapshot().Alerts
	if len(as) != 1 || !strings.Contains(as[0].Text, "waited") || !strings.Contains(as[0].Text, "Run go test in repo root") {
		t.Fatalf("after StuckAfter the alert is %+v: it should say what the worker is waiting for", as)
	}
	clock.Advance(10 * time.Minute)
	r.sw.superviseOnce(clock.Now())
	rvWait(t, "the run to be cancelled and reported", func() bool {
		return r.idle(id) && mailSent(r, "without an answer to its question to the person") > 0
	})
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	if tk.Status != StatusTodo || tk.Attempts != 1 {
		t.Fatalf("T1 = %s attempts=%d, want todo after one attempt: the next worker would ask the same question", tk.Status, tk.Attempts)
	}
	if mailSent(r, "without an answer to its question to the person: Run go test in repo root") != 1 {
		t.Fatal("the manager was not told, once, what the worker was waiting for")
	}
	if mailSent(r, "stuck: no progress") != 0 {
		t.Fatal("a worker that waited for an answer was reported as stuck")
	}
	answered() // the answer arrives late: nothing to undo, and a second call is harmless
	answered()
}

// Once the question is answered the worker is judged like any other: the watchdog says what it always said.
func TestAnAnsweredQuestionIsNoLongerWhatTheWatchdogSays(t *testing.T) {
	clock := newFakeClock()
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	r := newClockRig(t, Config{MaxWriters: 4, StuckAfter: 10 * time.Minute, StuckGrace: time.Minute}, clock, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" {
			rvBlock(ctx, gate)
		}
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker to reach the model", func() bool { return r.prov.callsFor(id) >= 1 })
	first := r.sw.Asking(id, "first question")
	second := r.sw.Asking(id, "second question") // two at once (parallel read-only tools): the worker waits until both are answered
	first()
	if m := r.sw.get(id); m.asking != 1 {
		t.Fatalf("%d questions open after one of two was answered", m.asking)
	}
	second()
	clock.Advance(11 * time.Minute)
	r.sw.superviseOnce(clock.Now())
	as := r.sw.Board.Snapshot().Alerts
	if len(as) != 1 || strings.Contains(as[0].Text, "question") || !strings.Contains(as[0].Text, "no progress") {
		t.Fatalf("the alert is %+v, want the usual one for a worker with no sign of life", as)
	}
}

// The manager's questions and an agent that is not there are not recorded (and are harmless).
func TestAskingOfTheManagerOrOfNobodyIsHarmless(t *testing.T) {
	r := newClockRig(t, Config{MaxWriters: 4}, newFakeClock(), func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	r.sw.Asking("mgr", "a question")()
	r.sw.Asking("nobody", "a question")()
	if m := r.sw.get("mgr"); m.asking != 0 {
		t.Fatalf("the manager has %d questions open", m.asking)
	}
}
