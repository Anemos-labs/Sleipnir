package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/shellparse"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The stop guard.
//
// A batch run (sleipnir run --swarm, sleipnir swarm) ends when the manager's run
// returns, and the session closes with it: a manager that gives its final answer
// while workers are still running or submissions wait for a verdict cuts them off.
// With Config.HoldManager the swarm vetoes completion while board work, docs or
// review remain. Individual agent runs bound stop vetoes. Persistent teams resume
// the same manager context after that bound until completion, cancellation or a
// budget limit; unconfigured embedded callers retain bounded per-run behavior.
// The guard briefly waits for workers whose accepted tasks are already complete.

// holdReasonTail is what the veto tells the manager to do about it.
const holdReasonTail = "Use wait, accept or reject submissions, and repair existing tasks (or resolve unneeded work with an explanation), then give your final answer."

// holdReasonMax caps the veto text: it lands in the manager's thread once per veto.
const holdReasonMax = 400

// holdListCap is how many ids one category of the veto lists before "+N more".
const holdListCap = 6

// holdSettleMax is the longest the guard waits, in all, for workers that are only
// finishing their last turn (see unfinishedWork.windingDown) before it decides. A
// worker that outlasts it is treated like any other running worker.
var holdSettleMax = 20 * time.Second

// holdSettlePoll is how often that wait looks at the workers.
const holdSettlePoll = 10 * time.Millisecond

// unfinishedWork is what the board still holds that a manager's final answer would
// abandon. Every entry is a harness-generated id, never text an agent wrote.
type unfinishedWork struct {
	running []string // workers running now, with their tasks: "be-1 (T3)"
	review  []string // tasks submitted and awaiting accept or reject
	doing   []string // tasks in progress whose worker is not running
	blocked []string // tasks a worker reported blocked
	todo    []string // tasks nobody has started

	// idle are the ids of the running workers that hold no task in progress.
	idle []string
}

// empty reports whether every unfinished-work category is empty.
func (u unfinishedWork) empty() bool {
	return len(u.running)+len(u.review)+len(u.doing)+len(u.blocked)+len(u.todo) == 0
}

// windingDown reports whether all that is unfinished is workers that are running but
// hold nothing on the board: the last turn of a worker whose task was just accepted,
// where the manager was quicker than the worker's closing message. Sending the manager
// back for that costs it a request and gives it nothing to do; the guard waits for the
// worker instead (holdHooks.settle).
func (u unfinishedWork) windingDown() bool {
	return len(u.running) > 0 && len(u.running) == len(u.idle) &&
		len(u.review)+len(u.doing)+len(u.blocked)+len(u.todo) == 0
}

// summary renders the categories in a fixed order, at most cap ids each.
func (u unfinishedWork) summary(cap int) string {
	var parts []string
	add := func(label string, ids []string) {
		if len(ids) == 0 {
			return
		}
		shown := ids
		more := ""
		if len(ids) > cap {
			shown, more = ids[:cap], " +"+strconv.Itoa(len(ids)-cap)+" more"
		}
		parts = append(parts, label+": "+strings.Join(shown, ", ")+more)
	}
	add("running", u.running)
	add("in review", u.review)
	add("doing", u.doing)
	add("blocked", u.blocked)
	add("not started", u.todo)
	return strings.Join(parts, "; ")
}

// reason is the veto text: deterministic (sorted ids, no clocks), one line, and at
// most holdReasonMax characters: the lists shrink to fit before the tail is cut.
func (u unfinishedWork) reason() string {
	for cap := holdListCap; cap >= 1; cap-- {
		txt := "Not finished: " + u.summary(cap) + ". " + holdReasonTail
		if len([]rune(txt)) <= holdReasonMax || cap == 1 {
			return truncRunes(txt, holdReasonMax)
		}
	}
	return ""
}

