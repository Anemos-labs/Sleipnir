package swarm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// endBarrier pauses the first worker ending before the event is recorded.
type endBarrier struct {
	events.Emitter
	entered, release chan struct{}
	held             atomic.Bool
}

func (b *endBarrier) Emit(agent, typ string, data any, opts ...events.Opt) (uint64, error) {
	if agent == "be-1" && typ == events.TypeAgentEnd && b.held.CompareAndSwap(false, true) {
		close(b.entered)
		<-b.release
	}
	return b.Emitter.Emit(agent, typ, data, opts...)
}

func TestWorkerEndingPrecedesReassignmentAndMailRecovery(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, via := range []string{"assignment", "mail"} {
			t.Run(fmt.Sprintf("failed=%t/%s", failed, via), func(t *testing.T) {
				barrier := &endBarrier{entered: make(chan struct{}), release: make(chan struct{})}
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
				var recovering atomic.Bool
				r := newRVRigWith(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
					if c.Sees("resume after stop") || c.Sees("second task") {
						recovering.Store(true)
						<-ctx.Done()
						return rvReply{}
					}
					if failed {
						return rvReply{Err: errors.New("request failed")}
					}
					return rvReply{Text: "first task finished"}
				}, func(d *Deps) {
					barrier.Emitter = d.Events
					d.Events = barrier
				})
				t.Cleanup(release) // unblock the emitter before the rig's Shutdown
				r.sw.StartManager()
				id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "first task", By: "mgr"})
				if err != nil {
					t.Fatal(err)
				}
				rvWait(t, "worker ending to reach the event log", func() bool {
					select {
					case <-barrier.entered:
						return true
					default:
						return false
					}
				})
				next := SpawnReq{Agent: id, Title: "second task", By: "mgr"}
				if via == "assignment" {
					if _, err := r.sw.Spawn(next); err == nil || !strings.Contains(err.Error(), "still working") {
						t.Fatalf("reuse before the previous ending was published: %v", err)
					}
				} else {
					if err := r.sw.deliver(Message{From: "mgr", To: id, Text: "resume after stop"}); err != nil {
						t.Fatal(err)
					}
					m := r.sw.get(id)
					m.mu.Lock()
					runs := m.runSeq
					m.mu.Unlock()
					if runs != 1 {
						t.Fatalf("mail started run %d before the previous ending was published", runs)
					}
				}
				release()
				if via == "assignment" {
					rvWait(t, "worker to become available", func() bool { return r.idle(id) })
					if _, err := r.sw.Spawn(next); err != nil {
						t.Fatal(err)
					}
				}
				rvWait(t, "recovery request", recovering.Load)
				ui := state.New()
				ui.ApplyAll(r.log.All())
				for _, a := range ui.Snapshot().Agents {
					if a.ID == id {
						if a.Status != state.StatusThinking || !r.running(id) {
							t.Fatalf("recovered worker shown as %s (running=%t)", a.Status, r.running(id))
						}
						return
					}
				}
				t.Fatal("recovered worker missing from the UI state")
			})
		}
	}
}

func TestLateFinishPanicCannotAlterANewerWorkerRun(t *testing.T) {
	r := newRVRig(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"edit", map[string]any{"path": "a.go"}}}}
		}
		<-ctx.Done()
		return rvReply{}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "current work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker to edit and request another response", func() bool { return r.prov.callsFor(id) == 2 })
	if len(r.sw.Leases.HeldBy(id)) != 1 {
		t.Fatal("worker did not acquire its write lease")
	}
	// An older finishRun can panic after publishing its ending, for example
	// while reporting to the manager after the worker has already been reused.
	r.sw.forceIdle(r.sw.get(id), &runState{})
	info, _ := r.sw.Board.Snapshot().Agent(id)
	if !r.running(id) || info.State != "running" || len(r.sw.Leases.HeldBy(id)) != 1 {
		t.Fatalf("late finish panic changed the current run: state=%s running=%t leases=%v", info.State, r.running(id), r.sw.Leases.HeldBy(id))
	}
}
