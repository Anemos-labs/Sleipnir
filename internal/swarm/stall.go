package swarm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The stall sweep. The watchdog (supervisor.go) catches a worker that shows no sign
// of life; the repetition guard catches a model that repeats one failing call. A team
// can also stop making progress while every agent is busy: a worker reads without
// ever changing anything, the manager waits for workers that are not running, a task
// is owned by an agent that no longer exists, two tasks are blocked on each other, a
// submission sits in review while the manager does other things. The sweep names
// these conditions from the board, the roster and turn counters only (no clocks, no
// model text), so the same state always gives the same findings.
//
// A finding is raised once, when its condition starts to hold: a swarm.stall event
// (action raise) is logged and the agent that can act on it is told once by harness
// mail. It clears itself when the condition no longer holds (action clear). Raising
// the same finding again needs the condition to clear first.

// StallKind names one way a team can stop making progress.
type StallKind string

const (
	// StallClaimedNoProgress: a running worker owns a doing task and has taken
	// Config.StallTurns model turns without a progress call (a successful edit or
	// command, or a task update, block or done).
	StallClaimedNoProgress StallKind = "claimed_no_progress"
	// StallManagerWaitingOnIdle: the manager's wait was refused because unfinished
	// work remains and no worker is running. It holds while the manager's latest
	// coordination call is still wait and that is still so. The refusal is the nudge:
	// no mail is sent for it.
	StallManagerWaitingOnIdle StallKind = "manager_waiting_on_idle"
	// StallOrphanedTask: a doing or blocked task, or a todo task reserved for an
	// agent, whose owner is not a registered agent, on two consecutive sweeps.
	StallOrphanedTask StallKind = "orphaned_task"
	// StallBlockedCycle: blocked tasks whose blocked_on targets lead back to
	// themselves (T1 waits for T2, T2 waits for T1).
	StallBlockedCycle StallKind = "blocked_cycle"
	// StallReviewStarved: a task has been in review for Config.StallTurns manager
	// turns without the manager reading or deciding it.
	StallReviewStarved StallKind = "review_starved"
)

// StallKinds lists every stall kind in a fixed order.
var StallKinds = []StallKind{StallClaimedNoProgress, StallManagerWaitingOnIdle, StallOrphanedTask, StallBlockedCycle, StallReviewStarved}

// Stall is one finding of the sweep. Every field is harness-written: ids, kinds and
// counts, never text an agent wrote.
type Stall struct {
	Kind StallKind `json:"kind"`
	// Task is the task the stall concerns (the lowest id of a cycle), or "".
	Task string `json:"task,omitempty"`
	// Agent is the agent whose behaviour shows the stall (the worker, or the manager).
	Agent string `json:"agent,omitempty"`
	// Notify is the agent that was told: the one that can act on it.
	Notify string `json:"notify,omitempty"`
	// Detail is one deterministic line saying what was observed.
	Detail string `json:"detail"`
}

// key identifies a finding across sweeps; the detail may change without re-raising it.
func (f Stall) key() string { return string(f.Kind) + "|" + f.Task + "|" + f.Agent }

// defaultStallTurns is Config.StallTurns when it is zero.
const defaultStallTurns = 12

// stallState is the sweep's memory. sweepMu makes one sweep (its findings, its events
// and its nudges) one step; mu guards the maps, which tool calls also touch.
type stallState struct {
	sweepMu sync.Mutex
	mu      sync.Mutex
	active  map[string]Stall
	raised  map[StallKind]int
	orphans map[string]bool  // orphaned-task candidates of the previous sweep
	review  map[string]int64 // "T3#rev": the manager's turn count when the review clock (re)started
	waiting bool             // the manager's latest coordination call is wait
	refused bool             // a manager wait was just refused (see waitRefused)
}

// init makes the maps; the zero value is not ready for use.
func (st *stallState) init() {
	st.active, st.raised, st.orphans, st.review = map[string]Stall{}, map[StallKind]int{}, map[string]bool{}, map[string]int64{}
}