// unfinishedWork reads the board and the roster. Read-only helper roles of the
// harness itself (the mailman) are not workers and never count.
func (s *Swarm) unfinishedWork() unfinishedWork {
	snap := s.Board.Snapshot()
	s.mu.Lock()
	members := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		members = append(members, m)
	}
	s.mu.Unlock()
	running := map[string]bool{}
	for _, m := range members {
		if m.manager || m.service {
			continue
		}
		m.mu.Lock()
		if m.life == lifeRunning || m.restart != nil { // a worker the harness is about to restart is at work (restart.go)
			running[m.id] = true
		}
		m.mu.Unlock()
	}
	var u unfinishedWork
	tasksOf := map[string][]string{}
	for _, t := range snap.Tasks {
		id := safeToken(t.ID, 24)
		switch t.Status {
		case StatusDoing:
			if running[t.Owner] {
				tasksOf[t.Owner] = append(tasksOf[t.Owner], id)
			} else {
				u.doing = append(u.doing, id)
			}
		case StatusReview:
			u.review = append(u.review, id)
		case StatusBlocked:
			u.blocked = append(u.blocked, id)
		case StatusFailed:
			if s.cfg.PersistentPools {
				u.todo = append(u.todo, id)
			}
		case StatusTodo:
			u.todo = append(u.todo, id)
		}
	}
	ids := make([]string, 0, len(running))
	for id := range running {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := safeToken(id, 24)
		if ts := tasksOf[id]; len(ts) > 0 {
			item += " (" + strings.Join(ts, ",") + ")"
		} else {
			u.idle = append(u.idle, id)
		}
		u.running = append(u.running, item)
	}
	return u
}

// freeListCap bounds the worker ids FreeCapacity names.
const freeListCap = 8

// FreeCapacity is a line for the manager that names the team's free workers, or "" when none is free. A standing goal adds it to
// the message that sends the manager on (session.GoalContinuation): what the judge found missing is work for them, and a manager
// sent on to "take the next concrete step" otherwise tends to take it alone while the team waits.
func (s *Swarm) FreeCapacity() string {
	free, workers := s.freeWorkers()
	if len(free) == 0 {
		return ""
	}
	names := strings.Join(free[:min(len(free), freeListCap)], ", ")
	if len(free) > freeListCap {
		names += fmt.Sprintf(" and %d more", len(free)-freeListCap)
	}
	return fmt.Sprintf("You lead a team, and %d of its %d workers are free (%s). Split what is missing by role into tasks and start "+
		"them side by side with spawn; do yourself only what is smaller than a handoff.", len(free), workers, names)
}

// freeWorkers returns the team's workers that are idle and hold no open task (sorted), and how many workers the team has: the
// capacity the manager is not using. The manager and the harness's services are not workers.
func (s *Swarm) freeWorkers() (free []string, workers int) {
	snap := s.Board.Snapshot()
	s.mu.Lock()
	members := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		members = append(members, m)
	}
	s.mu.Unlock()
	for _, m := range members {
		if m.manager || m.service {
			continue
		}
		m.mu.Lock()
		l := m.life
		m.mu.Unlock()
		if l == lifeRetired {
			continue
		}
		workers++
		if l == lifeIdle && !ownsOpenTask(snap, m.id) {
			free = append(free, m.id)
		}
	}
	sort.Strings(free)
	return free, workers
}

// holdHooks wraps the hooks an agent was given (the user's own, possibly none) and
// adds the stop guard to the manager's Stop.
type holdHooks struct {
	inner agent.Hooks
	s     *Swarm
	m     *member

	// settled is how much of holdSettleMax the guard has already spent waiting.
	settled atomic.Int64
	// noted are the submissions ("T3#rev") an edit of the manager's has carried a line about (editLine).
	notedMu sync.Mutex
	noted   map[string]bool
	// handed are the top-level directories a manager's edit has carried the free-slot line about (handoffLine).
	handed map[string]bool
}

var (
	_ agent.Hooks           = (*holdHooks)(nil)
	_ agent.CompactionHooks = (*holdHooks)(nil)
)

