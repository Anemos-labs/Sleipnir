package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// The undo store. A person's restore or edit through the store (RestoreWithUndo,
// WriteFile) first saves the state of every path it is about to write, in a capture of
// its own under <dir>/undo: content in the blob store, as a checkpoint saves it. After
// the operation the capture is sealed with a fingerprint of what the operation left, so
// that RestoreUndo can tell the operation's result from a later edit and refuses to
// overwrite the latter, as Restore refuses to overwrite someone else's work. A capture
// is separate from the checkpoints because a restore rewinds and drops checkpoints:
// what it would have to keep there is exactly what it removes.
//
// A restore's capture also keeps the checkpoints the restore rewrote, as they were
// before it: when its undo finds them as the restore left them, it puts them back, so
// that the store is as it was before the restore. Otherwise the undo is recorded like
// any other write, in the current checkpoint, so that a later rewind can undo it too.
//
// Captures are files written atomically; at most maxUndos are kept, oldest dropped.

const (
	maxUndos       = 64
	undoVersion    = 1
	maxUndoFiles   = 100_000
	maxUndoHistory = 4096 // checkpoints a capture may keep
)

var (
	// ErrUnknownUndo is returned for an undo id that does not exist (never taken,
	// already used, or dropped as one of the oldest).
	ErrUnknownUndo = errors.New("checkpoint: unknown undo")
	// ErrUndoConflict is returned when an undo would overwrite what changed after the
	// operation it undoes; nothing was written.
	ErrUndoConflict = errors.New("checkpoint: the files changed since; nothing was undone")
	// ErrChanged is returned by WriteFile when the file is not the content the caller
	// expected; nothing was written.
	ErrChanged = errors.New("checkpoint: the file changed")
)

var undoFileRE = regexp.MustCompile(`^u_(\d+)\.json$`)

// PersonAgent is the name the records give to what a person did through the store
// (an undo, a reverted hunk written with WriteFile by a caller that names no one else).
const PersonAgent = "person"

// undoRec is one path of a capture: the state to put back (Pre), and the state the
// operation left (Post, set when the capture is sealed).
type undoRec struct {
	Path    string   `json:"path"`
	Pre     state    `json:"pre"`
	Post    *state   `json:"post,omitempty"`
	NewDirs []string `json:"new_dirs,omitempty"`
}

// undoDoc is one capture on disk.
type undoDoc struct {
	V      int        `json:"v"`
	ID     string     `json:"id"`
	Label  string     `json:"label"`
	Time   time.Time  `json:"time"`
	Sealed time.Time  `json:"sealed,omitempty"`
	Files  []*undoRec `json:"files"`
	// Before are the checkpoints a restore rewrote, from the one it rewound to, as
	// they were before it; After is the fingerprint of what it left of them.
	Before []cpDoc `json:"before,omitempty"`
	After  string  `json:"after,omitempty"`
}

// UndoInfo describes one capture.
type UndoInfo struct {
	ID, Label string
	Time      time.Time
	Files     []string
}

// undoDir is where captures live.
func (s *Store) undoDir() string { return filepath.Join(s.dir, "undo") }

// CaptureUndo saves the current state of paths in the undo store and returns its id
// (taken before a restore or a revert). paths are relative to the root or absolute
// inside it; anything else is refused. Seal it after the operation (SealUndo) so that
// RestoreUndo can tell the operation's result from a later change.
func (s *Store) CaptureUndo(paths []string) (string, error) {
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		k, err := s.keyOf(p)
		if err != nil {
			return "", fmt.Errorf("%w: %.200q", err, p)
		}
		keys = append(keys, k)
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	doc, err := s.captureUndo("capture", keys)
	if err != nil {
		return "", err
	}
	return doc.ID, nil
}

