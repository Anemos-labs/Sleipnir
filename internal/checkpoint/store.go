// Package checkpoint records the state of files before agents modify them, so
// a session's edits can be inspected (Diff) and rewound (Restore).
//
// A checkpoint is opened per user prompt or swarm phase (Begin). While it is
// current, the first Before(agent, path) for each path saves that path's
// pre-modification state: content in the shared blob store, plus mode, or the
// fact that the path did not exist yet. Restoring checkpoint N rewinds every
// file touched in N or any later checkpoint to the state it had when N began.
//
// The Store satisfies tools.Snapshotter structurally, so this package does not
// import internal/tools.
//
// Consistency stance: the snapshot must exist before the write happens, so
// Before does not return until the pre-state is captured and recorded in the
// manifest, including for a second agent racing on a path whose first snapshot
// is still being taken. Anything that stops a file from being saved (too large,
// unreadable, special file) is recorded and reported at rewind time rather than
// blocking the edit; only harness failures (blob store or manifest I/O) make
// Before fail.
//
// The manifest (one JSON file per checkpoint, plus a counter) is replaced
// atomically with a temporary file and rename, so a crash never leaves a torn
// file. It is not fsynced: like the edits it protects, it survives the process
// dying, not necessarily the machine losing power.
//
// Trust: the state directory can be written by anyone who can write files as the
// user (and, if it sits inside the project, by whatever a repository ships), so a
// manifest is data, not authority. Loading vets every record: keys must be
// root-relative paths that stay inside the project root, every saved file must
// carry a content hash and a checksum, and sizes, modes and hashes must be in
// range; anything else is dropped with a warning. Files outside the project are
// recorded by absolute path while the process that made the record lives, and can
// be rewound by it, but such a record is not accepted back from disk. Restore and
// Diff resolve each path at the moment they use it and refuse anything that lands
// outside the project root, including through a symlink that appeared since (or
// that an earlier step of the same rewind created); restored content is read
// with a size bound, must match its checksum, and a rewind writes at most a
// bounded number of bytes.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

const (
	// MaxFileBytes caps the size of a file whose content is saved. Larger files
	// are recorded as unsaved and cannot be restored.
	MaxFileBytes = 64 << 20

	manifestVersion = 1

	// mtimeGrace is how far a file's mtime may trail the last recorded write
	// before Restore treats it as modified by someone else. It is only used when
	// After was never called for the write (see Store.After).
	mtimeGrace = 5 * time.Second

	maxLabelRunes = 120

	// maxKeyBytes bounds a path or symlink target read from a manifest (PATH_MAX).
	maxKeyBytes = 4096
	// maxSeq bounds checkpoint numbers and the id counter, so a hostile file name
	// or counter cannot overflow the next id.
	maxSeq = 1 << 30
	// maxRecords bounds the records one manifest file may hold.
	maxRecords = 200_000
	// maxWarnings bounds the warnings kept about a damaged state directory.
	maxWarnings = 100
	// maxID bounds an owner's uid/gid read from a manifest.
	maxID = 1<<31 - 1
)

// Limits that tests shrink. maxManifestBytes bounds one manifest file (a real one
// is a few hundred bytes per touched file); maxRestoreBytes bounds the content one
// Restore may write.
var (
	maxManifestBytes int64 = 64 << 20
	maxRestoreBytes  int64 = 1 << 30
)

// ErrUnknownCheckpoint is returned for an id that does not exist.
var ErrUnknownCheckpoint = errors.New("checkpoint: unknown checkpoint")

var cpFileRE = regexp.MustCompile(`^cp_(\d+)\.json$`)

// Info describes one checkpoint.
type Info struct {
	ID    string    `json:"id"`
	Label string    `json:"label"`
	Time  time.Time `json:"time"`
	// Files are the paths first touched in this checkpoint (relative to the
	// root when inside it, absolute otherwise, in which case only the process
	// that recorded it can rewind it: see the package documentation), sorted.
	Files []string `json:"files"`
	// Agents that touched at least one file, sorted.
	Agents []string `json:"agents"`
	// Unsaved lists files whose content could not be saved (too large,
	// unreadable): rewinding will not restore them.
	Unsaved []string `json:"unsaved,omitempty"`
}

