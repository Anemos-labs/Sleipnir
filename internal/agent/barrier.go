package agent

import (
	"context"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// batchWrites reports whether a batch of tool calls may change the work tree:
// whether any of its tools is declared neither read-only nor free of work-tree
// writes (bash, edit, write, apply_patch, a tool server's tools). A tool batch
// holds Config.Barrier only then: a batch of reads or of coordination (the
// swarm's task, mail, note and spawn, and a manager's wait, which can last
// hours) must never keep a savepoint from finding a moment when nothing writes.
func (a *Agent) batchWrites(calls []core.Block) bool {
	for _, c := range calls {
		t, ok := a.cfg.Tools.Get(a.callTool(c))
		if ok && !t.Spec().ReadOnly && !t.Spec().NoTreeWrites {
			return true
		}
	}
	return false
}

// SavepointBarrier is the Barrier a session shares among its agents (and its
// swarm's done check) so that a savepoint can stop writes for a moment. Tool
// batches that write hold it shared (RLock). Acquire takes it exclusively in
// two phases: first it waits, without holding anyone up, for a moment when no
// batch writes; past polite it also keeps new batches from starting, until the
// running ones end or firm runs out, in which case it gives up (the savepoint
// is skipped, nobody waited long).
type SavepointBarrier struct {
	mu      sync.Mutex
	cond    *sync.Cond
	readers int
	held    bool // a savepoint holds the barrier
	gating  bool // a savepoint is waiting and new batches wait for it
}

// NewSavepointBarrier returns an open barrier.
func NewSavepointBarrier() *SavepointBarrier {
	b := &SavepointBarrier{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// RLock waits while a savepoint holds the barrier or is gating, then counts one
// writing batch in.
func (b *SavepointBarrier) RLock() {
	b.mu.Lock()
	for b.held || b.gating {
		b.cond.Wait()
	}
	b.readers++
	b.mu.Unlock()
}

// RUnlock counts one writing batch out.
func (b *SavepointBarrier) RUnlock() {
	b.mu.Lock()
	b.readers--
	if b.readers == 0 {
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

// Acquire takes the barrier exclusively and reports whether it did: within
// polite without delaying anyone, then within firm while new writing batches
// wait. A false return leaves the barrier open. Release undoes a true one.
func (b *SavepointBarrier) Acquire(ctx context.Context, polite, firm time.Duration) bool {
	if b.waitFor(ctx, polite, false) {
		return true
	}
	return b.waitFor(ctx, firm, true)
}

// waitFor waits up to d for the readers to drain, gating new ones when gate is
// set; on success the barrier is held.
func (b *SavepointBarrier) waitFor(ctx context.Context, d time.Duration, gate bool) bool {
	deadline := time.Now().Add(d)
	// cond has no timeout: a timer (and the context) broadcast so the loop below
	// re-checks the deadline.
	t := time.AfterFunc(d, func() { b.mu.Lock(); b.cond.Broadcast(); b.mu.Unlock() })
	defer t.Stop()
	stop := context.AfterFunc(ctx, func() { b.mu.Lock(); b.cond.Broadcast(); b.mu.Unlock() })
	defer stop()
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.held || b.gating && !gate {
		// Another savepoint is in the barrier; wait for it like a reader would.
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		b.cond.Wait()
	}
	if gate {
		b.gating = true
	}
	for b.readers > 0 {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			if gate {
				b.gating = false
				b.cond.Broadcast()
			}
			return false
		}
		b.cond.Wait()
	}
	b.gating, b.held = false, true
	return true
}

// Release opens the barrier after a successful Acquire.
func (b *SavepointBarrier) Release() {
	b.mu.Lock()
	b.held = false
	b.cond.Broadcast()
	b.mu.Unlock()
}
