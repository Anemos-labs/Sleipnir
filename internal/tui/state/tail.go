package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

// DefaultPoll is how often Tail looks for new lines when it is given no ticker.
const DefaultPoll = 250 * time.Millisecond

// maxSeq is the largest sequence number a line may carry and still be an event: events.Scan and events.Open share that test
// (internal/events decodeEvent), and Tail has to agree with them on what a valid line is, or a person watching a session and one
// replaying it see two different sessions. The constant is not exported there, so it is repeated here, and
// TestTailAndScanAgreeOnWhatIsAnEvent fails if the two come apart.
const maxSeq = 1 << 53

// TailOptions configures Tail and Follow.
type TailOptions struct {
	// Tick, when not nil, is the ticker: each value received is one poll of the log, and Interval is not used. A test sends on a
	// channel it owns, so that no test of a tail sleeps. Nil means a time.Ticker of Interval.
	Tick <-chan time.Time
	// Interval is the time between polls when Tick is nil; zero means DefaultPoll.
	Interval time.Duration
	// WaitForFile makes a log that does not exist yet a log with nothing in it, polled until it appears, where by default it is an
	// error.
	WaitForFile bool
	// OnReset is called when the log was truncated or replaced by another file (rotation): everything delivered before is void, and
	// reading starts again from the top of the new file. It is called before the first event of the new log is delivered. Follow
	// resets its State there.
	OnReset func()
	// OnPoll is called after every poll, from the goroutine that polls, with what it found. It is the barrier of a test: send a
	// tick, wait for the poll to report, and only then look at what was delivered.
	OnPoll func(PollInfo)
}

// PollInfo is what one poll of the log found.
type PollInfo struct {
	// Offset is how many bytes of the log have been read.
	Offset int64
	// Delivered is the events this poll delivered, Total all events delivered since the last reset.
	Delivered int
	Total     int
	// Pending is the size of an unfinished last line held back until its end arrives: a write that was cut short, or has not
	// finished yet.
	Pending int
	// Corrupt counts the complete lines so far that were not valid events; they are skipped.
	Corrupt int
	// Resets counts how often the log was truncated or replaced.
	Resets int
}

// Tail follows the event log at path, calling fn for each event in order: first those already in the file, then those appended
// while it runs. It polls: once at the start and once per tick. It returns when ctx is done (ctx.Err()), when fn returns an error
// (that error), or when the log cannot be read (that error).
//
// The log is append-only, and Tail is tolerant of what that allows. A last line that is not finished (no newline yet, and not yet
// a whole event) is held back until it is, so a write that is torn, or simply in progress, is never delivered in pieces; a last
// line that is a whole event is delivered at once, and its newline, when it comes, is not taken for another line. A complete line
// that is not a valid event is skipped and counted (PollInfo.Corrupt), a line longer than events.MaxEventBytes is skipped without
// being buffered, and a torn tail that the writer cut off when it resumed is dropped. A log that shrinks, or is replaced by another
// file, is read again from its top, after OnReset.
//
// The options are optional (the first one given is used; none means the defaults: a ticker of DefaultPoll, and a log that must
// exist).
func Tail(ctx context.Context, path string, fn func(events.Event) error, options ...TailOptions) error {
	opt := firstOption(options)
	t := &tailer{path: path, fn: fn, opt: opt}
	defer t.close()
	tick := opt.Tick
	if tick == nil {
		iv := opt.Interval
		if iv <= 0 {
			iv = DefaultPoll
		}
		tk := time.NewTicker(iv)
		defer tk.Stop()
		tick = tk.C
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := t.poll(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
		}
	}
}

// Follow is Tail into a State: every event is applied to st, and a truncated or replaced log resets it. The options are optional,
// as in Tail; the damaged lines the follower has skipped are counted in st's Stats.Corrupt.
func Follow(ctx context.Context, path string, st *State, options ...TailOptions) error {
	opt := firstOption(options)
	reset, poll := opt.OnReset, opt.OnPoll
	opt.OnReset = func() {
		st.Reset()
		if reset != nil {
			reset()
		}
	}
	opt.OnPoll = func(p PollInfo) {
		st.setCorrupt(p.Corrupt)
		if poll != nil {
			poll(p)
		}
	}
	return Tail(ctx, path, func(e events.Event) error { st.Apply(e); return nil }, opt)
}

