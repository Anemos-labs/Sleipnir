package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// scheduledOf reports whether a restart of the worker waits and its scheduled event is logged, so a supervisor pass may start it.
func scheduledOf(r *rvRig, id string) bool {
	m := r.sw.get(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restart != nil && m.restart.logged
}

// pendingOf is the automatic restart waiting for a member, nil when there is none.
func pendingOf(r *rvRig, id string) *pendingRestart {
	m := r.sw.get(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restart
}

// boardAgent is the member as the board shows it.
func boardAgent(r *rvRig, id string) AgentInfo {
	a, _ := r.sw.Board.Snapshot().Agent(id)
	return a
}

// ends counts the agent.end events of a member, and how many of them say the run failed.
func ends(r *rvRig, id string) (all, failed int) {
	for _, e := range r.log.OfType(events.TypeAgentEnd) {
		var p struct{ ID, State string }
		if json.Unmarshal(e.Data, &p) == nil && p.ID == id {
			all++
			if p.State == "failed" {
				failed++
			}
		}
	}
	return all, failed
}

// restartPhases lists the phases of the swarm.restart events of a member, in order.
func restartPhases(r *rvRig, id string) []string {
	var out []string
	for _, e := range r.log.OfType(eventRestart) {
		var p struct{ Phase string }
		if e.Agent == id && json.Unmarshal(e.Data, &p) == nil {
			out = append(out, p.Phase)
		}
	}
	return out
}

// shownStatus is the status the interfaces show for an agent: the log folded into the State that the terminal draws and the web
// interface translates (internal/web/translate maps it to the page's state, StatusError to "stuck").
func shownStatus(t *testing.T, r *rvRig, id string) state.Status {
	t.Helper()
	ui := state.New()
	ui.ApplyAll(r.log.All())
	a, ok := ui.AgentLite(id)
	if !ok {
		t.Fatalf("the State has no agent %s", id)
	}
	return a.Status
}

// stepLimitedBackend makes the backend role's step window short: a worker that only reads ends its run at the step limit.
func stepLimitedBackend(r *rvRig) {
	be := r.sw.roles["backend"]
	be.MaxSteps = 4
	r.sw.roles["backend"] = be
}

// A worker whose run ends at the step limit while it holds its task is restarted on that task by the harness: the task stays
// its own (an attempt counted), the board shows it waiting for the restart instead of failed, the manager is told once not to
// reassign it, and after the backoff a fresh run (fresh step windows) gets a note that says why the last run stopped and what
// to do differently. In a long team run a worker that failed this way was shown stuck, with its task waiting, until the
// manager noticed it.
func TestAWorkerStoppedAtItsStepLimitIsRestartedOnItsTask(t *testing.T) {
	clock := newFakeClock()
	var after atomic.Int32 // requests of the restarted run
	r := newClockRig(t, Config{MaxWriters: 4}, clock, func(_ context.Context, c *rvCall) rvReply {
		switch {
		case c.Role != "backend":
			return rvReply{Text: "ok"}
		case !c.Sees("automatic restart 1 of 2"):
			return readReply // reads and never edits: the step windows end the run
		}
		switch after.Add(1) {
		case 1:
			return editReply
		case 2:
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "built"}}}}
		}
		return rvReply{Text: "built the API"}
	})
	stepLimitedBackend(r)
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "the step limit to end the run", func() bool { n, _ := ends(r, id); return n == 1 && r.idle(id) })
	if _, failed := ends(r, id); failed != 1 {
		t.Fatalf("the run's end does not say it failed")
	}
	if p := pendingOf(r, id); p == nil || p.n != 1 || p.why != "step limit reached" || strings.Join(p.tasks, ",") != "T1" {
		t.Fatalf("pending restart = %+v, want restart 1 on T1 after the step limit", p)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusDoing || tk.Owner != id || tk.Attempts != 1 {
		t.Fatalf("T1 = %s/%s attempts=%d, want doing, %s's, one attempt counted", tk.Status, tk.Owner, tk.Attempts, id)
	}
	if a := boardAgent(r, id); a.State != "idle" || a.Line != "restarting: step limit reached" {
		t.Fatalf("board shows %s as %q (%q) while its restart waits", id, a.State, a.Line)
	}
	if s := shownStatus(t, r, id); s != state.StatusIdle {
		t.Fatalf("the interfaces show %s as %s while its restart waits", id, s)
	}
	rvWait(t, "the manager's line", func() bool { return mailSent(r, "restarts it on its own") == 1 })
	if mailSent(r, id+" stopped (step limit reached). The harness restarts it on its own (automatic restart 1 of 2), and it keeps T1: do not reassign") != 1 ||
		mailSent(r, "returned to todo") != 0 {
		t.Fatal("the manager was not told, in one line, that the worker is restarted on its task")
	}
	if u := r.sw.unfinishedWork(); len(u.running) != 1 || !strings.HasPrefix(u.running[0], id) {
		t.Fatalf("a worker waiting for its restart is not counted at work: %+v", u)
	}

	r.sw.superviseOnce(clock.Now().Add(restartBackoff - time.Second))
	if !r.idle(id) || pendingOf(r, id) == nil {
		t.Fatal("the restart started before its backoff")
	}
	calls := r.prov.callsFor(id)
	clock.Advance(restartBackoff)
	r.sw.superviseOnce(clock.Now())
	rvWait(t, "the restarted run to submit T1", func() bool {
		tk, _ := r.sw.Board.Snapshot().Task("T1")
		return tk.Status == StatusReview && r.idle(id)
	})
	if r.prov.callsFor(id) <= calls {
		t.Fatal("no new run reached the model")
	}
	for _, want := range []string{"Your last run stopped (step limit reached)", "automatic restart 1 of 2", "T1 is still yours", "fresh step allowance", "task get T1"} {
		if !r.prov.sawEver(want) {
			t.Errorf("the restarted run was not told %q", want)
		}
	}
	if got := restartPhases(r, id); strings.Join(got, ",") != "scheduled,start" {
		t.Errorf("swarm.restart phases = %v", got)
	}
	m := r.sw.get(id)
	m.mu.Lock()
	n := m.restarts
	m.mu.Unlock()
	if n != 0 || pendingOf(r, id) != nil {
		t.Errorf("after a run that ended on its own: %d restarts counted, pending %+v", n, pendingOf(r, id))
	}
}