// captureUndo saves the current state of keys as a new capture. The caller holds the
// gate.
func (s *Store) captureUndo(label string, keys []string) (*undoDoc, error) {
	if len(keys) > maxUndoFiles {
		return nil, fmt.Errorf("checkpoint: too many files to capture (%d)", len(keys))
	}
	doc := &undoDoc{V: undoVersion, Label: cleanLabel(label), Files: []*undoRec{}}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		abs, err := s.resolveKey(key)
		if err != nil {
			return nil, fmt.Errorf("checkpoint: %.200q: %v", key, err)
		}
		st, data := s.capture(abs)
		if st.Kind == kFile {
			h, err := s.blobs.Put(data)
			if err != nil {
				return nil, fmt.Errorf("checkpoint: saving %s: %w", key, err)
			}
			st.Blob = h
		}
		rec := &undoRec{Path: key, Pre: st}
		if st.Kind == kAbsent {
			rec.NewDirs = s.missingParents(abs)
		}
		doc.Files = append(doc.Files, rec)
	}
	s.undoMu.Lock()
	defer s.undoMu.Unlock()
	s.mu.Lock()
	doc.Time = s.now()
	s.mu.Unlock()
	id, err := s.nextUndoIDLocked()
	if err != nil {
		return nil, err
	}
	doc.ID = id
	if err := s.saveUndoLocked(doc); err != nil {
		return nil, err
	}
	s.pruneUndosLocked()
	return doc, nil
}

// SealUndo fingerprints what the operation a capture was taken for left at each path,
// so that RestoreUndo can tell it from a later change. When keep is not nil, the paths
// it does not list (the operation did not write them) are dropped from the capture; a
// capture left with nothing is removed.
func (s *Store) SealUndo(id string, keep []string) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	return s.sealUndo(id, keep, nil)
}

// sealUndo is SealUndo for a caller that holds the gate; mutate may change the
// document before it is saved.
func (s *Store) sealUndo(id string, keep []string, mutate func(*undoDoc)) error {
	s.undoMu.Lock()
	defer s.undoMu.Unlock()
	doc, err := s.loadUndoLocked(id)
	if err != nil {
		return err
	}
	var want map[string]bool
	if keep != nil {
		want = map[string]bool{}
		for _, p := range keep {
			if k, err := s.keyOf(p); err == nil {
				want[k] = true
			}
		}
	}
	files := doc.Files[:0]
	for _, rec := range doc.Files {
		if want != nil && !want[rec.Path] {
			continue
		}
		if abs, err := s.resolveKey(rec.Path); err == nil {
			st, _ := s.capture(abs)
			rec.Post = &st
		}
		files = append(files, rec)
	}
	doc.Files = files
	if len(doc.Files) == 0 {
		return s.dropUndoLocked(id)
	}
	s.mu.Lock()
	doc.Sealed = s.now()
	s.mu.Unlock()
	if mutate != nil {
		mutate(doc)
	}
	return s.saveUndoLocked(doc)
}

// DropUndo forgets a capture (its operation can no longer be undone).
func (s *Store) DropUndo(id string) error {
	s.undoMu.Lock()
	defer s.undoMu.Unlock()
	return s.dropUndoLocked(id)
}

// Undos lists the captures, oldest first.
func (s *Store) Undos() []UndoInfo {
	s.undoMu.Lock()
	defer s.undoMu.Unlock()
	var out []UndoInfo
	for _, seq := range s.undoSeqsLocked() {
		doc, err := s.loadUndoLocked(undoID(seq))
		if err != nil {
			continue
		}
		info := UndoInfo{ID: doc.ID, Label: doc.Label, Time: doc.Time}
		for _, rec := range doc.Files {
			info.Files = append(info.Files, rec.Path)
		}
		sort.Strings(info.Files)
		out = append(out, info)
	}
	return out
}

