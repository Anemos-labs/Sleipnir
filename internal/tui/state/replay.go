package state

import (
	"errors"
	"iter"
	"math"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// DefaultFrame is the step Frames takes when it is given none: 30 frames a second of virtual time.
const DefaultFrame = time.Second / 30

// ReplayOptions configures Replay.
type ReplayOptions struct {
	// Speed is how many seconds of the recorded session pass in one second of virtual time: 8 plays a session eight times faster
	// than it happened. Zero, a negative number, NaN and infinity mean 1.
	Speed float64
	// Until stops the replay after the last event whose Seq is not above it. Zero means the whole log.
	Until uint64
	// State receives the events. Nil means a new State, which State returns.
	State *State
}

// Player plays a recorded event log into a State on a virtual clock (see Replay). Nothing in it sleeps or reads a clock: the caller
// says how much virtual time has passed (Advance), so a UI can drive it from a ticker, a test from a loop and the headless
// recorder frame by frame, all with the same result. An event is due when the time between the first event and it, as recorded,
// has passed on the virtual clock (scaled by Speed). A Player is not safe for concurrent use; the State it feeds is.
type Player struct {
	st    *State
	speed float64
	until uint64

	next func() (events.Event, error, bool)
	stop func()

	pend    events.Event  // the next event, read and not yet applied
	pendOff time.Duration // when it is due
	have    bool
	done    bool
	origin  time.Time // the timestamp of the first event
	started bool
	lastOff time.Duration
	vt      time.Duration // virtual time elapsed, in recorded time
	applied int
	err     error
}

// Replay opens the log at path for replay on a virtual clock. It reads the first event at once, so a log that does not exist or
// cannot be read is an error here (and the Player is nil). The log is read as events.Scan reads it (see Fold for what that
// tolerates: damage is counted in the State's Stats, never an error), and lazily: a replay of a huge log holds one event at a
// time. Close the Player when it is not played to the end. Options that make no sense (a speed that is zero, negative, NaN or
// infinite) are taken as the default.
func Replay(path string, opt ReplayOptions) (*Player, error) {
	r := &Player{st: opt.State, speed: opt.Speed, until: opt.Until}
	if r.st == nil {
		r.st = New()
	}
	if r.speed <= 0 || math.IsNaN(r.speed) || math.IsInf(r.speed, 0) {
		r.speed = 1
	}
	seq := func(yield func(events.Event, error) bool) {
		err := events.Scan(path, func(e events.Event) error {
			if !yield(e, nil) {
				return errStop
			}
			return nil
		})
		if err != nil && !errors.Is(err, errStop) {
			yield(events.Event{}, err)
		}
	}
	r.next, r.stop = iter.Pull2(seq)
	r.peek()
	if r.err != nil {
		r.Close()
		return nil, r.err
	}
	return r, nil
}

// State is the State the replay feeds.
func (r *Player) State() *State { return r.st }

// Close releases the log. It is safe to call twice and after the end.
func (r *Player) Close() error {
	r.done = true
	r.have = false
	if r.stop != nil {
		r.stop()
		r.stop = nil
	}
	return nil
}

// peek makes sure the next event is read, and reports whether there is one: false once the log is over, Until has been passed, or a
// read failed (Err says).
func (r *Player) peek() bool {
	if r.have {
		return true
	}
	if r.done || r.next == nil {
		return false
	}
	e, err, ok := r.next()
	if !ok {
		r.finish()
		return false
	}
	if err != nil {
		var ce *events.CorruptError
		if errors.As(err, &ce) {
			r.st.AddCorrupt(ce.Lines) // the damage is reported after the last good event: nothing follows it
		} else {
			r.err = err
		}
		r.finish()
		return false
	}
	if r.until > 0 && e.Seq > r.until {
		r.finish()
		return false
	}
	r.pend, r.pendOff, r.have = e, r.offset(e), true
	return true
}

func (r *Player) finish() {
	r.done = true
	if r.stop != nil {
		r.stop()
		r.stop = nil
	}
}

// offset is when an event is due: its distance in time from the first event. Timestamps that go backwards or are missing leave
// the offset where it was, so events stay in order.
func (r *Player) offset(e events.Event) time.Duration {
	if !r.started {
		r.started = true
		r.origin = e.TS
		return 0
	}
	if e.TS.IsZero() || r.origin.IsZero() {
		return r.lastOff
	}
	off := e.TS.Sub(r.origin)
	if off < r.lastOff {
		off = r.lastOff
	}
	return off
}

// Advance moves the virtual clock forward by dt, applies to the State every event that has become due and returns them, in
// order. A negative dt is taken as zero; Advance(0) applies the events that are due at the present moment (at the start, the
// first event and any with the same timestamp). The first event is due at the start, however late its timestamp.
func (r *Player) Advance(dt time.Duration) []events.Event {
	if dt < 0 {
		dt = 0
	}
	step := float64(dt) * r.speed
	if step >= float64(math.MaxInt64-int64(r.vt)) {
		r.vt = math.MaxInt64
	} else {
		r.vt += time.Duration(step)
	}
	var due []events.Event
	for r.peek() && r.pendOff <= r.vt {
		r.play(&due)
	}
	return due
}

// play applies the pending event.
func (r *Player) play(due *[]events.Event) {
	r.lastOff = r.pendOff
	r.st.Apply(r.pend)
	r.applied++
	*due = append(*due, r.pend)
	r.have = false
}

// Drain applies every event that remains (up to Until) regardless of time, and returns them: a jump to the end.
func (r *Player) Drain() []events.Event {
	var due []events.Event
	for r.peek() {
		r.vt = max(r.vt, r.pendOff)
		r.play(&due)
	}
	return due
}

// Done reports whether the replay has nothing more to play: the log is over, or Until was passed.
func (r *Player) Done() bool { return !r.peek() }

// Applied is the number of events played so far.
func (r *Player) Applied() int { return r.applied }

// Elapsed is the virtual time that has passed, in the time of the recording (real seconds of the session).
func (r *Player) Elapsed() time.Duration { return r.vt }

// Now is the moment of the recording that the replay has reached: the timestamp of its first event plus Elapsed. It is the zero
// time before the first event has been played. It is the now to give State.SnapshotAt.
func (r *Player) Now() time.Time {
	if !r.started || r.origin.IsZero() {
		return time.Time{}
	}
	return r.origin.Add(r.vt)
}

// Err is what stopped the replay if it was not the end of the log: a read error. Damaged lines are not an error; they are counted
// in the State's Stats.
func (r *Player) Err() error { return r.err }

// Frame is what a fixed step of the replay produced.
type Frame struct {
	Index int
	// Now is the moment of the recording at the end of the step (Player.Now).
	Now time.Time
	// Events are the events that became due in the step, already applied to the State.
	Events []events.Event
}

// Frames steps the replay by dt of virtual time (DefaultFrame when dt is not positive) until it is done, yielding one Frame per
// step; the last frame holds the last events. It is the loop of the headless recorder.
func (r *Player) Frames(dt time.Duration) iter.Seq[Frame] {
	if dt <= 0 {
		dt = DefaultFrame
	}
	return func(yield func(Frame) bool) {
		for i := 0; ; i++ {
			due := r.Advance(dt)
			if !yield(Frame{Index: i, Now: r.Now(), Events: due}) || r.Done() {
				return
			}
		}
	}
}
