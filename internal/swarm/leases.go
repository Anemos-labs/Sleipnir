package swarm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Leases is the swarm's tools.Guard. It does three jobs before a write happens:
//
//   - Ownership. When paths are owned by roles (SetOwners), a write to an owned
//     path by an agent of any other role, the manager included, is refused with a
//     message that names the owning role and says to hand the work to it.
//   - Scope. A task declares the paths it may touch; a write outside the writer's
//     task scope is refused with a message that names the scope. An agent with no
//     task, or whose task declares no scope, is not restricted.
//   - Leases. An agent that writes a file holds an exclusive lease on it for as
//     long as it keeps working (a TTL, refreshed by each write), so two agents do
//     not waste effort on the same file. The manager's lease yields to a worker:
//     its edits are interventions (a one-line fix it was asked for), and the worker
//     that owns the work must not be locked out of its own file until they expire. Leases complement, not replace, the
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
// read-only roles have no tree; the manager has its own writable tree), it keeps
// enforcing ownership and the task's scope, and it turns the lease table into an
// advisory one: two writers touching the same repository-relative path raise an alert
// that names the agents (never the file) and warns of a merge conflict.
//
// The guard sees the writes of the file tools only. A shell command's writes are not
// checked in a shared tree; in an isolated run the merge queue checks ownership and
// scope again on what a submission changed, whichever tool changed it.
type Leases struct {
	ttl   time.Duration
	now   func() time.Time
	board *Board
	ev    events.Emitter
	roots []string

	owners PathOwners          // nil: no path is owned
	roleOf func(string) string // agent id -> role, for owners

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

// SetOwners makes the guard enforce role ownership of paths: a write to a path
// owners assigns to roles is refused unless roleOf(agent) is one of them. roleOf
// answers "" for an id it does not know, which no role matches.
func (l *Leases) SetOwners(owners PathOwners, roleOf func(agent string) string) {
	l.mu.Lock()
	l.owners, l.roleOf = owners, roleOf
	l.mu.Unlock()
}

// emit snapshots the lease event emitter under lock and records the action with optional path and
// holder fields, ignoring emission errors.
func (l *Leases) emit(agent, action, path, holder string) {
	l.emitWith(agent, action, path, holder, nil)
}

// emitWith is emit with extra fields in the event's data.
func (l *Leases) emitWith(agent, action, path, holder string, extra map[string]any) {
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
	for k, v := range extra {
		data[k] = v
	}
	_, _ = ev.Emit(agent, events.TypeLease, data)
}

// checkOwner refuses a write by agent to the repository-relative path rel (relOK says
// the path lies inside the repository) when rel is owned by roles the agent's role is
// not among. The refusal raises an alert, emits a lease event of action "owner" with
// the owning roles, and is returned as the error the model reads.
func (l *Leases) checkOwner(agent, rel string, relOK bool) error {
	l.mu.Lock()
	owners, roleOf := l.owners, l.roleOf
	l.mu.Unlock()
	if owners == nil || !relOK {
		return nil
	}
	role := ""
	if roleOf != nil {
		role = roleOf(agent)
	}
	shown := foreignOwners(owners, role, rel)
	if len(shown) == 0 {
		return nil
	}
	if l.board != nil {
		l.board.RaiseAlertKey("scope", "owner:"+agent, fmt.Sprintf("%s tried to change a file of %s", safeToken(agent, 24), ownersText(shown)))
	}
	l.emitWith(agent, "owner", rel, "", map[string]any{"owners": shown})
	return errors.New(ownedMsg(rel, shown, role == "manager"))
}

// foreignOwners returns the roles that own the repository-relative path rel, made
// safe to show, when role is not among them. It returns nil when owners is nil, when
// nobody owns rel, or when role owns it; an empty role owns nothing. The manager is
// held to it as every other agent is (PathOwners): it hands the work to the owner.
func foreignOwners(owners PathOwners, role, rel string) []string {
	if owners == nil {
		return nil
	}
	own := owners.OwnersOf(rel)
	if len(own) == 0 || (role != "" && slices.Contains(own, role)) {
		return nil
	}
	shown := make([]string, len(own))
	for i, r := range own {
		shown[i] = safeToken(r, 24)
	}
	return shown
}

// ownedMsg is the refusal of a write to rel, which the roles own, by an agent of
// another role: it names the owning roles and tells the writer how to hand the work
// to them (the manager creates tasks; a worker asks the manager for one). The text
// contains "belongs to the" and "may change it", which agent.classifyToolError reads
// as a scope refusal.
func ownedMsg(rel string, roles []string, manager bool) string {
	worker := article(roles[0]) + " " + joinWords(roles, "or") + " worker"
	head := fmt.Sprintf("%s belongs to %s: only %s may change it. ", displayPath(rel), ownersText(roles), worker)
	if manager {
		return head + "Create a task for " + worker + " and leave the change to it"
	}
	return head + "Leave it alone; if your task needs the change, ask the manager (mail) to create a task for " + worker
}

// ownersText names the owning roles: "the frontend role", "the frontend and docs roles".
func ownersText(roles []string) string {
	if len(roles) == 1 {
		return "the " + roles[0] + " role"
	}
	return "the " + joinWords(roles, "and") + " roles"
}

// joinWords joins words as prose: "a", "a and b", "a, b and c" (conj is the last joiner).
func joinWords(words []string, conj string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conj + " " + words[len(words)-1]
}

// article is the indefinite article for a word: "an" before a vowel letter, else "a".
func article(word string) string {
	if word != "" && strings.ContainsRune("aeiouAEIOU", rune(word[0])) {
		return "an"
	}
	return "a"
}

// relFor returns path relative to the agent's own working tree when it has one (isolated mode), else to the repository root.
func (l *Leases) relFor(agent, path string) (string, bool) {
	l.mu.Lock()
	dir, has := l.trees[agent]
	l.mu.Unlock()
	if has {
		if rel, ok := relTo(dir, path); ok {
			return rel, true
		}
	}
	return l.rel(path)
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
		if t.Pool != nil || t.Owner != agent || t.Status != StatusDoing {
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

// checkScopeAt refuses a write by agent to path outside the scope of the tasks it is
// doing, and a pool child's write outside its parent's scope (parentRefuses). The
// caller has already made the path relative to the repository root (rel; relOK says
// it lies inside it).
func (l *Leases) checkScopeAt(agent, path, rel string, relOK bool) error {
	scopes, ids, scoped := l.scopeOf(agent)
	if !scoped {
		return nil
	}
	if relOK && inScope(scopes, rel) {
		parent, refused := l.parentRefuses(agent, rel)
		if !refused {
			return nil
		}
		l.raiseScopeAlert(agent, rel, relOK, ids)
		l.emit(agent, "scope", path, "")
		return fmt.Errorf("%s is inside the scope of your task but outside the scope of its parent %s (%s). Edit only inside both; if the task needs more, ask the manager (mail)",
			displayPath(path), parent.ID, strings.Join(shownScopes(parent.Files), ", "))
	}
	l.raiseScopeAlert(agent, rel, relOK, ids)
	l.emit(agent, "scope", path, "")
	return fmt.Errorf("%s is outside the scope of your task %s (scope: %s). Edit only inside your scope; if the task needs more, ask the manager to widen it (mail)",
		displayPath(path), strings.Join(ids, ", "), strings.Join(shownScopes(scopes), ", "))
}

// raiseScopeAlert tells the manager (in its hot view) which agent tried to write which file outside the scope of which tasks,
// so that it can widen the scope with task update instead of asking the worker what it tried to write. A path outside the
// repository is not named.
func (l *Leases) raiseScopeAlert(agent, rel string, relOK bool, ids []string) {
	what := "outside the scope of its task"
	if relOK && rel != "" {
		what = safeToken(rel, 72) + " outside the scope of its task"
	}
	l.board.RaiseAlertKey("scope", "scope:"+agent, fmt.Sprintf("%s tried to write %s (%s)", safeToken(agent, 24), what, strings.Join(ids, ",")))
}

// shownScopes is a scope list as a refusal shows it: at most four entries, then an ellipsis.
func shownScopes(scopes []string) []string {
	if len(scopes) > 4 {
		return append(append([]string(nil), scopes[:4]...), "…")
	}
	return scopes
}

// parentRefuses reports the parent whose scope refuses rel when no task agent is doing allows it: rel lies in a task's scope,
// and, for a pool child whose parent declares a scope, in the parent's too. A child's scope lies inside its parent's by the paths
// it names (scopeContains); one that names a directory must not reach below it out of the parent's area.
func (l *Leases) parentRefuses(agent, rel string) (Task, bool) {
	snap := l.board.Snapshot()
	var refusing Task
	for _, t := range snap.Tasks {
		if t.Pool != nil || t.Owner != agent || t.Status != StatusDoing || !inScope(t.Files, rel) {
			continue
		}
		p, ok := snap.Task(t.Parent)
		if t.Parent == "" || !ok || len(p.Files) == 0 || inScope(p.Files, rel) {
			return Task{}, false
		}
		refusing = p
	}
	return refusing, refusing.ID != ""
}

// displayPath abbreviates long paths to a 119-byte suffix preceded by an ellipsis; it may split
// UTF-8.
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
	if err := l.checkOwner(agent, rel, true); err != nil {
		return err
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
	rel, relOK := l.rel(path)
	if err := l.checkOwner(agent, rel, relOK); err != nil {
		return err
	}
	if err := l.checkScopeAt(agent, path, rel, relOK); err != nil {
		return err
	}
	l.mu.Lock()
	now := l.now()
	cur, ok := l.held[path]
	if ok && cur.holder != agent && now.Sub(cur.last) < l.ttl {
		holder, ago, roleOf := cur.holder, now.Sub(cur.last).Round(time.Second), l.roleOf
		l.mu.Unlock()
		// the roles are looked up without the lease lock (roleOf reads the swarm); a manager's lease gives way to a worker
		if !yields(roleOf, holder, agent) {
			if l.board != nil {
				l.board.RaiseAlertKey("lease", path, fmt.Sprintf("%s wanted a file leased to %s", safeToken(agent, 24), safeToken(holder, 24)))
			}
			l.emit(agent, "conflict", path, holder)
			return fmt.Errorf("%s is being edited by %s (last write %s ago). Work on something else, or mail %s if you need a change there", path, holder, ago, holder)
		}
		l.mu.Lock()
		if c, still := l.held[path]; still && c.holder != holder && c.holder != agent && now.Sub(c.last) < l.ttl {
			// another worker took the file meanwhile: that lease does not yield
			other := c.holder
			l.mu.Unlock()
			l.emit(agent, "conflict", path, other)
			return fmt.Errorf("%s is being edited by %s. Work on something else, or mail %s if you need a change there", path, other, other)
		}
		cur, ok = l.held[path]
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

// yields reports whether a lease that holder keeps gives way to a write by agent: the manager's lease does to any other agent.
func yields(roleOf func(string) string, holder, agent string) bool {
	return roleOf != nil && roleOf(holder) == "manager" && roleOf(agent) != "manager"
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
		l.board.ClearAlertKey("scope", "owner:"+agent)
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

// LeaseInfo is one entry of the lease table as Snapshot reports it. In a shared tree it
// is a write lease: Path is the absolute path the tools wrote, Holder the agent that
// holds it, Last its last write and Expires when it lapses without another. In an
// isolated run it is an advisory mark: Path is repository-relative, Agents every agent
// that wrote it recently (Holder the latest), and Isolated is set.
type LeaseInfo struct {
	Path     string
	Holder   string
	Agents   []string
	Last     time.Time
	Expires  time.Time
	Isolated bool
}

// Snapshot lists the leases in force (expired ones are left out), sorted by path.
func (l *Leases) Snapshot() []LeaseInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var out []LeaseInfo
	for p, cur := range l.held {
		if now.Sub(cur.last) >= l.ttl {
			continue
		}
		out = append(out, LeaseInfo{Path: p, Holder: cur.holder, Agents: []string{cur.holder}, Last: cur.last, Expires: cur.last.Add(l.ttl)})
	}
	for rel, m := range l.marks {
		info := LeaseInfo{Path: rel, Isolated: true}
		for a, at := range m {
			if now.Sub(at) >= l.ttl {
				continue
			}
			info.Agents = append(info.Agents, a)
			if at.After(info.Last) || (at.Equal(info.Last) && a > info.Holder) {
				info.Last, info.Holder = at, a
			}
		}
		if len(info.Agents) == 0 {
			continue
		}
		sort.Strings(info.Agents)
		info.Expires = info.Last.Add(l.ttl)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// LeaseScope is a task scope that covers a path: the task's owner, the task and the
// scope entry that matched.
type LeaseScope struct {
	Agent, Task, Glob string
}

// Covering finds the task scope that covers a repository-relative path: a task in
// progress whose scope matches it, as the guard would match a write by its owner
// (the first such task in board order).
func (l *Leases) Covering(rel string) (LeaseScope, bool) {
	if l.board == nil || rel == "" {
		return LeaseScope{}, false
	}
	for _, t := range l.board.Snapshot().Tasks {
		if t.Status != StatusDoing || t.Owner == "" {
			continue
		}
		for _, g := range t.Files {
			if inScope([]string{g}, rel) {
				return LeaseScope{Agent: t.Owner, Task: t.ID, Glob: g}, true
			}
		}
	}
	return LeaseScope{}, false
}

// Roots are the directories the guard makes absolute paths relative to.
func (l *Leases) Roots() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.roots...)
}

// Len is the number of lease entries (held or expired but not yet swept).
func (l *Leases) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held) + len(l.marks)
}