// firstOption is the options a variadic parameter carries: the first, or the zero options.
func firstOption(options []TailOptions) TailOptions {
	if len(options) > 0 {
		return options[0]
	}
	return TailOptions{}
}

// setCorrupt records the damaged lines a follower has seen so far (a total, not an increment).
func (s *State) setCorrupt(n int) {
	s.mu.Lock()
	if n > s.stats.Corrupt {
		s.stats.Corrupt = n
	}
	s.mu.Unlock()
}

// tailer is the state of one Tail.
type tailer struct {
	path string
	fn   func(events.Event) error
	opt  TailOptions

	f         *os.File
	opened    bool   // the log has been open at least once: if it vanishes later it is being rotated, not missing
	pos       int64  // the offset of the next byte to read
	lineStart int64  // the offset where the unfinished line began
	buf       []byte // the unfinished line
	sent      bool   // buf has been delivered already (it was a whole event) and only its newline is missing
	skipping  bool   // inside a line that is too long: discard until its newline
	tried     int    // len(buf) when an unfinished line was last tried as an event
	sum       []byte // the end of the last whole line, and where it sits: what to check the file against
	sumOff    int64

	total, delivered, corrupt, resets int
	chunk                             []byte
}

func (t *tailer) close() {
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}

// open opens the log if it is not open; ok is false when it does not exist and that is allowed.
func (t *tailer) open() (ok bool, err error) {
	if t.f != nil {
		return true, nil
	}
	f, err := os.Open(t.path)
	if err != nil {
		if (t.opt.WaitForFile || t.opened) && errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	t.f, t.opened = f, true
	return true, nil
}

// restart forgets what was read and starts at the top of the log (which may be a new file).
func (t *tailer) restart() {
	t.close()
	t.pos, t.lineStart, t.buf, t.sent, t.skipping, t.tried = 0, 0, nil, false, false, 0
	t.sum, t.sumOff = t.sum[:0], 0
	t.total, t.corrupt = 0, 0
	t.resets++
	if t.opt.OnReset != nil {
		t.opt.OnReset()
	}
}

// poll reads everything that is new.
func (t *tailer) poll() error {
	t.delivered = 0
	ok, err := t.open()
	if err != nil {
		return err
	}
	if ok {
		if err := t.checkRotation(); err != nil {
			return err
		}
		if t.f == nil { // replaced, and the new file is not there yet
			t.report()
			return nil
		}
		if err := t.readAll(); err != nil {
			return err
		}
		if err := t.tryUnfinished(); err != nil {
			return err
		}
	}
	t.report()
	return nil
}

func (t *tailer) report() {
	if t.opt.OnPoll == nil {
		return
	}
	pending := len(t.buf)
	if t.sent {
		pending = 0
	}
	t.opt.OnPoll(PollInfo{Offset: t.pos, Delivered: t.delivered, Total: t.total, Pending: pending, Corrupt: t.corrupt, Resets: t.resets})
}

// checkRotation notices that the path is another file than the one open, or that the file has shrunk.
func (t *tailer) checkRotation() error {
	fi, err := os.Stat(t.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed: keep what is open; the next poll sees what replaces it
		}
		return err
	}
	cur, err := t.f.Stat()
	if err != nil {
		return err
	}
	switch {
	case !os.SameFile(cur, fi):
		// Replaced (rotation): finish the old file, then start the new one.
		if err := t.readAll(); err != nil {
			return err
		}
		t.restart()
		_, err := t.open()
		return err
	case fi.Size() < t.lineStart:
		// Shorter than the whole lines already read: truncated, or replaced in place.
		t.restart()
		_, err := t.open()
		return err
	}
	return t.verify()
}