// BeforeCompact and AfterCompact pass automatic compactions on to the user's hooks,
// when they listen: the agent finds the compaction hooks through the Hooks it was
// given, and this wrapper is what it was given.
func (h *holdHooks) BeforeCompact(ctx context.Context, agentID, role, reason string) {
	if ch, ok := h.inner.(agent.CompactionHooks); ok {
		ch.BeforeCompact(ctx, agentID, role, reason)
	}
}

// AfterCompact forwards compaction notifications only when the wrapped hooks implement
// CompactionHooks.
func (h *holdHooks) AfterCompact(ctx context.Context, agentID, role, reason string) {
	if ch, ok := h.inner.(agent.CompactionHooks); ok {
		ch.AfterCompact(ctx, agentID, role, reason)
	}
}

// BeforeTool delegates to wrapped hooks when present and otherwise returns an empty outcome.
func (h *holdHooks) BeforeTool(ctx context.Context, c agent.ToolHookCall) agent.ToolHookOutcome {
	o := agent.ToolHookOutcome{}
	if h.inner != nil {
		o = h.inner.BeforeTool(ctx, c)
	}
	if o.Veto {
		return o
	}
	if editsFiles(c.Tool) {
		if line := h.editLine(); line != "" {
			o.Context = strings.TrimSpace(o.Context + "\n" + line)
		}
		if line := h.handoffLine(c.Input); line != "" {
			o.Context = strings.TrimSpace(o.Context + "\n" + line)
		}
	}
	if c.Tool == "bash" {
		var in struct {
			Command string `json:"command"`
		}
		input := c.Input
		if len(o.UpdatedInput) > 0 {
			input = o.UpdatedInput
		}
		if managerSleeps(c.Tool, input) {
			// A sleep in the shell is a wait, and is refused when a wait would be: the board needs the manager, or nothing it waits
			// for can change.
			if reason := h.s.stalledWaitReason(h.m.id); reason != "" {
				h.s.waitRefused()
				return agent.ToolHookOutcome{Veto: true, Reason: reason + " (A sleep in the shell is a wait. To follow a long job of your own, start it with run_in_background and read its output when the board is in hand.)"}
			}
			// For the stall sweep too. It is noted here, on the input the user's hooks may have rewritten, which the call runs
			// with: the tool-start notice before the hooks (memberSink.ToolStart) noted the call as written.
			h.s.noteCoordination(h.m.id, "wait", "")
		}
		if json.Unmarshal(input, &in) == nil && publishesManagerWork(in.Command) {
			if err := h.s.flushManager(ctx, h.m); err != nil {
				return agent.ToolHookOutcome{Veto: true, Reason: err.Error()}
			}
		}
	}
	return o
}

// editsFiles reports whether a tool call changes files: the manager doing work of its own.
func editsFiles(tool string) bool {
	switch tool {
	case "edit", "write", "apply_patch":
		return true
	}
	return false
}

// editLine is the line a manager's file edit carries while submissions wait for its decision (submissionsWaiting): the
// waiting submissions with their waits, oldest first, and that a submission comes before the manager's own edits. Each
// submission is named once, with the manager's next edit after it; a later edit names them all again only when a new one
// has joined them (past holdListCap the line counts the rest, which later edits name once the ones before them are decided).
// A manager that wrote verification tests of its own between acceptances kept four submissions waiting
// six to seven minutes each, and the stall sweep's mail about each reached it after five.
func (h *holdHooks) editLine() string {
	now := h.s.deps.Now()
	waiting := h.s.submissionsWaiting(h.s.Board.Snapshot(), now)
	h.notedMu.Lock()
	defer h.notedMu.Unlock()
	if h.noted == nil {
		h.noted = map[string]bool{}
	}
	// Only a named submission counts as noted: past holdListCap the line says "N more", and those are named by a later
	// edit, once the ones before them are decided.
	current, fresh := map[string]bool{}, false
	for i, w := range waiting {
		key := w.ID + "#" + strconv.FormatUint(w.Rev, 10)
		current[key] = true
		if !h.noted[key] && i < holdListCap {
			h.noted[key], fresh = true, true
		}
	}
	for key := range h.noted {
		if !current[key] {
			delete(h.noted, key)
		}
	}
	if !fresh {
		return ""
	}
	names := make([]string, 0, len(waiting))
	for i, w := range waiting {
		if i == holdListCap {
			names = append(names, fmt.Sprintf("%d more", len(waiting)-i))
			break
		}
		switch m := int(w.Waited / time.Minute); {
		case m >= 1:
			names = append(names, fmt.Sprintf("%s (%d min)", w.ID, m))
		default:
			names = append(names, w.ID)
		}
	}
	verb := "waits"
	if len(waiting) > 1 {
		verb = "wait"
	}
	return joinNames(names) + " " + verb + " in review. A submission comes before edits of your own: decide it, then go on; a worker's next task waits on it."
}

