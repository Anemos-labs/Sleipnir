package swarm

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Auto-docs (Config.AutoDocs) keeps the team's documentation current without the
// manager asking for it. Every task the board closes as done (an accepted
// implementation task or an accepted agreement) that the docs worker did not do
// becomes a pending change; when the docs worker is idle, or the team's docs slot is
// free, the harness turns all pending changes into one "docs: catch up" task and
// gives it to the docs worker (reusing an idle one, whose context is warm). It never
// interrupts a running docs worker, and a docs task's own acceptance is not a change,
// so the keeper cannot feed itself. The first pass waits for the first accepted
// implementation task: an agreement accepted before any code exists describes code
// that is not there yet, so it stays pending and is folded into that first pass.

// DocsRole is the role auto-docs gives its catch-up tasks to.
const DocsRole = "docs"

// DocsTaskTitle starts the title of every catch-up task the harness creates.
const DocsTaskTitle = "docs: catch up"

// docsScope is the scope every catch-up task has: the Markdown at the root and the docs
// directory. Code stays with the roles that own it, and so does Markdown inside a
// code directory: "**/*.md" would overlap every directory a task holds or will claim
// (internal/api/** may hold a README; web/index.html, not there yet, may become a
// directory), so a pass scoped so waited for every code task to close, and while it
// ran no task scoped to a directory could be claimed. The Markdown files that exist
// beside the changed code are added by name (docsScopeFor).
var docsScope = []string{"*.md", "docs/**"}

// docsScopeFor is docsScope plus the Markdown files that exist beside the changed code: for each file or directory a change's
// scope names, the *.md files of its directory (a module's README is what its change made stale), or, when that directory has none,
// of the nearest directory above it that has some (svc/README.md for a change to svc/ratelimit/: a docs worker scoped without it
// mailed the manager for it and waited a minute for an answer that never came). The root's Markdown is in docsScope. Named files, never globs, so a
// later code task on that directory collides with those files alone and only while the pass runs. Entries whose directory cannot
// be told (a glob with no literal directory) add nothing.
func (s *Swarm) docsScopeFor(b []docsChange) []string {
	out := append([]string(nil), docsScope...)
	root := s.deps.Root
	if root == "" {
		return out
	}
	seen := map[string]bool{}
	for _, c := range b {
		for _, f := range c.files {
			dir := docsDirOf(f)
			if dir == "" || dir == "." || dir == "docs" || seen[dir] {
				continue
			}
			var matches []string
			for d := dir; d != "." && d != "docs"; d = path.Dir(d) {
				if d != dir && seen[d] {
					break // searched already, from another change: its Markdown is in, or it and those above have none
				}
				seen[d] = true
				if matches, _ = filepath.Glob(filepath.Join(root, filepath.FromSlash(d), "*.md")); len(matches) > 0 {
					break
				}
			}
			sort.Strings(matches)
			for _, m := range matches {
				if rel, err := filepath.Rel(root, m); err == nil && filepath.IsLocal(rel) {
					out = append(out, filepath.ToSlash(rel))
				}
			}
		}
	}
	return out
}

// docsDirOf is the literal directory a scope entry names: the entry itself for a directory ("svc/ratelimit/", "web"), its
// directory for a file ("lru/lru.go" is lru), the part before the first glob character ("internal/api/**" is internal/api), and ""
// when nothing literal is left or the entry is absolute.
func docsDirOf(entry string) string {
	e := strings.TrimSpace(entry)
	if e == "" || strings.HasPrefix(e, "/") || strings.HasPrefix(e, "~") {
		return ""
	}
	if i := strings.IndexAny(e, "*?[{"); i >= 0 {
		e = e[:i]
		if j := strings.LastIndex(e, "/"); j >= 0 {
			return path.Clean(e[:j])
		}
		return ""
	}
	if strings.HasSuffix(e, "/") || path.Ext(e) == "" {
		return path.Clean(e)
	}
	return path.Dir(e)
}

