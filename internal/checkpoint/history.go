package checkpoint

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// The history of the project as the checkpoints record it: a file as it was when a
// checkpoint began (ContentAt), what each checkpoint changed by itself (Changes), and
// who wrote each line (Authorship). Every read goes through the same path rules as a
// rewind: keys stay inside the root, the live file is opened without following a
// symlink, and saved content must match its checksum.

var (
	// ErrOutsideRoot is returned for a path that is not inside the store's root.
	ErrOutsideRoot = errors.New("checkpoint: the path is not inside the project root")
	// ErrNotRegular is returned for content asked of something that is not a
	// regular file (a directory, a symlink, a device).
	ErrNotRegular = errors.New("checkpoint: not a regular file")
	// ErrNotSaved is returned for content the checkpoint could not save (too
	// large, unreadable, a special file).
	ErrNotSaved = errors.New("checkpoint: the content was not saved")
	// ErrTooLarge is returned for a live file larger than the store's cap.
	ErrTooLarge = errors.New("checkpoint: the file is too large")
)

// keyOf turns a caller's path (relative to the root, or absolute inside it) into the
// manifest key, refusing anything that is not inside the root once the symlinks of
// its directory part are resolved.
func (s *Store) keyOf(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) || len(path) > maxKeyBytes {
		return "", ErrOutsideRoot
	}
	key := s.keyFor(s.absolute(path))
	// keyFor leaves a path outside the root absolute, and a ".." that leaves it in place: neither is a key. Local is
	// tested on the key itself, before any other use of it, so that what follows (resolveKey, the file system) is only
	// ever given a path below the root.
	if !filepath.IsLocal(key) || validKey(key) != nil {
		return "", ErrOutsideRoot
	}
	return key, nil
}

// boundaryLocked is the state of key when checkpoint index idx began: the earliest
// pre-image among checkpoints idx and later, or nil when none of them touched it (the
// state is then the live file). The caller holds the store lock.
func (s *Store) boundaryLocked(idx int, key string) *state {
	for _, cp := range s.cps[idx:] {
		if r := cp.index[key]; r != nil {
			st := r.Pre
			return &st
		}
	}
	return nil
}

// ContentAt returns a file's content as it was when checkpoint id began: the earliest
// pre-image among checkpoints at or after id, else the current file; exists is false
// when the file did not exist then. The first checkpoint's id gives the content before
// the session (for files the session touched). Content that was never saved, or that
// is not a regular file, is an error (ErrNotSaved, ErrNotRegular) with exists true.
func (s *Store) ContentAt(id, path string) (data []byte, exists bool, err error) {
	key, err := s.keyOf(path)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, false, fmt.Errorf("%w: %q", ErrUnknownCheckpoint, id)
	}
	want := s.boundaryLocked(idx, key)
	s.mu.Unlock()
	if want != nil {
		return s.stateContent(*want)
	}
	return s.liveContent(key)
}

// CurrentContent returns the file as it is now, read under the same rules as
// ContentAt (inside the root, no symlink followed, at most the store's cap).
func (s *Store) CurrentContent(path string) (data []byte, exists bool, err error) {
	key, err := s.keyOf(path)
	if err != nil {
		return nil, false, err
	}
	return s.liveContent(key)
}

// stateContent reads the content a recorded state names.
func (s *Store) stateContent(st state) ([]byte, bool, error) {
	switch st.Kind {
	case kAbsent:
		return nil, false, nil
	case kFile:
		b, err := s.readBlob(st.Blob, s.maxBytes)
		if err != nil {
			return nil, true, fmt.Errorf("%w: %v", ErrNotSaved, err)
		}
		if st.Sum == "" || core.HashBytes(b) != st.Sum {
			return nil, true, fmt.Errorf("%w: the saved content is corrupt", ErrNotSaved)
		}
		return b, true, nil
	case kLink, kDir:
		return nil, true, ErrNotRegular
	}
	return nil, true, fmt.Errorf("%w: %s", ErrNotSaved, st.Note)
}

// liveContent reads the current file of a key without following a symlink at its end
// and without waiting on a FIFO.
func (s *Store) liveContent(key string) ([]byte, bool, error) {
	abs, err := s.resolveKey(key)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrOutsideRoot, err)
	}
	b, err := readBounded(abs, s.maxBytes)
	switch {
	case err == nil:
		return b, true, nil
	case isMissing(err):
		return nil, false, nil
	case errors.Is(err, errUnusable) && strings.Contains(err.Error(), "larger than"):
		return nil, true, ErrTooLarge
	case errors.Is(err, errUnusable):
		return nil, true, ErrNotRegular
	}
	return nil, true, err
}

