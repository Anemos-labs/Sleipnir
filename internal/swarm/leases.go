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
type Leases struct {
	ttl   time.Duration
	now   func() time.Time
	board *Board
	ev    events.Emitter
	roots []string

	mu   sync.Mutex
	held map[string]*lease
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
	return &Leases{ttl: ttl, now: time.Now, board: board, ev: events.Discard{}, held: map[string]*lease{}}
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
	scopes, ids, scoped := l.scopeOf(agent)
	if !scoped {
		return nil
	}
	if rel, ok := l.rel(path); ok && inScope(scopes, rel) {
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

// BeforeWrite implements tools.Guard.
func (l *Leases) BeforeWrite(agent, path string) error {
	path = filepath.Clean(path)
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
	gone := l.sweepLocked(l.now())
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
	sort.Strings(out)
	return out
}

// Len is the number of lease entries (held or expired but not yet swept).
func (l *Leases) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held)
}