// fileRec is what one checkpoint knows about one path.
type fileRec struct {
	Path string `json:"path"` // root-relative with "/" separators, or absolute
	// Pre is the state before the first modification in this checkpoint.
	Pre    state     `json:"pre"`
	Agents []string  `json:"agents,omitempty"`
	At     time.Time `json:"at"`
	// Last is the most recent Before/After for the path; Post, when known, is
	// the file's state right after the last recorded write. Together they let
	// Restore notice modifications made by someone who is not recording.
	Last time.Time `json:"last"`
	Post *state    `json:"post,omitempty"`
	// NewDirs are directories that did not exist when a path that was absent
	// got its Before: the write will have created them, so rewinding removes
	// them again (when empty). Top-most first.
	NewDirs []string `json:"new_dirs,omitempty"`

	// gen counts Before calls on this record (not persisted). After uses it to
	// notice that another agent announced a write while it was fingerprinting.
	gen uint64
}

// touch notes another Before on an already-recorded path. The write that
// follows makes any earlier post-write fingerprint stale, so it is dropped.
func (r *fileRec) touch(agent string, now time.Time) (agentAdded bool) {
	r.gen++
	r.Last = now
	r.Post = nil
	if agent != "" && !slices.Contains(r.Agents, agent) {
		r.Agents = append(r.Agents, agent)
		return true
	}
	return false
}

type checkpoint struct {
	ID    string
	Seq   int
	Label string
	Time  time.Time
	files []*fileRec // first-touch order
	index map[string]*fileRec
}

func (c *checkpoint) add(r *fileRec) {
	c.files = append(c.files, r)
	c.index[r.Path] = r
}

func (c *checkpoint) remove(key string) {
	if _, ok := c.index[key]; !ok {
		return
	}
	delete(c.index, key)
	c.files = slices.DeleteFunc(c.files, func(r *fileRec) bool { return r.Path == key })
}

// cpDoc is the on-disk form of one checkpoint. Each checkpoint has its own file
// so persisting a Before rewrites only that checkpoint, not the whole history.
type cpDoc struct {
	V     int        `json:"v"`
	ID    string     `json:"id"`
	Seq   int        `json:"seq"`
	Label string     `json:"label"`
	Time  time.Time  `json:"time"`
	Files []*fileRec `json:"files"`
}

// metaDoc holds the id counter. It is separate from the checkpoints so ids are
// never reused after a rewind drops later checkpoints.
type metaDoc struct {
	V    int `json:"v"`
	Next int `json:"next"`
}

// flight is a snapshot in progress. Agents that race on the same path wait for
// it instead of returning early: returning early would let them write the file
// while the first snapshot is still reading it.
type flight struct {
	done chan struct{}
	err  error
}

// Store is a set of checkpoints persisted under one directory. It is safe for
// concurrent use by many agents.
type Store struct {
	dir      string
	blobs    events.Blobs
	root     string // absolute, cleaned
	realRoot string // root with symlinks resolved (== root when it cannot be)
	maxBytes int64

	// gate lets Before/Begin/After run concurrently with each other while a
	// Restore excludes them all: nothing may be recorded while files are being
	// rewound underneath it.
	gate sync.RWMutex

	mu       sync.Mutex // guards everything below
	now      func() time.Time
	cps      []*checkpoint
	next     int
	inflight map[string]*flight
	warnings []string
	warnMore int // warnings beyond maxWarnings
}

