package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

func TestWatchdogIgnoresAReplacedRun(t *testing.T) {
	for _, quiet := range []time.Duration{11 * time.Minute, 21 * time.Minute} {
		t.Run(quiet.String(), func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			oldCtx, oldCancel := context.WithCancel(context.Background())
			defer oldCancel()
			newCtx, newCancel := context.WithCancel(context.Background())
			defer newCancel()
			old := &runState{id: 1, cancel: oldCancel}
			current := &runState{id: 2, cancel: newCancel}
			m := &member{id: "be-1", life: lifeRunning, run: current}
			m.progress.Store(now.Add(-quiet).UnixNano())
			s := &Swarm{cfg: Config{StuckAfter: 10 * time.Minute}, Board: NewBoard(events.Discard{})}

			// The supervisor captured old before this worker was reassigned. Even
			// if the successor is quiet, only a check of its own run may stop it.
			s.watch(m, old, now)
			if newCtx.Err() != nil || oldCtx.Err() != nil || current.reason != "" || !old.abortAt.IsZero() {
				t.Fatalf("stale check changed a run: old=%v new=%v reason=%q abort=%v", oldCtx.Err(), newCtx.Err(), current.reason, old.abortAt)
			}
			if m.stuckWarn || len(s.Board.Snapshot().Alerts) != 0 {
				t.Fatal("stale check warned about the successor")
			}

			// A check of the current run must still enforce the watchdog.
			s.watch(m, current, now)
			if quiet > 20*time.Minute {
				if newCtx.Err() != context.Canceled || current.abortAt != now || current.reason == "" {
					t.Fatal("current overdue run was not cancelled")
				}
			} else if !m.stuckWarn || len(s.Board.Snapshot().Alerts) != 1 {
				t.Fatal("current quiet run was not warned")
			}
		})
	}
}

func TestWatchdogLateWarningPreservesTheSuccessorWarning(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	hold := newHoldEvents(events.Discard{}, events.TypeBoardOp)
	t.Cleanup(hold.open)
	s := &Swarm{cfg: Config{StuckAfter: 10 * time.Minute}, Board: NewBoard(hold)}
	old, current := &runState{id: 1}, &runState{id: 2}
	m := &member{id: "be-1", life: lifeRunning, run: old}
	m.progress.Store(now.Add(-11 * time.Minute).UnixNano())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.watch(m, old, now)
	}()
	select {
	case <-hold.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("old warning did not reach the event barrier")
	}
	// The old warning is still being published while the worker's replacement
	// becomes overdue. Each run must own its alert, including late cleanup.
	m.mu.Lock()
	m.run, m.stuckWarn = current, false
	m.mu.Unlock()
	s.watch(m, current, now.Add(time.Minute))
	hold.open()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("old warning did not finish publishing")
	}
	alerts := s.Board.Snapshot().Alerts
	if len(alerts) != 1 || alerts[0].Text != "be-1 has made no progress for 12m0s" {
		t.Fatalf("late warning affected the successor alert: %+v", alerts)
	}
}

func TestWatchdogPreservesTheExistingStopReason(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := &runState{id: 1, reason: "stopped by manager", count: false, cancel: cancel}
	m := &member{life: lifeRunning, run: rs}
	m.progress.Store(now.Add(-21 * time.Minute).UnixNano())
	s := &Swarm{cfg: Config{StuckAfter: 10 * time.Minute}}
	s.watch(m, rs, now)
	if ctx.Err() != context.Canceled || rs.reason != "stopped by manager" || rs.count || rs.abortAt != now {
		t.Fatalf("watchdog changed the recorded stop: err=%v run=%+v", ctx.Err(), rs)
	}
}