// stallTurns is the turn bound of the turn-based kinds (0: they are off).
func (s *Swarm) stallTurns() int64 {
	switch {
	case s.cfg.StallTurns < 0:
		return 0
	case s.cfg.StallTurns == 0:
		return defaultStallTurns
	}
	return int64(s.cfg.StallTurns)
}

// Stalls returns the findings that hold now, sorted by kind, task and agent.
func (s *Swarm) Stalls() []Stall {
	s.stalls.mu.Lock()
	defer s.stalls.mu.Unlock()
	out := make([]Stall, 0, len(s.stalls.active))
	for _, f := range s.stalls.active {
		out = append(out, f)
	}
	sortStalls(out)
	return out
}

// StallCounts returns how many findings of each kind were raised in this swarm's life
// (a finding that clears and holds again counts twice). The same counts can be read
// from the swarm.stall events of the log.
func (s *Swarm) StallCounts() map[StallKind]int {
	s.stalls.mu.Lock()
	defer s.stalls.mu.Unlock()
	out := make(map[StallKind]int, len(s.stalls.raised))
	for k, n := range s.stalls.raised {
		out[k] = n
	}
	return out
}

// sortStalls orders findings by kind (in StallKinds order), then task id, then agent.
func sortStalls(fs []Stall) {
	rank := map[StallKind]int{}
	for i, k := range StallKinds {
		rank[k] = i
	}
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		switch {
		case a.Kind != b.Kind:
			return rank[a.Kind] < rank[b.Kind]
		case a.Task != b.Task:
			return lessTaskID(a.Task, b.Task)
		}
		return a.Agent < b.Agent
	})
}

// lessTaskID orders task ids numerically (T2 before T10), anything else after them by text.
func lessTaskID(a, b string) bool {
	na, ea := strconv.Atoi(strings.TrimPrefix(a, "T"))
	nb, eb := strconv.Atoi(strings.TrimPrefix(b, "T"))
	if ea == nil && eb == nil {
		return na < nb
	}
	if (ea == nil) != (eb == nil) {
		return ea == nil
	}
	return a < b
}

// noteCoordination records the manager's coordination calls for the stall sweep: a
// wait marks it as waiting, any other call ends that, and a task call that names a
// task restarts that task's review clock (the manager looked at it).
func (s *Swarm) noteCoordination(agentID, tool, taskID string) {
	if agentID == "" || agentID != s.ManagerID() {
		return
	}
	s.stalls.mu.Lock()
	defer s.stalls.mu.Unlock()
	s.stalls.waiting = tool == "wait"
	if tool == "task" && taskID != "" {
		for k := range s.stalls.review {
			if id, _, _ := strings.Cut(k, "#"); id == taskID {
				delete(s.stalls.review, k)
			}
		}
	}
}

// waitRefused records that the manager's wait was refused because no worker can make
// progress, and sweeps at once, so the finding is raised by the refusal itself.
func (s *Swarm) waitRefused() {
	s.stalls.mu.Lock()
	s.stalls.refused = true
	s.stalls.mu.Unlock()
	s.sweepStalls()
}

// progressCall reports whether a finished tool call is progress on a task: a
// successful call of a tool that is neither read-only nor a coordination tool, or a
// task call that reports on the work. Reading, listing, waiting and mail are not.
func (s *Swarm) progressCall(call core.Block, res *tools.Result) bool {
	if res == nil || res.IsError {
		return false
	}
	switch call.ToolName {
	case "task":
		switch jsonString(call.Input, "action") {
		case "update", "done", "block", "resume", "claim", "handover":
			return true
		}
		return false
	case "mail", "note", "spawn", "wait":
		return false
	}
	if reg := s.deps.Registry; reg != nil {
		if tl, ok := reg.Get(call.ToolName); ok && tl.Spec().ReadOnly {
			return false
		}
	}
	return true
}

// memberView is what the sweep reads of one member.
type memberView struct {
	running, manager, service bool
	quiet                     int64 // model turns since its last progress call
	turns                     int64
}