// The restarts of one worker on the same work are bounded: two in a row, each after a longer wait; the third failure is
// settled as a failure always was (the task back to the board, the worker shown failed) and the manager's line says what the
// harness tried.
func TestAutomaticRestartsStopAtTheirBoundAndTheManagerIsTold(t *testing.T) {
	clock := newFakeClock()
	r := newClockRig(t, Config{MaxWriters: 4, MaxAttempts: 10}, clock, func(_ context.Context, c *rvCall) rvReply {
		if c.Role != "backend" {
			return rvReply{Text: "ok"}
		}
		return readReply
	})
	stepLimitedBackend(r)
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= maxAutoRestarts; n++ {
		rvWait(t, fmt.Sprintf("failed run %d", n), func() bool { e, _ := ends(r, id); return e == n && r.idle(id) && scheduledOf(r, id) })
		p := pendingOf(r, id)
		if p.n != n {
			t.Fatalf("restart %d is counted as %d", n, p.n)
		}
		wait := restartBackoff << (n - 1)
		if n > 1 {
			r.sw.superviseOnce(clock.Now().Add(wait / 2))
			if pendingOf(r, id) == nil {
				t.Fatalf("restart %d did not wait longer than the first", n)
			}
		}
		clock.Advance(wait)
		r.sw.superviseOnce(clock.Now())
	}
	rvWait(t, "the last failed run", func() bool { e, _ := ends(r, id); return e == maxAutoRestarts+1 && r.idle(id) })
	rvWait(t, "the manager's line", func() bool { return mailSent(r, "does not restart it again") == 1 })
	if pendingOf(r, id) != nil {
		t.Fatal("a third restart was scheduled")
	}
	if a := boardAgent(r, id); a.State != "failed" {
		t.Fatalf("a worker that is not restarted shows %q", a.State)
	}
	if s := shownStatus(t, r, id); s != state.StatusError {
		t.Fatalf("the interfaces show a worker that is not restarted as %s", s)
	}
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	if tk.Status != StatusTodo || tk.Owner != "" || tk.Attempts != maxAutoRestarts+1 {
		t.Fatalf("T1 = %s/%s attempts=%d, want todo with %d attempts", tk.Status, tk.Owner, tk.Attempts, maxAutoRestarts+1)
	}
	if mailSent(r, "T1 returned to todo: "+id+" stopped (step limit reached), attempt 3 of 10. The harness restarted "+id+" automatically twice on this work, and does not restart it again.") != 1 {
		t.Error("the manager's line does not say what was tried")
	}
	if got := strings.Join(restartPhases(r, id), ","); got != "scheduled,start,scheduled,start,exhausted" {
		t.Errorf("swarm.restart phases = %s", got)
	}
	clock.Advance(10 * time.Minute) // less than IdleRetire, which would retire the idle worker
	r.sw.superviseOnce(clock.Now())
	if e, _ := ends(r, id); e != maxAutoRestarts+1 || r.running(id) {
		t.Fatal("the worker was started again past the bound")
	}
}

