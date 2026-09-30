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
	"errors"
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
	rec    Recovery

	// Group commit (see commitLocked). lastFl is when the buffer last went to
	// the OS, on the monotonic clock: event timestamps come from an injectable
	// clock (tests, simulation) and from the wall clock, which can step back.
	lastFl     time.Time
	timer      *time.Timer // flushes the tail of a burst; created on first use
	timerArmed bool
}

// groupCommitWindow bounds how long an event may wait in the write buffer.
// While events keep arriving the log flushes at most once per window, so a
// burst from a busy swarm shares write syscalls; when they stop, a timer flushes
// what is left within the window, so a crash or SIGKILL in a quiet period (every
// agent waiting on a model call, the manager parked in wait) loses at most the
// last few milliseconds of events, not everything since the last flush.
const groupCommitWindow = 5 * time.Millisecond

type sub struct {
	ch      chan Event
	dropped uint64
}

// MaxEventBytes bounds one encoded log line (an event and its newline). Emit
// refuses to write a longer one and the readers skip a longer one without
// buffering it, so a damaged or hostile log cannot make Open or Scan allocate
// without limit. It is far above any legitimate event: large payloads belong in
// Blobs.
const MaxEventBytes = 32 << 20

// maxLineBytes is MaxEventBytes; a variable so tests can shrink it.
var maxLineBytes = MaxEventBytes

// maxSeq is the largest sequence number a line may carry and still count as a
// valid event: JSON's exact-integer range, far beyond anything a log reaches, and
// well clear of the point where counting on would wrap around to 0.
const maxSeq = 1 << 53

// TypeLogCorrupt is emitted by Open, once per stretch of damage, when it found
// complete lines in an existing log that are not valid events. The payload is a
// Recovery.
const TypeLogCorrupt = "log.corrupt"

// Recovery says what Open found wrong in an existing log.
type Recovery struct {
	// CorruptLines counts complete lines that were not valid events (or were longer
	// than MaxEventBytes) and that no earlier Open had recorded. They stay in the
	// file, where readers skip them; nothing after them is lost.
	CorruptLines int `json:"corrupt_lines,omitempty"`
	// FirstCorruptLine is the 1-based number of the first of them.
	FirstCorruptLine int `json:"first_corrupt_line,omitempty"`
	// TornBytes is the size of a final line cut short by a crash (no newline, not a
	// whole event) that Open removed.
	TornBytes int64 `json:"torn_bytes,omitempty"`
}

