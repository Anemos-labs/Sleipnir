package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// Action is what a rewind does (or would do) to one path.
type Action string

const (
	ActionNone     Action = "none"     // already in the checkpoint's state
	ActionRestore  Action = "restore"  // put the saved content back
	ActionRecreate Action = "recreate" // the file was deleted; create it again
	ActionDelete   Action = "delete"   // the path did not exist at the checkpoint
	ActionChmod    Action = "chmod"    // content is right, permission bits are not
	ActionRelink   Action = "relink"   // put the saved symlink back
	ActionMkdir    Action = "mkdir"    // a directory existed at the checkpoint
	ActionRmdir    Action = "rmdir"    // remove a directory a tool created
)

// Outcome is how a path's rewind went.
type Outcome string

const (
	OutcomeDone      Outcome = "done"      // rewound
	OutcomePlanned   Outcome = "planned"   // dry run: would be rewound
	OutcomeUnchanged Outcome = "unchanged" // already in the checkpoint's state
	// OutcomeConflict: refused because the rewind would destroy someone else's
	// work; RestoreOpts.Force overrides it.
	OutcomeConflict Outcome = "conflict"
	// OutcomeUnrestorable: the original content was never saved (too large,
	// unreadable, special file), or the path leads outside the project root (a
	// symlink, a damaged record). Force cannot help.
	OutcomeUnrestorable Outcome = "unrestorable"
	// OutcomeFailed: an I/O error while applying. The path keeps its record so
	// the rewind can be retried.
	OutcomeFailed Outcome = "failed"
)

// RestoreOpts narrows or overrides a rewind.
type RestoreOpts struct {
	// DryRun reports what would happen without touching anything.
	DryRun bool
	// OnlyPaths restricts the rewind to these paths (a directory covers
	// everything under it). Relative paths are resolved against the root.
	OnlyPaths []string
	// OnlyAgent restricts the rewind to files this agent touched. Because a
	// file can only be rewound as a whole, a file that other agents touched as
	// well is a conflict: rewinding it would undo their edits too.
	OnlyAgent string
	// Force rewinds files even when doing so would overwrite changes made by
	// someone who is not part of the rewind (other agents, or edits the
	// checkpoint never saw).
	Force bool
}

// FileResult is the outcome for one path.
type FileResult struct {
	Path    string   `json:"path"`
	Action  Action   `json:"action"`
	Outcome Outcome  `json:"outcome"`
	Detail  string   `json:"detail,omitempty"`
	Agents  []string `json:"agents,omitempty"`
}

// RestoreReport describes a rewind path by path. Per-file problems are reported
// here rather than as a Go error, so one bad file never hides what happened to
// the others.
type RestoreReport struct {
	ID     string       `json:"id"`
	DryRun bool         `json:"dry_run,omitempty"`
	Files  []FileResult `json:"files"`
	// Rewound is true when the whole history since the checkpoint was undone and
	// dropped, leaving the checkpoint empty and current.
	Rewound bool `json:"rewound,omitempty"`
}

// Count returns how many paths ended with the given outcome.
func (r RestoreReport) Count(o Outcome) int {
	n := 0
	for _, f := range r.Files {
		if f.Outcome == o {
			n++
		}
	}
	return n
}

// OK reports whether every path was rewound (or already matched).
func (r RestoreReport) OK() bool {
	return r.Count(OutcomeConflict)+r.Count(OutcomeUnrestorable)+r.Count(OutcomeFailed) == 0
}

// Err summarizes what did not go through, or nil when OK.
func (r RestoreReport) Err() error {
	if r.OK() {
		return nil
	}
	var bad []string
	for _, f := range r.Files {
		switch f.Outcome {
		case OutcomeConflict, OutcomeUnrestorable, OutcomeFailed:
			bad = append(bad, fmt.Sprintf("%s: %s: %s", f.Path, f.Outcome, f.Detail))
		}
	}
	return fmt.Errorf("checkpoint %s: %d of %d files were not restored:\n  %s",
		r.ID, len(bad), len(r.Files), strings.Join(bad, "\n  "))
}

