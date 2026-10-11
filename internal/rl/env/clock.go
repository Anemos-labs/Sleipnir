package env

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// What a rollout does when its policy endpoint is down. A long rollout (hours, a team's) meets the gateway's outages: on one
// day, two of about twenty and eighty minutes, answered with 502s and 503s or not at all. Ending every rollout that ran then as an
// infra failure throws hours of work away, and charging the outage to the rollout's wall-clock budget ends it as a budget episode
// that the policy did nothing to earn. So the agents wait an outage out (RunSpec.OutagePatience), and the wall clock stops while
// they do (WallClock): an outage pauses a rollout instead of failing it.

const (
	// DefaultOutagePatience is how long one continuous outage of the policy endpoint may last before an agent gives up on it,
	// when Runner.OutagePatience is zero. It equals DefaultMaxOutagePause: a gateway outage of an hour or more (seen in
	// practice) is waited out once instead of failing the attempt and retrying it into the same outage.
	DefaultOutagePatience = 3 * time.Hour
	// DefaultMaxOutagePause is how much outage, in all, one attempt at a rollout sits out before it ends as an infrastructure
	// failure, when Runner.MaxOutagePause is zero.
	DefaultMaxOutagePause = 3 * time.Hour
)

var (
	// ErrWallBudget is the cause of a rollout's context when the time it was active (not paused for an outage) reached its
	// wall-clock budget. It is a deadline: errors.Is(ErrWallBudget, context.DeadlineExceeded) holds.
	ErrWallBudget = fmt.Errorf("the rollout's wall-clock budget is spent: %w", context.DeadlineExceeded)
	// ErrOutageLimit is the cause of a rollout's context when the policy endpoint was down for longer, in all, than the rollout
	// may sit out (Runner.MaxOutagePause): the rollout ends as an infrastructure failure.
	ErrOutageLimit = errors.New("the policy endpoint was down for longer than a rollout may wait")
)

// WallClock is the wall-clock budget of one attempt at a rollout, which does not run while the policy endpoint is down. The
// harness says when the endpoint goes down (Down, at the first failure that is an outage) and when it answers again (Up, at the
// next response); the runner's context for the harness (Watch) ends when the active time reaches the wall budget, or when the
// outages together reach the pause limit. It is safe for concurrent use: every agent of a team reports to the same clock, and an
// outage lasts from the first of them that met it to the first that got an answer.
//
// The methods of a nil *WallClock do nothing and report zero, so a harness can call them whether or not the runner keeps one.
type WallClock struct {
	wall, maxPause time.Duration // zero: no limit
	notify         func(down bool, cause string)

	mu        sync.Mutex
	start     time.Time
	stopped   time.Time     // when Stop froze the clock; zero while it runs
	downSince time.Time     // when the current outage began; zero while the endpoint is up
	paused    time.Duration // the outages that ended
	outages   int
	wake      chan struct{} // tells the watcher that the clock changed state
}

// NewWallClock starts a clock with an active-time budget of wall and an outage limit of maxPause (zero: no limit for either).
// notify, if set, is called after each change of state (down, with what caused it; up), outside the clock's lock.
func NewWallClock(wall, maxPause time.Duration, notify func(down bool, cause string)) *WallClock {
	return &WallClock{wall: max(wall, 0), maxPause: max(maxPause, 0), notify: notify, start: time.Now(), wake: make(chan struct{}, 1)}
}

// Down marks the policy endpoint as down from now, with cause describing the failure, and reports whether that began an outage
// (false: one was already under way, or the clock is stopped).
func (c *WallClock) Down(cause string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	if !c.stopped.IsZero() || !c.downSince.IsZero() {
		c.mu.Unlock()
		return false
	}
	c.downSince = time.Now()
	c.outages++
	c.mu.Unlock()
	c.changed(true, cause)
	return true
}

// Up marks the policy endpoint as answering again and reports how long the outage it ends lasted (false: there was none, or the
// clock is stopped).
func (c *WallClock) Up() (lasted time.Duration, ended bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	if !c.stopped.IsZero() || c.downSince.IsZero() {
		c.mu.Unlock()
		return 0, false
	}
	lasted = time.Since(c.downSince)
	c.paused += lasted
	c.downSince = time.Time{}
	c.mu.Unlock()
	c.changed(false, "")
	return lasted, true
}

// changed wakes the watcher and tells the runner.
func (c *WallClock) changed(down bool, cause string) {
	select {
	case c.wake <- struct{}{}:
	default: // a wake is already pending: the watcher reads the state afresh anyway
	}
	if c.notify != nil {
		c.notify(down, cause)
	}
}

// Stop freezes the clock: what Active and Paused report from now on is what they were at this moment, an outage under way
// included in Paused, and Down and Up do nothing.
func (c *WallClock) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.stopped.IsZero() {
		c.stopped = time.Now()
	}
	c.mu.Unlock()
}

// Active is how long the clock has run while the endpoint was up: what counts against the wall budget.
func (c *WallClock) Active() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.nowLocked()
	return now.Sub(c.start) - c.pausedLocked(now)
}

// Paused is how long the endpoint has been down while the clock ran, the outage under way included.
func (c *WallClock) Paused() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pausedLocked(c.nowLocked())
}

// IsDown reports whether an outage is under way.
func (c *WallClock) IsDown() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.downSince.IsZero()
}

// Outages is how many outages began.
func (c *WallClock) Outages() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outages
}

// nowLocked is the clock's present: the moment it was stopped, or now.
func (c *WallClock) nowLocked() time.Time {
	if !c.stopped.IsZero() {
		return c.stopped
	}
	return time.Now()
}

// pausedLocked is the paused time up to now.
func (c *WallClock) pausedLocked(now time.Time) time.Duration {
	p := c.paused
	if !c.downSince.IsZero() {
		p += max(now.Sub(c.downSince), 0)
	}
	return p
}

// next says how long until a limit is reached in the clock's present state, and which (nil: none can be, as things stand). A
// left of zero or less means it is reached.
func (c *WallClock) next() (left time.Duration, cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.stopped.IsZero() {
		return 0, nil
	}
	now := time.Now()
	paused := c.pausedLocked(now)
	switch {
	case c.downSince.IsZero() && c.wall > 0:
		return c.wall - (now.Sub(c.start) - paused), ErrWallBudget
	case !c.downSince.IsZero() && c.maxPause > 0:
		return c.maxPause - paused, ErrOutageLimit
	}
	return 0, nil
}

// Watch derives the harness's context from parent. It is cancelled when the active time reaches the wall budget (its cause,
// context.Cause, is ErrWallBudget) or the outages together reach the pause limit (ErrOutageLimit), and by the returned function,
// which must be called when the harness is done with it.
func (c *WallClock) Watch(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		for {
			left, cause := c.next()
			if cause != nil && left <= 0 {
				cancel(cause)
				return
			}
			var fire <-chan time.Time
			var t *time.Timer
			if cause != nil {
				t = time.NewTimer(left)
				fire = t.C
			}
			select {
			case <-fire:
			case <-c.wake:
			case <-ctx.Done():
			case <-done:
			}
			if t != nil {
				t.Stop()
			}
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			default:
			}
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(done) })
		cancel(context.Canceled)
	}
}