// Open opens (or resumes) the log for a session under dir. Existing events
// are counted so sequence numbers continue. A torn final line from a crash is
// truncated; a complete line in the middle that is not a valid event is not: it
// is skipped and counted (see Recovery, and the log.corrupt event Open appends),
// and everything after it is kept and appended to. Directories are created 0700
// and the log 0600; an existing log with wider permissions (written by an older
// version) is tightened, an existing directory keeps its mode.
func Open(dir, session string) (*Log, error) {
	if err := makePrivateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(path, openLogFlags, 0o600)
	if err != nil {
		if isSymlinkRefusal(err) {
			return nil, fmt.Errorf("events: %s is a symlink; refusing to write through it", path)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("events: %s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		_ = f.Chmod(fi.Mode().Perm() &^ 0o077) // our file; best effort
	}
	sc, err := scanLog(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(sc.size); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(sc.size, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	l := &Log{
		dir:     dir,
		session: session,
		now:     time.Now,
		seq:     sc.last,
		f:       f,
		w:       bufio.NewWriterSize(f, 64<<10),
		subs:    map[int]*sub{},
		rec:     sc.Recovery,
	}
	if sc.needNewline {
		l.w.WriteByte('\n') // a whole event that only lost its newline in the crash
	}
	if sc.last == 0 {
		l.emitLocked("", "log.open", map[string]any{"schema": SchemaVersion}, nil)
	}
	if sc.CorruptLines > 0 {
		l.emitLocked("", TypeLogCorrupt, sc.Recovery, nil)
	}
	return l, nil
}

// Recovery reports what Open found wrong in the log it resumed; the zero value
// means nothing.
func (l *Log) Recovery() Recovery { return l.rec }

// logScan is what scanLog learned about an existing log.
type logScan struct {
	Recovery
	last uint64 // highest sequence number of a valid event
	size int64  // where appending resumes
	// needNewline: the final line is a valid event that lost its newline; add it.
	needNewline bool
}

// decodeEvent reads a line as an Event. It is the one test for "a valid event", used by
// Scan (which delivers events) and by Open (which resumes a log): a JSON object whose
// members have their types and whose "seq" is positive and within range. Two tests
// that differ disagree about lines (a timestamp that is not a time, a sequence number
// above 2^53), and a log that Scan reads as sound would then be damaged to Open.
func decodeEvent(line []byte) (Event, bool) {
	var e Event
	if json.Unmarshal(line, &e) != nil || e.Seq == 0 || e.Seq > maxSeq {
		return Event{}, false
	}
	return e, true
}

// parseLine reads the sequence number and type of a line that decodeEvent accepts.
func parseLine(line []byte) (seq uint64, typ string, ok bool) {
	e, ok := decodeEvent(line)
	if !ok {
		return 0, "", false
	}
	return e.Seq, e.Type, true
}

// scanLog reads a log from r (positioned at its start) and works out where to
// carry on. Only a torn final line, one without a newline that is not a whole
// event, is dropped; a corrupt complete line is counted and left where it is.
// Damage that an earlier Open already recorded (a log.corrupt event after it) is
// not counted again. A read error is returned, never mistaken for the end of the
// file: cutting the log at a transient failure would destroy everything after it.
func scanLog(r io.Reader) (logScan, error) {
	var sc logScan
	lr := newLineReader(r, maxLineBytes)
	var off int64
	unrecorded := 0
	for lineNo := 1; ; lineNo++ {
		line, n, complete, tooLong, err := lr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return sc, err
		}
		var seq uint64
		var typ string
		ok := false
		if !tooLong {
			seq, typ, ok = parseLine(line)
		}
		if !complete {
			if ok { // whole event, missing only its newline
				sc.last = max(sc.last, seq)
				sc.needNewline = true
				off += int64(n)
			} else {
				sc.TornBytes = int64(n)
			}
			break
		}
		if ok {
			sc.last = max(sc.last, seq)
			if typ == TypeLogCorrupt {
				unrecorded = 0 // the damage above it has been written down already
			}
		} else {
			unrecorded++
			if unrecorded == 1 {
				sc.FirstCorruptLine = lineNo
			}
		}
		off += int64(n)
	}
	sc.CorruptLines = unrecorded
	if unrecorded == 0 {
		sc.FirstCorruptLine = 0
	}
	sc.size = off
	return sc, nil
}

// lineReader reads newline-terminated lines without ever holding more than max
// bytes of one.
type lineReader struct {
	r   *bufio.Reader
	buf []byte
	max int
}

func newLineReader(r io.Reader, max int) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 64<<10), max: max}
}

// next returns the next line, including its newline when complete is true. The
// slice is valid until the following call. A line longer than max is consumed but
// not kept: tooLong is true and line is nil. n is the line's full length. At the
// end of the input, with nothing left, err is io.EOF; a last line without a
// newline comes back with complete false. Any other err is a read error.
func (lr *lineReader) next() (line []byte, n int, complete, tooLong bool, err error) {
	if cap(lr.buf) > 1<<20 {
		lr.buf = nil // do not sit on the memory one long line needed
	}
	lr.buf = lr.buf[:0]
	for {
		chunk, rerr := lr.r.ReadSlice('\n')
		n += len(chunk)
		if !tooLong {
			if len(lr.buf)+len(chunk) > lr.max {
				tooLong = true
				lr.buf = lr.buf[:0]
			} else {
				lr.buf = append(lr.buf, chunk...)
			}
		}
		switch {
		case rerr == nil:
			return lr.kept(tooLong), n, true, tooLong, nil
		case errors.Is(rerr, bufio.ErrBufferFull):
			continue
		case rerr == io.EOF:
			if n == 0 {
				return nil, 0, false, false, io.EOF
			}
			return lr.kept(tooLong), n, false, tooLong, nil
		default:
			return nil, n, false, tooLong, rerr
		}
	}
}