// New opens (creating if needed) the checkpoint store in dir. Existing
// checkpoints are loaded, so rewind works across restarts. blobs is the shared
// content store (a private one under dir/blobs is created when nil); root is the
// project root that recorded paths are made relative to.
func New(dir string, blobs events.Blobs, root string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("checkpoint: dir is required")
	}
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("checkpoint: %w", err)
		}
		root = wd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("checkpoint: %w", err)
	}
	if blobs == nil {
		if blobs, err = events.NewDirBlobs(filepath.Join(dir, "blobs")); err != nil {
			return nil, fmt.Errorf("checkpoint: %w", err)
		}
	}
	s := &Store{
		dir:      dir,
		blobs:    blobs,
		root:     absRoot,
		realRoot: resolveDir(absRoot),
		maxBytes: MaxFileBytes,
		now:      func() time.Time { return time.Now().UTC() },
		next:     1,
		inflight: map[string]*flight{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetClock overrides the timestamp source (tests, simulation).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// Warnings returns problems found while loading persisted checkpoints (corrupt
// files that were skipped, records that were dropped).
func (s *Store) Warnings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.warnings)
	if s.warnMore > 0 {
		out = append(out, fmt.Sprintf("and %d more problems in the checkpoint files", s.warnMore))
	}
	return out
}

func (s *Store) warnf(format string, args ...any) {
	if len(s.warnings) >= maxWarnings {
		s.warnMore++
		return
	}
	s.warnings = append(s.warnings, fmt.Sprintf(format, args...))
}

func cpID(seq int) string { return fmt.Sprintf("cp_%04d", seq) }

// normalizeID accepts "cp_0003", "cp_3", "0003" and "3".
func normalizeID(id string) string {
	id = strings.TrimSpace(id)
	if n, err := strconv.Atoi(strings.TrimPrefix(id, "cp_")); err == nil && n > 0 {
		return cpID(n)
	}
	return id
}

func (s *Store) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	var loaded []*checkpoint
	maxSeen := 0 // highest number among checkpoint files, readable or not
	for _, e := range entries {
		m := cpFileRE.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		seq, err := strconv.Atoi(m[1])
		if err != nil || seq > maxSeq {
			s.warnf("skipped checkpoint file %s: its number is out of range", e.Name())
			continue
		}
		if seq > maxSeen {
			maxSeen = seq
		}
		data, err := readBounded(filepath.Join(s.dir, e.Name()), maxManifestBytes)
		if errors.Is(err, errUnusable) {
			s.warnf("skipped checkpoint file %s: %v", e.Name(), err)
			continue
		}
		if err != nil {
			return fmt.Errorf("checkpoint: %w", err)
		}
		var doc cpDoc
		if err := json.Unmarshal(data, &doc); err != nil {
			s.warnf("skipped corrupt checkpoint file %s: %v", e.Name(), err)
			continue
		}
		if doc.Seq == 0 {
			doc.Seq = seq
		}
		if doc.Seq != seq || doc.ID != cpID(seq) {
			s.warnf("skipped checkpoint file %s: contents do not match its name", e.Name())
			continue
		}
		if doc.V > manifestVersion {
			s.warnf("skipped checkpoint file %s: it was written by a newer version", e.Name())
			continue
		}
		if len(doc.Files) > maxRecords {
			s.warnf("skipped checkpoint file %s: more than %d records", e.Name(), maxRecords)
			continue
		}
		cp := &checkpoint{ID: doc.ID, Seq: doc.Seq, Label: cleanLabel(doc.Label), Time: doc.Time, index: map[string]*fileRec{}}
		for _, r := range doc.Files {
			if r == nil {
				s.warnf("dropped an empty record in %s", e.Name())
				continue
			}
			if err := s.validRecord(r); err != nil {
				s.warnf("dropped an invalid record in %s (%.120q): %v", e.Name(), r.Path, err)
				continue
			}
			if _, dup := cp.index[r.Path]; dup {
				continue
			}
			cp.add(r)
		}
		loaded = append(loaded, cp)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Seq < loaded[j].Seq })
	s.cps = loaded

	if data, err := readBounded(filepath.Join(s.dir, "meta.json"), 1<<20); err == nil {
		var m metaDoc
		if json.Unmarshal(data, &m) == nil && m.Next > s.next && m.Next <= maxSeq {
			s.next = m.Next
		}
	}
	// A skipped file keeps its number: reusing it would overwrite evidence.
	if maxSeen >= s.next {
		s.next = maxSeen + 1
	}
	if n := len(loaded); n > 0 && loaded[n-1].Seq >= s.next {
		s.next = loaded[n-1].Seq + 1
	}
	return nil
}