// stallFindings computes what holds now. It reads the board, the roster and the turn
// counters, and updates the sweep's own memory (orphan candidates, review clocks).
func (s *Swarm) stallFindings() []Stall {
	snap := s.Board.Snapshot()
	mgr := s.ManagerID()
	s.mu.Lock()
	views := make(map[string]memberView, len(s.members))
	for id, m := range s.members {
		m.mu.Lock()
		v := memberView{running: m.life == lifeRunning, manager: m.manager, service: m.service}
		m.mu.Unlock()
		v.turns = m.turns.Load()
		v.quiet = v.turns - m.quietFrom.Load()
		views[id] = v
	}
	s.mu.Unlock()
	k := s.stallTurns()
	anyWorker := false
	for _, v := range views {
		if v.running && !v.manager && !v.service {
			anyWorker = true
		}
	}
	u := s.unfinishedWork()
	var out []Stall

	// claimed_no_progress: one finding per worker, on the task its status shows.
	if k > 0 {
		for id, v := range views {
			if !v.running || v.manager || v.service || v.quiet < k {
				continue
			}
			if task := currentTask(snap, id); task != "" {
				if t, _ := snap.Task(task); t.Status == StatusDoing {
					out = append(out, Stall{Kind: StallClaimedNoProgress, Task: task, Agent: id, Notify: id,
						Detail: fmt.Sprintf("%s took %d model turns on %s without editing a file, running a command or updating the task", id, v.quiet, task)})
				}
			}
		}
	}

	s.stalls.mu.Lock()
	defer s.stalls.mu.Unlock()

	// manager_waiting_on_idle: raised only by a refused wait (the harness has decided
	// then that waiting cannot help), kept while the manager still waits on nobody.
	waitKey := Stall{Kind: StallManagerWaitingOnIdle, Agent: mgr}.key()
	_, waitActive := s.stalls.active[waitKey]
	if mgr != "" && (s.stalls.refused || waitActive) && s.stalls.waiting && !anyWorker && !u.empty() {
		out = append(out, Stall{Kind: StallManagerWaitingOnIdle, Agent: mgr,
			Detail: "the manager waited, but no worker is running and the board needs it: " + u.summary(holdListCap)})
	}
	s.stalls.refused = false

	// orphaned_task: confirmed on a second sweep, so the moment between a spawn's
	// assignment and its registration (or a retirement and its requeue) is not one.
	orphans := map[string]bool{}
	for _, t := range snap.Tasks {
		held := t.Status == StatusDoing || t.Status == StatusBlocked || (t.Status == StatusTodo && t.Owner != "")
		if !held || t.Owner == "" {
			continue
		}
		if _, ok := views[t.Owner]; ok {
			continue
		}
		key := t.ID + "#" + strconv.FormatUint(t.Rev, 10)
		orphans[key] = true
		if s.stalls.orphans[key] {
			out = append(out, Stall{Kind: StallOrphanedTask, Task: t.ID, Agent: t.Owner, Notify: mgr,
				Detail: fmt.Sprintf("%s is %s but its owner %s is not on the team", t.ID, t.Status, safeToken(t.Owner, 24))})
		}
	}
	s.stalls.orphans = orphans

	// blocked_cycle
	out = append(out, blockedCycles(snap, mgr)...)

	// review_starved: the clock is the manager's own turn count.
	if mv, ok := views[mgr]; ok && k > 0 {
		inReview := map[string]bool{}
		for _, t := range snap.Tasks {
			if t.Status != StatusReview {
				continue
			}
			key := t.ID + "#" + strconv.FormatUint(t.Rev, 10)
			inReview[key] = true
			since, ok := s.stalls.review[key]
			if !ok {
				s.stalls.review[key] = mv.turns
				continue
			}
			if n := mv.turns - since; n >= k {
				out = append(out, Stall{Kind: StallReviewStarved, Task: t.ID, Agent: mgr, Notify: mgr,
					Detail: fmt.Sprintf("%s has waited in review for %d manager turns", t.ID, n)})
			}
		}
		for key := range s.stalls.review {
			if !inReview[key] {
				delete(s.stalls.review, key)
			}
		}
	}
	sortStalls(out)
	return out
}