// Summary is a one-line description for logs and UIs. Directory cleanups are
// not counted as restored files.
func (r RestoreReport) Summary() string {
	verb, want := "restored", OutcomeDone
	if r.DryRun {
		verb, want = "would restore", OutcomePlanned
	}
	done := 0
	for _, f := range r.Files {
		if f.Outcome == want && f.Action != ActionRmdir {
			done++
		}
	}
	parts := []string{fmt.Sprintf("%s %d", verb, done)}
	if n := r.Count(OutcomeUnchanged); n > 0 {
		parts = append(parts, fmt.Sprintf("%d already matched", n))
	}
	if n := r.Count(OutcomeConflict); n > 0 {
		parts = append(parts, fmt.Sprintf("%d in conflict (Force overrides)", n))
	}
	if n := r.Count(OutcomeUnrestorable); n > 0 {
		parts = append(parts, fmt.Sprintf("%d unrestorable", n))
	}
	if n := r.Count(OutcomeFailed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	return "checkpoint " + r.ID + ": " + strings.Join(parts, ", ")
}

// task is one path being rewound.
type task struct {
	it     *planned
	cur    state
	action Action
	res    FileResult
}

// Restore rewinds every file touched since checkpoint id ("" means the latest)
// to the state it had when that checkpoint began: content and mode restored,
// files that did not exist deleted (and directories the tools created removed
// when empty), deleted files recreated, symlinks relinked. Each file is
// written atomically (temporary file plus rename).
//
// A file is refused (OutcomeConflict) unless opts.Force is set when rewinding
// it would destroy work by someone who is not part of the rewind: other agents'
// edits (with OnlyAgent), or modifications made after the last write the
// checkpoint recorded. Files whose original content was never saved are
// OutcomeUnrestorable and are never touched.
//
// A path is only ever written after it has been resolved again, and one that
// leads outside the project root (through a symlink that appeared after the
// checkpoint, or that an earlier step of the same rewind created) is refused as
// OutcomeUnrestorable; content is verified against its checksum and a rewind
// writes at most maxRestoreBytes in total.
//
// Files that were rewound are forgotten by the checkpoints; files that failed
// or were refused keep their records so the rewind can be retried. When every
// file was rewound and no filter was given, the checkpoints after id are dropped
// too (their history no longer exists) and id itself stays, empty and current.
//
// Restore must not run while agents are still writing: it excludes concurrent
// Before/Begin calls but cannot stop a tool that is already mid-write.
func (s *Store) Restore(id string, opts RestoreOpts) (RestoreReport, error) {
	var changed []string
	defer func() { s.notify(changed...) }() // after the gate is released
	if !opts.DryRun {
		s.gate.Lock()
		defer s.gate.Unlock()
	}
	rep, changed, err := s.restore(id, opts, restoreHook{})
	return rep, err
}

// restoreHook lets a caller that holds the exclusive gate act around the writes of a
// restore: before runs with the planned tasks before anything is written (an error
// stops the restore with nothing changed), after once the files are written and the
// records committed. Neither runs for a dry run.
type restoreHook struct {
	before func(idx int, tasks []*task) error
	after  func(idx int, tasks []*task)
}

// restore is Restore for a caller that holds the exclusive gate (or none, for a dry
// run). It also returns the checkpoints whose records changed.
func (s *Store) restore(id string, opts RestoreOpts, hook restoreHook) (RestoreReport, []string, error) {
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		if strings.TrimSpace(id) == "" {
			return RestoreReport{}, nil, fmt.Errorf("%w: there are no checkpoints", ErrUnknownCheckpoint)
		}
		return RestoreReport{}, nil, fmt.Errorf("%w: %q", ErrUnknownCheckpoint, id)
	}
	cpID := s.cps[idx].ID
	items := s.planLocked(idx)
	s.mu.Unlock()

	rep := RestoreReport{ID: cpID, DryRun: opts.DryRun, Files: []FileResult{}}
	scoped, unmatched := s.filter(items, opts)
	for _, p := range unmatched {
		rep.Files = append(rep.Files, FileResult{Path: p, Action: ActionNone, Outcome: OutcomeUnchanged,
			Detail: "not modified since this checkpoint (or never recorded)"})
	}

	tasks := make([]*task, 0, len(scoped))
	for _, it := range scoped {
		tasks = append(tasks, s.evaluate(it, opts))
	}

	// Deletions first, deepest first, so a directory's contents go before the
	// directory itself is asked to give way; then restorations, shallowest first,
	// so parents exist before their children.
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
	if !opts.DryRun && hook.before != nil {
		if err := hook.before(idx, tasks); err != nil {
			return RestoreReport{}, nil, err
		}
	}
	if !opts.DryRun {
		budget := maxRestoreBytes
		for _, t := range append(dels, puts...) {
			if err := s.apply(t, &budget); err != nil {
				t.res.Outcome, t.res.Detail = OutcomeFailed, err.Error()
			} else {
				t.res.Outcome = OutcomeDone
			}
		}
	}

	satisfied := map[string]bool{}
	allSatisfied := true
	for _, t := range tasks {
		rep.Files = append(rep.Files, t.res)
		switch t.res.Outcome {
		case OutcomeDone, OutcomeUnchanged:
			satisfied[t.it.key] = true
		default:
			allSatisfied = false
		}
	}
	rep.Files = append(rep.Files, s.removeCreatedDirs(tasks, opts.DryRun)...)
	sort.SliceStable(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })

	if opts.DryRun {
		return rep, nil, nil
	}
	full := len(opts.OnlyPaths) == 0 && opts.OnlyAgent == ""
	rewound := full && allSatisfied
	err := s.commitRestore(idx, satisfied, rewound)
	rep.Rewound = rewound && err == nil
	s.mu.Lock()
	var changed []string
	for _, cp := range s.cps[min(idx, len(s.cps)):] {
		changed = append(changed, cp.ID)
	}
	s.mu.Unlock()
	if hook.after != nil {
		hook.after(idx, tasks)
	}
	return rep, changed, err
}

