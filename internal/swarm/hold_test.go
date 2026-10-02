package swarm

// Tests for the manager stop guard (hold.go): a batch run's manager cannot give a
// final answer while the board holds unfinished work, the veto text is short and
// deterministic, the agent loop's bound releases a manager that will not settle,
// and a cancelled or broke run is never held.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// noticeSink records the notices an agent's sink was given (the person's view).
type noticeSink struct {
	agent.NopSink
	mu   sync.Mutex
	logs []string
}

func (n *noticeSink) Notice(a, level, msg string) {
	n.mu.Lock()
	n.logs = append(n.logs, a+" "+level+": "+msg)
	n.mu.Unlock()
}

func (n *noticeSink) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.logs...)
}

func (n *noticeSink) has(sub string) bool {
	for _, l := range n.all() {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// recordingHooks is a user's own hooks: it records Stop calls and vetoes the first
// `vetoes` of them.
type recordingHooks struct {
	mu      sync.Mutex
	stops   []string // the order Stop hooks were consulted, for the manager
	vetoes  int
	reason  string
	toolCts int
}

func (h *recordingHooks) BeforeTool(context.Context, agent.ToolHookCall) agent.ToolHookOutcome {
	h.mu.Lock()
	h.toolCts++
	h.mu.Unlock()
	return agent.ToolHookOutcome{}
}

func (h *recordingHooks) AfterTool(context.Context, agent.ToolHookCall, *tools.Result) agent.ToolHookOutcome {
	return agent.ToolHookOutcome{}
}

func (h *recordingHooks) BeforeStop(_ context.Context, id, role, _ string, _ bool) agent.StopOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stops = append(h.stops, id)
	if h.vetoes > 0 {
		h.vetoes--
		return agent.StopOutcome{Veto: true, Reason: h.reason}
	}
	return agent.StopOutcome{}
}

func (h *recordingHooks) stopCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.stops)
}

// managerCalls counts the requests the manager has made.
func managerCalls(r *rvRig) int { return r.prov.callsFor("mgr") }

// poll reports whether cond became true within d. Unlike rvWait it never calls
// t.Fatal, so goroutines other than the test's own may use it.
func poll(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// releaseWhen closes gate once cond holds (or after 10 s, so nothing hangs).
func releaseWhen(t *testing.T, gate chan struct{}, what string, cond func() bool) {
	go func() {
		if !poll(10*time.Second, cond) {
			t.Errorf("timed out waiting for %s", what)
		}
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
}

const fullHoldReason = "Not finished: running: be-1 (T1). Use wait, accept or reject submissions, and fail tasks you abandon, then give your final answer."

// A manager that answers while its worker is still running is sent back, told what is
// unfinished, and completes after it waits, accepts and answers again.
func TestHeldManagerIsSentBackUntilTheBoardIsSettled(t *testing.T) {
	gate := make(chan struct{})
	r := newRVRig(t, Config{HoldManager: true, MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			switch c.Assistants {
			case 0:
				return rvReply{Tools: []rvToolCall{{"spawn", map[string]any{"role": "backend", "task": "Add notes"}}}}
			case 1:
				return rvReply{Text: "the worker is on it, done"} // answers early
			case 2:
				return rvReply{Tools: []rvToolCall{{"wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20}}}}
			case 3:
				return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "accept", "id": "T1"}}}}
			}
			return rvReply{Text: "all done"}
		}
		if c.Assistants == 0 {
			rvBlock(ctx, gate)
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "added"}}}}
		}
		return rvReply{Text: "summary"}
	})
	releaseWhen(t, gate, "the veto to reach the manager", func() bool { return r.prov.sawEver("[stop hook] " + fullHoldReason) })
	res, err := r.sw.RunManager(context.Background(), "add notes")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "all done" {
		t.Fatalf("the run's result = %q (a settled board adds no report)", res.Text)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusDone {
		t.Fatalf("T1 = %s, want done", tk.Status)
	}
	if n := len(r.log.OfType(events.TypeSwarmHold)); n != 1 {
		t.Fatalf("%d swarm.hold events, want 1", n)
	}
	if n := len(r.log.OfType(events.TypeSwarmUnfinished)); n != 0 {
		t.Fatalf("%d swarm.unfinished events for a run that settled its board", n)
	}
	if got := r.sw.Unfinished(); got != "" {
		t.Fatalf("Unfinished() = %q for a run that settled its board", got)
	}
	// One request to spawn, the early answer, then wait, accept, the real answer.
	if n := managerCalls(r); n != 5 {
		t.Fatalf("the manager made %d requests, want 5", n)
	}
}