// RestoreUndo puts back the state an undo capture saved, with Restore's conflict rules:
// a path that changed after the operation the capture was taken for is a conflict,
// and then nothing is written (ErrUndoConflict, with the report saying which paths).
// The capture is used up when the undo succeeds. The undo is recorded in the current
// checkpoint as a write of PersonAgent's, unless it puts back the checkpoints of a
// restore it undoes (see the undo store's description).
func (s *Store) RestoreUndo(id string) (RestoreReport, error) {
	agent := PersonAgent
	var changed []string
	defer func() { s.notify(changed...) }()
	s.gate.Lock()
	defer s.gate.Unlock()
	s.undoMu.Lock()
	doc, err := s.loadUndoLocked(id)
	s.undoMu.Unlock()
	if err != nil {
		return RestoreReport{}, err
	}
	rep := RestoreReport{ID: doc.ID, Files: []FileResult{}}
	tasks := make([]*task, 0, len(doc.Files))
	for _, rec := range doc.Files {
		it := &planned{key: rec.Path, want: rec.Pre, newDirs: slices.Clone(rec.NewDirs),
			last: fileRec{Path: rec.Path, Post: rec.Post, Last: doc.Sealed}}
		if rec.Post == nil {
			it.last.Last = doc.Time
		}
		tasks = append(tasks, s.evaluate(it, RestoreOpts{}))
	}
	bad := false
	for _, t := range tasks {
		switch t.res.Outcome {
		case OutcomeConflict, OutcomeUnrestorable:
			bad = true
		}
	}
	if bad {
		for _, t := range tasks {
			rep.Files = append(rep.Files, t.res)
		}
		sort.SliceStable(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })
		return rep, ErrUndoConflict
	}

	// The checkpoints of an undone restore come back when they are as it left them;
	// otherwise the undo is recorded as a write of agent's.
	reinstate := s.restoredUntouched(doc)
	if !reinstate {
		for _, t := range tasks {
			if t.res.Outcome != OutcomePlanned {
				continue
			}
			if abs, err := s.resolveKey(t.it.key); err == nil {
				if err := s.snapshot(agent, abs); err != nil {
					return rep, err
				}
			}
		}
	}
	s.applyTasks(tasks)
	for _, t := range tasks {
		rep.Files = append(rep.Files, t.res)
	}
	rep.Files = append(rep.Files, s.removeCreatedDirs(tasks, false)...)
	sort.SliceStable(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })

	if reinstate {
		changed = s.reinstate(doc)
	} else {
		for _, t := range tasks {
			if t.res.Outcome == OutcomeDone {
				if abs, err := s.resolveKey(t.it.key); err == nil {
					s.after(agent, abs)
				}
			}
		}
		s.mu.Lock()
		if n := len(s.cps); n > 0 {
			changed = []string{s.cps[n-1].ID}
		}
		s.mu.Unlock()
	}
	if rep.OK() {
		s.undoMu.Lock()
		_ = s.dropUndoLocked(id)
		s.undoMu.Unlock()
		return rep, nil
	}
	return rep, rep.Err()
}

// applyTasks performs planned tasks as Restore does: deletions deepest first, then
// restorations shallowest first, within the restore budget. The caller holds the
// exclusive gate.
func (s *Store) applyTasks(tasks []*task) {
	var dels, puts []*task
	for _, t := range tasks {
		if t.res.Outcome == OutcomePlanned {
			if t.action == ActionDelete {
				dels = append(dels, t)
			} else {
				puts = append(puts, t)
			}
		}
	}
	sort.SliceStable(dels, func(i, j int) bool { return depth(dels[i].it.key) > depth(dels[j].it.key) })
	sort.SliceStable(puts, func(i, j int) bool { return depth(puts[i].it.key) < depth(puts[j].it.key) })
	budget := maxRestoreBytes
	for _, t := range append(dels, puts...) {
		if err := s.apply(t, &budget); err != nil {
			t.res.Outcome, t.res.Detail = OutcomeFailed, err.Error()
		} else {
			t.res.Outcome = OutcomeDone
		}
	}
}