// unneeded reports whether a task was closed as no longer needed (task resolve) without its change reaching the shared tree in
// this run (landed): such a task leaves no document stale. In a team run a task resolved as a duplicate of the one that did the
// work was listed in a catch-up pass beside it. A task resolved after its work merged (in review, under isolation) is still a
// change: its code ships.
func unneeded(t Task, landed map[string]bool) bool {
	return t.Closure.Kind() == CloseUnneeded && !landed[t.ID]
}

// docsRetry is how long the keeper waits before it tries again to start a catch-up
// task that could not be started (no free slot, a scope overlap, the budget).
const docsRetry = 30 * time.Second

// docsChange is one change waiting for the docs worker.
type docsChange struct {
	task, title, kind string
	files             []string
}

// docsKeeper is the auto-docs state of a swarm.
type docsKeeper struct {
	mu      sync.Mutex
	primed  bool
	seen    map[string]uint64 // last observed terminal revision; same-ID repairs are new changes
	pending []docsChange
	open    string    // the catch-up task in progress ("" none)
	waiting string    // why open could not be started at the last attempt ("" it was)
	last    string    // the last task a finished pass covered
	passes  int       // finished passes
	retryAt time.Time // no new attempt before this
	coded   bool      // an implementation task has been accepted: agreements alone may start a pass

	// edited are the product files the manager changed by itself since the last pass took them (noteManagerEdit), and
	// editedAt is when it last changed one: no task's acceptance covers those edits, so they become one change of their
	// own once the manager has left them alone for docsEditQuiet, or once it means to finish (flushManagerEdits).
	edited   []string
	editedAt time.Time
}

// docsEditQuiet is how long the manager's own edits wait for more before they become a change: the edits of one piece of
// work come in a burst, and a pass started in the middle of it would need another.
const docsEditQuiet = 90 * time.Second

// managerEdits is the change the manager's own edits are listed as.
const managerEdits = "manager"

