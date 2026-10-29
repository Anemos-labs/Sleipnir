package swarm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The stall sweep. The watchdog (supervisor.go) catches a worker that shows no sign
// of life; the repetition guard catches a model that repeats one failing call. A team
// can also stop making progress while every agent is busy: a worker reads without
// ever changing anything, the manager waits for workers that are not running, a task
// is owned by an agent that no longer exists, two tasks are blocked on each other, a
// submission sits in review while the manager does other things, most of a team sits
// idle while the manager waits on the rest. The sweep names these conditions from the
// board, the roster and turn counters (no model text), so the same state always gives
// the same findings; idle_capacity alone also reads how long workers have been idle.
//
// A finding is raised once, when its condition starts to hold: a swarm.stall event
// (action raise) is logged and the agent that can act on it is told once by harness
// mail. It clears itself when the condition no longer holds (action clear). Raising
// the same finding again needs the condition to clear first.

// StallKind names one way a team can stop making progress.
type StallKind string

const (
	// StallClaimedNoProgress: a running worker owns a doing task and has taken
	// Config.StallTurns model turns without a progress call (a successful edit, a
	// test run or a command that may change files, or a task update, block or done).
	// A command that only reads and prints is not one (progressCall).
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
	// turns, or for reviewStarvedAfter, without the manager reading or deciding it.
	StallReviewStarved StallKind = "review_starved"
	// StallIdleCapacity: the manager of a configured team waits on the few workers
	// still at work while at least half of the team, and two workers or more, have been
	// idle for idleCapacityAfter owning no open task, and the workers at work are not all
	// read-only roles or on docs tasks (verdictPhase: then the wait is for a verdict). Waiting is right when the open tasks are all the
	// objective needs, and costs the idle workers' time when more remains: the manager is
	// told who is free, once, and decides. It clears when they are at work again, when no
	// worker runs or no task is open; the manager's calls between two waits do not clear it.
	StallIdleCapacity StallKind = "idle_capacity"
	// StallReviewOverrun: a running reviewer's task has run longer than the work it reviews took to build (the longest of the merges
	// no accepted review covers), and at least reviewOverrunFloor. A review that walks the same code
	// over and over finds what its first pass found; the reviewer is told to write the report with what it has.
	StallReviewOverrun StallKind = "review_overrun"
)

// reviewStarvedAfter is how long a submission may wait in review, whatever the manager's turn count, before
// StallReviewStarved holds: a manager that waits on a long review with long timeouts takes few turns, and a
// docs pass or a fix that sits unread meanwhile holds up the pass that follows it.
var reviewStarvedAfter = 5 * time.Minute

// reviewOverrunFloor is the least a review may run before StallReviewOverrun can hold, whatever the work took.
var reviewOverrunFloor = 10 * time.Minute

// idleCapacityAfter is how long a worker of a configured team has been idle before it
// counts toward StallIdleCapacity.
var idleCapacityAfter = 15 * time.Minute

// StallKinds lists every stall kind in a fixed order.
var StallKinds = []StallKind{StallClaimedNoProgress, StallManagerWaitingOnIdle, StallOrphanedTask, StallBlockedCycle, StallReviewStarved, StallIdleCapacity, StallReviewOverrun}

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
	orphans map[string]bool        // orphaned-task candidates of the previous sweep
	review  map[string]reviewClock // "T3#rev": when the review clock (re)started
	waiting bool                   // the manager's latest coordination call is wait
	refused bool                   // a manager wait was just refused (see waitRefused)
	// since is when each active finding was first seen by escalateStalls, and escalated the findings it escalated.
	since     map[string]time.Time
	escalated map[string]bool
}

// reviewClock is when a submission's review clock (re)started: the manager's turn count and the time.
type reviewClock struct {
	turns int64
	at    time.Time
}