// RestoreWithUndo is Restore (with default options: every file, no force) that first
// captures what it is about to overwrite, so that RestoreUndo can put it back. The
// capture is taken, the files written and the capture sealed while the store is held
// exclusively, so no recorded write can come in between. It returns the capture's id
// ("" when nothing was written).
func (s *Store) RestoreWithUndo(id string) (RestoreReport, string, error) {
	var changed []string
	defer func() { s.notify(changed...) }()
	s.gate.Lock()
	defer s.gate.Unlock()
	var doc *undoDoc
	var history []cpDoc
	hook := restoreHook{
		before: func(idx int, tasks []*task) error {
			var keys []string
			for _, t := range tasks {
				if t.res.Outcome == OutcomePlanned {
					keys = append(keys, t.it.key)
				}
			}
			if len(keys) == 0 {
				return nil
			}
			s.mu.Lock()
			history = s.docsLocked(idx)
			label := "restore " + s.cps[idx].ID
			s.mu.Unlock()
			var err error
			doc, err = s.captureUndo(label, keys)
			return err
		},
	}
	rep, ch, err := s.restore(id, RestoreOpts{}, hook)
	changed = ch
	if doc == nil {
		return rep, "", err
	}
	var done []string
	for _, f := range rep.Files {
		if f.Outcome == OutcomeDone && f.Action != ActionRmdir {
			done = append(done, f.Path)
		}
	}
	if len(done) == 0 {
		_ = s.DropUndo(doc.ID)
		return rep, "", err
	}
	after := ""
	if len(history) > 0 {
		s.mu.Lock()
		after = s.fingerprintLocked(history[0].Seq)
		s.mu.Unlock()
	}
	if serr := s.sealUndo(doc.ID, done, func(d *undoDoc) {
		if len(history) <= maxUndoHistory {
			d.Before, d.After = history, after
		}
	}); serr != nil {
		return rep, "", errors.Join(err, serr)
	}
	return rep, doc.ID, err
}

// WriteFile writes data to path as agent (a person's edit through the store, such as a
// reverted hunk) when the file's content is still expect (a checksum), and returns the
// id of an undo capture of what it replaced. The write is recorded like an agent's
// (Before, the write, After) and is atomic; the file keeps its mode. While it runs no
// recorded write can start. A path outside the root, or a file that is not the
// expected one, is refused with nothing written (ErrOutsideRoot, ErrChanged).
func (s *Store) WriteFile(agent, path string, data []byte, expect core.Hash) (string, error) {
	key, err := s.keyOf(path)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > s.maxBytes {
		return "", ErrTooLarge
	}
	var changed []string
	defer func() { s.notify(changed...) }()
	s.gate.Lock()
	defer s.gate.Unlock()
	abs, err := s.resolveKey(key)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOutsideRoot, err)
	}
	cur, _ := s.capture(abs)
	if cur.Kind != kFile || cur.Sum != expect {
		return "", ErrChanged
	}
	doc, err := s.captureUndo("write "+key, []string{key})
	if err != nil {
		return "", err
	}
	if err := s.snapshot(agent, abs); err != nil {
		_ = s.DropUndo(doc.ID)
		return "", err
	}
	if err := writeFileAtomic(abs, data, goMode(cur.Mode), cur.Owner); err != nil {
		_ = s.DropUndo(doc.ID)
		return "", fmt.Errorf("checkpoint: cannot write %s: %s", key, reason(err))
	}
	s.after(agent, abs)
	if err := s.sealUndo(doc.ID, nil, nil); err != nil {
		return "", err
	}
	s.mu.Lock()
	if n := len(s.cps); n > 0 {
		changed = []string{s.cps[n-1].ID}
	}
	s.mu.Unlock()
	return doc.ID, nil
}