// validKey vets a manifest path: root-relative, staying inside the root. Absolute
// paths are what a live Store records for files outside the project, but they are
// only trusted in the process that made them, so they are not accepted from disk.
func validKey(key string) error {
	switch {
	case key == "":
		return errors.New("empty path")
	case len(key) > maxKeyBytes:
		return errors.New("path too long")
	case strings.ContainsRune(key, 0):
		return errors.New("path contains a NUL byte")
	}
	p := filepath.FromSlash(key)
	if filepath.IsAbs(p) {
		return errors.New("names a path outside the project root (only the process that recorded it can rewind such a path)")
	}
	if !filepath.IsLocal(p) {
		return errors.New("path leaves the project root")
	}
	if key == "." || path.Clean(key) != key { // keys are written clean; another spelling is an alias or a trick
		return errors.New("path is not in canonical form")
	}
	return nil
}

// validRecord vets a record read from disk (see the package documentation). It may
// tidy the record (drop a fingerprint or directory names it cannot trust) and
// returns why the whole record has to go, or nil.
func (s *Store) validRecord(r *fileRec) error {
	if err := validKey(r.Path); err != nil {
		return err
	}
	if err := s.validState(&r.Pre); err != nil {
		return fmt.Errorf("recorded state: %w", err)
	}
	if r.Post != nil && s.validState(r.Post) != nil {
		r.Post = nil // a fingerprint that does not check out is no fingerprint
	}
	r.NewDirs = slices.DeleteFunc(r.NewDirs, func(k string) bool { return validKey(k) != nil })
	if len(r.Agents) > 64 {
		r.Agents = r.Agents[:64]
	}
	for i, a := range r.Agents {
		r.Agents[i] = cleanText(a, 128)
	}
	return nil
}

// validState checks one recorded state: a known kind, and for a saved file the
// hashes that name and vouch for its content (a rewind refuses a file without a
// checksum), a size within the store's cap, and a mode in range.
func (s *Store) validState(st *state) error {
	switch st.Kind {
	case kAbsent, kDir, kOther, kUnsaved:
	case kFile:
		if !events.ValidHash(st.Blob) || !events.ValidHash(st.Sum) {
			return errors.New("a saved file needs a content hash and a checksum")
		}
		if st.Size < 0 || st.Size > s.maxBytes {
			return fmt.Errorf("saved file size %d is out of range", st.Size)
		}
	case kLink:
		if st.Target == "" || len(st.Target) > maxKeyBytes || strings.ContainsRune(st.Target, 0) {
			return errors.New("bad symlink target")
		}
	default:
		return fmt.Errorf("unknown kind %.40q", string(st.Kind))
	}
	if st.Mode > 0o7777 {
		return fmt.Errorf("mode %o is out of range", st.Mode)
	}
	if o := st.Owner; o != nil && (o.UID < 0 || o.GID < 0 || o.UID > maxID || o.GID > maxID) {
		st.Owner = nil
	}
	st.Note = cleanText(st.Note, 300)
	return nil
}

func (s *Store) persistLocked(cp *checkpoint) error {
	doc := cpDoc{V: manifestVersion, ID: cp.ID, Seq: cp.Seq, Label: cp.Label, Time: cp.Time, Files: append([]*fileRec{}, cp.files...)}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(s.dir, cp.ID+".json"), data, 0o600, nil); err != nil {
		return fmt.Errorf("checkpoint: saving manifest: %w", err)
	}
	return nil
}

