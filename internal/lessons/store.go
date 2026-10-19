package lessons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// fileVersion is the version of the file format Store writes and reads.
const fileVersion = 1

// lockWait is how long a mutation waits for another session's lock before it gives up.
const lockWait = 10 * time.Second

// file is the JSON document on disk.
type file struct {
	Version int      `json:"version"`
	Lessons []Lesson `json:"lessons"`
}

// Store is the lessons of one JSON file. Its methods are safe for concurrent use, also by several Stores and processes over
// the same file: every access holds an exclusive lock on path+".lock", and every write replaces the file atomically.
type Store struct {
	path     string
	now      func() time.Time
	lockWait time.Duration
}

// NewStore returns the store over the file at path (such as ~/.sleipnir/lessons.json). The file and its directory are
// created by the first change.
func NewStore(path string) *Store {
	return &Store{path: path, now: time.Now, lockWait: lockWait}
}

// Path is the file the store reads and writes.
func (s *Store) Path() string { return s.path }

// pathLocks orders the Stores of one process over the same file before they take the file lock.
var pathLocks sync.Map // cleaned absolute path -> *sync.Mutex

// locked runs fn while holding the in-process mutex and the file lock of the store's path.
func (s *Store) locked(fn func() error) error {
	key, err := filepath.Abs(s.path)
	if err != nil {
		key = filepath.Clean(s.path)
	}
	m, _ := pathLocks.LoadOrStore(key, new(sync.Mutex))
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("lessons: %w", err)
	}
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("lessons: %w", err)
	}
	defer f.Close()
	deadline := time.Now().Add(s.lockWait)
	for {
		ok, err := tryLock(f)
		if err != nil {
			return fmt.Errorf("lessons: locking %s: %w", s.path, err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lessons: %s is locked by another process", s.path)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer unlockFile(f)
	return fn()
}

// Load returns the lessons in file order. A missing file is no lessons; a file that is not a lessons file is an error naming it.
func (s *Store) Load() ([]Lesson, error) {
	if _, err := os.Lstat(s.path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var ls []Lesson
	err := s.locked(func() (err error) {
		ls, err = s.read()
		return err
	})
	return ls, err
}

// List returns the lessons by utility, highest first, then most recently touched, then ID.
func (s *Store) List() ([]Lesson, error) {
	ls, err := s.Load()
	sort.SliceStable(ls, func(i, j int) bool {
		a, b := ls[i], ls[j]
		if ua, ub := a.Utility(), b.Utility(); ua != ub {
			return ua > ub
		}
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		return a.ID < b.ID
	})
	return ls, err
}

// read parses the file; the caller holds the lock.
func (s *Store) read() ([]Lesson, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lessons: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lessons: reading %s: %w", s.path, err)
	}
	if len(raw) > MaxFileBytes {
		return nil, fmt.Errorf("lessons: %s is larger than %d bytes; it was not written by this version, so it is left alone", s.path, MaxFileBytes)
	}
	var doc file
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("lessons: %s is not a lessons file (%v); fix or remove it", s.path, err)
	}
	if doc.Version != fileVersion {
		return nil, fmt.Errorf("lessons: %s has format version %d, not %d; it is left alone", s.path, doc.Version, fileVersion)
	}
	for i, l := range doc.Lessons {
		if l.ID == "" || !validKind(l.Kind) || strings.TrimSpace(l.Text) == "" {
			return nil, fmt.Errorf("lessons: %s: lesson %d has no id, kind or text; fix or remove it", s.path, i)
		}
	}
	return doc.Lessons, nil
}

// write replaces the file with ls, after evicting down to MaxLessons and MaxFileBytes. Lessons whose ID is in fresh go last.
// The caller holds the lock.
func (s *Store) write(ls []Lesson, fresh map[string]bool) error {
	ls = evict(ls, MaxLessons, fresh)
	var data []byte
	for {
		var err error
		data, err = json.Marshal(file{Version: fileVersion, Lessons: ls})
		if err != nil {
			return fmt.Errorf("lessons: %w", err)
		}
		if len(data) <= MaxFileBytes || len(ls) == 0 {
			break
		}
		ls = evict(ls, len(ls)-1, fresh)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("lessons: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("lessons: %w", err)
	}
	name := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("lessons: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("lessons: writing %s: %w", s.path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("lessons: writing %s: %w", s.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("lessons: writing %s: %w", s.path, err)
	}
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
		done = true
		return fmt.Errorf("lessons: %w", err)
	}
	done = true
	return nil
}

// evict returns ls without its worst lessons, keeping at most max in their order. Worst is: not fresh before fresh, then
// lowest utility, then least recently touched, then highest ID.
func evict(ls []Lesson, max int, fresh map[string]bool) []Lesson {
	if len(ls) <= max {
		return ls
	}
	idx := make([]int, len(ls))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, b := ls[idx[i]], ls[idx[j]]
		if fa, fb := fresh[a.ID], fresh[b.ID]; fa != fb {
			return fb
		}
		if ua, ub := a.Utility(), b.Utility(); ua != ub {
			return ua < ub
		}
		if !a.Last.Equal(b.Last) {
			return a.Last.Before(b.Last)
		}
		return a.ID > b.ID
	})
	gone := make(map[int]bool, len(ls)-max)
	for _, i := range idx[:len(ls)-max] {
		gone[i] = true
	}
	out := make([]Lesson, 0, max)
	for i, l := range ls {
		if !gone[i] {
			out = append(out, l)
		}
	}
	return out
}

