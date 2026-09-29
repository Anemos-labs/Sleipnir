// Package events is Sleipnir's source of truth: an append-only, ordered log of
// everything that happens in a session, plus a content-addressed blob store.
//
// Every other structure (transcripts, layer versions, the task board, mailboxes,
// cost reports, training exports) is either written through this log or can be
// re-derived from it. That single decision buys crash recovery, resume,
// deterministic replay for offline cache-policy simulation, and training data
// by construction.
package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SchemaVersion is bumped on breaking changes to the envelope.
const SchemaVersion = 1

// Event is one log record.
type Event struct {
	Seq     uint64          `json:"seq"`
	TS      time.Time       `json:"ts"`
	Session string          `json:"session"`
	Agent   string          `json:"agent,omitempty"` // empty: kernel-level
	Type    string          `json:"type"`
	Cause   uint64          `json:"cause,omitempty"` // seq of the triggering event
	Data    json.RawMessage `json:"data,omitempty"`
	V       int             `json:"v,omitempty"` // schema version, present on the first record only
}

// Emitter is what producers depend on. Keeping it an interface lets packages
// be tested against a MemLog without touching disk.
type Emitter interface {
	// Emit appends an event. data is marshalled to JSON. It returns the
	// assigned sequence number.
	Emit(agent, typ string, data any, opts ...Opt) (uint64, error)
}

// Opt tweaks one Emit call.
type Opt func(*Event)

// Cause links the event to the event that triggered it.
func Cause(seq uint64) Opt { return func(e *Event) { e.Cause = seq } }

// Log is the durable Emitter: JSONL on disk plus in-process subscribers.
type Log struct {
	dir     string
	session string
	now     func() time.Time

	mu     sync.Mutex
	seq    uint64
	f      *os.File
	w      *bufio.Writer
	subs   map[int]*sub
	nextID int
	closed bool
	lastFl time.Time
}

type sub struct {
	ch      chan Event
	dropped uint64
}

// Open opens (or resumes) the log for a session under dir. Existing events
// are counted so sequence numbers continue; a torn final line from a crash is
// truncated.
func Open(dir, session string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "events.jsonl")
	last, size, err := scanTail(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(size, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	l := &Log{
		dir:     dir,
		session: session,
		now:     time.Now,
		seq:     last,
		f:       f,
		w:       bufio.NewWriterSize(f, 64<<10),
		subs:    map[int]*sub{},
	}
	if last == 0 {
		l.emitLocked("", "log.open", map[string]any{"schema": SchemaVersion}, nil)
	}
	return l, nil
}

// scanTail returns the last valid sequence number and the byte offset just
// after the last complete, parseable line.
func scanTail(path string) (last uint64, size int64, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var off int64
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var e struct {
				Seq uint64 `json:"seq"`
			}
			if json.Unmarshal(line, &e) == nil && e.Seq > 0 {
				last = e.Seq
				off += int64(len(line))
			} else {
				return last, off, nil
			}
		}
		if rerr != nil {
			return last, off, nil
		}
	}
}

// Dir returns the directory holding the log.
func (l *Log) Dir() string { return l.dir }

// Session returns the session id stamped on every event.
func (l *Log) Session() string { return l.session }

// SetClock overrides the timestamp source (tests, simulation).
func (l *Log) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

// Emit implements Emitter.
func (l *Log) Emit(agent, typ string, data any, opts ...Opt) (uint64, error) {
	var raw json.RawMessage
	switch d := data.(type) {
	case nil:
	case json.RawMessage:
		raw = d
	case []byte:
		raw = d
	default:
		b, err := json.Marshal(d)
		if err != nil {
			return 0, fmt.Errorf("events: marshal %s: %w", typ, err)
		}
		raw = b
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, fmt.Errorf("events: log closed")
	}
	return l.emitLocked(agent, typ, raw, opts)
}

func (l *Log) emitLocked(agent, typ string, data any, opts []Opt) (uint64, error) {
	var raw json.RawMessage
	switch d := data.(type) {
	case json.RawMessage:
		raw = d
	case nil:
	default:
		b, err := json.Marshal(d)
		if err != nil {
			return 0, err
		}
		raw = b
	}
	l.seq++
	e := Event{Seq: l.seq, TS: l.now().UTC(), Session: l.session, Agent: agent, Type: typ, Data: raw}
	if l.seq == 1 {
		e.V = SchemaVersion
	}
	for _, o := range opts {
		o(&e)
	}
	line, err := json.Marshal(e)
	if err != nil {
		l.seq--
		return 0, err
	}
	line = append(line, '\n')
	if _, err := l.w.Write(line); err != nil {
		return 0, err
	}
	// Group commit: flush at most every few milliseconds so bursts of events
	// from a busy swarm share one write syscall, while a crash loses at most a
	// handful of trailing events.
	if t := e.TS; t.Sub(l.lastFl) > 5*time.Millisecond {
		if err := l.w.Flush(); err != nil {
			return 0, err
		}
		l.lastFl = t
	}
	for _, s := range l.subs {
		select {
		case s.ch <- e:
		default:
			s.dropped++
		}
	}
	return e.Seq, nil
}

// Subscribe returns a channel of future events. The channel is buffered;
// slow subscribers lose events (counted) instead of stalling the swarm.
func (l *Log) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 256
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	id := l.nextID
	l.nextID++
	s := &sub{ch: make(chan Event, buffer)}
	l.subs[id] = s
	return s.ch, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, ok := l.subs[id]; ok {
			delete(l.subs, id)
			close(s.ch)
		}
	}
}

// Flush forces buffered events to the OS and fsyncs.
func (l *Log) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if err := l.w.Flush(); err != nil {
		return err
	}
	return l.f.Sync()
}

// Close flushes and closes the log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	for id, s := range l.subs {
		close(s.ch)
		delete(l.subs, id)
	}
	if err := l.w.Flush(); err != nil {
		l.f.Close()
		return err
	}
	if err := l.f.Sync(); err != nil {
		l.f.Close()
		return err
	}
	return l.f.Close()
}

// Scan reads every event in path in order, stopping at the first error from fn.
func Scan(path string, fn func(Event) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 {
			var e Event
			if err := json.Unmarshal(line, &e); err != nil {
				return fmt.Errorf("events: corrupt line: %w", err)
			}
			if err := fn(e); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// MemLog is an in-memory Emitter for tests.
type MemLog struct {
	mu     sync.Mutex
	events []Event
	now    func() time.Time
}

// NewMemLog returns an empty MemLog.
func NewMemLog() *MemLog { return &MemLog{now: time.Now} }

// Emit implements Emitter.
func (m *MemLog) Emit(agent, typ string, data any, opts ...Opt) (uint64, error) {
	var raw json.RawMessage
	switch d := data.(type) {
	case nil:
	case json.RawMessage:
		raw = d
	default:
		b, err := json.Marshal(d)
		if err != nil {
			return 0, err
		}
		raw = b
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := Event{Seq: uint64(len(m.events)) + 1, TS: m.now().UTC(), Session: "mem", Agent: agent, Type: typ, Data: raw}
	for _, o := range opts {
		o(&e)
	}
	m.events = append(m.events, e)
	return e.Seq, nil
}

// All returns a copy of the recorded events.
func (m *MemLog) All() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events...)
}

// OfType returns recorded events with the given type.
func (m *MemLog) OfType(typ string) []Event {
	var out []Event
	for _, e := range m.All() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// Discard is an Emitter that drops everything.
type Discard struct{}

// Emit implements Emitter.
func (Discard) Emit(string, string, any, ...Opt) (uint64, error) { return 0, nil }