// A restart whose scheduled event is not yet in the log does not start, however due: the supervisor leaves it to a later pass,
// so the log never shows a restart starting before it was scheduled.
func TestARestartDoesNotStartBeforeItsScheduledEventIsLogged(t *testing.T) {
	clock := newFakeClock()
	r := newClockRig(t, Config{MaxWriters: 4, MaxAttempts: 10}, clock, func(_ context.Context, c *rvCall) rvReply {
		if c.Role != "backend" {
			return rvReply{Text: "ok"}
		}
		return readReply
	})
	stepLimitedBackend(r)
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "the failed run", func() bool { e, _ := ends(r, id); return e == 1 && r.idle(id) && scheduledOf(r, id) })
	m := r.sw.get(id)
	setLogged := func(v bool) {
		m.mu.Lock()
		m.restart.logged = v
		m.mu.Unlock()
	}
	setLogged(false) // as between the run publishing the restart and logging its event
	clock.Advance(restartBackoff)
	r.sw.superviseOnce(clock.Now())
	if pendingOf(r, id) == nil {
		t.Fatal("a restart started before its scheduled event was logged")
	}
	setLogged(true)
	r.sw.superviseOnce(clock.Now())
	rvWait(t, "the restarted run", func() bool { e, _ := ends(r, id); return e == 2 })
	if got := strings.Join(restartPhases(r, id), ","); !strings.HasPrefix(got, "scheduled,start") {
		t.Errorf("swarm.restart phases = %s", got)
	}
}