func (lr *lineReader) kept(tooLong bool) []byte {
	if tooLong {
		return nil
	}
	return lr.buf
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
	if l.seq >= maxSeq { // Open refuses a line above this: the writer must not produce one its reader rejects
		return 0, fmt.Errorf("events: the log has reached its last sequence number")
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
	if len(line)+1 > maxLineBytes { // a line the readers would refuse: never write one
		l.seq--
		return 0, fmt.Errorf("events: %s event is %d bytes, over the %d byte limit; store large payloads as blobs", typ, len(line)+1, maxLineBytes)
	}
	line = append(line, '\n')
	if _, err := l.w.Write(line); err != nil {
		return 0, err
	}
	if err := l.commitLocked(); err != nil {
		return 0, err
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

// commitLocked is the group commit, run after every buffered write. The first
// event after a quiet period is flushed at once; further events inside the
// window only mark the buffer dirty and make sure a timer will flush it when the
// window ends, so the last event of a burst is never left waiting for a next
// event that may not come. (A write that overflowed the 64 KiB buffer has already
// gone out on its own.)
func (l *Log) commitLocked() error {
	if l.w.Buffered() == 0 {
		return nil
	}
	since := time.Since(l.lastFl)
	if l.lastFl.IsZero() || since >= groupCommitWindow {
		return l.flushBufferLocked()
	}
	if !l.timerArmed {
		l.timerArmed = true
		if d := groupCommitWindow - since; l.timer == nil {
			l.timer = time.AfterFunc(d, l.flushTail)
		} else {
			l.timer.Reset(d)
		}
	}
	return nil
}

func (l *Log) flushBufferLocked() error {
	err := l.w.Flush()
	l.lastFl = time.Now()
	return err
}

// flushTail is the timer's callback: it writes out whatever a burst left in the
// buffer. A write error is not lost: bufio's error is sticky, so the next Emit,
// Flush or Close returns it.
func (l *Log) flushTail() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.timerArmed = false
	if l.closed || l.w.Buffered() == 0 {
		return
	}
	_ = l.flushBufferLocked()
}

// Subscribe returns a channel of future events. The channel is buffered;
// slow subscribers lose events (counted) instead of stalling the swarm.
// Subscribing to a closed log returns a channel that is already closed (and a
// cancel that does nothing): nothing will ever be delivered, and a consumer
// ranging over the channel ends instead of waiting for ever.
func (l *Log) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 256
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
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

// Flush forces buffered events to the OS and fsyncs: everything emitted before
// the call is durable when it returns.
func (l *Log) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if err := l.flushBufferLocked(); err != nil {
		return err
	}
	return l.f.Sync()
}

// Close flushes and closes the log, and stops its background flush.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.timer != nil {
		l.timer.Stop() // a callback already waiting on mu sees closed and returns
	}
	l.timerArmed = false
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

// ErrCorruptLog is matched (errors.Is) by the error Scan returns after skipping
// damaged lines.
var ErrCorruptLog = errors.New("events: corrupt log lines")

// CorruptError is what Scan returns when it skipped complete lines that are not
// valid events. Every valid event was delivered first.
type CorruptError struct {
	Lines int // how many were skipped
	First int // 1-based line number of the first
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("events: %d corrupt line(s) skipped (first: line %d)", e.Lines, e.First)
}

// Is reports that a CorruptError is an ErrCorruptLog.
func (e *CorruptError) Is(target error) bool { return target == ErrCorruptLog }

// Scan reads every event in path in order, stopping at the first error from fn.
// Damage does not stop it: a complete line that is not a valid event (or is longer
// than MaxEventBytes) is skipped, a torn final line, which is what a crash leaves,
// is ignored, and once every valid event has been delivered Scan returns a
// *CorruptError (matching ErrCorruptLog) if it skipped anything, so a caller that
// cares can tell and one that does not can carry on with what it got. Memory use
// is bounded by MaxEventBytes.
func Scan(path string, fn func(Event) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	lr := newLineReader(f, maxLineBytes)
	var bad, first int
	for lineNo := 1; ; lineNo++ {
		line, _, complete, tooLong, err := lr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		var e Event
		ok := false
		if !tooLong {
			e, ok = decodeEvent(line)
		}
		if !ok {
			if complete { // an unterminated last line is a torn write, not damage
				bad++
				if first == 0 {
					first = lineNo
				}
			}
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	if bad > 0 {
		return &CorruptError{Lines: bad, First: first}
	}
	return nil
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