// The veto text lists ids in a fixed order, fits in a few hundred characters however
// much work is left, and is the same for the same board.
func TestHoldReasonIsDeterministicAndBounded(t *testing.T) {
	u := unfinishedWork{
		running: []string{"be-1 (T3)", "te-1 (T4)"}, review: []string{"T2"}, todo: []string{"T5"},
	}
	want := "Not finished: running: be-1 (T3), te-1 (T4); in review: T2; not started: T5. " + holdReasonTail
	if got := u.reason(); got != want {
		t.Fatalf("reason:\n got %q\nwant %q", got, want)
	}
	if u.reason() != u.reason() {
		t.Fatal("the reason changes between calls")
	}

	var big unfinishedWork
	for i := 1; i <= 60; i++ {
		id := "T" + string(rune('0'+i%10)) + string(rune('0'+i/10))
		big.review = append(big.review, id)
		big.todo = append(big.todo, id)
		big.blocked = append(big.blocked, id)
		big.doing = append(big.doing, id)
		big.running = append(big.running, "worker-"+id+" ("+id+","+id+")")
	}
	got := big.reason()
	if n := len([]rune(got)); n > holdReasonMax {
		t.Fatalf("reason is %d characters, cap %d:\n%s", n, holdReasonMax, got)
	}
	if !strings.HasPrefix(got, "Not finished: running: worker-") || !strings.Contains(got, " more") {
		t.Fatalf("a long list must say how many more there are:\n%s", got)
	}
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("the reason must be one line: %q", got)
	}
	if !strings.HasSuffix(got, holdReasonTail) {
		t.Fatalf("the lists shrink to fit, the instruction is kept: %q", got)
	}
}

// The reason is a pure function of the board and the roster: two identical swarms in
// the same state produce byte-identical text (it lands in the manager's thread).
func TestHoldReasonListsWorkersAndTasksByCategory(t *testing.T) {
	gate := make(chan struct{})
	r := newRVRig(t, Config{HoldManager: true, MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	r.sw.StartManager()
	for _, title := range []string{"one", "two"} {
		if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: title, By: "mgr"}); err != nil {
			t.Fatal(err)
		}
	}
	// A task in review, one blocked, one waiting, one nobody started.
	rvWait(t, "both workers running", func() bool { return r.running("be-1") && r.running("be-2") })
	rev, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "reviewed"})
	if err := r.sw.Board.Assign("mgr", "be-9", rev.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Submit("be-9", rev.ID, "ok", ""); err != nil {
		t.Fatal(err)
	}
	blk, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "blocked one"})
	_ = r.sw.Board.Assign("mgr", "be-8", blk.ID)
	_ = r.sw.Board.Block("be-8", blk.ID, "waiting for the schema")
	_, _ = r.sw.Board.CreateTask("mgr", TaskSpec{Title: "not started"})

	u := r.sw.unfinishedWork()
	want := "Not finished: running: be-1 (T1), be-2 (T2); in review: T3; blocked: T4; not started: T5. " + holdReasonTail
	if got := u.reason(); got != want {
		t.Fatalf("reason:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(u.reason(), "waiting for the schema") || strings.Contains(u.reason(), "reviewed") {
		t.Fatal("the reason must contain ids only, never text an agent wrote")
	}
}

// A session that asked for no hold (an interactive one) lets the manager answer while
// workers run: they outlive the turn.
func TestManagerIsNotHeldWithoutHoldManager(t *testing.T) {
	gate := make(chan struct{})
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			if c.Assistants == 0 {
				return rvReply{Tools: []rvToolCall{{"spawn", map[string]any{"role": "backend", "task": "Add notes"}}}}
			}
			return rvReply{Text: "the worker is on it"}
		}
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	res, err := r.sw.RunManager(context.Background(), "add notes")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the worker is on it" || strings.Contains(res.Text, "[harness]") {
		t.Fatalf("result %q", res.Text)
	}
	if n := managerCalls(r); n != 2 {
		t.Fatalf("the manager made %d requests, want 2 (spawn, answer): it must not be held", n)
	}
	if !r.running("be-1") {
		t.Fatal("the worker must keep running after the manager's turn")
	}
	if len(r.log.OfType(events.TypeSwarmHold)) != 0 {
		t.Fatal("a hold event without a hold")
	}
}