// filter applies OnlyPaths/OnlyAgent. unmatched lists OnlyPaths entries that
// selected nothing, so a typo shows up in the report instead of vanishing.
func (s *Store) filter(items []*planned, opts RestoreOpts) (kept []*planned, unmatched []string) {
	if len(opts.OnlyPaths) == 0 && opts.OnlyAgent == "" {
		return items, nil
	}
	type sel struct {
		orig, key string
		hit       bool
	}
	sels := make([]*sel, 0, len(opts.OnlyPaths))
	for _, p := range opts.OnlyPaths {
		if p == "" {
			continue
		}
		abs := s.absolute(p)
		key := s.keyFor(abs)
		if abs == s.root || abs == s.realRoot {
			key = "." // the root itself: everything inside it
		}
		sels = append(sels, &sel{orig: p, key: key})
	}
	for _, it := range items {
		if opts.OnlyAgent != "" && !slices.Contains(it.agents, opts.OnlyAgent) {
			continue
		}
		if len(opts.OnlyPaths) > 0 {
			matched := false
			for _, sl := range sels {
				inRoot := sl.key == "." && !filepath.IsAbs(filepath.FromSlash(it.key))
				if inRoot || it.key == sl.key || strings.HasPrefix(it.key, sl.key+"/") {
					sl.hit, matched = true, true
				}
			}
			if !matched {
				continue
			}
		}
		kept = append(kept, it)
	}
	for _, sl := range sels {
		if !sl.hit {
			unmatched = append(unmatched, sl.orig)
		}
	}
	return kept, unmatched
}

// evaluate compares a path's current state with the state to restore and
// decides what to do about it, without changing anything.
func (s *Store) evaluate(it *planned, opts RestoreOpts) *task {
	t := &task{it: it}
	t.res = FileResult{Path: it.key, Action: ActionNone, Agents: slices.Clone(it.agents)}
	abs, err := s.resolveKey(it.key)
	if err != nil {
		t.res.Outcome = OutcomeUnrestorable
		t.res.Detail = "refused: " + err.Error()
		return t
	}
	cur, _ := s.capture(abs)
	t.cur = cur

	if sameState(cur, it.want) {
		t.res.Outcome = OutcomeUnchanged
		return t
	}
	switch it.want.Kind {
	case kUnsaved:
		t.res.Outcome = OutcomeUnrestorable
		t.res.Detail = "the original content was not saved: " + it.want.Note
		return t
	case kOther:
		t.res.Outcome = OutcomeUnrestorable
		t.res.Detail = "cannot restore a " + it.want.Note
		return t
	case kAbsent:
		t.action = ActionDelete
	case kFile:
		switch {
		case cur.Kind == kAbsent:
			t.action = ActionRecreate
		case cur.Kind == kFile && cur.Sum == it.want.Sum:
			t.action = ActionChmod
		default:
			t.action = ActionRestore
		}
	case kLink:
		t.action = ActionRelink
	case kDir:
		if cur.Kind == kDir {
			t.action = ActionChmod
		} else {
			t.action = ActionMkdir
		}
	}
	t.res.Action = t.action

	if !opts.Force {
		if why := s.conflict(it, cur, t.action, opts); why != "" {
			t.res.Outcome, t.res.Detail = OutcomeConflict, why
			return t
		}
	}
	t.res.Outcome = OutcomePlanned
	return t
}