// What the harness does not restart: a run that spent the agent's budget, one that was interrupted, and a worker whose restart
// was waiting when the person interrupted the team (the restart is called off and its task returns to the board). A step-limit
// stop under the same configuration is restarted, so each case differs from it only by how the run ended.
func TestNoAutomaticRestartAfterABudgetStopOrAnInterrupt(t *testing.T) {
	spend := core.Usage{InputTokens: 1000} // $0.004 at the rig's price
	cases := []struct {
		name    string
		cfg     Config
		model   func(context.Context, *rvCall) rvReply
		stop    func(r *rvRig, id string) // ends the run (nil: it ends on its own)
		restart bool
	}{
		{name: "step limit", model: func(context.Context, *rvCall) rvReply { return readReply }, restart: true},
		{name: "agent budget", cfg: Config{AgentBudgetUSD: 0.001}, model: func(context.Context, *rvCall) rvReply {
			return rvReply{Tools: readReply.Tools, Usage: spend}
		}},
		{name: "interrupted", model: idleModel, stop: func(r *rvRig, id string) {
			rvWait(r.t, "the worker's request", func() bool { return r.prov.callsFor(id) >= 1 })
			r.sw.stopWorkers("interrupted", false)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock := newFakeClock()
			cfg := c.cfg
			cfg.MaxWriters = 4
			r := newClockRig(t, cfg, clock, func(ctx context.Context, rc *rvCall) rvReply {
				if rc.Role != "backend" {
					return rvReply{Text: "ok"}
				}
				return c.model(ctx, rc)
			})
			stepLimitedBackend(r)
			r.sw.StartManager()
			id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", By: "mgr"})
			if err != nil {
				t.Fatal(err)
			}
			if c.stop != nil {
				c.stop(r, id)
			}
			rvWait(t, "the run to end", func() bool { e, _ := ends(r, id); return e == 1 && r.idle(id) })
			if got := pendingOf(r, id) != nil; got != c.restart {
				t.Fatalf("restart scheduled: %v, want %v", got, c.restart)
			}
			if c.restart {
				return
			}
			if mailSent(r, "restarts it on its own") != 0 {
				t.Error("the manager was told of a restart")
			}
			if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusTodo || tk.Owner != "" {
				t.Errorf("T1 = %s/%s, want back on the board", tk.Status, tk.Owner)
			}
			clock.Advance(10 * time.Minute) // less than IdleRetire, which would retire the idle worker
			r.sw.superviseOnce(clock.Now())
			if e, _ := ends(r, id); e != 1 || r.running(id) {
				t.Error("the worker was started again")
			}
		})
	}
	t.Run("restart waiting when the team is interrupted", func(t *testing.T) {
		clock := newFakeClock()
		r := newClockRig(t, Config{MaxWriters: 4}, clock, func(_ context.Context, c *rvCall) rvReply {
			if c.Role != "backend" {
				return rvReply{Text: "ok"}
			}
			return readReply
		})
		stepLimitedBackend(r)
		r.sw.StartManager()
		id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", By: "mgr"})
		if err != nil {
			t.Fatal(err)
		}
		rvWait(t, "the step limit to end the run", func() bool { return r.idle(id) && scheduledOf(r, id) })
		r.sw.stopWorkers("interrupted", false)
		if pendingOf(r, id) != nil {
			t.Fatal("the interrupt left the restart waiting")
		}
		if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusTodo || tk.Owner != "" || tk.Attempts != 1 {
			t.Fatalf("T1 = %s/%s attempts=%d, want todo with the failed run's one attempt", tk.Status, tk.Owner, tk.Attempts)
		}
		if a := boardAgent(r, id); a.State != "failed" || a.Line != "step limit reached" {
			t.Fatalf("board shows %s as %q (%q), want failed: it is not restarted", id, a.State, a.Line)
		}
		clock.Advance(10 * time.Minute) // less than IdleRetire, which would retire the idle worker
		r.sw.superviseOnce(clock.Now())
		if e, _ := ends(r, id); e != 1 || r.running(id) {
			t.Fatal("the worker was started after the interrupt")
		}
	})
}

// Which stops the harness restarts on its own, and the advice each restart carries.
func TestRestartCauses(t *testing.T) {
	stepErr := fmt.Errorf("agent be-1: %w 6 reached", agent.ErrStepLimit)
	for _, c := range []struct {
		name         string
		kind         stopKind
		count, asked bool
		fixed        bool
		err          error
		want         restartCause
		ok           bool
	}{
		{name: "step limit", kind: stopFailed, err: stepErr, want: causeSteps, ok: true},
		{name: "step limit under fixed steps", kind: stopFailed, err: stepErr, fixed: true},
		{name: "repetition guard", kind: stopFailed, err: fmt.Errorf("agent be-1: %w: read failed the same way 8 times", agent.ErrStuck), want: causeRepeat, ok: true},
		{name: "output limit", kind: stopFailed, err: agent.ErrOutputLimit, want: causeOutput, ok: true},
		{name: "crash", kind: stopFailed, err: &panicError{val: "boom"}, want: causeCrash, ok: true},
		{name: "provider error", kind: stopFailed, err: errors.New("provider: server (http 502)"), want: causeError, ok: true},
		{name: "agent budget", kind: stopFailed, err: agent.ErrBudget},
		{name: "closed agent", kind: stopFailed, err: agent.ErrClosed},
		{name: "watchdog", kind: stopHarness, count: true, err: context.Canceled, want: causeQuiet, ok: true},
		{name: "watchdog, question unanswered", kind: stopHarness, count: true, asked: true, err: context.Canceled},
		{name: "harness stop that is no failure", kind: stopHarness, err: context.Canceled},
		{name: "interrupted", kind: stopInterrupted, err: context.Canceled},
		{name: "clean", kind: stopClean},
	} {
		got, ok := restartCauseOf(c.kind, c.count, c.err, c.asked, c.fixed)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: restart %v (cause %d), want %v (cause %d)", c.name, ok, got, c.ok, c.want)
		}
	}
}