// A manager that never settles its board is released by the agent loop's bound; the
// run then reports what was left, in its result and as a notice.
func TestHoldIsBoundedAndTheRunReportsWhatWasLeft(t *testing.T) {
	gate := make(chan struct{})
	sink := &noticeSink{}
	r := newRVRigWith(t, Config{HoldManager: true, MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			if c.Assistants == 0 {
				return rvReply{Tools: []rvToolCall{{"spawn", map[string]any{"role": "backend", "task": "Add notes"}}}}
			}
			return rvReply{Text: "I am done"} // never waits
		}
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	}, func(d *Deps) { d.NewSink = func(string) agent.Sink { return sink } })
	t.Cleanup(func() { close(gate) })

	res, err := r.sw.RunManager(context.Background(), "add notes")
	if err != nil {
		t.Fatal(err)
	}
	// spawn + the first answer + one more answer per veto (three) = 5 requests.
	if n := managerCalls(r); n != 5 {
		t.Fatalf("the manager made %d requests, want 5 (the bound is three vetoes)", n)
	}
	if !strings.HasPrefix(res.Text, "I am done") || !strings.Contains(res.Text, "[harness] Unfinished when the manager stopped: running: be-1 (T1).") {
		t.Fatalf("the result must report the unfinished work: %q", res.Text)
	}
	if !sink.has("the manager stopped with unfinished work: running: be-1 (T1)") {
		t.Fatalf("the person was not told: %v", sink.all())
	}
	if n := len(r.log.OfType(events.TypeSwarmHold)); n != 3 {
		t.Fatalf("%d swarm.hold events, want 3", n)
	}
	if n := len(r.log.OfType(events.TypeSwarmUnfinished)); n != 1 {
		t.Fatalf("%d swarm.unfinished events, want 1", n)
	}
	// the caller can tell from the result's data, without parsing its text, that the run did not finish
	if got := r.sw.Unfinished(); got != "running: be-1 (T1)" {
		t.Fatalf("Unfinished() = %q, want the running worker", got)
	}
}

// The user's own Stop hooks run first and either may veto: when both would, the
// user's reason is delivered and ours is not consulted until the user's hook lets go.
func TestUserStopHooksRunBeforeTheGuard(t *testing.T) {
	gate := make(chan struct{})
	inner := &recordingHooks{vetoes: 1, reason: "run the linter first"}
	r := newRVRigWith(t, Config{HoldManager: true, MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			switch {
			case c.Assistants == 0:
				return rvReply{Tools: []rvToolCall{{"spawn", map[string]any{"role": "backend", "task": "Add notes"}}}}
			case c.Assistants <= 2:
				return rvReply{Text: "done"} // vetoed by the user's hook, then by the guard
			case c.Assistants == 3:
				return rvReply{Tools: []rvToolCall{{"wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20}}}}
			case c.Assistants == 4:
				return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "accept", "id": "T1"}}}}
			}
			return rvReply{Text: "finished"}
		}
		if c.Assistants == 0 {
			rvBlock(ctx, gate)
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "added"}}}}
		}
		return rvReply{Text: "summary"}
	}, func(d *Deps) { d.Hooks = inner })
	releaseWhen(t, gate, "the guard's veto", func() bool { return r.prov.sawEver("[stop hook] " + fullHoldReason) })
	res, err := r.sw.RunManager(context.Background(), "add notes")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "finished" {
		t.Fatalf("result %q", res.Text)
	}
	if !r.prov.sawEver("[stop hook] run the linter first") {
		t.Fatal("the user's Stop hook was not consulted")
	}
	// The guard was not consulted while the user's hook vetoed: its first swarm.hold
	// event comes after the user's veto reached the manager, so exactly one event per
	// guard veto, and the user's hook saw every stop attempt.
	if n := inner.stopCalls(); n < 3 {
		t.Fatalf("the user's hook was consulted %d times, want at least 3 (every stop attempt)", n)
	}
	if n := len(r.log.OfType(events.TypeSwarmHold)); n != 1 {
		t.Fatalf("%d swarm.hold events, want 1 (the user's veto is not the guard's)", n)
	}
	if inner.toolCts == 0 {
		t.Fatal("the wrapper must pass tool hooks through to the user's hooks")
	}
}

