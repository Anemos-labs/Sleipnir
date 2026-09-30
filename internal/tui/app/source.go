package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/tui/state"
)

// Source is the session a program shows: a log that is being written (LiveSource) or one that was recorded, played on a clock of its
// own (ReplaySource). Both feed a state.State, which the program looks at through snapshots.
type Source interface {
	// State is the state the source feeds; a source may replace it (a replay that seeks backwards starts again), so ask again at
	// every frame.
	State() *state.State
	// Now is the moment of the session the screen shows: the wall clock for a live session, the place the recording has reached
	// for a replay.
	Now() time.Time
	// Advance moves the clock of the source by dt of the screen's time: a replay plays the events that have become due, a live
	// source has nothing to do.
	Advance(dt time.Duration)
	// Err is what made the source stop before its end: a log that could not be read.
	Err() error
	// Close releases the log and stops the goroutine a live source follows it with.
	Close() error
}

// LiveSource follows the event log of a session as it is written.
type LiveSource struct {
	st     *state.State
	clock  func() time.Time
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

// LiveOptions configure OpenLive.
type LiveOptions struct {
	// Clock is the wall clock (time.Now when nil): the TTL clocks and the right edge of the activity lanes are measured from it.
	Clock func() time.Time
	// Tick, when not nil, is the ticker that polls the log (see state.TailOptions.Tick); nil polls at state.DefaultPoll.
	Tick <-chan time.Time
	// OnPoll is called after every poll of the log (a test's barrier).
	OnPoll func(state.PollInfo)
	// WaitForFile waits for a log that does not exist yet instead of failing.
	WaitForFile bool
}

// OpenLive starts following the log at path in a goroutine that ends with ctx or Close.
func OpenLive(ctx context.Context, path string, o LiveOptions) *LiveSource {
	ctx, cancel := context.WithCancel(ctx)
	l := &LiveSource{st: state.New(), clock: o.Clock, cancel: cancel, done: make(chan struct{})}
	if l.clock == nil {
		l.clock = time.Now
	}
	go func() {
		defer close(l.done)
		err := state.Follow(ctx, path, l.st, state.TailOptions{Tick: o.Tick, OnPoll: o.OnPoll, WaitForFile: o.WaitForFile})
		if err != nil && !errors.Is(err, context.Canceled) {
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *LiveSource) State() *state.State   { return l.st }
func (l *LiveSource) Now() time.Time        { return l.clock() }
func (l *LiveSource) Advance(time.Duration) {}

// Err is the error that ended the follower, nil while it runs.
func (l *LiveSource) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Close stops following and waits for the follower to finish.
func (l *LiveSource) Close() error {
	l.cancel()
	<-l.done
	return nil
}

// ReplaySource plays a recorded log on a virtual clock that the program drives: the speed can change and the position can be
// moved, forwards and back.
type ReplaySource struct {
	path  string
	until uint64
	pl    *state.Player
	speed float64
	total time.Duration
}

// Replay speeds, in the order + and - step through them.
var replaySpeeds = []float64{0.25, 0.5, 1, 2, 4, 8, 16, 32, 64}

// OpenReplay opens the log at path for replay at speed (1 when not positive). The log is read once to find how long it is, and
// again to play it, so a recording of any size is held one event at a time.
func OpenReplay(path string, speed float64, until uint64) (*ReplaySource, error) {
	if speed <= 0 {
		speed = 1
	}
	st, err := state.FoldUntil(path, until)
	if err != nil {
		return nil, err
	}
	sn := st.Snapshot()
	var total time.Duration
	if !sn.First.IsZero() && sn.Clock.After(sn.First) {
		total = sn.Clock.Sub(sn.First)
	}
	r := &ReplaySource{path: path, until: until, speed: speed, total: total}
	if err := r.reopen(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *ReplaySource) reopen() error {
	pl, err := state.Replay(r.path, state.ReplayOptions{Speed: 1, Until: r.until})
	if err != nil {
		return err // the player that is playing stays
	}
	if r.pl != nil {
		r.pl.Close()
	}
	r.pl = pl
	pl.Advance(0) // the first event is due at the start
	return nil
}

func (r *ReplaySource) State() *state.State { return r.pl.State() }
func (r *ReplaySource) Now() time.Time      { return r.pl.Now() }

// Advance plays dt of the screen's time, which is dt times the speed of the recording.
func (r *ReplaySource) Advance(dt time.Duration) {
	r.pl.Advance(time.Duration(float64(dt) * r.speed))
}

func (r *ReplaySource) Err() error { return r.pl.Err() }
func (r *ReplaySource) Close() error {
	if r.pl != nil {
		return r.pl.Close()
	}
	return nil
}

// Speed is how many seconds of the recording pass in a second of the screen's.
func (r *ReplaySource) Speed() float64 { return r.speed }

// SetSpeed sets the speed (not below 1/16 and not above 256).
func (r *ReplaySource) SetSpeed(v float64) { r.speed = min(max(v, 1.0/16), 256) }

// Faster and Slower step the speed through 0.25 0.5 1 2 4 8 16 32 64.
func (r *ReplaySource) Faster() { r.step(+1) }
func (r *ReplaySource) Slower() { r.step(-1) }

func (r *ReplaySource) step(d int) {
	i := 0
	for j, v := range replaySpeeds { // the nearest speed of the list
		if v <= r.speed {
			i = j
		}
	}
	r.speed = replaySpeeds[min(max(i+d, 0), len(replaySpeeds)-1)]
}

// Elapsed is how far into the recording the clock is, Total how long it is (0 when it holds one moment).
func (r *ReplaySource) Elapsed() time.Duration { return r.pl.Elapsed() }
func (r *ReplaySource) Total() time.Duration   { return r.total }

// Done reports whether every event has been played.
func (r *ReplaySource) Done() bool { return r.pl.Done() }

// Seek moves the clock to an absolute place in the recording: forwards it plays what lies between at once, backwards it starts
// again from the top and plays up to it, so the state is always what the log says it was at that moment.
func (r *ReplaySource) Seek(to time.Duration) error {
	to = max(to, 0)
	if r.total > 0 {
		to = min(to, r.total)
	}
	if to < r.pl.Elapsed() {
		if err := r.reopen(); err != nil {
			return fmt.Errorf("replay: %w", err)
		}
	}
	r.pl.Advance(to - r.pl.Elapsed())
	return r.pl.Err()
}

// Restart seeks to the beginning.
func (r *ReplaySource) Restart() error { return r.Seek(0) }