// docsLocked copies the checkpoints from index idx on as their manifests hold them.
func (s *Store) docsLocked(idx int) []cpDoc {
	var out []cpDoc
	for _, cp := range s.cps[idx:] {
		b, err := json.Marshal(cpDoc{V: manifestVersion, ID: cp.ID, Seq: cp.Seq, Label: cp.Label, Time: cp.Time, Files: cp.files})
		if err != nil {
			return nil
		}
		var d cpDoc
		if json.Unmarshal(b, &d) != nil {
			return nil
		}
		out = append(out, d)
	}
	return out
}

// fingerprintLocked hashes the manifests of the checkpoints from sequence number seq
// on, as they are now.
func (s *Store) fingerprintLocked(seq int) string {
	h := sha256.New()
	for _, cp := range s.cps {
		if cp.Seq < seq {
			continue
		}
		b, _ := json.Marshal(cpDoc{V: manifestVersion, ID: cp.ID, Seq: cp.Seq, Label: cp.Label, Time: cp.Time, Files: cp.files})
		h.Write(b)
		h.Write([]byte{0})
	}
	fmt.Fprintf(h, "next=%d", s.next)
	return hex.EncodeToString(h.Sum(nil))
}

// restoredUntouched reports whether a capture holds the checkpoints of a restore and
// the store still holds them as the restore left them.
func (s *Store) restoredUntouched(doc *undoDoc) bool {
	if len(doc.Before) == 0 || doc.After == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fingerprintLocked(doc.Before[0].Seq) == doc.After
}

// reinstate puts a restore's checkpoints back as they were before it and returns the
// ids of the checkpoints it wrote. The caller holds the exclusive gate.
func (s *Store) reinstate(doc *undoDoc) []string {
	from := doc.Before[0].Seq
	var back []*checkpoint
	for _, d := range doc.Before {
		cp := &checkpoint{ID: d.ID, Seq: d.Seq, Label: cleanLabel(d.Label), Time: d.Time, index: map[string]*fileRec{}}
		for _, r := range d.Files {
			if r == nil || s.validRecord(r) != nil {
				continue
			}
			if _, dup := cp.index[r.Path]; !dup {
				cp.add(r)
			}
		}
		back = append(back, cp)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.cps[:0:0]
	for _, cp := range s.cps {
		if cp.Seq < from {
			keep = append(keep, cp)
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, cp.ID+".json"))
	}
	s.cps = append(keep, back...)
	var ids []string
	for _, cp := range back {
		_ = s.persistLocked(cp)
		if cp.Seq >= s.next {
			s.next = cp.Seq + 1
		}
		ids = append(ids, cp.ID)
	}
	_ = s.persistMetaLocked()
	return ids
}

// ---- the capture files --------------------------------------------------------------

// undoID formats a capture's sequence number.
func undoID(seq int) string { return fmt.Sprintf("u_%04d", seq) }

// undoSeq parses a capture id ("u_0003", "u_3").
func undoSeq(id string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(id), "u_"))
	if err != nil || n <= 0 || n > maxSeq || !strings.HasPrefix(strings.TrimSpace(id), "u_") {
		return 0, false
	}
	return n, true
}

// undoSeqsLocked lists the sequence numbers of the captures on disk, ascending.
func (s *Store) undoSeqsLocked() []int {
	entries, err := os.ReadDir(s.undoDir())
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		m := undoFileRE.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n <= maxSeq {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// nextUndoIDLocked allocates the next capture id: one past the highest on disk and
// the counter, so an id is never reused while its file could still exist.
func (s *Store) nextUndoIDLocked() (string, error) {
	if err := os.MkdirAll(s.undoDir(), 0o700); err != nil {
		return "", fmt.Errorf("checkpoint: %w", err)
	}
	next := 1
	if b, err := readBounded(filepath.Join(s.undoDir(), "meta.json"), 1<<20); err == nil {
		var m metaDoc
		if json.Unmarshal(b, &m) == nil && m.Next > 0 && m.Next <= maxSeq {
			next = m.Next
		}
	}
	for _, n := range s.undoSeqsLocked() {
		if n >= next {
			next = n + 1
		}
	}
	if next > maxSeq {
		return "", errors.New("checkpoint: the undo counter is exhausted")
	}
	data, _ := json.Marshal(metaDoc{V: undoVersion, Next: next + 1})
	if err := writeFileAtomic(filepath.Join(s.undoDir(), "meta.json"), data, 0o600, nil); err != nil {
		return "", fmt.Errorf("checkpoint: saving the undo counter: %w", err)
	}
	return undoID(next), nil
}

// saveUndoLocked writes a capture atomically.
func (s *Store) saveUndoLocked(doc *undoDoc) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(s.undoDir(), doc.ID+".json"), data, 0o600, nil); err != nil {
		return fmt.Errorf("checkpoint: saving an undo capture: %w", err)
	}
	return nil
}