// A worker that is running but holds nothing on the board is only finishing its last
// turn: the guard waits for it, it does not send the manager back.
func TestWindingDownIsOnlyRunningWorkersThatHoldNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    unfinishedWork
		want bool
	}{
		{"a worker finishing its last turn", unfinishedWork{running: []string{"be-1"}, idle: []string{"be-1"}}, true},
		{"two of them", unfinishedWork{running: []string{"be-1", "te-1"}, idle: []string{"be-1", "te-1"}}, true},
		{"a worker on a task", unfinishedWork{running: []string{"be-1 (T1)"}}, false},
		{"one finishing, one working", unfinishedWork{running: []string{"be-1", "te-1 (T2)"}, idle: []string{"be-1"}}, false},
		{"finishing, with a submission to judge", unfinishedWork{running: []string{"be-1"}, idle: []string{"be-1"}, review: []string{"T1"}}, false},
		{"finishing, with a blocked task", unfinishedWork{running: []string{"be-1"}, idle: []string{"be-1"}, blocked: []string{"T1"}}, false},
		{"finishing, with work nobody started", unfinishedWork{running: []string{"be-1"}, idle: []string{"be-1"}, todo: []string{"T1"}}, false},
		{"nobody running", unfinishedWork{review: []string{"T1"}}, false},
		{"nothing at all", unfinishedWork{}, false},
	} {
		if got := tc.u.windingDown(); got != tc.want {
			t.Errorf("%s: windingDown = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// finishingRig is a swarm whose one worker has handed its task in and is now writing its
// closing message, which the test holds back. The manager waits for the task, accepts it
// and answers; its answer reaches the guard while the worker is still running.
func finishingRig(t *testing.T, gate chan struct{}) (*rvRig, *recordingHooks) {
	t.Helper()
	inner := &recordingHooks{}
	r := newRVRigWith(t, Config{HoldManager: true, MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			switch c.Assistants {
			case 0:
				return rvReply{Tools: []rvToolCall{{"spawn", map[string]any{"role": "backend", "task": "Add notes"}}}}
			case 1:
				return rvReply{Tools: []rvToolCall{{"wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20}}}}
			case 2:
				return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "accept", "id": "T1"}}}}
			}
			return rvReply{Text: "finished"}
		}
		if c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "added"}}}}
		}
		rvBlock(ctx, gate) // the closing message
		return rvReply{Text: "summary"}
	}, func(d *Deps) { d.Hooks = inner })
	return r, inner
}

func TestGuardWaitsForAWorkerThatIsOnlyFinishing(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	r, inner := finishingRig(t, gate)

	type outcome struct {
		res *agent.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.sw.RunManager(context.Background(), "add notes")
		done <- outcome{res, err}
	}()

	// The manager's final answer has reached the stop hooks; the worker is still in its last turn.
	rvWait(t, "the manager's final answer", func() bool { return inner.stopCalls() >= 1 })
	if !r.running("be-1") {
		t.Fatal("setup: the worker should still be writing its closing message")
	}
	select {
	case o := <-done:
		t.Fatalf("the run ended while the worker was still finishing: %+v", o)
	case <-time.After(150 * time.Millisecond):
	}
	if n := len(r.log.OfType(events.TypeSwarmHold)); n != 0 {
		t.Fatalf("%d hold events: the manager was sent back for a worker that only had its closing message to write", n)
	}
	if n := managerCalls(r); n != 4 {
		t.Fatalf("the manager made %d requests while the guard waited, want 4 (spawn, wait, accept, answer)", n)
	}

	release()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.res.Text != "finished" {
			t.Fatalf("result %q: a settled board adds no report", o.res.Text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not end after the worker finished")
	}
	if n := len(r.log.OfType(events.TypeSwarmHold)); n != 0 {
		t.Fatalf("%d hold events for a run whose worker finished on its own", n)
	}
	if n := managerCalls(r); n != 4 {
		t.Fatalf("the manager made %d requests, want 4: the wait must cost it none", n)
	}
}

// The wait is short, and the guard has only so much of it: a worker that will not end is
// waited for once, and after that it is an ordinary running worker that vetoes the answer.
func TestGuardsWaitForAFinishingWorkerIsBounded(t *testing.T) {
	prev := holdSettleMax
	holdSettleMax = 100 * time.Millisecond
	t.Cleanup(func() { holdSettleMax = prev })

	gate := make(chan struct{}) // never opened: the worker does not end
	r, _ := finishingRig(t, gate)
	t.Cleanup(func() { close(gate) })

	start := time.Now()
	res, err := r.sw.RunManager(context.Background(), "add notes")
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Fatalf("the run took %s: the wait for a finishing worker is not bounded", took)
	}
	if n := len(r.log.OfType(events.TypeSwarmHold)); n != 3 {
		t.Fatalf("%d swarm.hold events, want 3 (the veto bound: the wait is spent once, then each answer is vetoed)", n)
	}
	if !strings.Contains(res.Text, "[harness] Unfinished when the manager stopped: running: be-1.") {
		t.Fatalf("the run must report the worker it left running: %q", res.Text)
	}
}