// conflict says why rewinding a path would destroy work that is not part of the
// rewind, or "" when it is safe.
func (s *Store) conflict(it *planned, cur state, action Action, opts RestoreOpts) string {
	if opts.OnlyAgent != "" {
		var others []string
		for _, a := range it.agents {
			if a != opts.OnlyAgent {
				others = append(others, a)
			}
		}
		if len(others) > 0 {
			return fmt.Sprintf("also modified by %s; a file can only be rewound as a whole, so this would undo their edits too", strings.Join(others, ", "))
		}
	}
	// Overwriting or deleting existing content is what can destroy someone's
	// work; recreating a missing file or fixing permission bits cannot. A
	// directory is only ever removed when empty, so there is nothing to protect
	// (and its mtime moves with every child, which would only cause false alarms).
	if cur.Kind == kAbsent || cur.Kind == kDir || action == ActionRecreate || action == ActionChmod {
		return ""
	}
	if it.last.Post != nil {
		if !sameState(cur, *it.last.Post) {
			return "it changed after the last write the checkpoint recorded (edited by someone else)"
		}
		return ""
	}
	// No post-write fingerprint (After was not called): fall back to mtime.
	if cur.MTime != 0 && !it.last.Last.IsZero() && time.Unix(0, cur.MTime).After(it.last.Last.Add(mtimeGrace)) {
		return "its modification time is later than the last write the checkpoint recorded (edited by someone else)"
	}
	return ""
}

// apply performs one task's action on disk. The path is resolved again here, at
// the moment of writing: what the key led to when the rewind was planned may not
// be what it leads to now.
func (s *Store) apply(t *task, budget *int64) error {
	it := t.it
	abs, err := s.resolveKey(it.key)
	if err != nil {
		return fmt.Errorf("refused: %v", err)
	}
	switch t.action {
	case ActionDelete:
		if err := os.Remove(abs); err != nil && !isMissing(err) {
			return fmt.Errorf("cannot remove: %s", reason(err))
		}
		return nil
	case ActionChmod:
		if err := chmodNoFollow(abs, goMode(it.want.Mode)); err != nil {
			return fmt.Errorf("cannot change permissions: %s", reason(err))
		}
		return nil
	case ActionRestore, ActionRecreate:
		// Read and verify the saved content before touching the file, so a
		// missing or corrupt blob leaves the current file exactly as it was.
		if it.want.Size > s.maxBytes {
			return fmt.Errorf("the saved file is larger than the %s cap", humanBytes(s.maxBytes))
		}
		data, err := s.readBlob(it.want.Blob, s.maxBytes)
		switch {
		case errors.Is(err, events.ErrBlobTooLarge):
			return fmt.Errorf("the saved content is larger than the %s cap", humanBytes(s.maxBytes))
		case errors.Is(err, events.ErrBlobCorrupt):
			return fmt.Errorf("the saved content is corrupt: %w", err)
		case err != nil:
			return fmt.Errorf("the saved content is missing from the blob store: %w", err)
		}
		if it.want.Sum == "" || core.HashBytes(data) != it.want.Sum {
			return errors.New("the saved content is corrupt (checksum mismatch)")
		}
		if int64(len(data)) > *budget {
			return errors.New("this rewind has already written its limit of content; rewind again for the rest")
		}
		if err := s.prepare(abs, t.cur); err != nil {
			return err
		}
		if err := writeFileAtomic(abs, data, goMode(it.want.Mode), it.want.Owner); err != nil {
			return fmt.Errorf("cannot write: %s", reason(err))
		}
		*budget -= int64(len(data))
		return nil
	case ActionRelink:
		if err := s.prepare(abs, t.cur); err != nil {
			return err
		}
		if err := symlinkAtomic(it.want.Target, abs, it.want.Owner); err != nil {
			return fmt.Errorf("cannot create symlink: %s", reason(err))
		}
		return nil
	case ActionMkdir:
		if t.cur.Kind != kAbsent && t.cur.Kind != kDir {
			if err := os.Remove(abs); err != nil {
				return fmt.Errorf("cannot remove what is in the way: %s", reason(err))
			}
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return fmt.Errorf("cannot create directory: %s", reason(err))
		}
		chownPath(abs, it.want.Owner)
		if err := chmodNoFollow(abs, goMode(it.want.Mode)); err != nil {
			return fmt.Errorf("cannot change permissions: %s", reason(err))
		}
		return nil
	}
	return nil
}

