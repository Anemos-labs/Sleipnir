package tools

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

// Truncate shortens s to at most max characters, keeping the head and the tail
// (errors and summaries usually live at the end of command output) and marking
// the elision. It reports whether anything was cut.
func Truncate(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	head := max * 6 / 10
	tail := max - head
	// Cut on line boundaries when that costs little.
	if i := strings.LastIndexByte(s[:head], '\n'); i > head*8/10 {
		head = i
	}
	if i := strings.IndexByte(s[len(s)-tail:], '\n'); i >= 0 && i < tail/5 {
		tail -= i + 1
	}
	cut := len(s) - head - tail
	return s[:head] + fmt.Sprintf("\n… [%d chars elided] …\n", cut) + s[len(s)-tail:], true
}

// Finish applies the output limit to text. When it truncates, the complete
// text is stored in the blob store and a recall handle is attached so the
// model can page through what was cut instead of re-running the tool.
func (e *Env) Finish(text string, isErr bool) *Result {
	res := &Result{IsError: isErr}
	cut, truncated := Truncate(text, e.Limits.MaxOutputChars)
	res.Text = cut
	if !truncated {
		return res
	}
	res.Truncated = true
	if e.Blobs != nil {
		if h, err := e.Blobs.Put([]byte(text)); err == nil {
			res.FullRef, res.FullChars = h, len(text)
			res.Handle = e.Handles.Add(h, len(text))
			res.Text += fmt.Sprintf("\n[full output saved as %s (%d chars); use recall to read a range]", res.Handle, len(text))
		}
	}
	return res
}

// Handles maps short recall handles ("out_0123456789abcdef") to blobs. It is
// shared by a session so any agent can page through output another agent
// produced.
//
// A handle is "out_" plus the first 16 hex characters (64 bits) of the blob's
// hash. That is short enough for a model to copy and wide enough that two
// different outputs practically never share one, but "practically never" is not
// a guarantee the table can lean on: it is shared by every agent, an agent that
// can influence what its tools print can grind for a prefix, and a collision
// would make recall(handle) answer with another agent's output. So Add never
// trusts the width: when a different blob already holds the id, the newcomer's
// id is lengthened, four hex characters at a time, until it is free. A handle
// that was issued therefore keeps resolving to its own blob for the life of the
// table, and the ids depend only on the hashes and the order of the calls
// (never on time or map order), so a replayed session mints the same ones.
type Handles struct {
	mu sync.RWMutex
	m  map[string]handle
}

type handle struct {
	ref core.Hash
	len int
}

const (
	handlePrefix  = "out_"
	handleMinHex  = 16 // 64 bits of the hash
	handleHexStep = 4  // how much a colliding newcomer's id grows per attempt
)

// NewHandles returns an empty table.
func NewHandles() *Handles { return &Handles{m: map[string]handle{}} }

// Add registers a blob and returns its handle. Adding the same blob again
// returns the same handle; a different blob never receives (or replaces) a
// handle that is already taken.
func (h *Handles) Add(ref core.Hash, length int) string {
	digits := string(ref)
	h.mu.Lock()
	defer h.mu.Unlock()
	for n := handleMinHex; ; n += handleHexStep {
		n = min(n, len(digits))
		id := handlePrefix + digits[:n]
		cur, taken := h.m[id]
		if !taken {
			h.m[id] = handle{ref: ref, len: length}
			return id
		}
		if cur.ref == ref {
			return id
		}
		if n == len(digits) {
			break
		}
	}
	// Not reachable for real hashes: an id that spells a whole hash can only be
	// held by that hash. Arbitrary input (a Blobs implementation with odd
	// hashes) still must never overwrite, so fall back to numbering.
	for i := 2; ; i++ {
		id := handlePrefix + digits + "_" + strconv.Itoa(i)
		cur, taken := h.m[id]
		if !taken {
			h.m[id] = handle{ref: ref, len: length}
			return id
		}
		if cur.ref == ref {
			return id
		}
	}
}

// Resolve returns the blob behind a handle. Whitespace around the handle (a
// model copying it out of a sentence) is ignored; anything else must match
// exactly: there is no prefix matching, so a truncated handle is unknown rather
// than possibly someone else's.
func (h *Handles) Resolve(id string) (core.Hash, int, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	x, ok := h.m[strings.TrimSpace(id)]
	return x.ref, x.len, ok
}

// FileState tracks what each agent last saw of each file, across agents.
//
// It is what makes concurrent editing safe without locks: an edit is only
// accepted if the agent's last read of the file matches the file's current
// content (or the agent itself wrote the current content). Otherwise someone
// else changed it in between and the agent must re-read before editing.
type FileState struct {
	mu    sync.Mutex
	files map[string]*fileEntry
}

type fileEntry struct {
	// current is the hash of the last content any agent wrote or read.
	current core.Hash
	// lastWriter is who produced current, when known.
	lastWriter string
	wroteAt    time.Time
	// seen maps agent -> hash of the content it last read or wrote.
	seen map[string]core.Hash
}

// NewFileState returns an empty tracker.
func NewFileState() *FileState { return &FileState{files: map[string]*fileEntry{}} }

func (f *FileState) entry(path string) *fileEntry {
	e, ok := f.files[path]
	if !ok {
		e = &fileEntry{seen: map[string]core.Hash{}}
		f.files[path] = e
	}
	return e
}

// RecordRead notes that agent has seen content of path.
func (f *FileState) RecordRead(agent, path string, content []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.entry(path)
	h := core.HashBytes(content)
	e.seen[agent] = h
	if e.current == "" {
		e.current = h
	}
}

// RecordWrite notes that agent wrote content to path; the writer has, by
// definition, seen the new content.
func (f *FileState) RecordWrite(agent, path string, content []byte, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.entry(path)
	h := core.HashBytes(content)
	e.current, e.lastWriter, e.wroteAt = h, agent, now
	e.seen[agent] = h
}

// CheckFresh verifies agent may modify path whose content on disk is current.
// requireRead demands that the agent has read the file at all.
func (f *FileState) CheckFresh(agent, path string, current []byte, requireRead bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.entry(path)
	h := core.HashBytes(current)
	seen, ok := e.seen[agent]
	switch {
	case !ok && requireRead:
		return fmt.Errorf("%s has not been read by you yet; read it before modifying it", path)
	case !ok:
		return nil
	case seen == h:
		return nil
	}
	who := "another process"
	if e.lastWriter != "" && e.lastWriter != agent {
		who = "agent " + e.lastWriter
	}
	return fmt.Errorf("%s changed since you last read it (modified by %s); read it again before editing", path, who)
}

// Discard is a no-op emitter reference used to keep imports honest in tests.
var _ events.Emitter = events.Discard{}