// loadUndoLocked reads and vets a capture: like a manifest, it is data, not authority.
func (s *Store) loadUndoLocked(id string) (*undoDoc, error) {
	seq, ok := undoSeq(id)
	if !ok {
		return nil, fmt.Errorf("%w: %.40q", ErrUnknownUndo, id)
	}
	b, err := readBounded(filepath.Join(s.undoDir(), undoID(seq)+".json"), maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %.40q", ErrUnknownUndo, id)
	}
	var doc undoDoc
	if err := json.Unmarshal(b, &doc); err != nil || doc.ID != undoID(seq) || doc.V > undoVersion || len(doc.Files) > maxUndoFiles {
		return nil, fmt.Errorf("%w: %.40q is damaged", ErrUnknownUndo, id)
	}
	files := doc.Files[:0]
	seen := map[string]bool{}
	for _, rec := range doc.Files {
		if rec == nil || validKey(rec.Path) != nil || seen[rec.Path] || s.validState(&rec.Pre) != nil {
			continue
		}
		if rec.Post != nil && !s.validFingerprint(rec.Post) {
			rec.Post = nil
		}
		rec.NewDirs = slices.DeleteFunc(rec.NewDirs, func(k string) bool { return validKey(k) != nil })
		seen[rec.Path] = true
		files = append(files, rec)
	}
	doc.Files = files
	if len(doc.Before) > maxUndoHistory {
		doc.Before, doc.After = nil, ""
	}
	for i, d := range doc.Before {
		if d.ID != cpID(d.Seq) || d.Seq <= 0 || d.Seq > maxSeq || (i > 0 && d.Seq <= doc.Before[i-1].Seq) {
			doc.Before, doc.After = nil, ""
			break
		}
	}
	doc.Label = cleanLabel(doc.Label)
	return &doc, nil
}

// validFingerprint vets a post-operation fingerprint: a state that needs no saved
// content (a regular file is named by its checksum alone).
func (s *Store) validFingerprint(st *state) bool {
	if st.Kind == kFile {
		probe := *st
		probe.Blob = probe.Sum // the fingerprint has no content of its own
		if s.validState(&probe) != nil {
			return false
		}
		st.Note = probe.Note
		return st.Blob == "" || events.ValidHash(st.Blob)
	}
	return s.validState(st) == nil
}

// dropUndoLocked removes a capture.
func (s *Store) dropUndoLocked(id string) error {
	seq, ok := undoSeq(id)
	if !ok {
		return fmt.Errorf("%w: %.40q", ErrUnknownUndo, id)
	}
	if err := os.Remove(filepath.Join(s.undoDir(), undoID(seq)+".json")); err != nil && !isMissing(err) {
		return fmt.Errorf("checkpoint: %w", err)
	}
	return nil
}

// pruneUndosLocked keeps the newest maxUndos captures.
func (s *Store) pruneUndosLocked() {
	seqs := s.undoSeqsLocked()
	for len(seqs) > maxUndos {
		_ = os.Remove(filepath.Join(s.undoDir(), undoID(seqs[0])+".json"))
		seqs = seqs[1:]
	}
}