// handoffLine is the line a manager's file edit carries while writer roles have a free slot or an idle member (freeWriters): a
// part of the goal bigger than a handoff runs beside the rest on one of them. It is said once for each top-level directory the
// manager edits in, so a manager that keeps to small fixes hears it at most once a module. A manager with seven free slots wrote two
// of four independent modules itself and gave the other two to one backend member, one after the other: 2435 s, against 1020 s
// for a run whose manager gave each module its own worker.
func (h *holdHooks) handoffLine(input json.RawMessage) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || in.Path == "" {
		return ""
	}
	dir := topDir(h.s.deps.Workdir, in.Path)
	h.notedMu.Lock()
	seen := h.handed[dir]
	h.notedMu.Unlock()
	if seen {
		return ""
	}
	free := h.s.freeWriters("")
	if len(free) == 0 {
		return ""
	}
	h.notedMu.Lock()
	if h.handed == nil {
		h.handed = map[string]bool{}
	}
	h.handed[dir] = true
	h.notedMu.Unlock()
	where := dir
	if dir == "." {
		where = "the top of the tree"
	}
	return fmt.Sprintf("Writer slots are free (%s). Work in %s bigger than a handoff is a task for one of them (task create, then spawn), "+
		"running beside the rest while you decide and review; keep to yourself only what is smaller.", strings.Join(free, ", "), where)
}

