package inspect

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxDiscoverDepth = 4
	maxSessions      = 5000
	maxLoaded        = 8        // sessions kept in memory at once in multi-session mode
	indexMaxBytes    = 64 << 20 // logs larger than this get a digest only once opened
	loadRetryAfter   = 5 * time.Second
)

// entry is one discoverable session. Its model is loaded lazily, on first use,
// in the background, so opening a very large log never blocks a request.
type entry struct {
	id, name, dir string

	mu       sync.Mutex
	bytes    int64
	mtime    time.Time
	sess     *Session
	err      error
	errAt    time.Time // when the last load failed; a failed load is retried after a few seconds
	loading  bool
	done     chan struct{}
	lastUse  time.Time
	digest   *Digest
	episode  *EpisodeInfo
	digestAt int64 // file size the digest was computed at
	indexAt  time.Time

	read, total atomic.Int64
}

// registry knows which sessions live under the root. Session ids are relative
// paths that were found by walking the root; requests can only name one of them
// (a map lookup), never supply a path.
type registry struct {
	root   string
	single bool
	opts   Options
	logf   func(string, ...any)

	mu       sync.Mutex
	entries  map[string]*entry
	indexing atomic.Bool
}

func newRegistry(root string, opts Options, logf func(string, ...any)) (*registry, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// The root is the operator's own argument, so a symlink there ("latest") is fine;
	// the walk below does not follow links, so resolve it once here.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	r := &registry{root: abs, opts: opts, logf: logf, entries: map[string]*entry{}}
	if !fi.IsDir() { // a path to events.jsonl itself
		r.root = filepath.Dir(abs)
	}
	if err := r.scan(); err != nil {
		return nil, err
	}
	return r, nil
}

// hasLog reports whether dir holds a regular events.jsonl.
func hasLog(dir string) (fs.FileInfo, bool) {
	fi, err := os.Lstat(filepath.Join(dir, "events.jsonl"))
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	return fi, true
}

// scan (re)discovers sessions. Directories named blobs or checkpoints, and
// symlinked directories, are never entered; a session's own subdirectories are
// not searched for more sessions.
func (r *registry) scan() error {
	found := map[string]fs.FileInfo{}
	single := false
	if fi, ok := hasLog(r.root); ok {
		single = true
		found["."] = fi
	} else {
		err := filepath.WalkDir(r.root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == r.root {
					return err
				}
				return nil // unreadable subdirectory: skip it
			}
			if !d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(r.root, p)
			if rel == "." {
				return nil
			}
			switch d.Name() {
			case "blobs", "checkpoints", ".git", "node_modules":
				return filepath.SkipDir
			}
			if strings.Count(filepath.ToSlash(rel), "/") >= maxDiscoverDepth {
				return filepath.SkipDir
			}
			if fi, ok := hasLog(p); ok {
				if len(found) < maxSessions {
					found[filepath.ToSlash(rel)] = fi
				}
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(found) == 0 {
		return errors.New("no events.jsonl found under " + filepath.Base(r.root))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.single = single
	for id, fi := range found {
		e := r.entries[id]
		if e == nil {
			dir := r.root
			if id != "." {
				dir = filepath.Join(r.root, filepath.FromSlash(id))
			}
			name := id
			if id == "." {
				name = filepath.Base(r.root)
			}
			e = &entry{id: id, name: name, dir: dir}
			r.entries[id] = e
		}
		e.mu.Lock()
		e.bytes, e.mtime = fi.Size(), fi.ModTime()
		e.mu.Unlock()
	}
	for id := range r.entries {
		if _, ok := found[id]; !ok {
			delete(r.entries, id)
		}
	}
	return nil
}

// get returns the entry for an id exactly as scan produced it. In single-session
// mode the empty id names the only session.
func (r *registry) get(id string) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.single && (id == "" || id == ".") {
		return r.entries["."]
	}
	return r.entries[id]
}

// only returns the sole entry when there is exactly one.
func (r *registry) only() *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) != 1 {
		return nil
	}
	for _, e := range r.entries {
		return e
	}
	return nil
}

// ensure starts loading the session if needed and waits up to wait for it.
// It returns ready=false while the load is still going (see entry.read/total).
func (e *entry) ensure(opts Options, wait time.Duration, logf func(string, ...any)) (s *Session, ready bool, err error) {
	e.mu.Lock()
	e.lastUse = time.Now()
	if e.sess != nil {
		s = e.sess
		e.mu.Unlock()
		return s, true, nil
	}
	if e.err != nil && !e.loading {
		if time.Since(e.errAt) < loadRetryAfter {
			err = e.err
			e.mu.Unlock()
			return nil, true, err
		}
		e.err = nil // the log may have been fixed or finished being written: try again
	}
	if !e.loading {
		e.loading, e.done = true, make(chan struct{})
		o := opts
		o.ID, o.Name = e.id, e.name
		if e.id == "." {
			o.ID = ""
		}
		go func(done chan struct{}) {
			s, err := LoadContext(context.Background(), e.dir, o, func(read, total int64) {
				e.read.Store(read)
				e.total.Store(total)
			})
			if err != nil && logf != nil {
				logf("inspect: loading %s: %v", e.name, err)
			}
			e.mu.Lock()
			e.sess, e.err, e.loading = s, err, false
			if err != nil {
				e.errAt = time.Now()
			}
			e.mu.Unlock()
			close(done)
		}(e.done)
	}
	done := e.done
	e.mu.Unlock()
	select {
	case <-done:
	case <-time.After(wait):
		return nil, false, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sess, true, e.err
}

// loaded returns the session if it is in memory.
func (e *entry) loaded() *Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sess
}