// poolRestartRig is a backend pool of three in a persistent team, planning. fail says, for each request, whether it fails:
// a member whose request fails ends its run with an error; the others' requests block until their run is cancelled.
func poolRestartRig(t *testing.T, clock *fakeClock, fail func(c *rvCall) bool) (*rvRig, Task) {
	t.Helper()
	cfg := Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}}
	r := newClockRig(t, cfg, clock, func(ctx context.Context, c *rvCall) rvReply {
		if fail(c) {
			return rvReply{Err: errors.New("request failed")}
		}
		<-ctx.Done()
		return rvReply{Text: "stopped"}
	})
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	return r, r.sw.Board.Snapshot().Tasks[0]
}

// A pool member that holds no task of its own (it waits for the pool's next phase) and whose run fails is restarted in its
// pool: shown waiting for the restart, not failed, and told which pool it is still a member of. In a long team run such a
// member was shown stuck for half an hour, until the pool's next mail happened to wake it.
func TestAPoolMemberWithoutATaskIsRestartedInItsPool(t *testing.T) {
	clock := newFakeClock()
	var broken atomic.Bool
	broken.Store(true)
	r, parent := poolRestartRig(t, clock, func(c *rvCall) bool { return c.Agent == "be-3" && broken.Load() })
	member := "be-3"
	rvWait(t, member+"'s run to fail", func() bool { _, f := ends(r, member); return f == 1 && r.idle(member) })
	p := pendingOf(r, member)
	if p == nil || p.pool != parent.ID || len(p.tasks) != 0 {
		t.Fatalf("pending restart = %+v, want one in pool %s", p, parent.ID)
	}
	if a := boardAgent(r, member); a.State != "idle" || !strings.HasPrefix(a.Line, "restarting: stopped with an error: request failed") {
		t.Fatalf("board shows %s as %q (%q) while its restart waits", member, a.State, a.Line)
	}
	if s := shownStatus(t, r, member); s != state.StatusIdle {
		t.Fatalf("the interfaces show %s as %s while its restart waits", member, s)
	}
	rvWait(t, "the manager's line", func() bool { return mailSent(r, "it keeps its place in pool "+parent.ID) == 1 })
	broken.Store(false)
	clock.Advance(restartBackoff)
	r.sw.superviseOnce(clock.Now())
	rvWait(t, "the restarted run", func() bool { return r.running(member) })
	rvWait(t, "the note", func() bool {
		return r.prov.sawEver("You are still a member of pool " + parent.ID + " (phase planning). Carry on from where your work stands.")
	})
	if !r.prov.sawEver("Your last run stopped (stopped with an error: request failed)") {
		t.Error("the restarted member was not told why its last run stopped")
	}
}