// maxGetter is implemented by blob stores that can refuse an oversized blob
// before reading it into memory (events.DirBlobs and events.MemBlobs do).
type maxGetter interface {
	GetMax(h core.Hash, max int64) ([]byte, error)
}

// readBlob reads a blob of at most max bytes.
func (s *Store) readBlob(h core.Hash, max int64) ([]byte, error) {
	if g, ok := s.blobs.(maxGetter); ok {
		return g.GetMax(h, max)
	}
	b, err := s.blobs.Get(h)
	if err == nil && int64(len(b)) > max {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", events.ErrBlobTooLarge, len(b), max)
	}
	return b, err
}

// prepare makes room for a file or symlink at abs: parents are created, and a
// directory that is in the way is removed if (and only if) it is empty. A
// populated directory is somebody's data and is never removed.
func (s *Store) prepare(abs string, cur state) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("cannot create parent directory: %s", reason(err))
	}
	if cur.Kind == kDir {
		if err := os.Remove(abs); err != nil {
			return errors.New("a directory with contents is in the way")
		}
	}
	return nil
}

// removeCreatedDirs removes directories that tools created for files that are
// now gone again, deepest first, only when they are empty and only when they are
// still directories (a path that was a directory when the tool ran may have been
// replaced by a restored file since).
func (s *Store) removeCreatedDirs(tasks []*task, dry bool) []FileResult {
	dirs := map[string]bool{}
	for _, t := range tasks {
		if t.action != ActionDelete {
			continue
		}
		switch t.res.Outcome {
		case OutcomeDone, OutcomeUnchanged, OutcomePlanned:
			for _, d := range t.it.newDirs {
				dirs[d] = true
			}
		}
	}
	keys := make([]string, 0, len(dirs))
	for d := range dirs {
		keys = append(keys, d)
	}
	sort.Slice(keys, func(i, j int) bool {
		if di, dj := depth(keys[i]), depth(keys[j]); di != dj {
			return di > dj
		}
		return keys[i] < keys[j]
	})
	var out []FileResult
	for _, key := range keys {
		abs, err := s.resolveKey(key)
		if err != nil {
			continue // a name a manifest made up, or one that now leads out of the project: not ours to remove
		}
		fi, err := os.Lstat(abs)
		if err != nil || !fi.IsDir() {
			continue
		}
		res := FileResult{Path: key, Action: ActionRmdir}
		switch {
		case dry:
			res.Outcome, res.Detail = OutcomePlanned, "directory created by an edit; removed if empty"
		default:
			if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
				res.Outcome, res.Detail = OutcomeUnchanged, "left in place: it is not empty"
			} else if err := os.Remove(abs); err != nil {
				res.Outcome, res.Detail = OutcomeFailed, "cannot remove: "+reason(err)
			} else {
				res.Outcome = OutcomeDone
			}
		}
		out = append(out, res)
	}
	return out
}

// commitRestore forgets the paths that were rewound and, for a complete rewind,
// drops the checkpoints after idx. It runs under the exclusive gate.
func (s *Store) commitRestore(idx int, satisfied map[string]bool, rewound bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx >= len(s.cps) {
		return nil
	}
	changed := map[*checkpoint]bool{}
	for _, cp := range s.cps[idx:] {
		for key := range satisfied {
			if cp.index[key] != nil {
				cp.remove(key)
				changed[cp] = true
			}
		}
	}
	var firstErr error
	if rewound {
		for _, cp := range s.cps[idx+1:] {
			delete(changed, cp)
			if err := os.Remove(filepath.Join(s.dir, cp.ID+".json")); err != nil && !isMissing(err) && firstErr == nil {
				firstErr = fmt.Errorf("checkpoint: dropping %s: %w", cp.ID, err)
			}
		}
		s.cps = s.cps[:idx+1]
	}
	for _, cp := range s.cps {
		if changed[cp] {
			if err := s.persistLocked(cp); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
