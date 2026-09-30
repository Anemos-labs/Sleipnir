package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

// Leases is the swarm's tools.Guard. It does two jobs before a write happens:
//
//   - Scope. A task declares the paths it may touch; a write outside the writer's
//     task scope is refused with a message that names the scope. An agent with no
//     task, or whose task declares no scope, is not restricted.
//   - Leases. An agent that writes a file holds an exclusive lease on it for as
//     long as it keeps working (a TTL, refreshed by each write), so two agents do
//     not waste effort on the same file. Leases complement, not replace, the
//     content-hash staleness check in tools.FileState: that check guarantees
//     correctness even if a lease is stolen.
//
// Reads are never blocked, only writes, so agents can freely study each other's
// files.
//
// In an isolated swarm (swarm.isolation = "worktree", see Isolate) every writer
// edits a git worktree of its own, so exclusion between writers is unnecessary and a
// lease never blocks a write. The guard then does three other things: it confines
// each writer's writes to its own tree (and refuses writes by agents that have none:
// the manager and read-only roles do not edit files in an isolated run, because
// anything written into the shared checkout would bypass the merge queue), it keeps
// enforcing the task's scope, and it turns the lease table into an advisory one: two
// writers touching the same repository-relative path raise an alert that names the
// agents (never the file) and warns of a merge conflict.
type Leases struct {
	ttl   time.Duration
	now   func() time.Time
	board *Board
	ev    events.Emitter
	roots []string

	mu       sync.Mutex
	held     map[string]*lease
	isolated bool
	trees    map[string]string               // isolated: agent -> the directory of its tree
	marks    map[string]map[string]time.Time // isolated: repository-relative path -> agent -> last write
}

type lease struct {
	holder string
	last   time.Time
}

// maxLeases is the table size above which every write also sweeps expired leases.
const maxLeases = 512

// NewLeases returns a lease table. board may be nil.
func NewLeases(ttl time.Duration, board *Board) *Leases {
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	return &Leases{ttl: ttl, now: time.Now, board: board, ev: events.Discard{}, held: map[string]*lease{},
		trees: map[string]string{}, marks: map[string]map[string]time.Time{}}
}

// Isolate puts the guard in isolated mode: see the type's comment.
func (l *Leases) Isolate() {
	l.mu.Lock()
	l.isolated = true
	l.mu.Unlock()
}

// BindTree tells an isolated guard which directory is an agent's own tree: its
// writes must lie inside it, and scopes are matched against paths relative to it.
func (l *Leases) BindTree(agent, dir string) {
	if agent == "" || dir == "" {
		return
	}
	l.mu.Lock()
	l.trees[agent] = filepath.Clean(dir)
	l.mu.Unlock()
}

// UnbindTree forgets an agent's tree (it is gone) and its advisory marks.
func (l *Leases) UnbindTree(agent string) {
	l.mu.Lock()
	delete(l.trees, agent)
	l.mu.Unlock()
	l.ReleaseAll(agent)
}

// SetEmitter sends lease events (acquire, conflict, scope, release) to ev.
func (l *Leases) SetEmitter(ev events.Emitter) {
	if ev == nil {
		ev = events.Discard{}
	}
	l.mu.Lock()
	l.ev = ev
	l.mu.Unlock()
}

// SetRoots tells the guard which directories are the repository root: absolute
// paths are made relative to them to be checked against a task's scope.
func (l *Leases) SetRoots(roots ...string) {
	var rs []string
	for _, r := range roots {
		if r != "" {
			rs = append(rs, filepath.Clean(r))
		}
	}
	l.mu.Lock()
	l.roots = rs
	l.mu.Unlock()
}

func (l *Leases) emit(agent, action, path, holder string) {
	l.mu.Lock()
	ev := l.ev
	l.mu.Unlock()
	data := map[string]any{"action": action, "agent": agent}
	if path != "" {
		data["path"] = path
	}
	if holder != "" {
		data["holder"] = holder
	}
	_, _ = ev.Emit(agent, events.TypeLease, data)
}

// rel returns path relative to the repository root, as a slash path.
func (l *Leases) rel(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return relTo("", path)
	}
	l.mu.Lock()
	roots := l.roots
	l.mu.Unlock()
	if len(roots) == 0 {
		if wd, err := os.Getwd(); err == nil {
			roots = []string{wd}
		}
	}
	for _, r := range roots {
		if rel, ok := relTo(r, path); ok {
			return rel, true
		}
	}
	return "", false
}