// Change is one file of a checkpoint's own change set: its status (added, modified,
// deleted) from the state when the checkpoint began to the state when the next one
// began (or now, for a file no later checkpoint touched), the lines added and removed
// (zero for binary and oversized content), and the agents that wrote it.
type Change struct {
	Path, Status   string
	Added, Removed int
	Agents         []string
	Binary         bool
}

// lineCount is the cached line count of a pair of contents.
type lineCount struct {
	added, removed int
	binary         bool
}

// maxCountCache bounds the cache of line counts.
const maxCountCache = 4096

// Changes returns the change set of one checkpoint (what was written while it was
// current), not the cumulative diff of Diff. Files that ended as they began are left
// out. Paths are sorted.
func (s *Store) Changes(id string) ([]Change, error) {
	type item struct {
		key    string
		from   state
		to     *state
		agents []string
	}
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrUnknownCheckpoint, id)
	}
	var items []item
	for _, r := range s.cps[idx].files {
		it := item{key: r.Path, from: r.Pre, agents: slices.Clone(r.Agents)}
		if idx+1 < len(s.cps) {
			it.to = s.boundaryLocked(idx+1, r.Path)
		}
		items = append(items, it)
	}
	s.mu.Unlock()

	out := []Change{}
	for _, it := range items {
		var to state
		var toData []byte
		if it.to != nil {
			to = *it.to
		} else {
			abs, err := s.resolveKey(it.key)
			if err != nil {
				continue // never read through a path that leads out of the project
			}
			to, toData = s.capture(abs)
		}
		if sameState(it.from, to) {
			continue
		}
		c := Change{Path: it.key, Status: string(DiffModified), Agents: it.agents}
		sort.Strings(c.Agents)
		switch {
		case it.from.Kind == kAbsent:
			c.Status = string(DiffAdded)
		case to.Kind == kAbsent:
			c.Status = string(DiffDeleted)
		}
		n := s.countLines(it.from, to, toData)
		c.Added, c.Removed, c.Binary = n.added, n.removed, n.binary
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// countLines counts the lines added and removed from one state to another. Only
// saved regular files (or nothing) have lines; toData is the content of a live `to`
// state when the caller has it. Results are cached by the pair of checksums.
func (s *Store) countLines(from, to state, toData []byte) lineCount {
	textual := func(st state) bool { return st.Kind == kAbsent || st.Kind == kFile }
	if !textual(from) || !textual(to) {
		return lineCount{}
	}
	key := [2]core.Hash{from.Sum, to.Sum}
	s.mu.Lock()
	if n, ok := s.counts[key]; ok {
		s.mu.Unlock()
		return n
	}
	s.mu.Unlock()
	load := func(st state, have []byte) ([]byte, bool) {
		switch {
		case st.Kind == kAbsent:
			return nil, true
		case have != nil || st.Size == 0:
			return have, true
		case st.Size > maxDiffBytes:
			return nil, false
		}
		b, err := s.readBlob(st.Blob, maxDiffBytes)
		return b, err == nil
	}
	a, okA := load(from, nil)
	b, okB := load(to, toData)
	var n lineCount
	switch {
	case !okA || !okB || len(a) > maxDiffBytes || len(b) > maxDiffBytes:
		return lineCount{} // not cached: a blob may be readable later
	case isBinary(a) || isBinary(b):
		n.binary = true
	default:
		for _, op := range editScript(splitLines(string(a)), splitLines(string(b))) {
			switch op {
			case 'i':
				n.added++
			case 'd':
				n.removed++
			}
		}
	}
	s.mu.Lock()
	if s.counts == nil || len(s.counts) >= maxCountCache {
		s.counts = map[[2]core.Hash]lineCount{}
	}
	s.counts[key] = n
	s.mu.Unlock()
	return n
}

// ---- line diffs, shared with the web workspace -----------------------------------

// HunkLine is one line of a hunk: Op is ' ' (context), '-' (only in the old text) or
// '+' (only in the new text); Text is the line without its line terminator.
type HunkLine struct {
	Op   byte
	Text string
}

// Hunk is one hunk of a line diff with three lines of context, numbered as a unified
// diff numbers them (an empty side starts at the line before it).
type Hunk struct {
	OldStart, OldLines, NewStart, NewLines int
	Lines                                  []HunkLine
}

// LineDiff is the difference of two texts as hunks, with the lines added and removed.
// Truncated says the hunks stop early because their text passed the output bound
// (256 KiB, as /diff's).
type LineDiff struct {
	Hunks          []Hunk
	Added, Removed int
	Truncated      bool
}

// DiffLines compares two texts line by line with the algorithm and hunk rules of Diff
// (three lines of context; changes six lines apart or closer share a hunk). Callers
// bound the input (Diff refuses more than 2 MiB a side) and check for binary content
// themselves (IsBinary).
func DiffLines(oldText, newText []byte) LineDiff {
	a, b := splitLines(string(oldText)), splitLines(string(newText))
	script := editScript(a, b)
	var d LineDiff
	for _, op := range script {
		switch op {
		case 'i':
			d.Added++
		case 'd':
			d.Removed++
		}
	}
	blks := changeBlocks(script)
	size := 0
	trim := func(l string) string { return strings.TrimSuffix(strings.TrimSuffix(l, "\n"), "\r") }
	i := 0
	for i < len(blks) {
		j := i
		for j+1 < len(blks) && blks[j+1].a0-blks[j].a1 <= 2*diffContext {
			j++
		}
		first, last := blks[i], blks[j]
		a0 := max(0, first.a0-diffContext)
		a1 := min(len(a), last.a1+diffContext)
		b0 := a0 + (first.b0 - first.a0)
		b1 := a1 + (last.b1 - last.a1)
		h := Hunk{OldStart: hunkStart(a0, a1-a0), OldLines: a1 - a0, NewStart: hunkStart(b0, b1-b0), NewLines: b1 - b0}
		add := func(op byte, line string) {
			h.Lines = append(h.Lines, HunkLine{Op: op, Text: trim(line)})
			size += len(line) + 1
		}
		pos := a0
		for k := i; k <= j; k++ {
			blk := blks[k]
			for ; pos < blk.a0; pos++ {
				add(' ', a[pos])
			}
			for x := blk.a0; x < blk.a1; x++ {
				add('-', a[x])
			}
			for y := blk.b0; y < blk.b1; y++ {
				add('+', b[y])
			}
			pos = blk.a1
		}
		for ; pos < a1; pos++ {
			add(' ', a[pos])
		}
		d.Hunks = append(d.Hunks, h)
		i = j + 1
		if size > maxDiffOutput {
			d.Truncated = i < len(blks)
			break
		}
	}
	return d
}

// hunkStart is the start line of a hunk side as a unified diff writes it.
func hunkStart(start0, count int) int {
	if count == 0 {
		return start0
	}
	return start0 + 1
}

// IsBinary uses git's heuristic: a NUL byte in the first 8000 bytes.
func IsBinary(b []byte) bool { return isBinary(b) }

// UnifiedDiff renders the difference of two texts as /diff does (three lines of
// context, at most 256 KiB, "[diff truncated]" past it), with the lines added and
// removed. oldName and newName are the header names ("a/x", "b/x", "/dev/null").
func UnifiedDiff(oldName, newName, oldText, newText string) (out string, added, removed int) {
	return unifiedDiff(oldName, newName, oldText, newText)
}

// ---- authorship -------------------------------------------------------------------

// LineAuthor says who wrote one line: the agent and the checkpoint the write was made
// in. An empty Agent means the line was there before the session, or that no recorded
// write explains it.
type LineAuthor struct {
	Agent, Checkpoint string
}

// Authorship is a file's content at a point in time with the author of each line.
// Exact is true when every change since the session began was a journaled write with
// its content kept, so that every line is attributed for certain; otherwise lines the
// journal cannot explain have no author and Exact is false. Binary content has no
// lines. Exists is false for a file that did not exist at that point.
type Authorship struct {
	Content []byte
	Exists  bool
	Binary  bool
	Exact   bool
	Lines   []LineAuthor
}

// authorRec is the part of one checkpoint's record of a path that authorship reads.
type authorRec struct {
	cp     string
	pre    state
	writes []jwrite
	lost   int
	agents []string
}

// AuthorshipAt returns the content of path when checkpoint id began (id "" means the
// file as it is now) and who wrote each of its lines, by replaying the write journal
// over the pre-image of the first checkpoint that touched the file. Content over the
// diff bound (2 MiB) is returned without authors (Exact false).
func (s *Store) AuthorshipAt(id, path string) (Authorship, error) {
	key, err := s.keyOf(path)
	if err != nil {
		return Authorship{}, err
	}
	s.mu.Lock()
	cut := len(s.cps)
	if id != "" {
		if cut = s.indexLocked(id); cut < 0 {
			s.mu.Unlock()
			return Authorship{}, fmt.Errorf("%w: %q", ErrUnknownCheckpoint, id)
		}
	}
	var recs []authorRec
	var target *state
	for i, cp := range s.cps {
		r := cp.index[key]
		if r == nil {
			continue
		}
		if i >= cut {
			if target == nil {
				st := r.Pre
				target = &st
			}
			continue
		}
		recs = append(recs, authorRec{cp: cp.ID, pre: r.Pre, writes: slices.Clone(r.Writes), lost: r.WritesLost, agents: slices.Clone(r.Agents)})
	}
	s.mu.Unlock()

	var out Authorship
	if target != nil {
		out.Content, out.Exists, err = s.stateContent(*target)
	} else {
		out.Content, out.Exists, err = s.liveContent(key)
	}
	if err != nil {
		return out, err
	}
	if !out.Exists {
		out.Exact = true
		return out, nil
	}
	if isBinary(out.Content) {
		out.Binary, out.Exact = true, true
		return out, nil
	}
	lines := splitLines(string(out.Content))
	out.Lines = make([]LineAuthor, len(lines))
	if len(out.Content) > maxDiffBytes {
		return out, nil
	}
	if len(recs) == 0 {
		out.Exact = true // nothing in the session wrote it: every line predates it
		return out, nil
	}
	r := replay{s: s, exact: true}
	if !r.run(recs) {
		return out, nil // a content of the history cannot be read: no authors rather than wrong ones
	}
	r.step(lines, r.pending)
	out.Lines, out.Exact = r.who, r.exact
	return out, nil
}

// replay carries the attribution of a file's lines from one known content to the next.
type replay struct {
	s     *Store
	lines []string
	who   []LineAuthor
	sum   core.Hash
	known bool // lines holds a known content
	exact bool
	// pending is who is assumed to have made the change up to the next known content:
	// the writer of an entry whose content was not kept, or the last agent of a
	// record that has no journal (a manifest older than the journal). Lines it is
	// given make the result inexact.
	pending LineAuthor
}

// run replays the records of one path in checkpoint order; false when a content of
// the history cannot be read.
func (r *replay) run(recs []authorRec) bool {
	for i, rec := range recs {
		pre, ok := r.content(rec.pre)
		switch {
		case !ok:
			return false
		case i == 0:
			r.lines, r.who, r.sum, r.known = pre, make([]LineAuthor, len(pre)), rec.pre.Sum, true
		case !r.known || rec.pre.Sum != r.sum:
			// changed between the checkpoints by someone who does not record
			r.exact = false
			r.step(pre, r.pending)
			r.sum, r.known = rec.pre.Sum, true
		}
		r.pending = LineAuthor{}
		if rec.lost > 0 {
			r.exact = false
		}
		if len(rec.writes) == 0 && len(rec.agents) > 0 {
			r.exact = false
			r.pending = LineAuthor{Agent: rec.agents[len(rec.agents)-1], Checkpoint: rec.cp}
		}
		for _, w := range rec.writes {
			if w.Gap {
				r.exact = false
			}
			next, ok := r.written(w)
			if !ok {
				r.exact, r.known = false, false
				r.pending = LineAuthor{Agent: w.Agent, Checkpoint: rec.cp}
				continue
			}
			r.step(next, LineAuthor{Agent: w.Agent, Checkpoint: rec.cp})
			r.sum, r.known, r.pending = w.Sum, true, LineAuthor{}
		}
	}
	return true
}

// written returns the lines a journaled write left, false when the entry does not
// say (content not kept, unreadable, or not text).
func (r *replay) written(w jwrite) ([]string, bool) {
	switch {
	case w.Kind == kAbsent:
		return nil, true
	case !w.known():
		return nil, false
	}
	b, err := r.s.readBlob(w.Blob, maxJournalBytes)
	if err != nil || core.HashBytes(b) != w.Sum || isBinary(b) {
		return nil, false
	}
	return splitLines(string(b)), true
}

// content returns the lines of a recorded state, false when they cannot be read.
func (r *replay) content(st state) ([]string, bool) {
	switch st.Kind {
	case kAbsent:
		return nil, true
	case kFile:
		if st.Size > maxDiffBytes {
			return nil, false
		}
		b, err := r.s.readBlob(st.Blob, maxDiffBytes)
		if err != nil || core.HashBytes(b) != st.Sum || isBinary(b) {
			return nil, false
		}
		return splitLines(string(b)), true
	}
	return nil, false
}

// step moves to the next content: kept lines keep their author, new lines get by. A
// change (lines added or removed) that no agent accounts for (by is empty) makes the
// result inexact.
func (r *replay) step(next []string, by LineAuthor) {
	script := editScript(r.lines, next)
	who := make([]LineAuthor, 0, len(next))
	ai := 0
	changed := false
	for _, op := range script {
		switch op {
		case 'e':
			who = append(who, r.who[ai])
			ai++
		case 'd':
			ai++
			changed = true
		case 'i':
			who = append(who, by)
			changed = true
		}
	}
	if changed && by.Agent == "" {
		r.exact = false
	}
	r.lines, r.who = next, who
}