// A pool leader whose run fails is restarted on its pool instead of the manager being asked to resume the pool; once the bound
// is reached the manager is asked, as before, with what was tried.
func TestAFailedPoolLeaderIsRestartedBeforeTheManagerIsAsked(t *testing.T) {
	clock := newFakeClock()
	r, parent := poolRestartRig(t, clock, func(c *rvCall) bool { return c.Agent == "be-1" })
	lead := parent.Owner
	if lead != "be-1" {
		t.Fatalf("leader %s", lead)
	}
	for n := 1; n <= maxAutoRestarts; n++ {
		rvWait(t, fmt.Sprintf("failed run %d", n), func() bool { _, f := ends(r, lead); return f == n && r.idle(lead) && scheduledOf(r, lead) })
		if p := pendingOf(r, lead); p.n != n || strings.Join(p.tasks, ",") != parent.ID {
			t.Fatalf("pending restart = %+v, want restart %d on %s", p, n, parent.ID)
		}
		if harnessMail(r, "Pool leader stopped")["mgr"] != 0 {
			t.Fatalf("the manager was asked to resume the pool the harness restarts (restart %d)", n)
		}
		clock.Advance(restartBackoff << (n - 1))
		r.sw.superviseOnce(clock.Now())
	}
	rvWait(t, "the last failed run", func() bool { _, f := ends(r, lead); return f == maxAutoRestarts+1 && r.idle(lead) })
	rvWait(t, "the manager to be asked", func() bool { return harnessMail(r, "Pool leader stopped on "+parent.ID)["mgr"] == 1 })
	// The harness does not restart the leader again, so the line names the way past a leader whose model keeps failing.
	if harnessMail(r, "excuse it (pool action=excuse id="+parent.ID+" member="+lead+" text=<why>) and the next member leads.")["mgr"] != 1 {
		t.Error("the manager's line does not say it may excuse the leader")
	}
	// The line of what was tried is a mail of its own, sent when the stop is settled, after the pool leader's: it is waited for, not
	// assumed to be there already (on a loaded CI runner it was not, and the test failed on main).
	rvWait(t, "the manager to be told what was tried", func() bool { return mailSent(r, "The harness restarted "+lead+" automatically twice") == 1 })
	if p, _ := r.sw.Board.Snapshot().Task(parent.ID); p.Status != StatusDoing || p.Owner != lead || p.Attempts != 0 {
		t.Errorf("the pool task = %s/%s attempts=%d: a leader's stop changes nothing on it", p.Status, p.Owner, p.Attempts)
	}
	if !r.prov.sawEver("You still lead pool " + parent.ID + " (phase planning)") {
		t.Error("the restarted leader was not told it still leads its pool")
	}
}

// A pool member's child task that its run failed on stays the member's, in doing, while the harness restarts it: the repair
// pass, which restarts a todo task on its last owner, has nothing to do, and nobody else can claim the child meanwhile.
func TestAPoolMembersChildStaysItsOwnWhileItIsRestarted(t *testing.T) {
	clock := newFakeClock()
	var broken atomic.Bool
	r, parent := poolRestartRig(t, clock, func(c *rvCall) bool { return c.Agent == "be-2" && broken.Load() })
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask(parent.Owner, TaskSpec{Parent: parent.ID, Title: "handlers", Role: "backend", Files: []string{"api/handlers.go"}})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "be-2 in a turn", func() bool { return r.running("be-2") })
	broken.Store(true)
	r.sw.stopRun(r.sw.get("be-2"), "test: end the turn", false) // its next run, on the child, fails
	rvWait(t, "be-2 idle", func() bool { return r.idle("be-2") && pendingOf(r, "be-2") == nil })
	if _, err := r.sw.Spawn(SpawnReq{Agent: "be-2", TaskID: child.ID, By: parent.Owner}); err != nil {
		t.Fatal(err)
	}
	// The restart is waited for once it is logged: the supervisor passes over one not yet logged, however due.
	rvWait(t, "the child's run to fail", func() bool { return r.idle("be-2") && scheduledOf(r, "be-2") })
	if c, _ := r.sw.Board.Snapshot().Task(child.ID); c.Status != StatusDoing || c.Owner != "be-2" || c.Attempts != 1 {
		t.Fatalf("%s = %s/%s attempts=%d, want doing, be-2's, one attempt", child.ID, c.Status, c.Owner, c.Attempts)
	}
	r.sw.repairTick(clock.Now().Add(time.Hour))
	if c, _ := r.sw.Board.Snapshot().Task(child.ID); c.Status != StatusDoing || c.Owner != "be-2" || !r.idle("be-2") {
		t.Fatalf("the repair pass moved the child (%s/%s) or started be-2 before its restart", c.Status, c.Owner)
	}
	broken.Store(false)
	clock.Advance(restartBackoff)
	r.sw.superviseOnce(clock.Now())
	rvWait(t, "the restarted run", func() bool { return r.running("be-2") })
	rvWait(t, "the note", func() bool { return r.prov.sawEver(child.ID + " is still yours") })
}