// scopeOf returns the scope an agent is bound to: the union of the scopes of the
// tasks it is doing, and those tasks' ids. Unscoped means no restriction.
func (l *Leases) scopeOf(agent string) (scopes, ids []string, scoped bool) {
	if l.board == nil {
		return nil, nil, false
	}
	for _, t := range l.board.Snapshot().Tasks {
		if t.Owner != agent || t.Status != StatusDoing {
			continue
		}
		if len(t.Files) == 0 {
			return nil, nil, false
		}
		scopes = append(scopes, t.Files...)
		ids = append(ids, t.ID)
	}
	return scopes, ids, len(ids) > 0
}

func (l *Leases) checkScope(agent, path string) error {
	rel, ok := l.rel(path)
	return l.checkScopeAt(agent, path, rel, ok)
}

// checkScopeAt is checkScope for a caller that has already made the path relative
// to the repository root (ok says it lies inside it).
func (l *Leases) checkScopeAt(agent, path, rel string, relOK bool) error {
	scopes, ids, scoped := l.scopeOf(agent)
	if !scoped {
		return nil
	}
	if relOK && inScope(scopes, rel) {
		return nil
	}
	shown := scopes
	if len(shown) > 4 {
		shown = append(append([]string(nil), shown[:4]...), "…")
	}
	l.board.RaiseAlertKey("scope", "scope:"+agent, fmt.Sprintf("%s tried to write outside the scope of its task (%s)", safeToken(agent, 24), strings.Join(ids, ",")))
	l.emit(agent, "scope", path, "")
	return fmt.Errorf("%s is outside the scope of your task %s (scope: %s). Edit only inside your scope; if the task needs more, ask the manager to widen it (mail)",
		displayPath(path), strings.Join(ids, ", "), strings.Join(shown, ", "))
}

func displayPath(p string) string {
	if len(p) > 120 {
		p = "…" + p[len(p)-119:]
	}
	return p
}

// beforeIsolatedWrite is BeforeWrite in isolated mode: confine the write to the
// agent's own tree, enforce the scope, and note the overlap with other writers.
func (l *Leases) beforeIsolatedWrite(agent, path string) error {
	l.mu.Lock()
	dir, has := l.trees[agent]
	l.mu.Unlock()
	if !has {
		return fmt.Errorf("this run gives every writer a git worktree of its own, and %s has none: it does not edit files. Ask the manager (mail) or, as the manager, spawn a worker for the change: the harness verifies and merges its work", safeToken(agent, 24))
	}
	rel, ok := relTo(dir, path)
	if !ok {
		return fmt.Errorf("%s is outside your working tree: this run gives every writer its own git worktree, so edit files under your working directory (the paths there are the project's paths)", displayPath(path))
	}
	if err := l.checkScopeAt(agent, path, rel, true); err != nil {
		return err
	}
	l.advise(agent, rel)
	return nil
}

// advise records that agent writes rel and, if another writer wrote it recently,
// raises an alert (advisory: the write goes ahead, the merge queue settles it).
func (l *Leases) advise(agent, rel string) {
	now := l.now()
	l.mu.Lock()
	m := l.marks[rel]
	if m == nil {
		if len(l.marks) >= maxLeases {
			l.sweepMarksLocked(now)
		}
		m = map[string]time.Time{}
		l.marks[rel] = m
	}
	m[agent] = now
	var others []string
	for a, t := range m {
		if a != agent && now.Sub(t) < l.ttl {
			others = append(others, a)
		}
	}
	l.mu.Unlock()
	if len(others) == 0 {
		return
	}
	sort.Strings(others)
	who := append([]string{agent}, others...)
	sort.Strings(who)
	if l.board != nil {
		l.board.RaiseAlertKey("lease", rel, fmt.Sprintf("%s edit the same file in separate trees; expect a merge conflict", safeList(who, 4)))
	}
	l.emit(agent, "overlap", rel, others[0])
}

// safeList joins agent ids for an alert: harness-made ids only, at most n named.
func safeList(ids []string, n int) string {
	var out []string
	for i, id := range ids {
		if i == n {
			out = append(out, fmt.Sprintf("and %d more", len(ids)-n))
			break
		}
		out = append(out, safeToken(id, 24))
	}
	return strings.Join(out, ", ")
}