// topDir is the first directory of path under the work tree ("." for a file at its top), for an absolute or a relative path.
func topDir(workdir, path string) string {
	if filepath.IsAbs(path) && workdir != "" {
		if rel, err := filepath.Rel(workdir, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if i := strings.IndexByte(path, '/'); i > 0 {
		return path[:i] + "/"
	}
	return "."
}

// joinNames joins ids for a sentence: "T3", "T3 and T4", "T3, T4 and T5".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// AfterTool delegates to wrapped hooks when present and otherwise returns an empty outcome.
func (h *holdHooks) AfterTool(ctx context.Context, c agent.ToolHookCall, r *tools.Result) agent.ToolHookOutcome {
	if h.inner == nil {
		return agent.ToolHookOutcome{}
	}
	return h.inner.AfterTool(ctx, c, r)
}

// BeforeStop runs the user's Stop hooks first (either may veto), then the guard.
// The guard never vetoes a run that was cancelled or whose budget is spent: those
// runs are ending anyway, and a veto would only turn a clean stop into an error.
func (h *holdHooks) BeforeStop(ctx context.Context, agentID, role, final string, continuing bool) agent.StopOutcome {
	if h.inner != nil {
		if o := h.inner.BeforeStop(ctx, agentID, role, final, continuing); o.Veto {
			return o
		}
	}
	ending := func() bool {
		return ctx.Err() != nil || h.s.isClosed() || h.s.budgetErr() != nil || h.s.agentBudgetSpent(h.m)
	}
	if ending() {
		return agent.StopOutcome{}
	}
	if err := h.s.flushManager(ctx, h.m); err != nil {
		return agent.StopOutcome{Veto: true, Reason: err.Error()}
	}
	if !h.s.cfg.HoldManager {
		h.s.flushManagerEdits() // the turn is over: no more edits of the manager's are coming in it
		return agent.StopOutcome{}
	}
	u := h.s.unfinishedWork()
	if u.windingDown() {
		h.settle(ctx, u.idle)
		if ending() {
			return agent.StopOutcome{}
		}
		u = h.s.unfinishedWork()
	}
	if u.empty() {
		h.s.flushManagerEdits()
	}
	if u.empty() && h.s.docsPending() {
		// The docs worker has not caught up with the last accepted work: the
		// harness starts or finishes that pass by itself, so the manager waits.
		reason := "Not finished: the docs worker has not caught up with the accepted work (the harness runs that pass). Use wait, accept its submission, then give your final answer."
		h.s.emitAs(agentID, events.TypeSwarmHold, map[string]any{"reason": reason})
		return agent.StopOutcome{Veto: true, Reason: reason}
	}
	if u.empty() {
		if last := h.s.reviewPending(); last != "" {
			reason := "Not finished: " + h.s.reviewLine(last) + ". Then give your final answer."
			h.s.emitAs(agentID, events.TypeSwarmHold, map[string]any{"reason": reason})
			return agent.StopOutcome{Veto: true, Reason: reason}
		}
		return agent.StopOutcome{}
	}
	reason := u.reason()
	h.s.emitAs(agentID, events.TypeSwarmHold, map[string]any{"reason": reason})
	return agent.StopOutcome{Veto: true, Reason: reason}
}

// settle waits until none of the workers is running any more, or the guard has spent
// holdSettleMax in all, or the run is cancelled or the swarm closed. It waits for the
// ending of a turn, which takes about one model request, and it decides nothing: the
// caller looks at the board again.
func (h *holdHooks) settle(ctx context.Context, ids []string) {
	left := holdSettleMax - time.Duration(h.settled.Load())
	if left <= 0 {
		return
	}
	start := time.Now()
	defer func() { h.settled.Add(int64(time.Since(start))) }()
	tick := time.NewTicker(holdSettlePoll)
	defer tick.Stop()
	timer := time.NewTimer(left)
	defer timer.Stop()
	for h.s.anyRunning(ids) {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-tick.C:
		}
		if h.s.isClosed() {
			return
		}
	}
}

// anyRunning reports whether one of the members is in a run.
func (s *Swarm) anyRunning(ids []string) bool {
	for _, id := range ids {
		m := s.get(id)
		if m == nil {
			continue
		}
		m.mu.Lock()
		running := m.life == lifeRunning
		m.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

// agentBudgetSpent reports whether the member has used its own budget: its next
// request would be refused.
func (s *Swarm) agentBudgetSpent(m *member) bool {
	if s.cfg.AgentBudgetUSD <= 0 || m == nil {
		return false
	}
	_, c := m.a.Usage()
	return !(c < s.cfg.AgentBudgetUSD) // a cost that is not a number counts as spent
}

// publishesManagerWork recognizes publication commands, including shell wrappers
// and Git's global directory/configuration flags. Opaque scripts remain subject
// to ordinary session permissions; this hook does not interpret their contents.
func publishesManagerWork(command string) bool {
	for _, c := range shellparse.Parse(command).Commands {
		program := shellparse.ProgramName(c.Program)
		if program != "git" && program != "gh" {
			continue
		}
		args := c.Args
		for len(args) > 0 {
			word := args[0]
			args = args[1:]
			switch word {
			case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--repo", "-R", "--hostname":
				if len(args) > 0 {
					args = args[1:]
				}
				continue
			}
			if strings.HasPrefix(word, "-") {
				continue
			}
			if program == "git" && word == "push" || program == "gh" && word == "pr" {
				return true
			}
			break
		}
	}
	return false
}