func (s *Store) persistMetaLocked() error {
	data, err := json.Marshal(metaDoc{V: manifestVersion, Next: s.next})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.dir, "meta.json"), data, 0o600, nil); err != nil {
		return fmt.Errorf("checkpoint: saving manifest: %w", err)
	}
	return nil
}

func cleanLabel(label string) string {
	label = cleanText(strings.Join(strings.Fields(label), " "), 4*maxLabelRunes)
	if r := []rune(label); len(r) > maxLabelRunes {
		label = string(r[:maxLabelRunes]) + "…"
	}
	return label
}

// Begin opens a new checkpoint and makes it current: files first touched from
// now on are recorded in it. Call it once per user prompt or swarm phase. Ids
// are short and ordered ("cp_0001", "cp_0002", ...) and never reused.
func (s *Store) Begin(label string) (ID string) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginLocked(label).ID
}

// beginLocked persists on a best-effort basis: an empty checkpoint has nothing
// to lose, and the next Before that lands in it persists it again and reports
// any failure to its caller, which is the moment that matters.
func (s *Store) beginLocked(label string) *checkpoint {
	cp := &checkpoint{Seq: s.next, Label: cleanLabel(label), Time: s.now(), index: map[string]*fileRec{}}
	cp.ID = cpID(cp.Seq)
	s.next++
	s.cps = append(s.cps, cp)
	_ = s.persistLocked(cp)
	_ = s.persistMetaLocked()
	return cp
}

// currentLocked returns the checkpoint that new records go into. Editing before
// any Begin still records into an implicit first checkpoint: dropping the
// snapshot would silently make the edit un-rewindable.
func (s *Store) currentLocked() *checkpoint {
	if n := len(s.cps); n > 0 {
		return s.cps[n-1]
	}
	return s.beginLocked("session start")
}

// Before records the pre-modification state of path in the current checkpoint,
// once per checkpoint: the content (or "absent" when the file does not exist
// yet), its mode, and for symlinks the link target. When path is a symlink, the
// state of what it points to is recorded too, because a write through the link
// modifies the target. Call it immediately before writing, from any goroutine.
//
// Relative paths are resolved against the store's root; paths outside the root
// are recorded as absolute paths.
func (s *Store) Before(agent, path string) error {
	if path == "" {
		return errors.New("checkpoint: empty path")
	}
	if strings.ContainsRune(path, 0) {
		return errors.New("checkpoint: path contains a NUL byte")
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	for _, p := range s.chain(s.absolute(path)) {
		if err := s.snapshot(agent, p); err != nil {
			return err
		}
	}
	return nil
}

// absolute turns a caller's path into the canonical absolute path used for
// I/O and keys. Symlinks in the directory part are resolved so two spellings of
// the same file cannot become two records; the last element is left alone
// because a symlink there is itself something a tool may replace.
func (s *Store) absolute(path string) string {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.root, p)
	}
	p = filepath.Clean(p)
	dir := filepath.Dir(p)
	if dir == p {
		return p
	}
	return filepath.Join(resolveDir(dir), filepath.Base(p))
}

// chain lists abs followed by each symlink target it leads to, ending at the
// first thing that is not a symlink (possibly nothing, for a dangling link).
func (s *Store) chain(abs string) []string {
	out := []string{abs}
	seen := map[string]bool{abs: true}
	cur := abs
	for range 32 {
		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			break
		}
		target, err := os.Readlink(cur)
		if err != nil {
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cur), target)
		}
		next := s.absolute(target)
		if seen[next] {
			break
		}
		seen[next] = true
		out = append(out, next)
		cur = next
	}
	return out
}

