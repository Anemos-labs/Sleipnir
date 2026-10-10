package translate

import (
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Config configures one tab generation's translator.
type Config struct {
	Tab  string
	Gen  uint64
	Root string
	// StartedAt is the start of the run, from which every event's t is counted. The zero time means the moment Attach is called (the
	// host attaches right after session.New returns, which is the run's start) or, for a followed log, its first event.
	StartedAt time.Time
	// Publish sends frames to the pages (seam.Host.Publish); it never blocks.
	Publish func(wire.Frame)
	Now     func() time.Time
	Limits  Limits
	// Verify returns the session's --verify command and whether the team is isolated (queue texts).
	Verify func() (cmd string, isolated bool)
}

// Limits bound the journal and the sink queue; the zero value means the defaults.
type Limits struct {
	JournalEvents int
	JournalBytes  int
	SinkQueue     int
	History       int
}

// The default limits.
const (
	DefaultJournalEvents = 50_000
	DefaultJournalBytes  = 32 << 20
	DefaultSinkQueue     = 8_192
	DefaultHistory       = 4_000
	// sinkQueueBytes bounds the text the sink queue holds.
	sinkQueueBytes = 8 << 20
	// tickEvery is the translator's clock: the coalescing of streamed text (one more per message per 100 ms), the rate limits and the
	// governor's gauge.
	tickEvery = 100 * time.Millisecond
	// subscriptionBuffer is the capacity of the log subscription's channel.
	subscriptionBuffer = 4096
)

// withDefaults fills the zero limits.
func (l Limits) withDefaults() Limits {
	if l.JournalEvents <= 0 {
		l.JournalEvents = DefaultJournalEvents
	}
	if l.JournalBytes <= 0 {
		l.JournalBytes = DefaultJournalBytes
	}
	if l.SinkQueue <= 0 {
		l.SinkQueue = DefaultSinkQueue
	}
	if l.History <= 0 {
		l.History = DefaultHistory
	}
	return l
}

// Translator turns one tab generation's harness activity into the UI events of package wire, keeps them in the tab's journal and
// publishes them. It implements seam.Translator. Every method is safe for concurrent use; the sinks never block.
type Translator struct {
	cfg Config
	lim Limits
	now func() time.Time

	sq     *sinkQueue
	wake   chan struct{}
	attach chan attachReq
	quit   chan struct{}
	done   chan struct{}
	once   sync.Once

	mu      sync.Mutex
	closed  bool
	started time.Time
	j       *journal
	st      *state.State
	chA     []string // what the log event being applied touched (state.OnChange)
	chT     []string

	d deriveState
}

var _ seam.Translator = (*Translator)(nil)

// attachReq hands the loop a log subscription to follow.
type attachReq struct {
	ch     <-chan events.Event
	cancel func()
}

// New returns a translator for one tab generation; it implements seam.Translator. Its goroutine runs until Close.
func New(cfg Config) *Translator {
	t := newTranslator(cfg)
	go t.run()
	return t
}

// newTranslator builds a translator without starting its goroutine (the tests drive it step by step).
func newTranslator(cfg Config) *Translator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	lim := cfg.Limits.withDefaults()
	t := &Translator{cfg: cfg, lim: lim, now: cfg.Now, wake: make(chan struct{}, 1), attach: make(chan attachReq, 1),
		quit: make(chan struct{}), done: make(chan struct{}), started: cfg.StartedAt}
	t.sq = newSinkQueue(lim.SinkQueue, sinkQueueBytes, t.wake)
	t.j = newJournal(lim.JournalEvents, lim.JournalBytes)
	t.st = state.New()
	t.st.OnChange(func(agents, tasks []string) { t.chA, t.chT = append(t.chA, agents...), append(t.chT, tasks...) })
	t.d.init()
	t.ensureRoster("mgr")
	return t
}

// run is the translator's goroutine: it drains the sink queue, follows the attached log and keeps the clock.
func (t *Translator) run() {
	defer close(t.done)
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	var logCh <-chan events.Event
	var cancel func()
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	for {
		select {
		case <-t.quit:
			return
		case <-t.wake:
			t.drainSink()
		case a := <-t.attach:
			if cancel != nil {
				cancel()
			}
			logCh, cancel = a.ch, a.cancel
		case e, ok := <-logCh:
			if !ok {
				logCh = nil
				continue
			}
			t.mu.Lock()
			if !t.closed {
				t.followEvent(e)
			}
			t.mu.Unlock()
		case <-tick.C:
			t.mu.Lock()
			if !t.closed {
				t.tick(t.now())
			}
			t.mu.Unlock()
		}
	}
}

// drainSink applies the queued sink calls. A loss is reported once, with a fresh state, token table and layers for every agent.
func (t *Translator) drainSink() {
	items, lost := t.sq.take()
	if len(items) == 0 && lost == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if lost > 0 {
		t.fellBehind(t.sessT(t.now()))
	}
	for i := range items {
		t.applySink(&items[i])
	}
}