// blockedCycles finds the cycles among blocked tasks' blocked_on links. Each cycle is
// one finding, named by its lowest task id.
func blockedCycles(snap *Snapshot, mgr string) []Stall {
	next := map[string]string{}
	for _, t := range snap.Tasks {
		if t.Status == StatusBlocked && t.BlockedOn != "" {
			next[t.ID] = t.BlockedOn
		}
	}
	ids := make([]string, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return lessTaskID(ids[i], ids[j]) })
	done := map[string]bool{}
	var out []Stall
	for _, start := range ids {
		if done[start] {
			continue
		}
		var path []string
		at := map[string]int{}
		cur := start
		for {
			if done[cur] {
				break
			}
			if i, ok := at[cur]; ok {
				cyc := append([]string(nil), path[i:]...)
				low := 0
				for j := range cyc {
					if lessTaskID(cyc[j], cyc[low]) {
						low = j
					}
				}
				cyc = append(cyc[low:], cyc[:low]...)
				out = append(out, Stall{Kind: StallBlockedCycle, Task: cyc[0], Notify: mgr,
					Detail: "blocked on each other: " + strings.Join(append(cyc, cyc[0]), " -> ")})
				break
			}
			nx, ok := next[cur]
			if !ok {
				break
			}
			at[cur] = len(path)
			path = append(path, cur)
			cur = nx
		}
		for _, id := range path {
			done[id] = true
		}
	}
	return out
}

// sweepStalls is one pass of the stall sweep: new findings are raised (event, count,
// one nudge), findings whose condition no longer holds are cleared (event).
func (s *Swarm) sweepStalls() {
	st := &s.stalls
	st.sweepMu.Lock()
	defer st.sweepMu.Unlock()
	found := s.stallFindings()
	st.mu.Lock()
	now := make(map[string]bool, len(found))
	var raised, cleared []Stall
	for _, f := range found {
		k := f.key()
		now[k] = true
		if _, ok := st.active[k]; !ok {
			raised = append(raised, f)
			st.raised[f.Kind]++
		}
		st.active[k] = f
	}
	for k, f := range st.active {
		if !now[k] {
			cleared = append(cleared, f)
			delete(st.active, k)
		}
	}
	st.mu.Unlock()
	sortStalls(cleared)
	for _, f := range cleared {
		s.emit(events.TypeSwarmStall, stallEvent("clear", f))
	}
	for _, f := range raised {
		s.emit(events.TypeSwarmStall, stallEvent("raise", f))
		if f.Notify != "" {
			s.notify(f.Notify, "request", stallNudge(f, s.stallTurns()))
		}
	}
}

// stallEvent is the payload of a swarm.stall event.
func stallEvent(action string, f Stall) map[string]any {
	return map[string]any{"action": action, "kind": string(f.Kind), "task": f.Task, "agent": f.Agent, "notify": f.Notify, "detail": f.Detail}
}

// stallNudge is the harness mail that tells the responsible agent about a finding:
// what was observed and what would end it. It holds ids and counts only.
func stallNudge(f Stall, k int64) string {
	switch f.Kind {
	case StallClaimedNoProgress:
		return fmt.Sprintf("Stall check: you took %d turns on %s without editing a file, running a command or updating the task. "+
			"Make the next change, or update the task with what you are doing, block it (target = the task you wait for), or hand it over (task handover) if you cannot continue.", k, f.Task)
	case StallOrphanedTask:
		return fmt.Sprintf("Stall check: %s. Nobody can finish it: fail it with a reason (and reopen it to start it again).", f.Detail)
	case StallBlockedCycle:
		return "Stall check: tasks are " + f.Detail + ", so none of them can continue. Decide one (resume it with what it needs), or fail one with reason blocked_on."
	case StallReviewStarved:
		return fmt.Sprintf("Stall check: %s. Read it (task get %s), then accept or reject it.", f.Detail, f.Task)
	}
	return "Stall check: " + f.Detail
}
