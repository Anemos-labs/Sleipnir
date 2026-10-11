package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The barrier waits politely, then gates new writers for at most firm, and
// gives up without leaving anyone blocked.
func TestSavepointBarrier(t *testing.T) {
	ctx := context.Background()
	b := agent.NewSavepointBarrier()
	if !b.Acquire(ctx, 10*time.Millisecond, 10*time.Millisecond) {
		t.Fatal("an idle barrier could not be acquired")
	}
	b.Release()

	// A writer that outlasts both phases: the savepoint gives up, and new
	// writers are not held after it did.
	b.RLock()
	start := time.Now()
	if b.Acquire(ctx, 20*time.Millisecond, 30*time.Millisecond) {
		t.Fatal("acquired while a writer held the barrier")
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Errorf("gave up after %v, want about polite+firm", d)
	}
	done := make(chan struct{})
	go func() { b.RLock(); b.RUnlock(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a new writer stayed blocked after the savepoint gave up")
	}
	b.RUnlock()

	// A writer that ends during the firm phase: the savepoint gets the barrier,
	// and a writer arriving meanwhile waits for the release.
	b.RLock()
	got := make(chan bool, 1)
	go func() { got <- b.Acquire(ctx, 5*time.Millisecond, 2*time.Second) }()
	time.Sleep(50 * time.Millisecond) // the savepoint is gating now
	var late atomic.Bool
	lateDone := make(chan struct{})
	go func() { b.RLock(); late.Store(true); b.RUnlock(); close(lateDone) }()
	time.Sleep(20 * time.Millisecond)
	if late.Load() {
		t.Fatal("a new writer started while the savepoint was gating")
	}
	b.RUnlock()
	if !<-got {
		t.Fatal("the savepoint did not get the barrier when the writer ended")
	}
	if late.Load() {
		t.Fatal("a writer ran while the savepoint held the barrier")
	}
	b.Release()
	<-lateDone

	// A cancelled context ends the wait.
	b.RLock()
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if b.Acquire(cctx, time.Minute, time.Minute) {
		t.Fatal("acquired with a cancelled context")
	}
	b.RUnlock()
}

// An agent with a barrier keeps a recoverable snapshot at tool-batch
// boundaries, holds the barrier only while a writing batch runs, and writes the
// snapshot on request.
func TestAgentRecoverableSnapshotAtToolBoundaries(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	bash := fakeTool{name: "bash", runCtx: func(ctx context.Context, c *tools.Call) *tools.Result {
		started <- struct{}{}
		<-release
		return &tools.Result{Text: "built"}
	}}
	look := fakeTool{name: "look", readOnly: true, run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "seen"} }}
	b := agent.NewSavepointBarrier()
	step := 0
	r := newLimRig(t, func(c *agent.Config) { c.Barrier = b }, []fakeTool{bash, look}, func(c *mock.Call) mock.Reply {
		step++
		switch step {
		case 1:
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "r1", Name: "look", Args: `{}`}}}
		case 2:
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "b1", Name: "bash", Args: `{"command":"make"}`}}}
		}
		return mock.Reply{Text: "done"}
	})
	ctx := context.Background()
	res := make(chan error, 1)
	go func() { _, err := r.agent.Run(ctx, "build it"); res <- err }()
	<-started // the writing batch is running
	if b.Acquire(ctx, 20*time.Millisecond, 20*time.Millisecond) {
		t.Fatal("a savepoint got the barrier while bash ran")
	}
	snap, ok := r.agent.Recoverable()
	if !ok || !strings.Contains(turnsText(snap.Turns), "seen") || strings.Contains(turnsText(snap.Turns), "built") {
		t.Fatalf("recoverable snapshot mid-batch should end after the read batch: ok=%v %q", ok, turnsText(snap.Turns))
	}
	close(release)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	if !b.Acquire(ctx, time.Second, time.Second) {
		t.Fatal("the barrier stayed held after the run")
	}
	b.Release()
	snap, _ = r.agent.Recoverable()
	if !strings.Contains(turnsText(snap.Turns), "built") {
		t.Fatalf("the recoverable snapshot lacks the bash result: %q", turnsText(snap.Turns))
	}
	h, err := r.agent.WriteRecoverable()
	if err != nil || h == "" {
		t.Fatalf("WriteRecoverable: %q %v", h, err)
	}
	found := false
	for _, e := range r.log.OfType(events.TypeAgentSnapshot) {
		if strings.Contains(string(e.Data), `"savepoint":true`) && strings.Contains(string(e.Data), string(h)) {
			found = true
		}
	}
	if !found {
		t.Fatal("no savepoint agent.snapshot event")
	}
}

// turnsText concatenates the text of turns, tool results included.
func turnsText(ts []core.Turn) string {
	var sb strings.Builder
	var walk func([]core.Block)
	walk = func(bs []core.Block) {
		for _, b := range bs {
			sb.WriteString(b.Text)
			sb.WriteString("\n")
			walk(b.Result)
		}
	}
	for _, t := range ts {
		walk(t.Blocks)
	}
	return sb.String()
}

