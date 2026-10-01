package swarm

// The bound on runs that peer mail starts (Config.MaxMailWakes) holds however the mail meets the
// worker: when the worker is idle, while it runs, and in the moment between the end of a run (the
// board already says idle) and the harness looking at what its inbox still holds. The first CI run
// on GitHub failed once on the black-box test of the bound ("peer mail started 4 runs of the worker,
// want exactly the bound of 3"): mail that came in that moment started a run that nothing counted.
//
// Everything here makes the moment the test's own, not luck's: a hook on the event log sends the
// mail from inside the worker's last event, and the model's reply is held while the mail arrives.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// wbHook is an event log that calls on after recording each event, on the goroutine that emitted it.
type wbHook struct {
	events.Emitter
	on func(agent, typ string)
}

func (h *wbHook) Emit(agent, typ string, data any, opts ...events.Opt) (uint64, error) {
	seq, err := h.Emitter.Emit(agent, typ, data, opts...)
	h.on(agent, typ)
	return seq, err
}

// wbGate holds the replies of the model, while it is closed, at the point where a run has read its
// inbox and is waiting for the model: mail that comes in then comes in during the run.
type wbGate struct {
	mu      sync.Mutex
	ch      chan struct{} // nil: open
	arrived atomic.Int32  // replies that have waited at the gate, ever
}

func (g *wbGate) shut() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch == nil {
		g.ch = make(chan struct{})
	}
}

func (g *wbGate) open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch != nil {
		close(g.ch)
		g.ch = nil
	}
}

// cycle lets the replies that wait go and closes the gate again for the next: with no moment in
// between at which a run that starts straight away would find it open.
func (g *wbGate) cycle() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch != nil {
		close(g.ch)
	}
	g.ch = make(chan struct{})
}

func (g *wbGate) pass(ctx context.Context) {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	if ch == nil {
		return
	}
	g.arrived.Add(1)
	rvBlock(ctx, ch)
}

// wbQuiet is how long a test waits for a run that must not start: a start is a matter of
// microseconds, and a wait that is too short can only miss a bug, never report one that is not there.
const wbQuiet = 150 * time.Millisecond

// wbTeam is a swarm with a manager and two idle workers (a backend and a frontend) that have each
// made the one request of their first run. fn is the model: it sees the agent that asks.
type wbTeam struct {
	*rvRig
	be, fe string
	gate   *wbGate
}

func newWBTeam(t *testing.T, limit int, tweak func(*Deps)) *wbTeam {
	t.Helper()
	loose := RouterConfig{MaxPerMinute: 1 << 30, MaxPerPairPerMin: 1 << 30, MaxChars: 600, DedupeWindow: time.Nanosecond}
	g := &wbGate{}
	r := newRVRigWith(t, Config{SessionID: "wb", MaxMailWakes: limit, Router: loose}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role != "manager" {
			g.pass(ctx)
		}
		return rvReply{Text: "noted by " + c.Agent}
	}, tweak)
	r.sw.StartManager()
	be, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "Backend work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	fe, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "Frontend work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "both workers to finish their first run", func() bool {
		return r.idle(be) && r.idle(fe) && r.prov.callsFor(be) == 1 && r.prov.callsFor(fe) == 1
	})
	return &wbTeam{rvRig: r, be: be, fe: fe, gate: g}
}

// peer mails the backend worker from the frontend one, manager from the manager. (They are called
// from a hook as well, so they report with Errorf.)
func (w *wbTeam) peer(text string) {
	w.t.Helper()
	if _, err := w.sw.Router.Send(w.fe, w.be, "info", text); err != nil {
		w.t.Errorf("mail from %s: %v", w.fe, err)
	}
}

func (w *wbTeam) manager(text string) {
	w.t.Helper()
	if _, err := w.sw.Router.Send("mgr", w.be, "info", text); err != nil {
		w.t.Errorf("mail from the manager: %v", err)
	}
}

// atModel waits for the n-th reply the gate has held to be waiting there: the n-th run that
// reached the model with it closed.
func (w *wbTeam) atModel(n int32) {
	w.t.Helper()
	rvWait(w.t, "a run to wait for the model", func() bool { return w.gate.arrived.Load() >= n })
}

func (w *wbTeam) calls() int      { return w.prov.callsFor(w.be) }
func (w *wbTeam) unread() int     { return w.sw.get(w.be).a.PendingInbox() }
func (w *wbTeam) limitNotes() int { return len(w.log.OfType(events.TypeSwarmWakeLimit)) }

// Mail from another worker that arrives in the moment between the end of a run and the harness
// looking at what is left in the inbox is mail like any other: past the bound it starts no run.
func TestPeerMailThatArrivesAsARunEndsStartsNoRunPastTheBound(t *testing.T) {
	// The worker's first run (its task) ends with the first agent.end event of be-1; the run the first ping starts ends with the second.
	// The late ping is sent from inside that second event, whatever else the other goroutines are doing: counting the events, not
	// watching the clock, is what makes the moment the test's own.
	var ends atomic.Int32
	var team *wbTeam
	team = newWBTeam(t, 1, func(d *Deps) {
		inner := d.Events
		d.Events = &wbHook{Emitter: inner, on: func(agent, typ string) {
			if typ == events.TypeAgentEnd && agent == "be-1" && ends.Add(1) == 2 {
				team.peer("the late ping")
			}
		}}
	})

	// The first ping wakes the worker: the one wake the bound allows. As its run ends, after the
	// worker is idle and before the harness has looked at its inbox, the late ping arrives.
	team.peer("the first ping")
	rvWait(t, "the first ping to be answered", func() bool { return team.calls() >= 2 && team.idle(team.be) })
	time.Sleep(wbQuiet)
	if got := team.calls(); got != 2 {
		t.Fatalf("the late ping started a run: the worker made %d requests, want 2 (its task, the first ping)", got)
	}
	if !team.idle(team.be) {
		t.Fatal("the worker is running again")
	}
	if n := team.unread(); n != 1 {
		t.Fatalf("%d messages wait in the inbox, want the late ping alone", n)
	}
	if n := team.limitNotes(); n != 1 {
		t.Fatalf("the bound was reported %d times, want once", n)
	}

	// The late ping was held, not lost: the manager's word wakes the worker and it reads both.
	team.manager("the manager speaks")
	rvWait(t, "the manager's mail to wake the worker", func() bool { return team.calls() == 3 && team.idle(team.be) })
	if !team.prov.sawEver("the late ping") {
		t.Fatal("the held ping was not read by the run the manager started")
	}
}