// trim drops the least recently used loaded sessions beyond maxLoaded (in
// single-session mode nothing is dropped).
func (r *registry) trim() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.single {
		return
	}
	var loaded []*entry
	for _, e := range r.entries {
		e.mu.Lock()
		if e.sess != nil {
			loaded = append(loaded, e)
		}
		e.mu.Unlock()
	}
	if len(loaded) <= maxLoaded {
		return
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].lastUse.After(loaded[j].lastUse) })
	for _, e := range loaded[maxLoaded:] {
		e.mu.Lock()
		if e.sess != nil {
			d, ep := e.sess.Digest(), e.sess.Episode()
			e.digest, e.episode, e.digestAt = &d, ep, e.bytes
			e.sess = nil
		}
		e.mu.Unlock()
	}
}

// tailAll polls every loaded session for new events.
func (r *registry) tailAll() {
	r.mu.Lock()
	es := make([]*entry, 0, len(r.entries))
	for _, e := range r.entries {
		es = append(es, e)
	}
	r.mu.Unlock()
	for _, e := range es {
		if s := e.loaded(); s != nil {
			if _, err := s.Tail(); err != nil {
				r.logf("inspect: tailing %s: %v", e.name, err)
			}
		}
	}
}

// indexPending computes digests for sessions that have none (or a stale one) by
// loading them one at a time and letting go of the model afterwards, so the
// session list can show hit ratio and cost without the user opening each one.
func (r *registry) indexPending(ctx context.Context) {
	if r.single || !r.indexing.CompareAndSwap(false, true) {
		return
	}
	defer r.indexing.Store(false)
	r.mu.Lock()
	es := make([]*entry, 0, len(r.entries))
	for _, e := range r.entries {
		es = append(es, e)
	}
	r.mu.Unlock()
	sort.Slice(es, func(i, j int) bool { return es[i].id < es[j].id })
	for _, e := range es {
		if ctx.Err() != nil {
			return
		}
		e.mu.Lock()
		skip := e.sess != nil || e.loading || e.bytes > indexMaxBytes ||
			(e.digest != nil && e.digestAt == e.bytes) || time.Since(e.indexAt) < 5*time.Second
		size := e.bytes
		e.indexAt = time.Now()
		e.mu.Unlock()
		if skip {
			continue
		}
		o := r.opts
		o.ID, o.Name = e.id, e.name
		s, err := LoadWith(e.dir, o)
		if err != nil {
			continue
		}
		d, ep := s.Digest(), s.Episode()
		e.mu.Lock()
		e.digest, e.episode, e.digestAt = &d, ep, size
		e.mu.Unlock()
	}
}

// list is the session list, live sessions first, then newest.
func (r *registry) list(current string) SessionList {
	r.mu.Lock()
	es := make([]*entry, 0, len(r.entries))
	for _, e := range r.entries {
		es = append(es, e)
	}
	single := r.single
	r.mu.Unlock()
	out := SessionList{Mode: "multi", Root: filepath.Base(r.root), Current: current, Sessions: []SessionInfo{}}
	if single {
		out.Mode = "single"
	}
	now := r.opts.Now
	if now == nil {
		now = time.Now
	}
	window := r.opts.LiveWindow
	if window <= 0 {
		window = 20 * time.Second
	}
	for _, e := range es {
		e.mu.Lock()
		info := SessionInfo{ID: e.id, Name: e.name, Bytes: e.bytes, Updated: e.mtime, Episode: e.episode, Digest: e.digest}
		if e.sess != nil {
			d := e.sess.Digest()
			info.Digest, info.Episode = &d, e.sess.Episode()
			info.Bytes, info.Updated = e.bytes, e.mtime
		}
		e.mu.Unlock()
		info.Live = now().Sub(info.Updated) < window
		if info.Digest != nil && info.Digest.State == StateEnded {
			info.Live = false
		}
		out.Sessions = append(out.Sessions, info)
	}
	sort.Slice(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		if a.Live != b.Live {
			return a.Live
		}
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		return a.ID < b.ID
	})
	return out
}

// Sessions scans root and returns the session list with digests, loading each
// log once (logs over 64 MiB get a digest only when opened). It is what
// `sleipnir inspect --json DIR` prints for a directory of sessions.
func Sessions(root string, opts Options) (SessionList, error) {
	opts.fill()
	r, err := newRegistry(root, opts, nil)
	if err != nil {
		return SessionList{}, err
	}
	r.indexPending(context.Background())
	list := r.list("")
	if r.single {
		list.Current = "."
	}
	return list, nil
}

// OpenSession loads one session found under root by its id (as listed by
// Sessions). The id is matched against the sessions discovered under root; it is
// never used as a path. In a directory that is itself a session the id is ignored.
func OpenSession(root, id string, opts Options) (*Session, error) {
	opts.fill()
	r, err := newRegistry(root, opts, nil)
	if err != nil {
		return nil, err
	}
	e := r.get(id)
	if e == nil && id == "" {
		e = r.only()
	}
	if e == nil {
		if id == "" {
			return nil, errors.New("inspect: this directory holds several sessions; pick one with --session")
		}
		return nil, errors.New("inspect: no such session under " + filepath.Base(r.root))
	}
	o := opts
	o.ID, o.Name = e.id, e.name
	if e.id == "." {
		o.ID = ""
	}
	return LoadWith(e.dir, o)
}