// A batch of coordination (a manager creating a task and waiting for it) does
// not hold the barrier: its tools change no file of the work tree.
func TestCoordinationDoesNotHoldTheBarrier(t *testing.T) {
	release := make(chan struct{})
	waiting := make(chan struct{}, 1)
	task := fakeTool{name: "task", noTreeWrites: true, run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "T1 created"} }}
	wait := fakeTool{name: "wait", readOnly: true, runCtx: func(ctx context.Context, c *tools.Call) *tools.Result {
		waiting <- struct{}{}
		<-release
		return &tools.Result{Text: "T1 is done"}
	}}
	b := agent.NewSavepointBarrier()
	step := 0
	r := newLimRig(t, func(c *agent.Config) { c.Barrier = b }, []fakeTool{task, wait}, func(c *mock.Call) mock.Reply {
		step++
		if step == 1 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "t1", Name: "task", Args: `{}`}, {ID: "w1", Name: "wait", Args: `{}`}}}
		}
		return mock.Reply{Text: "done"}
	})
	ctx := context.Background()
	res := make(chan error, 1)
	go func() { _, err := r.agent.Run(ctx, "coordinate"); res <- err }()
	<-waiting
	if !b.Acquire(ctx, time.Second, time.Second) {
		t.Fatal("a savepoint waited for a batch of coordination")
	}
	b.Release()
	close(release)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}

// A tool the agent knows nothing special about (a tool server's) holds the
// barrier while it runs unless its spec says it is read-only: the barrier
// follows the tools' declarations, not a list of names.
func TestAnyToolThatMayWriteHoldsTheBarrier(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	deploy := fakeTool{name: "deploy", runCtx: func(ctx context.Context, c *tools.Call) *tools.Result {
		started <- struct{}{}
		<-release
		return &tools.Result{Text: "deployed"}
	}}
	b := agent.NewSavepointBarrier()
	step := 0
	r := newLimRig(t, func(c *agent.Config) { c.Barrier = b }, []fakeTool{deploy}, func(c *mock.Call) mock.Reply {
		step++
		if step == 1 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "d1", Name: "deploy", Args: `{}`}}}
		}
		return mock.Reply{Text: "done"}
	})
	ctx := context.Background()
	res := make(chan error, 1)
	go func() { _, err := r.agent.Run(ctx, "ship it"); res <- err }()
	<-started
	if b.Acquire(ctx, 20*time.Millisecond, 20*time.Millisecond) {
		t.Fatal("a savepoint got the barrier while a tool that may write ran")
	}
	close(release)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}

// A batch that cannot write (a team's wait) does not hold the barrier, and a
// savepoint taken while it runs records it interrupted where it has got to: the
// calls that returned with their results, the others as interrupted. Once the
// batch ends, the recoverable state has the real results again.
func TestAWaitIsRecordedInterruptedAtASavepoint(t *testing.T) {
	release := make(chan struct{})
	waiting := make(chan struct{}, 1)
	looked := make(chan struct{})
	look := fakeTool{name: "look", readOnly: true, run: func(json.RawMessage) *tools.Result {
		defer close(looked)
		return &tools.Result{Text: "seen"}
	}}
	wait := fakeTool{name: "wait", readOnly: true, runCtx: func(ctx context.Context, c *tools.Call) *tools.Result {
		waiting <- struct{}{}
		<-release
		return &tools.Result{Text: "T1 is done"}
	}}
	b := agent.NewSavepointBarrier()
	step := 0
	r := newLimRig(t, func(c *agent.Config) { c.Barrier = b }, []fakeTool{look, wait}, func(c *mock.Call) mock.Reply {
		step++
		if step == 1 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "l1", Name: "look", Args: `{}`}, {ID: "w1", Name: "wait", Args: `{}`}}}
		}
		return mock.Reply{Text: "done"}
	})
	ctx := context.Background()
	res := make(chan error, 1)
	go func() { _, err := r.agent.Run(ctx, "coordinate"); res <- err }()
	<-waiting
	<-looked // both are read-only and run side by side: wait until the look's result is recorded
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if snap, ok := r.agent.Recoverable(); ok && strings.Contains(turnsText(snap.Turns), "seen") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the look's result was never recorded")
		}
	}
	if !b.Acquire(ctx, time.Second, time.Second) {
		t.Fatal("a savepoint waited for a batch that cannot write")
	}
	snap, ok := r.agent.Recoverable()
	b.Release()
	if !ok || len(snap.Turns) == 0 {
		t.Fatalf("no recoverable state mid-batch: %v", ok)
	}
	last := snap.Turns[len(snap.Turns)-1]
	text := turnsText([]core.Turn{last})
	if last.Role != core.RoleUser || !strings.Contains(text, "seen") || !strings.Contains(text, agent.InterruptedCall) || last.ID != snap.NextTurn-1 {
		t.Fatalf("the interrupted batch's turn: %+v (next %d)\n%s", last, snap.NextTurn, text)
	}
	close(release)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	snap, _ = r.agent.Recoverable()
	if all := turnsText(snap.Turns); !strings.Contains(all, "T1 is done") || strings.Contains(all, agent.InterruptedCall) {
		t.Fatalf("after the batch the recoverable state lacks its results:\n%s", all)
	}
}