// keyFor is the manifest key of an absolute path: relative to the root (with
// "/" separators, so the manifest survives a moved project) when inside it,
// absolute otherwise.
func (s *Store) keyFor(abs string) string {
	for _, r := range []string{s.root, s.realRoot} {
		if rel, err := filepath.Rel(r, abs); err == nil && filepath.IsLocal(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(abs)
}

// errEscapes is returned for a manifest path that resolves outside the project root.
var errEscapes = errors.New("it leads outside the project root (through a symlink?)")

// resolveKey maps a manifest key to the absolute path an operation may touch, or
// says why not. It is called at the moment of use, not when a rewind is planned:
// an earlier step of the same rewind may have put a symlink in the way.
//
// A relative key must stay inside the root once the symlinks in its directory part
// are resolved, and the path returned has those symlinks resolved, so writing to
// it cannot be redirected. The last element is left alone: every operation
// replaces or removes it rather than following it. An absolute key can only be one
// this process recorded (load drops the others) for a file outside the project, and
// is used as is.
func (s *Store) resolveKey(key string) (string, error) {
	p := filepath.FromSlash(key)
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	if key == "" || strings.ContainsRune(key, 0) || !filepath.IsLocal(p) {
		return "", errors.New("the recorded path is not inside the project root")
	}
	abs := filepath.Join(s.realRoot, p)
	dir := resolveDir(filepath.Dir(abs))
	if _, ok := underRoot(s.realRoot, dir); !ok {
		return "", errEscapes
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}

// underRoot reports whether p is root or inside it.
func underRoot(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", false
	}
	if rel == "." || filepath.IsLocal(rel) {
		return rel, true
	}
	return "", false
}

func (s *Store) snapshot(agent, abs string) error {
	key := s.keyFor(abs)
	for {
		s.mu.Lock()
		cp := s.currentLocked()
		if rec := cp.index[key]; rec != nil {
			// Already recorded in this checkpoint. Only attribution changes, and the
			// pre-state is already safe on disk, so a failure to persist the extra
			// agent name must not block this agent's edit.
			if rec.touch(agent, s.now()) {
				_ = s.persistLocked(cp)
			}
			s.mu.Unlock()
			return nil
		}
		fk := cp.ID + "\x00" + key
		if f := s.inflight[fk]; f != nil {
			s.mu.Unlock()
			<-f.done
			if f.err != nil {
				return f.err
			}
			continue // the record exists now; take the fast path above
		}
		f := &flight{done: make(chan struct{})}
		s.inflight[fk] = f
		now := s.now()
		s.mu.Unlock()

		// Slow work (reading, hashing, blob write) happens outside the lock so
		// agents snapshotting different files do not serialize behind each other.
		rec, err := s.newRecord(agent, abs, key, now)

		s.mu.Lock()
		if err == nil {
			cp.add(rec)
			if err = s.persistLocked(cp); err != nil {
				cp.remove(key) // not durable, so not recorded: the next Before retries
			}
		}
		delete(s.inflight, fk)
		s.mu.Unlock()
		f.err = err
		close(f.done)
		return err
	}
}

func (s *Store) newRecord(agent, abs, key string, now time.Time) (*fileRec, error) {
	st, data := s.capture(abs)
	if st.Kind == kFile {
		h, err := s.blobs.Put(data)
		if err != nil {
			return nil, fmt.Errorf("checkpoint: saving %s: %w", key, err)
		}
		st.Blob = h
	}
	rec := &fileRec{Path: key, Pre: st, At: now, Last: now}
	if agent != "" {
		rec.Agents = []string{agent}
	}
	if st.Kind == kAbsent {
		rec.NewDirs = s.missingParents(abs)
	}
	return rec, nil
}

// missingParents lists the ancestors of abs that do not exist, top-most first:
// the directories a write to abs is about to create.
func (s *Store) missingParents(abs string) []string {
	var missing []string
	for d := filepath.Dir(abs); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); !isMissing(err) {
			break
		}
		missing = append(missing, s.keyFor(d))
		if filepath.Dir(d) == d {
			break
		}
	}
	slices.Reverse(missing)
	return missing
}