// verify checks that what was read is still what the file holds. A log that was truncated and then written past its old end looks
// like one that only grew, so its size says nothing; its bytes do. If the last whole line read is not there any more the log is
// another one and starts again; if only the unfinished last line is not (a writer that resumes after a crash removes its torn tail
// and appends) that line is dropped and reading goes on from where it began.
func (t *tailer) verify() error {
	if n := len(t.sum); n > 0 {
		got := make([]byte, n)
		if m, _ := t.f.ReadAt(got, t.sumOff); m != n || !bytes.Equal(got, t.sum) {
			t.restart()
			_, err := t.open()
			return err
		}
	}
	if n := len(t.buf); n > 0 && !t.skipping {
		got := make([]byte, n)
		if m, _ := t.f.ReadAt(got, t.lineStart); m != n || !bytes.Equal(got, t.buf) {
			t.buf, t.sent, t.tried = t.buf[:0], false, 0
			t.pos = t.lineStart
			_, err := t.f.Seek(t.pos, io.SeekStart)
			return err
		}
	}
	return nil
}

// remember keeps the end of a whole line (at most 256 bytes of it, and its newline) and where it ends.
func (t *tailer) remember(line []byte, end int64) {
	const keep = 256
	if len(line) > keep-1 {
		line = line[len(line)-(keep-1):]
	}
	t.sum = append(append(t.sum[:0], line...), '\n')
	t.sumOff = end - int64(len(t.sum))
}

// readAll reads the open file to its end.
func (t *tailer) readAll() error {
	if t.chunk == nil {
		t.chunk = make([]byte, 64<<10)
	}
	for {
		n, err := t.f.Read(t.chunk)
		if n > 0 {
			base := t.pos
			t.pos += int64(n)
			if ferr := t.feed(t.chunk[:n], base); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// feed splits newly read bytes into lines. base is the file offset of data[0].
func (t *tailer) feed(data []byte, base int64) error {
	off := base
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			t.extend(data)
			return nil
		}
		t.extend(data[:i])
		off += int64(i) + 1
		data = data[i+1:]
		if err := t.endLine(off); err != nil {
			return err
		}
		t.lineStart = off
	}
	return nil
}

// extend adds bytes of the unfinished line; a line that grows past events.MaxEventBytes is given up and skipped to its newline.
func (t *tailer) extend(b []byte) {
	if t.skipping || len(b) == 0 {
		return
	}
	if len(t.buf)+len(b) > events.MaxEventBytes {
		t.skipping, t.buf, t.sent, t.tried = true, nil, false, 0
		return
	}
	t.buf = append(t.buf, b...)
}

// endLine handles a line whose newline has arrived; end is the offset just after it.
func (t *tailer) endLine(end int64) error {
	line, sent, skipping := t.buf, t.sent, t.skipping
	t.buf, t.sent, t.skipping, t.tried = t.buf[:0], false, false, 0
	switch {
	case skipping:
		t.corrupt++
		return nil
	case sent:
		t.remember(line, end)
		return nil // delivered when it became a whole event; this is its newline
	}
	t.remember(line, end)
	return t.deliver(line, true)
}

// tryUnfinished delivers an unfinished last line if it already is a whole event: a JSON object is complete at its closing brace,
// and no shorter piece of one is valid JSON, so a line that parses is complete whether or not its newline has been written.
func (t *tailer) tryUnfinished() error {
	if t.sent || t.skipping || len(t.buf) == 0 || len(t.buf) == t.tried {
		return nil
	}
	t.tried = len(t.buf)
	if tail := bytes.TrimRight(t.buf, " \t\r"); len(tail) == 0 || tail[len(tail)-1] != '}' {
		return nil
	}
	return t.deliver(t.buf, false)
}

// deliver parses a line as an event and hands it to fn. complete says the newline has been seen: a line that is not an event is
// then corrupt, where an unfinished one may just not be written yet.
func (t *tailer) deliver(line []byte, complete bool) error {
	var e events.Event
	if json.Unmarshal(line, &e) != nil || e.Seq == 0 || e.Seq > maxSeq {
		if complete {
			t.corrupt++
		}
		return nil
	}
	if !complete {
		t.sent = true
	}
	t.total++
	t.delivered++
	return t.fn(e)
}