// Add saves drafts. Each is cleaned (kind lowercased and checked, text and task cut to one line of MaxTextRunes and
// MaxTaskRunes); empty ones, ones of another kind and repeats within drafts are dropped. The rest are embedded in one call,
// outside the lock, so a slow endpoint does not hold up other sessions. A draft whose text is that of a lesson of its kind,
// or whose vector is within MergeSimilarity of one, is merged into it: the existing text stays and Last is refreshed.
// Otherwise it is appended. added and merged count the two.
func (s *Store) Add(ctx context.Context, emb Embedder, drafts []Draft) (added, merged int, err error) {
	var ds []Draft
	seen := map[string]bool{}
	for _, d := range drafts {
		d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
		d.Text = Clean(d.Text, MaxTextRunes)
		d.Task = Clean(d.Task, MaxTaskRunes)
		key := d.Kind + "\x00" + strings.ToLower(d.Text)
		if !validKind(d.Kind) || d.Text == "" || seen[key] {
			continue
		}
		seen[key] = true
		ds = append(ds, d)
	}
	if len(ds) == 0 {
		return 0, 0, nil
	}
	texts := make([]string, len(ds))
	for i, d := range ds {
		texts[i] = d.Text
	}
	raw, err := emb.Embed(ctx, texts)
	if err != nil {
		return 0, 0, err
	}
	if len(raw) != len(ds) {
		return 0, 0, fmt.Errorf("lessons: %d vectors for %d lessons", len(raw), len(ds))
	}
	vecs := make([][]float32, len(raw))
	for i, v := range raw {
		if vecs[i] = normalize(v); vecs[i] == nil {
			return 0, 0, fmt.Errorf("lessons: the embedding of %q is empty, zero or not finite", ds[i].Text)
		}
	}
	err = s.locked(func() error {
		ls, err := s.read()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		fresh := map[string]bool{}
		for i, d := range ds {
			id := lessonID(d.Kind, d.Text)
			best, bestSim := -1, 0.0
			for j, l := range ls {
				if l.Kind != d.Kind {
					continue
				}
				if l.ID == id {
					best, bestSim = j, 2 // the same lesson; its vector is renewed, in case the embedding model changed
					ls[j].Vec = vecs[i]
					break
				}
				if c := cosine(l.Vec, vecs[i]); c > bestSim {
					best, bestSim = j, c
				}
			}
			if best >= 0 && bestSim >= MergeSimilarity {
				ls[best].Last = now
				merged++
				continue
			}
			ls = append(ls, Lesson{ID: id, Kind: d.Kind, Text: d.Text, Task: d.Task, Vec: vecs[i], Created: now, Last: now})
			fresh[id] = true
			added++
		}
		return s.write(ls, fresh)
	})
	if err != nil {
		return 0, 0, err
	}
	return added, merged, nil
}

// Retrieve returns at most k lessons that bear on task, best first. A lesson's score is its cosine similarity to the task
// times (0.5 + Utility()), so a lesson with a record of wins outranks an equally similar untried one; lessons below
// MinSimilarity are left out whatever their utility, and equal scores are ordered by ID. Nothing is marked: what was shown is
// reported later with Record. No lessons, k <= 0 or an empty task is no embedding call.
func (s *Store) Retrieve(ctx context.Context, emb Embedder, task string, k int) ([]Lesson, error) {
	task = Clean(task, maxQueryRunes)
	if k <= 0 || task == "" {
		return nil, nil
	}
	ls, err := s.Load()
	if err != nil || len(ls) == 0 {
		return nil, err
	}
	vs, err := emb.Embed(ctx, []string{task})
	if err != nil {
		return nil, err
	}
	if len(vs) != 1 {
		return nil, fmt.Errorf("lessons: %d vectors for one task", len(vs))
	}
	q := normalize(vs[0])
	if q == nil {
		return nil, errors.New("lessons: the embedding of the task is empty, zero or not finite")
	}
	type scored struct {
		l     Lesson
		score float64
	}
	var hits []scored
	for _, l := range ls {
		if c := cosine(q, l.Vec); c >= MinSimilarity {
			hits = append(hits, scored{l, c * (0.5 + l.Utility())})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].l.ID < hits[j].l.ID
	})
	out := make([]Lesson, 0, min(k, len(hits)))
	for _, h := range hits[:min(k, len(hits))] {
		out = append(out, h.l)
	}
	return out, nil
}

// Record reports the outcome of a goal that was shown the lessons ids: each known one (once, however often it is named) gets
// a use and a win or a loss, and Last is refreshed. Then lessons with at least PruneUses uses and a utility below
// PruneUtility are removed. Unknown ids are ignored; nothing known is no write.
func (s *Store) Record(ids []string, won bool) error {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	if len(want) == 0 {
		return nil
	}
	if _, err := os.Lstat(s.path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return s.locked(func() error {
		ls, err := s.read()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		changed := false
		keep := ls[:0]
		for _, l := range ls {
			if want[l.ID] {
				l.Uses++
				if won {
					l.Wins++
				} else {
					l.Losses++
				}
				l.Last = now
				changed = true
			}
			if l.Uses >= PruneUses && l.Utility() < PruneUtility {
				changed = true
				continue
			}
			keep = append(keep, l)
		}
		if !changed {
			return nil
		}
		return s.write(keep, nil)
	})
}

// Remove deletes the lesson id and reports whether there was one.
func (s *Store) Remove(id string) (bool, error) {
	if _, err := os.Lstat(s.path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	found := false
	err := s.locked(func() error {
		ls, err := s.read()
		if err != nil {
			return err
		}
		keep := ls[:0]
		for _, l := range ls {
			if l.ID == id {
				found = true
				continue
			}
			keep = append(keep, l)
		}
		if !found {
			return nil
		}
		return s.write(keep, nil)
	})
	return found, err
}