// noteManagerEdit records a product file the manager changed by itself. Tests and documentation leave no document stale.
// A manager that implemented two of four modules itself left their READMEs to a docs task it created by hand after the
// last review, eight minutes after its edits, while the docs worker sat idle.
func (s *Swarm) noteManagerEdit(p string) {
	k := s.docs
	if k == nil || p == "" || testFile(p) || docsFile(p) {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.editedAt = s.deps.Now()
	for _, f := range k.edited {
		if f == p {
			return
		}
	}
	k.edited = append(k.edited, p)
}

// flushManagerEdits turns the manager's own edits into a pending change now: the manager means to finish, so no more
// edits are coming.
func (s *Swarm) flushManagerEdits() {
	if k := s.docs; k != nil {
		k.mu.Lock()
		k.takeEditsLocked()
		k.mu.Unlock()
	}
}

// takeEditsLocked moves the manager's edits into pending as one change.
func (k *docsKeeper) takeEditsLocked() {
	if len(k.edited) == 0 {
		return
	}
	k.pending = append(k.pending, docsChange{task: managerEdits, title: "edits the manager made itself", kind: "change", files: k.edited})
	k.edited, k.coded = nil, true
}

// newDocsKeeper returns the keeper of a swarm whose team has a docs slot and asks
// for auto-docs, else nil.
func newDocsKeeper(cfg Config) *docsKeeper {
	if !cfg.AutoDocs {
		return nil
	}
	if n, _ := TeamCounts(cfg.Team); n[DocsRole] == 0 {
		return nil
	}
	return &docsKeeper{seen: map[string]uint64{}}
}

// AutoDocs reports whether the harness keeps the docs worker at work by itself.
func (s *Swarm) AutoDocs() bool { return s.docs != nil }

// docsTick is the keeper's supervisor step: collect newly finished tasks, close a
// finished pass, and start the next one when there is something to do and a docs
// worker to do it.
func (s *Swarm) docsTick(now time.Time) {
	k := s.docs
	if k == nil {
		return
	}
	snap := s.Board.Snapshot()
	landed := s.review.landedIDs()
	k.mu.Lock()
	if !k.primed {
		// What was done before the keeper first looked (a resumed session) is not news.
		for _, t := range snap.Tasks {
			if t.Status == StatusDone || t.Status == StatusFailed {
				k.seen[t.ID] = t.Rev
				k.coded = k.coded || t.Status == StatusDone && s.implementationTask(t) && !unneeded(t, landed)
			}
		}
		k.primed = true
	}
	for _, t := range snap.Tasks {
		if rev, seen := k.seen[t.ID]; (seen && rev == t.Rev) || (t.Status != StatusDone && t.Status != StatusFailed) {
			continue
		}
		k.seen[t.ID] = t.Rev
		if t.Status != StatusDone || s.isDocsTask(t) || t.Kind != TaskKindPlan && !s.implementationTask(t) || testsOnly(t.Files) || unneeded(t, landed) {
			continue
		}
		kind := "change"
		if t.Kind == TaskKindPlan {
			kind = "agreement"
		} else {
			k.coded = true
		}
		k.pending = append(k.pending, docsChange{task: t.ID, title: t.Title, kind: kind, files: t.Files})
	}
	if len(k.edited) > 0 && now.Sub(k.editedAt) >= docsEditQuiet {
		k.takeEditsLocked()
	}
	if k.open != "" {
		t, ok := snap.Task(k.open)
		if ok && t.Status == StatusTodo && t.Owner == "" {
			// The pass went back to the board (its run was refused, interrupted or handed
			// back): it is the docs worker's again as soon as one is idle.
			open, due := k.open, !now.Before(k.retryAt)
			k.mu.Unlock()
			if agentID, busy := s.docsWorker(); due && !busy {
				_, err := s.Spawn(SpawnReq{Role: DocsRole, Agent: agentID, TaskID: open, By: "harness"})
				k.mu.Lock()
				k.waiting = ""
				if err != nil {
					k.retryAt, k.waiting = now.Add(docsRetry), cleanText(err.Error(), 160)
				}
				k.mu.Unlock()
			}
			return
		}
		if ok && t.Status != StatusDone && t.Status != StatusFailed {
			k.mu.Unlock()
			return
		}
		if ok && t.Status == StatusDone {
			k.passes++
		}
		k.open, k.waiting = "", ""
	}
	if len(k.pending) == 0 || !k.coded || now.Before(k.retryAt) {
		k.mu.Unlock()
		return
	}
	batch := append([]docsChange(nil), k.pending...)
	k.mu.Unlock()

	agentID, busy := s.docsWorker()
	if busy {
		return // the docs worker is at work: the changes wait for it
	}
	// The pass is put on the board first and then assigned by id: a start that fails
	// leaves this one task in todo, which the next tick hands out again (above),
	// never a second task for the same changes.
	task, err := s.Board.CreateTask("harness", TaskSpec{Title: DocsTaskTitle + " (" + docsSummary(batch) + ")", Desc: docsBrief(batch), Role: DocsRole, Files: s.docsScopeFor(batch)})
	k.mu.Lock()
	if err != nil {
		k.retryAt, k.waiting = now.Add(docsRetry), cleanText(err.Error(), 160)
		k.mu.Unlock()
		return
	}
	k.pending = k.pending[len(batch):]
	k.open, k.waiting = task.ID, ""
	for i := len(batch) - 1; i >= 0; i-- {
		if batch[i].task != managerEdits {
			k.last = batch[i].task
			break
		}
	}
	k.mu.Unlock()
	id, err := s.Spawn(SpawnReq{Role: DocsRole, Agent: agentID, TaskID: task.ID, By: "harness"})
	if err != nil {
		k.mu.Lock()
		k.retryAt, k.waiting = now.Add(docsRetry), cleanText(err.Error(), 160)
		k.mu.Unlock()
		return
	}
	s.emit("docs.pass", map[string]any{"agent": id, "task": task.ID, "changes": len(batch)})
}

// isDocsTask reports whether t is the docs worker's own work: a catch-up task, or a
// task owned by a docs worker.
func (s *Swarm) isDocsTask(t Task) bool {
	if strings.HasPrefix(t.Title, DocsTaskTitle) {
		return true
	}
	if t.Role == DocsRole {
		return true
	}
	return t.Owner != "" && s.roleOf(t.Owner) == DocsRole
}

// docsWorker returns an idle docs worker to reuse ("" when there is none) and
// whether every docs worker is busy.
func (s *Swarm) docsWorker() (string, bool) {
	s.mu.Lock()
	var ms []*member
	for _, m := range s.members {
		if m.role == DocsRole && !m.manager && !m.service {
			ms = append(ms, m)
		}
	}
	s.mu.Unlock()
	sort.Slice(ms, func(i, j int) bool { return ms[i].id < ms[j].id })
	for _, m := range ms {
		m.mu.Lock()
		idle := m.life == lifeIdle
		m.mu.Unlock()
		if idle {
			return m.id, false
		}
	}
	if len(ms) == 0 {
		return "", false
	}
	s.mu.Lock()
	free := s.freeSlotErr(DocsRole) == nil
	s.mu.Unlock()
	return "", !free
}

// docsSummary is the short form of a batch for the task title: "T3, T5 and 2 more".
func docsSummary(b []docsChange) string {
	ids := make([]string, 0, len(b))
	for _, c := range b {
		ids = append(ids, c.task)
	}
	if len(ids) > 3 {
		return strings.Join(ids[:3], ", ") + fmt.Sprintf(" and %d more", len(ids)-3)
	}
	return strings.Join(ids, ", ")
}

// docsBrief is the catch-up task's description: what changed, and what to keep
// current.
func docsBrief(b []docsChange) string {
	var sb strings.Builder
	sb.WriteString("Standing docs pass, started by the harness. Read each change (task get <id>; the code it touched) and bring the documentation up to date with what is merged or accepted:\n")
	for _, c := range b {
		fmt.Fprintf(&sb, "- %s %s: %s", c.kind, c.task, cleanText(c.title, maxTitleRunes))
		if len(c.files) > 0 {
			fmt.Fprintf(&sb, " (%s)", strings.Join(c.files, ", "))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Keep three readers current: people (README, docs/), agents of later sessions (AGENTS.md: commands, layout, conventions, decisions), and this team (AGENTS.md is also its map, with a Capabilities section: one line per existing system, what it does, where it lives, its doc as path#heading). Change only what these changes made wrong or missing, in the documents that exist (your scope names the Markdown beside the changed code); a catch-up adds no README or AGENTS.md to a project that has none, which is a task of its own for the manager to create. Keep the pass short: read the diff of the files above (git diff -- <files>, or git log -p when they are committed) rather than whole sources, fix the lines it made wrong, and finish with task done and a one-line summary of what you changed. Do not test or probe the code: the reviewer and the verifier do; run a documented command only when the change altered it.")
	return sb.String()
}

// DocsState is the keeper's line for the live view, or "" without auto-docs:
// "docs (auto): 2 changes pending · last pass after T7".
func (s *Swarm) DocsState() string {
	k := s.docs
	if k == nil {
		return ""
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var parts []string
	switch {
	case k.open != "" && k.waiting != "":
		parts = append(parts, "pass "+k.open+" waiting to start: "+k.waiting)
	case k.waiting != "":
		parts = append(parts, "pass waiting to start: "+k.waiting)
	case k.open != "":
		parts = append(parts, "pass "+k.open+" in progress")
	case len(k.pending) > 0 && !k.coded:
		parts = append(parts, fmt.Sprintf("%d agreement(s) pending until the first implementation task is accepted", len(k.pending)))
	case len(k.pending) > 0:
		parts = append(parts, fmt.Sprintf("%d change(s) pending", len(k.pending)))
	case len(k.edited) > 0:
		parts = append(parts, fmt.Sprintf("%d file(s) the manager edited wait for its edits to settle", len(k.edited)))
	default:
		parts = append(parts, "up to date")
	}
	if k.open != "" && len(k.pending) > 0 {
		parts = append(parts, fmt.Sprintf("%d more pending", len(k.pending)))
	}
	if k.last != "" {
		parts = append(parts, "last pass covered up to "+k.last)
	}
	return "docs (auto): " + strings.Join(parts, " · ")
}

// docsWillStart reports whether auto-docs has work (docsPending) or is about to collect some: a task finished since the keeper last
// looked that would start a pass. The keeper collects on the supervisor's tick, so a manager that waits right after an accept sees
// the board before the keeper does.
func (s *Swarm) docsWillStart(snap *Snapshot) bool {
	k := s.docs
	if k == nil {
		return false
	}
	if s.docsPending() {
		return true
	}
	landed := s.review.landedIDs()
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.primed {
		return false // what was done before the keeper first looks is not news to it (docsTick)
	}
	for _, t := range snap.Tasks {
		if rev, seen := k.seen[t.ID]; t.Status != StatusDone || (seen && rev == t.Rev) || s.isDocsTask(t) || testsOnly(t.Files) || unneeded(t, landed) {
			continue
		}
		if s.implementationTask(t) || t.Kind == TaskKindPlan && k.coded {
			return true
		}
	}
	return false
}

// docsPending reports whether auto-docs still has work: a pass in progress, pending
// changes that may start one (agreements alone, before any implementation task was
// accepted, start nothing and hold nobody), or edits of the manager's not yet taken.
func (s *Swarm) docsPending() bool {
	k := s.docs
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.open != "" || len(k.pending) > 0 && k.coded || len(k.edited) > 0
}

// testsOnly reports whether every file a task's scope names is a test file (by the common conventions: a _test.go,
// test_*.py, *_test.py, *.test.ts or *.spec.js name, or a path under a test, tests, __tests__, spec or testdata
// directory). Such a change leaves no document stale, so it starts no docs pass. An empty scope, or a scope that names
// a directory or a glob that is not a test pattern, is not tests only.
func testsOnly(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !testFile(f) {
			return false
		}
	}
	return true
}

func testFile(f string) bool {
	f = filepath.ToSlash(strings.TrimSpace(f))
	if f == "" || strings.HasSuffix(f, "/") || strings.ContainsAny(f, "*?[") {
		return false // a directory or a glob names more than test files
	}
	base := path.Base(f)
	if strings.HasPrefix(base, ".") || !strings.Contains(base, ".") {
		return false
	}
	for _, dir := range strings.Split(path.Dir(f), "/") {
		switch dir {
		case "test", "tests", "__tests__", "spec", "testdata":
			return true
		}
	}
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch {
	case ext == ".go":
		return strings.HasSuffix(stem, "_test")
	case ext == ".py":
		return strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test")
	case codeExt[ext]:
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") || strings.HasSuffix(stem, "_spec") || strings.HasSuffix(stem, "_test")
	}
	return false
}

// codeExt are the source extensions whose test files are told apart by a .test, .spec, _spec or _test suffix; a document
// or a data file with such a suffix (api_spec.md) is not a test.
var codeExt = map[string]bool{
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".rb": true, ".rs": true, ".java": true, ".kt": true, ".kts": true, ".scala": true, ".cs": true, ".php": true,
	".ex": true, ".exs": true, ".swift": true, ".dart": true, ".c": true, ".cc": true, ".cpp": true, ".m": true, ".zig": true,
}
