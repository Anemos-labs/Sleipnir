package inspect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
)

// Options tune a Session. The zero value is usable.
type Options struct {
	// MaxRequests bounds the per-request detail kept in memory (a sliding window
	// of the newest requests). Totals stay exact over the whole log. Default 60000.
	MaxRequests int
	// MaxLineBytes: a log line longer than this is skipped and counted as bad
	// instead of buffered. Default 16 MiB.
	MaxLineBytes int
	// Prices resolves model ids to prices for the counterfactual bills.
	// Default cost.Defaults().
	Prices *cost.Table
	// Now is the clock (tests). Default time.Now.
	Now func() time.Time
	// LiveWindow: a log written to this recently, and not ended, is "live". Default 20s.
	LiveWindow time.Duration
	// ID and Name override the session id and display name (multi-session mode
	// uses the relative directory as both).
	ID   string
	Name string
}

func (o *Options) fill() {
	if o.MaxRequests <= 0 {
		o.MaxRequests = 60000
	}
	if o.MaxLineBytes <= 0 {
		o.MaxLineBytes = 16 << 20
	}
	if o.Prices == nil {
		o.Prices = cost.Defaults()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.LiveWindow <= 0 {
		o.LiveWindow = 20 * time.Second
	}
}

// indexEvery is the spacing of the sparse seq -> file offset index that lets
// the raw event view seek instead of scanning the whole log.
const indexEvery = 256

type indexEntry struct {
	seq uint64
	off int64
}

// Session is the in-memory model of one session's event log. It is safe for
// concurrent use: Tail (one writer) may run while any number of readers call
// the view methods.
type Session struct {
	opts  Options
	dir   string
	path  string
	blobs *blobStore

	tailMu sync.Mutex // serialises Tail and reset
	mu     sync.RWMutex

	// File position.
	off     int64
	size    int64
	mtime   time.Time
	fileID  os.FileInfo
	torn    int64
	bad     int64
	badPay  int64
	events  int64
	lastSeq uint64
	reloads int
	index   []indexEntry
	types   map[string]int64

	rev uint64

	// Model state, rebuilt from scratch by reset.
	meta     metaState
	agents   map[string]*agent
	order    []string
	reqs     []*req
	byID     map[string]*req
	dropped  int
	tot      Totals
	cache    cacheAgg
	costAgg  costAgg
	comp     compAgg
	anoms    []*anomaly
	anomTot  AnomalyTotals
	checked  int
	swarm    swarmState
	board    *boardTracker
	rl       rlState
	prices   map[string]*priceInfo
	blobSize map[string]int64

	episode    *EpisodeInfo
	episodeKey string
	sumMemo    *Summary
	sumMemoRev uint64
}

// metaState is the session-level facts gathered from events.
type metaState struct {
	id, version, provider, dialect, renderer, root, goal, sharedHash, model string
	swarm                                                                  bool
	models                                                                 []string
	reconTokens                                                            int
	first, last                                                            time.Time
	ended                                                                  bool
	endTS                                                                  time.Time
	endCost                                                                float64
	schema                                                                 int
}

// New creates an empty Session for dir, which holds events.jsonl (and blobs/).
// dir may also name the events.jsonl file itself. Nothing is read until Tail.
func New(dir string, opts Options) (*Session, error) {
	opts.fill()
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	path := dir
	if fi.IsDir() {
		path = filepath.Join(dir, "events.jsonl")
	} else {
		dir = filepath.Dir(dir)
	}
	if lf, err := os.Stat(path); err != nil {
		return nil, err
	} else if !lf.Mode().IsRegular() {
		return nil, fmt.Errorf("inspect: %s is not a regular file", filepath.Base(path))
	}
	s := &Session{opts: opts, dir: dir, path: path}
	s.blobs = openBlobs(filepath.Join(dir, "blobs"))
	s.reset()
	return s, nil
}

// Load reads the whole log once and returns the model. Call Tail to follow the
// file while the session is still writing.
func Load(dir string) (*Session, error) { return LoadWith(dir, Options{}) }

// LoadWith is Load with options.
func LoadWith(dir string, opts Options) (*Session, error) {
	return LoadContext(context.Background(), dir, opts, nil)
}

// LoadContext is LoadWith with cancellation and progress (bytes read, file size).
// It reads up to the size the file had when it was called; Tail picks up the rest.
func LoadContext(ctx context.Context, dir string, opts Options, progress func(read, total int64)) (*Session, error) {
	s, err := New(dir, opts)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	target := fi.Size()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := s.tail(ctx, progress)
		if err != nil {
			return nil, err
		}
		s.mu.RLock()
		done := s.off >= target
		s.mu.RUnlock()
		if n == 0 || done {
			break
		}
	}
	return s, nil
}