// init makes the maps; the zero value is not ready for use.
func (st *stallState) init() {
	st.active, st.raised, st.orphans, st.review = map[string]Stall{}, map[StallKind]int{}, map[string]bool{}, map[string]reviewClock{}
	st.since, st.escalated = map[string]time.Time{}, map[string]bool{}
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
// task call that reports on the work, or a call that added to a record the harness keeps (tools.Result.Recorded). Reading, listing, waiting and mail are not, and
// neither is a shell command that only reads and prints (shellReadsOnly): a worker that
// reads the code one `cat` or `sed -n` at a time is reading, whatever tool it reads with. A write that changed nothing
// (tools.Result.Unchanged: the content the file already had, edits that leave it identical) is not progress either: a
// worker that rewrites the same file with the same bytes succeeds every time and moves nothing.
func (s *Swarm) progressCall(call core.Block, res *tools.Result) bool {
	if res == nil || res.IsError || res.Unchanged {
		return false
	}
	if res.Recorded {
		return true // a read-only tool that added to the task's record (a scan's points, a research run's notes)
	}
	switch call.ToolName {
	case "task":
		switch jsonString(call.Input, "action") {
		case "update", "done", "block", "resume", "claim", "handover":
			return true
		}
		return false
	case "mail", "note", "spawn", "wait", "approve":
		return false
	case "bash":
		return !shellReadsOnly(jsonString(call.Input, "command"))
	}
	if reg := s.deps.Registry; reg != nil {
		if tl, ok := reg.Get(call.ToolName); ok && tl.Spec().ReadOnly {
			return false
		}
	}
	return true
}

// shellReadsOnly reports whether a shell command line only inspects: every command of it is on the permission engine's
// read-only allowlist and no redirection writes a file (perm.ReadsOnly), and it runs no test. A test or check run (go test,
// npm test, pytest, go vet: classifyCommand, the evidence's own test detection) is progress even though it changes no file:
// it is how a tester works and how a writer checks its edits, and it is what the evidence reports as the last test.
func shellReadsOnly(cmd string) bool {
	if isTest, _ := classifyCommand(cmd); isTest {
		return false
	}
	return perm.ReadsOnly(cmd)
}

// memberView is what the sweep reads of one member.
type memberView struct {
	running, manager, service bool
	quiet                     int64 // model turns since its last progress call
	turns                     int64
	idleSince                 time.Time // when its last run ended (zero: it has not run, or runs now)
	runSince                  time.Time // when its current run started (zero: it runs now)
	role                      string
}

// stallFindings computes what holds now. It reads the board, the roster and the turn
// counters, and updates the sweep's own memory (orphan candidates, review clocks).
func (s *Swarm) stallFindings() []Stall {
	snap := s.Board.Snapshot()
	mgr := s.ManagerID()
	s.mu.Lock()
	views := make(map[string]memberView, len(s.members))
	arriving := make(map[string]bool, len(s.arriving))
	for id := range s.arriving {
		arriving[id] = true
	}
	for id, m := range s.members {
		m.mu.Lock()
		v := memberView{running: m.life == lifeRunning, manager: m.manager, service: m.service, role: m.role}
		if m.life == lifeIdle {
			v.idleSince = m.idleAt
		}
		if m.run != nil {
			v.runSince = m.run.started
		}
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
						Detail: fmt.Sprintf("%s took %d model turns on %s without editing a file, running a test or a command that changes files, or updating the task", id, v.quiet, task)})
				}
			}
		}
	}

	// review_overrun: a reviewer's run has outlasted the work it reviews. The bound is the work's own duration (the longest of the merges
	// no review covers yet, with a floor), so a review of a day's work may take hours and a review of a fix may not.
	for id, v := range views {
		if !v.running || v.manager || v.service || v.role != ReviewerRole || v.runSince.IsZero() {
			continue
		}
		task := currentTask(snap, id)
		t, ok := snap.Task(task)
		if !ok || t.Status != StatusDoing || t.Kind == TaskKindPlan {
			continue // a reviewer's planning task (a team without an architect) is an agreement to write, not a review
		}
		since, ok := s.review.beganAt(t.ID, t.Rev) // the assignment's own start: a task claimed mid-run is not older than the claim
		if !ok {
			since = v.runSince
		}
		ran := s.deps.Now().Sub(since)
		work, ids := s.review.unreviewedWork()
		bound := max(work, reviewOverrunFloor)
		if ran <= bound {
			continue
		}
		covers := "no merge"
		if len(ids) > 0 {
			covers = strings.Join(ids, ", ")
		}
		out = append(out, Stall{Kind: StallReviewOverrun, Task: task, Agent: id, Notify: id,
			Detail: fmt.Sprintf("%s has reviewed %s for %d minutes; the work it reviews (%s) took %d minutes to build",
				id, task, int(ran/time.Minute), covers, int(work/time.Minute))})
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

	// idle_capacity: the manager waits on a few workers while most of the team has been idle for a while. Waiting raises it; once
	// raised it holds while the team stays so, whatever the manager does in between: a manager wakes from a wait for every task
	// that settles, and a finding cleared by each of its other calls was raised, and mailed, anew by its next wait (41 times in two
	// hours of a 12-worker run).
	idleKey := Stall{Kind: StallIdleCapacity, Agent: mgr}.key()
	_, idleActive := s.stalls.active[idleKey]
	if mgr != "" && (s.stalls.waiting || idleActive) && anyWorker && openTask(snap) && s.teamLimited() && !s.verdictPhase(snap, views) {
		at := s.deps.Now()
		var idle []string
		workers := 0
		for id, v := range views {
			if v.manager || v.service {
				continue
			}
			workers++
			if !v.running && !v.idleSince.IsZero() && at.Sub(v.idleSince) >= idleCapacityAfter && !ownsOpenTask(snap, id) {
				idle = append(idle, id)
			}
		}
		if len(idle) >= 2 && 2*len(idle) >= workers {
			sort.Strings(idle)
			out = append(out, Stall{Kind: StallIdleCapacity, Agent: mgr, Notify: mgr,
				Detail: fmt.Sprintf("%d of %d workers have been idle for %d minutes or more (%s) while the manager waits for %s",
					len(idle), workers, int(idleCapacityAfter/time.Minute), strings.Join(idle, ", "), u.summary(holdListCap))})
		}
	}

	// orphaned_task: confirmed on a second sweep, so the moment between a spawn's
	// assignment and its registration (or a retirement and its requeue) is not one.
	orphans := map[string]bool{}
	for _, t := range snap.Tasks {
		held := t.Status == StatusDoing || t.Status == StatusBlocked || (t.Status == StatusTodo && t.Owner != "")
		if !held || t.Owner == "" {
			continue
		}
		if _, ok := views[t.Owner]; ok || arriving[t.Owner] {
			continue // on the team, or being added to it (arrive)
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

	// review_starved: the clock is the manager's own turn count, and the wall clock behind it.
	if mv, ok := views[mgr]; ok && k > 0 {
		now := s.deps.Now()
		inReview := map[string]bool{}
		for _, t := range snap.Tasks {
			if t.Status != StatusReview {
				continue
			}
			key := t.ID + "#" + strconv.FormatUint(t.Rev, 10)
			inReview[key] = true
			since, ok := s.stalls.review[key]
			if !ok {
				s.stalls.review[key] = reviewClock{turns: mv.turns, at: now}
				continue
			}
			n, waited := mv.turns-since.turns, now.Sub(since.at)
			switch {
			case n >= k:
				out = append(out, Stall{Kind: StallReviewStarved, Task: t.ID, Agent: mgr, Notify: mgr,
					Detail: fmt.Sprintf("%s has waited in review for %d manager turns", t.ID, n)})
			case waited >= reviewStarvedAfter:
				out = append(out, Stall{Kind: StallReviewStarved, Task: t.ID, Agent: mgr, Notify: mgr,
					Detail: fmt.Sprintf("%s has waited in review for %d minutes", t.ID, int(waited/time.Minute))})
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
			s.notify(f.Notify, "request", stallNudge(f, s.stallTurns(), s.cfg.PersistentPools))
		}
	}
	s.escalateStalls()
}

// stallEvent is the payload of a swarm.stall event.
func stallEvent(action string, f Stall) map[string]any {
	return map[string]any{"action": action, "kind": string(f.Kind), "task": f.Task, "agent": f.Agent, "notify": f.Notify, "detail": f.Detail}
}

// stallNudge is the harness mail that tells the responsible agent about a finding:
// what was observed and what would end it. It holds ids and counts only. pools says the
// swarm keeps persistent pools, which refuse a handover (Handover): a worker there that
// cannot continue says so on the task and blocks it instead.
func stallNudge(f Stall, k int64, pools bool) string {
	switch f.Kind {
	case StallClaimedNoProgress:
		out := "or hand it over (task handover) if you cannot continue."
		if pools {
			out = "or, if you cannot continue, say why on the task (task update) and block it."
		}
		return fmt.Sprintf("Stall check: you took %d turns on %s without editing a file, running a test or a command that changes files, or updating the task "+
			"(reading files, with any tool or command, is not progress). "+
			"Make the next change, or update the task with what you are doing, block it (target = the task you wait for), %s", k, f.Task, out)
	case StallOrphanedTask:
		return fmt.Sprintf("Stall check: %s. Nobody can finish it: fail it with a reason (and reopen it to start it again).", f.Detail)
	case StallBlockedCycle:
		return "Stall check: tasks are " + f.Detail + ", so none of them can continue. Decide one (resume it with what it needs), or fail one with reason blocked_on."
	case StallReviewStarved:
		return fmt.Sprintf("Stall check: %s. Read it (task get %s), then accept or reject it.", f.Detail, f.Task)
	case StallIdleCapacity:
		return "Stall check: " + f.Detail + ". If the objective needs more than the open tasks (a requirement nobody has started, a gap you found), " +
			"create that work and assign it now: an idle worker starts at once. If the open tasks are all that is left, keep waiting."
	case StallReviewOverrun:
		return "Stall check: " + f.Detail + ". A second walk over the same code finds what the first did. Write the report now with what you have: " +
			"the findings so far in priority order, what you checked and found fine, and what you did not reach, named as unverified; then task done."
	}
	return "Stall check: " + f.Detail
}

// verdictPhase reports whether every running worker is a read-only role or on a docs task: the team waits for a verdict on finished
// work (a review, an agreement, the docs catch-up), not for work, and idle writers are expected. Told of idle capacity then, a
// manager invented work to fill it (an adversarial test task after the review had started) and the run waited on that too.
func (s *Swarm) verdictPhase(snap *Snapshot, views map[string]memberView) bool {
	for id, v := range views {
		if !v.running || v.manager || v.service {
			continue
		}
		if s.roles[v.role].ReadOnly {
			continue
		}
		task := currentTask(snap, id)
		if task == "" {
			// a writer finishing its turn after its task was accepted (or failed): not work the team waits on
			continue
		}
		if t, ok := snap.Task(task); ok && s.isDocsTask(t) {
			continue
		}
		return false
	}
	return true
}

// ownsOpenTask reports whether agent owns a task that is not closed: one reserved for it, in progress, blocked or in review (a pool
// leader's pool task among them). Such a worker is not free, whatever it is doing now.
func ownsOpenTask(sn *Snapshot, agent string) bool {
	for _, t := range sn.Tasks {
		if t.Owner == agent && t.Status != StatusDone && t.Status != StatusFailed {
			return true
		}
	}
	return false
}

// openTask reports whether the board holds a task that is not closed (todo, doing, blocked or in review): a running worker with no
// task is not unfinished work.
func openTask(sn *Snapshot) bool {
	for _, t := range sn.Tasks {
		if t.Status != StatusDone && t.Status != StatusFailed {
			return true
		}
	}
	return false
}