// After tells the store that the write announced by Before(agent, path) has
// finished. It is optional but recommended (wire it from tools.Guard's
// AfterWrite, whose signature it matches): it fingerprints the file as the
// agents left it, which lets Restore tell "the agents' last write" from "someone
// edited the file afterwards" exactly. Without it Restore falls back to comparing
// the file's mtime with the time of the last Before.
func (s *Store) After(agent, path string) {
	if path == "" || strings.ContainsRune(path, 0) {
		return
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	for _, p := range s.chain(s.absolute(path)) {
		key := s.keyFor(p)
		// The write was announced in whichever checkpoint was current then, which
		// may not be the newest any more if Begin ran in between.
		s.mu.Lock()
		var cp *checkpoint
		var rec *fileRec
		for i := len(s.cps) - 1; i >= 0 && rec == nil; i-- {
			if r := s.cps[i].index[key]; r != nil {
				cp, rec = s.cps[i], r
			}
		}
		var gen uint64
		if rec != nil {
			gen = rec.gen
		}
		s.mu.Unlock()
		if rec == nil {
			continue
		}
		// Fingerprinting reads the file, so it happens outside the lock. If another
		// agent announced a write meanwhile (gen moved), what was read may already
		// be stale: drop it and let that agent's own After record the truth.
		st, _ := s.capture(p)
		s.mu.Lock()
		if rec.gen == gen {
			rec.Post = &st
			rec.Last = s.now()
			_ = s.persistLocked(cp)
		}
		s.mu.Unlock()
	}
}

// List returns every checkpoint, oldest first.
func (s *Store) List() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.cps))
	for _, cp := range s.cps {
		info := Info{ID: cp.ID, Label: cp.Label, Time: cp.Time, Files: []string{}, Agents: []string{}}
		seen := map[string]bool{}
		for _, r := range cp.files {
			info.Files = append(info.Files, r.Path)
			for _, a := range r.Agents {
				if !seen[a] {
					seen[a] = true
					info.Agents = append(info.Agents, a)
				}
			}
			switch r.Pre.Kind {
			case kUnsaved, kOther:
				info.Unsaved = append(info.Unsaved, r.Path)
			}
		}
		sort.Strings(info.Files)
		sort.Strings(info.Agents)
		sort.Strings(info.Unsaved)
		out = append(out, info)
	}
	return out
}

// indexLocked finds a checkpoint by id ("" means the latest). It returns -1
// when there is none.
func (s *Store) indexLocked(id string) int {
	if len(s.cps) == 0 {
		return -1
	}
	if strings.TrimSpace(id) == "" {
		return len(s.cps) - 1
	}
	want := normalizeID(id)
	for i, cp := range s.cps {
		if cp.ID == want {
			return i
		}
	}
	return -1
}

// planned is one path that a rewind to a checkpoint concerns: the state it had
// when the checkpoint began (from the earliest record, since later records only
// describe states the agents themselves produced), who touched it since, and
// the latest record (for the modified-behind-our-back check).
type planned struct {
	key     string
	want    state
	agents  []string
	last    fileRec
	newDirs []string
}

// planLocked merges the records of checkpoints idx.. into one entry per path.
// Records are visited oldest first and the first one for a path wins, which is
// what makes a rewind across several checkpoints land on the oldest state.
func (s *Store) planLocked(idx int) []*planned {
	byKey := map[string]*planned{}
	var order []*planned
	for _, cp := range s.cps[idx:] {
		for _, r := range cp.files {
			p := byKey[r.Path]
			if p == nil {
				p = &planned{key: r.Path, want: r.Pre}
				byKey[r.Path] = p
				order = append(order, p)
			}
			for _, a := range r.Agents {
				if !slices.Contains(p.agents, a) {
					p.agents = append(p.agents, a)
				}
			}
			for _, d := range r.NewDirs {
				if !slices.Contains(p.newDirs, d) {
					p.newDirs = append(p.newDirs, d)
				}
			}
			p.last = *r
			p.last.Agents = slices.Clone(r.Agents)
			p.last.NewDirs = slices.Clone(r.NewDirs)
			if r.Post != nil {
				post := *r.Post
				p.last.Post = &post
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].key < order[j].key })
	return order
}