// LoadReader builds a Session from a stream of JSONL events with no file
// behind it (tests, piping). Blobs are unavailable.
func LoadReader(r io.Reader, opts Options) (*Session, error) {
	opts.fill()
	s := &Session{opts: opts}
	s.reset()
	if _, err := s.pump(context.Background(), newLineReader(bufio.NewReaderSize(r, 1<<20), 0, opts.MaxLineBytes), 0, nil); err != nil {
		return nil, err
	}
	return s, nil
}

// reset clears the model and the file position.
func (s *Session) reset() {
	s.off, s.torn, s.bad, s.badPay, s.events, s.lastSeq = 0, 0, 0, 0, 0, 0
	s.index = s.index[:0]
	s.types = map[string]int64{}
	s.rev = 0
	s.meta = metaState{}
	s.agents = map[string]*agent{}
	s.order = nil
	s.reqs = nil
	s.byID = map[string]*req{}
	s.dropped = 0
	s.tot = Totals{}
	s.cache = cacheAgg{}
	s.costAgg = newCostAgg()
	s.comp = compAgg{}
	s.anoms = nil
	s.anomTot = AnomalyTotals{}
	s.checked = 0
	s.swarm = newSwarmState()
	s.board = newBoardTracker()
	s.rl = rlState{kinds: map[string]int{}}
	s.prices = map[string]*priceInfo{}
	s.blobSize = map[string]int64{}
	s.sumMemo = nil
}

// Dir returns the session directory.
func (s *Session) Dir() string { return s.dir }

// Rev is a counter that changes whenever the model does.
func (s *Session) Rev() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}

// parsed is one decoded line with the file offset it started at.
type parsed struct {
	ev  events.Event
	off int64
}

// errLineTooLong marks a line skipped for exceeding MaxLineBytes.
var errLineTooLong = errors.New("inspect: line too long")

// lineReader yields lines with their start offsets. Unlike bufio.Scanner it
// keeps going past a long line (skipping it) and tells the caller whether a
// line was newline-terminated, which is what makes a torn tail detectable.
type lineReader struct {
	r    *bufio.Reader
	off  int64
	max  int
	buf  []byte
	over bool
}

func newLineReader(r *bufio.Reader, off int64, max int) *lineReader {
	return &lineReader{r: r, off: off, max: max}
}

// next returns the next line without its terminator. complete reports whether
// it ended with '\n'. At EOF it returns io.EOF together with any trailing
// fragment (complete == false). A line over the size limit yields
// errLineTooLong once it has been skipped in full.
func (l *lineReader) next() (line []byte, start int64, complete bool, err error) {
	start = l.off
	l.buf = l.buf[:0]
	l.over = false
	for {
		chunk, rerr := l.r.ReadSlice('\n')
		l.off += int64(len(chunk))
		if !l.over {
			if len(l.buf)+len(chunk) > l.max {
				l.over = true
				l.buf = l.buf[:0]
			} else {
				l.buf = append(l.buf, chunk...)
			}
		}
		switch {
		case rerr == nil:
			if l.over {
				return nil, start, true, errLineTooLong
			}
			b := l.buf[:len(l.buf)-1]
			if n := len(b); n > 0 && b[n-1] == '\r' {
				b = b[:n-1]
			}
			return b, start, true, nil
		case errors.Is(rerr, bufio.ErrBufferFull):
			continue
		case rerr == io.EOF:
			if l.over {
				return nil, start, false, errLineTooLong
			}
			return l.buf, start, false, io.EOF
		default:
			return nil, start, false, rerr
		}
	}
}

// parseEvent decodes one log line. Lines that are JSON but not events (no seq
// or no type) are rejected, as events.Open does when it recovers a log.
func parseEvent(line []byte) (events.Event, bool) {
	var ev events.Event
	if err := json.Unmarshal(line, &ev); err != nil || ev.Seq == 0 || ev.Type == "" {
		return ev, false
	}
	return ev, true
}