// sweepMarksLocked drops expired advisory marks and returns the paths that lost one
// (the alerts about them are stale: a write that is still contested raises it again).
func (l *Leases) sweepMarksLocked(now time.Time) []string {
	var changed []string
	for rel, m := range l.marks {
		dropped := false
		for a, t := range m {
			if now.Sub(t) >= l.ttl {
				delete(m, a)
				dropped = true
			}
		}
		if len(m) == 0 {
			delete(l.marks, rel)
		}
		if dropped {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	return changed
}

// BeforeWrite implements tools.Guard.
func (l *Leases) BeforeWrite(agent, path string) error {
	path = filepath.Clean(path)
	l.mu.Lock()
	isolated := l.isolated
	l.mu.Unlock()
	if isolated {
		return l.beforeIsolatedWrite(agent, path)
	}
	if err := l.checkScope(agent, path); err != nil {
		return err
	}
	l.mu.Lock()
	now := l.now()
	cur, ok := l.held[path]
	if ok && cur.holder != agent && now.Sub(cur.last) < l.ttl {
		holder, ago := cur.holder, now.Sub(cur.last).Round(time.Second)
		l.mu.Unlock()
		if l.board != nil {
			l.board.RaiseAlertKey("lease", path, fmt.Sprintf("%s wanted a file leased to %s", safeToken(agent, 24), safeToken(holder, 24)))
		}
		l.emit(agent, "conflict", path, holder)
		return fmt.Errorf("%s is being edited by %s (last write %s ago). Work on something else, or mail %s if you need a change there", path, holder, ago, holder)
	}
	acquired := !ok || cur.holder != agent
	l.held[path] = &lease{holder: agent, last: now}
	if len(l.held) > maxLeases {
		l.sweepLocked(now)
	}
	l.mu.Unlock()
	if acquired {
		l.emit(agent, "acquire", path, "")
	}
	return nil
}

// AfterWrite implements tools.Guard: a write refreshes the lease.
func (l *Leases) AfterWrite(agent, path string) {
	path = filepath.Clean(path)
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.held[path]; ok && cur.holder == agent {
		cur.last = l.now()
	}
}

// sweepLocked deletes expired leases and returns their paths.
func (l *Leases) sweepLocked(now time.Time) []string {
	var gone []string
	for p, cur := range l.held {
		if now.Sub(cur.last) >= l.ttl {
			delete(l.held, p)
			gone = append(gone, p)
		}
	}
	return gone
}

// Sweep drops expired leases (and the alerts about them).
func (l *Leases) Sweep() {
	l.mu.Lock()
	now := l.now()
	gone := append(l.sweepLocked(now), l.sweepMarksLocked(now)...)
	l.mu.Unlock()
	if l.board != nil {
		for _, p := range gone {
			l.board.ClearAlertKey("lease", p)
		}
	}
}

// ReleaseAll drops every lease an agent holds and returns the paths. The alerts
// about contention on those files go with them.
func (l *Leases) ReleaseAll(agent string) []string {
	l.mu.Lock()
	var out []string
	for p, cur := range l.held {
		if cur.holder == agent {
			out = append(out, p)
			delete(l.held, p)
		}
	}
	// Isolated mode: the agent's advisory marks. The alert about a file goes when
	// fewer than two writers still have it marked.
	for rel, m := range l.marks {
		if _, marked := m[agent]; !marked {
			continue
		}
		delete(m, agent)
		out = append(out, rel)
		if len(m) == 0 {
			delete(l.marks, rel)
		}
	}
	l.mu.Unlock()
	sort.Strings(out)
	if l.board != nil {
		for _, p := range out {
			l.board.ClearAlertKey("lease", p)
		}
		l.board.ClearAlertKey("scope", "scope:"+agent)
	}
	if len(out) > 0 {
		l.emit(agent, "release", "", "")
	}
	return out
}

// Holder reports who holds a path.
func (l *Leases) Holder(path string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.held[filepath.Clean(path)]
	if !ok || l.now().Sub(cur.last) >= l.ttl {
		return "", false
	}
	return cur.holder, true
}

// HeldBy lists an agent's leases.
func (l *Leases) HeldBy(agent string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for p, cur := range l.held {
		if cur.holder == agent {
			out = append(out, p)
		}
	}
	for rel, m := range l.marks {
		if _, ok := m[agent]; ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// Len is the number of lease entries (held or expired but not yet swept).
func (l *Leases) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held) + len(l.marks)
}