// restartWaiting is a rig whose backend worker ended its run at the step limit on T1 and waits for its automatic restart.
func restartWaiting(t *testing.T, cfg Config) (*rvRig, *fakeClock, string) {
	t.Helper()
	clock := newFakeClock()
	cfg.MaxWriters = 4
	r := newClockRig(t, cfg, clock, func(_ context.Context, c *rvCall) rvReply {
		if c.Role != "backend" {
			return rvReply{Text: "ok"}
		}
		return readReply
	})
	stepLimitedBackend(r)
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "the step limit to end the run", func() bool { return r.idle(id) && scheduledOf(r, id) })
	return r, clock, id
}

// The repair pass starts a todo task on its last owner when that worker is idle. A worker waiting for its automatic restart is
// idle, but not free: a run on another task would adopt the work the restart keeps, and the restart's wait and note would be
// lost. The repair pass leaves it to its restart.
func TestTheRepairPassLeavesAWorkerThatWaitsForItsRestart(t *testing.T) {
	r, clock, id := restartWaiting(t, Config{PersistentPools: true})
	other, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "write the docs", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Assign("mgr", id, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.sw.Board.Requeue(id, other.ID, 0, "test: its run failed", true, 0); !ok {
		t.Fatal("the other task was not returned to the board")
	}
	r.sw.repairTick(clock.Now())
	if !r.idle(id) || pendingOf(r, id) == nil {
		t.Fatalf("the repair pass started %s while its restart waited", id)
	}
	if tk, _ := r.sw.Board.Snapshot().Task(other.ID); tk.Status != StatusTodo || tk.Owner != "" {
		t.Fatalf("%s = %s/%s, want left on the board", other.ID, tk.Status, tk.Owner)
	}
}

// A task an automatic restart keeps can be revised and given to the same worker again while the restart waits. That is a new
// assignment: the restart, called off or due, neither returns it to the board nor restarts the worker on it.
func TestARestartLeavesANewAssignmentOfTheTaskItKept(t *testing.T) {
	reassign := func(t *testing.T, r *rvRig, id string) Task {
		t.Helper()
		if _, ok := r.sw.Board.Requeue(id, "T1", 0, "test: revised", false, 0); !ok {
			t.Fatal("T1 was not revised")
		}
		if err := r.sw.Board.Assign("mgr", id, "T1"); err != nil {
			t.Fatal(err)
		}
		tk, _ := r.sw.Board.Snapshot().Task("T1")
		return tk
	}
	t.Run("called off", func(t *testing.T) {
		r, _, id := restartWaiting(t, Config{})
		again := reassign(t, r, id)
		r.sw.dropRestarts("test: the swarm stopped")
		if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusDoing || tk.Owner != id || tk.Rev != again.Rev {
			t.Fatalf("T1 = %s/%s rev %d, want the new assignment (doing, %s's, rev %d)", tk.Status, tk.Owner, tk.Rev, id, again.Rev)
		}
	})
	t.Run("due", func(t *testing.T) {
		r, clock, id := restartWaiting(t, Config{})
		reassign(t, r, id)
		clock.Advance(restartBackoff)
		r.sw.superviseOnce(clock.Now())
		if got := restartPhases(r, id); strings.Join(got, ",") != "scheduled,dropped" {
			t.Fatalf("swarm.restart phases = %v, want the restart dropped: its work has moved on", got)
		}
		if r.prov.sawEver("automatic restart 1 of 2") {
			t.Fatal("the worker was restarted on a new assignment")
		}
	})
}