// Tail reads whatever was appended since the last call and applies it. It
// returns the number of events applied. A torn last line (the writer is mid
// line, or crashed) is left unread and picked up when it is completed. If the
// file shrank or was replaced, the model is rebuilt from the start.
func (s *Session) Tail() (int, error) { return s.tail(context.Background(), nil) }

func (s *Session) tail(ctx context.Context, progress func(read, total int64)) (int, error) {
	s.tailMu.Lock()
	defer s.tailMu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	ep := s.readEpisode()
	s.mu.Lock()
	// Truncation (events.Open cuts a torn tail on resume) or replacement: the
	// offsets we hold no longer describe this file, so start over.
	if fi.Size() < s.off || (s.fileID != nil && !os.SameFile(s.fileID, fi)) {
		reloads := s.reloads + 1
		s.reset()
		s.reloads = reloads
	}
	s.fileID = fi
	s.size = fi.Size()
	s.mtime = fi.ModTime()
	s.setEpisodeLocked(ep)
	off := s.off
	s.mu.Unlock()

	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	lr := newLineReader(bufio.NewReaderSize(f, 1<<20), off, s.opts.MaxLineBytes)
	progressFn := progress
	if progressFn != nil {
		total := fi.Size()
		progressFn = func(read, _ int64) { progress(read, total) }
	}
	return s.pump(ctx, lr, off, progressFn)
}

// pump reads lines until EOF and applies them in batches, keeping s.off at the
// end of the last consumed line. The final unterminated fragment is consumed
// only if it is a whole event (the writer flushed just before the newline).
func (s *Session) pump(ctx context.Context, lr *lineReader, consumed int64, progress func(read, total int64)) (int, error) {
	applied := 0
	var batch []parsed
	var bad, torn int64

	commit := func() {
		s.mu.Lock()
		for i := range batch {
			s.apply(&batch[i].ev, batch[i].off)
		}
		s.off = consumed
		s.bad += bad
		s.torn = torn
		s.mu.Unlock()
		applied += len(batch)
		batch = batch[:0]
		bad = 0
		if progress != nil {
			progress(consumed, 0)
		}
	}

	for {
		if len(batch) >= 2000 {
			commit()
			if err := ctx.Err(); err != nil {
				return applied, err
			}
		}
		line, start, _, rerr := lr.next()
		if errors.Is(rerr, errLineTooLong) {
			bad++
			consumed = lr.off
			continue
		}
		if rerr != nil && rerr != io.EOF {
			commit()
			return applied, rerr
		}
		if rerr == io.EOF {
			torn = 0
			switch {
			case len(bytes.TrimSpace(line)) == 0:
				consumed = lr.off
			default:
				if ev, ok := parseEvent(line); ok {
					batch = append(batch, parsed{ev, start})
					consumed = lr.off
				} else {
					torn = int64(len(line))
				}
			}
			break
		}
		consumed = lr.off
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		ev, ok := parseEvent(line)
		if !ok {
			bad++
			continue
		}
		batch = append(batch, parsed{ev, start})
	}
	commit()
	return applied, nil
}

// Log describes the file as read so far.
func (s *Session) Log() LogMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logMetaLocked()
}

func (s *Session) logMetaLocked() LogMeta {
	types := make(map[string]int64, len(s.types))
	for k, v := range s.types {
		types[k] = v
	}
	return LogMeta{
		File: filepath.Base(s.path), Events: s.events, Bytes: s.off, FileBytes: s.size, LastSeq: s.lastSeq,
		TornBytes: s.torn, BadLines: s.bad, Reloads: s.reloads, Schema: s.meta.schema, Updated: s.mtime,
		Blobs: s.blobs != nil, Types: types,
	}
}

// typeNames returns the event types seen, sorted.
func (s *Session) typeNamesLocked() []string {
	out := make([]string, 0, len(s.types))
	for k := range s.types {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ID is the session id as recorded in the log, falling back to the directory name.
func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.idLocked()
}

func (s *Session) idLocked() string {
	if s.opts.ID != "" {
		return s.opts.ID
	}
	if s.meta.id != "" {
		return s.meta.id
	}
	return filepath.Base(s.dir)
}

func (s *Session) nameLocked() string {
	if s.opts.Name != "" {
		return s.opts.Name
	}
	if s.dir != "" {
		return filepath.Base(s.dir)
	}
	return s.idLocked()
}