// The log of who wrote what is waiting must say who wrote what is waiting now, not what has been
// read: the manager's mail that a run took does not make the peer mail that came after it the
// manager's.
func TestPeerMailAfterTheManagersWasReadStillStartsNoRunPastTheBound(t *testing.T) {
	team := newWBTeam(t, 1, nil)
	m := team.sw.get(team.be)
	m.mu.Lock()
	m.mailWakes = 1 // the task's bound is used up
	m.mu.Unlock()

	team.gate.shut()
	team.manager("manager first") // a run, which reads this and waits for the model
	team.atModel(1)
	team.peer("peer during the run") // the worker is running: nothing wakes it, the mail waits
	team.gate.cycle()                // the run ends; a run that should not start would wait at the gate
	rvWait(t, "the run to end, or another to start", func() bool { return team.idle(team.be) || team.gate.arrived.Load() > 1 })
	time.Sleep(wbQuiet)
	if !team.idle(team.be) || team.gate.arrived.Load() != 1 || team.calls() != 2 {
		t.Fatalf("the peer mail restarted the worker past the bound (%d requests, %d runs at the model)", team.calls(), team.gate.arrived.Load())
	}
	if n := team.unread(); n != 1 {
		t.Fatalf("%d messages wait in the inbox, want the peer's alone", n)
	}
	if n := team.limitNotes(); n != 1 {
		t.Fatalf("the bound was reported %d times, want once", n)
	}
	team.gate.open()
}

// Peer mail that a run leaves waiting starts a run like peer mail that finds the worker idle, and the
// two kinds together are bounded: two workers whose mail always arrives while the other is running
// cannot keep each other running for ever.
func TestPeerMailLeftWaitingByRunsStartsNoMoreRunsThanTheBound(t *testing.T) {
	const limit = 2
	team := newWBTeam(t, limit, nil)
	m := team.sw.get(team.be)

	team.gate.shut()
	team.manager("start") // a run the bound does not count
	for i := 1; i <= limit; i++ {
		team.atModel(int32(i))
		team.peer("ping")          // arrives during the run
		team.gate.cycle()          // the run ends; the next waits for the model here
		team.atModel(int32(i + 1)) // the harness started it for the mail that was left waiting
		// The count follows the start by a moment: the run is already at the model when the harness writes it down.
		want := i
		rvWait(t, fmt.Sprintf("the %d restarts for peer mail to be counted", want), func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.mailWakes == want
		})
	}
	team.peer("one ping too many") // arrives during the last run the bound allowed
	team.gate.cycle()
	rvWait(t, "the last run to end, or another to start", func() bool { return team.idle(team.be) || team.gate.arrived.Load() > limit+1 })
	time.Sleep(wbQuiet)
	if !team.idle(team.be) || team.gate.arrived.Load() != limit+1 {
		t.Fatalf("the worker ran past the bound (%d requests, %d runs at the model)", team.calls(), team.gate.arrived.Load())
	}
	if got, want := team.calls(), 1+1+limit; got != want {
		t.Fatalf("the worker made %d requests, want %d (its task, the manager's run, one per ping within the bound)", got, want)
	}
	if n := team.unread(); n != 1 {
		t.Fatalf("%d messages wait in the inbox, want the last ping alone", n)
	}
	if n := team.limitNotes(); n != 1 {
		t.Fatalf("the bound was reported %d times, want once", n)
	}
	team.gate.open()
}

// Mail the bound does not apply to still restarts a worker that has used its wakes up: the manager's
// word, with peer mail or without, and mail the log knows nothing about.
func TestMailTheBoundDoesNotApplyToStillRestartsAWorker(t *testing.T) {
	for _, c := range []struct {
		name string
		mail func(w *wbTeam)
	}{
		{"the manager's", func(w *wbTeam) { w.manager("from the manager") }},
		{"the manager's and a peer's", func(w *wbTeam) { w.peer("from a peer"); w.manager("from the manager") }},
		{"a message nothing recorded", func(w *wbTeam) { w.sw.get(w.be).a.Send("[mail h1 info from harness] unrecorded") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			team := newWBTeam(t, 1, nil)
			m := team.sw.get(team.be)
			m.mu.Lock()
			m.mailWakes = 1
			m.mu.Unlock()
			// Put the mail in the inbox as a worker that is running would have it, then let a run end.
			m.mu.Lock()
			m.life = lifeRunning
			m.mu.Unlock()
			c.mail(team)
			m.mu.Lock()
			m.life = lifeIdle
			m.mu.Unlock()
			team.gate.shut()
			team.sw.afterIdle(m, true)
			team.atModel(1) // the restart: a run is waiting for the model
			team.gate.open()
		})
	}
}