// A run that was cancelled, and a run whose budget is spent, are never held: they are
// ending, and a veto would only turn a clean stop into an error.
func TestHoldNeverVetoesACancelledOrBrokeRun(t *testing.T) {
	r := newRVRig(t, Config{HoldManager: true, MaxWriters: 4, BudgetUSD: 5}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "unfinished", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	// The board holds unfinished work (a todo task nobody runs).
	_, _ = r.sw.Board.CreateTask("mgr", TaskSpec{Title: "waiting"})
	h := &holdHooks{s: r.sw, m: m}
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false); !o.Veto {
		t.Fatal("setup: an ordinary stop with work left must be vetoed")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if o := h.BeforeStop(cancelled, "mgr", "manager", "done", false); o.Veto {
		t.Fatal("a cancelled run was held")
	}
	// Budget spent: the swarm's ledger is over the line.
	r.sw.mu.Lock()
	r.sw.spent = 6
	r.sw.mu.Unlock()
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false); o.Veto {
		t.Fatal("a run whose budget is spent was held")
	}
	r.sw.mu.Lock()
	r.sw.spent = 0
	r.sw.mu.Unlock()
	// A closed swarm does not hold either.
	r.sw.Shutdown()
	if o := h.BeforeStop(context.Background(), "mgr", "manager", "done", false); o.Veto {
		t.Fatal("a shut down swarm held its manager")
	}
}

// The agent loop does not consult Stop hooks for a run that was cancelled: the guard
// is never asked, so a Ctrl-C cannot be turned into "not finished".
func TestCancelledManagerRunNeverReachesTheStopHooks(t *testing.T) {
	inner := &recordingHooks{}
	started := make(chan struct{}, 1)
	r := newRVRigWith(t, Config{HoldManager: true}, func(ctx context.Context, c *rvCall) rvReply {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return rvReply{Text: "late"}
	}, func(d *Deps) { d.Hooks = inner })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.sw.RunManager(ctx, "go"); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled run must report the cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not stop")
	}
	if n := inner.stopCalls(); n != 0 {
		t.Fatalf("Stop hooks consulted %d times for a cancelled run", n)
	}
}

// An exhausted budget ends the run before any Stop hook is consulted (the loop's own
// check comes first), and the guard does not veto the request that overspent.
func TestBudgetExhaustionIsNotHeld(t *testing.T) {
	r := newRVRig(t, Config{HoldManager: true, MaxWriters: 4, BudgetUSD: 0.01}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			if c.Assistants == 0 {
				return rvReply{Tools: []rvToolCall{{"spawn", map[string]any{"role": "backend", "task": "Add notes"}}}}
			}
			return rvReply{Text: "done", Usage: core.Usage{InputTokens: 200_000}} // overspends
		}
		<-ctx.Done()
		return rvReply{Text: "stopped"}
	})
	done := make(chan error, 1)
	go func() { _, err := r.sw.RunManager(context.Background(), "go"); done <- err }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a manager over budget was held")
	}
	if n := len(r.log.OfType(events.TypeSwarmHold)); n != 0 {
		t.Fatalf("%d hold events for a run that was over budget", n)
	}
}

// A person who presses Esc has not made the manager fail: it is idle, ready for the next goal. It was recorded as "failed", and the agents
// page showed it as "stuck".
func TestACancelledManagerRunLeavesTheManagerIdleNotFailed(t *testing.T) {
	started := make(chan struct{}, 1)
	r := newRVRigWith(t, Config{HoldManager: true}, func(ctx context.Context, c *rvCall) rvReply {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return rvReply{Text: "late"}
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.sw.RunManager(ctx, "go"); done <- err }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not stop")
	}
	last := ""
	for _, e := range r.log.OfType(events.TypeAgentState) {
		var d struct{ ID, State string }
		if json.Unmarshal(e.Data, &d) == nil && d.ID == "mgr" {
			last = d.State
		}
	}
	if last != "idle" {
		t.Errorf("the manager's state after a cancelled run is %q, want idle", last)
	}
}