// Sink is the agent.Sink to install as session.Options.Sink.
func (t *Translator) Sink() agent.Sink { return &sink{t: t} }

// NewSink is the per-agent sink constructor for session.Options.NewSink.
func (t *Translator) NewSink(agentID string) agent.Sink { return &sink{t: t, fixed: agentID} }

// Emit appends host-originated events (stamped with the current session time) and publishes them.
func (t *Translator) Emit(evs ...wire.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	ts := t.sessT(t.now())
	for _, e := range evs {
		if e == nil || baseOf(e) == nil || kindOf(e) == "" {
			continue
		}
		t.hostEvent(e, ts)
	}
}

// Question records an open question and emits its ask event.
func (t *Translator) Question(q wire.Question) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.question(q, t.now())
}

// Answered closes a question and emits its answer event.
func (t *Translator) Answered(a wire.Answer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.answered(a, t.now())
}

// Roster is the tab's roster now: the manager first, then the workers in the order they started.
func (t *Translator) Roster() []wire.RosterEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rosterList()
}

// Journal returns the keyframe and the retained events (both encoded), the last seq and the session time now.
func (t *Translator) Journal() (keyframe, evs []wire.Raw, seq uint64, now float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	keyframe, evs, seq = t.j.snapshot()
	return keyframe, evs, seq, t.sessT(t.now())
}

// Seek returns the state at seq as a keyframe and the retained events after it up to seq (the newest periodic keyframe at or before
// seq stands for what precedes it): what a scrubber or a page that wants one moment of the run reduces. ok is false when seq is
// older than what the journal retains.
func (t *Translator) Seek(seq uint64) (keyframe, evs []wire.Raw, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.j.seek(seq)
}

// Hist is the lines the person sent to the tab's agent, oldest first (the newest 200): the composer's history of a snapshot.
func (t *Translator) Hist() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.d.hist...)
}

// StartedAt is the start of the run the events' t is counted from (zero until it is known: see Config.StartedAt).
func (t *Translator) StartedAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.started
}

// Now is the session time now, in seconds.
func (t *Translator) Now() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessT(t.now())
}

// Close stops the translator; later calls do nothing. Open messages are ended first. The journal stays readable.
func (t *Translator) Close() {
	t.once.Do(func() {
		t.mu.Lock()
		if !t.closed {
			t.endAll(t.sessT(t.now()))
		}
		t.closed = true
		stop := t.d.stopFollow
		t.d.stopFollow = nil
		detach := t.d.detach
		t.d.detach = nil
		t.mu.Unlock()
		t.sq.close()
		close(t.quit)
		if detach != nil {
			detach()
		}
		if stop != nil {
			stop()
		}
		select {
		case <-t.done:
		case <-time.After(5 * time.Second):
		}
	})
}

// sessT is the session time of a wall time: seconds since the run's start, rounded to milliseconds, never negative (and 0 while
// the start is unknown).
func (t *Translator) sessT(w time.Time) float64 {
	if t.started.IsZero() || !w.After(t.started) {
		return 0
	}
	return math.Round(w.Sub(t.started).Seconds()*1000) / 1000
}

// put stamps an event with the next seq and a t that is not before the journal's last, encodes it, journals it and publishes it.
// at, when not zero, is the real time of a history event in epoch milliseconds; pre, when not nil, is called with the seq before
// the event is encoded (a message's mid is m + its seq).
func (t *Translator) put(e wire.Event, ts float64, at int64, pre func(seq uint64)) uint64 {
	if t.d.history {
		t.d.histPut(histEntry{e: e, at: at, pre: pre}, t.lim.History)
		return 0
	}
	h := baseOf(e)
	if h == nil {
		return 0
	}
	kind := kindOf(e)
	if ts < t.j.lastT {
		ts = t.j.lastT
	}
	seq := t.j.lastSeq + 1
	h.K, h.T, h.Seq = kind, ts, seq
	if at != 0 {
		h.At = at
	}
	if pre != nil {
		pre(seq)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return 0
	}
	first := false
	if c, ok := e.(*wire.Ckpt); ok {
		first = !t.d.ckptSeen[c.CID]
		t.d.ckptSeen[c.CID] = true
	}
	t.j.add(seq, ts, kind, raw, e)
	if t.cfg.Publish != nil {
		cl := classOf(t.cfg.Tab, kind, e, first)
		t.cfg.Publish(wire.Frame{Type: "ev", Tab: t.cfg.Tab, Data: wire.EvFrame{Tab: t.cfg.Tab, Ev: raw}, Critical: cl.critical,
			Coalescable: cl.coalescable, Key: cl.key})
	}
	return seq
}
