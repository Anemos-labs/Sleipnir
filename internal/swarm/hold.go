package swarm

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tools"
)

// The stop guard.
//
// A batch run (sleipnir run --swarm, sleipnir swarm) ends when the manager's run
// returns, and the session closes with it: a manager that gives its final answer
// while workers are still running or submissions wait for a verdict cuts them off.
// With Config.HoldManager the swarm wraps the hooks it gives the manager so that a
// final answer is vetoed, with one short harness-written reason, while the board
// holds unfinished work. The agent loop bounds vetoes per run (maxStopVetoes), so a
// manager that will not settle its board cannot be held forever: when the bound is
// reached the run ends and RunManager reports what was left (see afterManagerRun).
//
// An interactive session (a person talks to the manager between turns) is not
// held: workers legitimately outlive a turn there, and finished work wakes the
// manager instead (wake.go).

// holdReasonTail is what the veto tells the manager to do about it.
const holdReasonTail = "Use wait, accept or reject submissions, and fail tasks you abandon, then give your final answer."

// holdReasonMax caps the veto text: it lands in the manager's thread once per veto.
const holdReasonMax = 400

// holdListCap is how many ids one category of the veto lists before "+N more".
const holdListCap = 6

// unfinishedWork is what the board still holds that a manager's final answer would
// abandon. Every entry is a harness-generated id, never text an agent wrote.
type unfinishedWork struct {
	running []string // workers running now, with their tasks: "be-1 (T3)"
	review  []string // tasks submitted and awaiting accept or reject
	doing   []string // tasks in progress whose worker is not running
	blocked []string // tasks a worker reported blocked
	todo    []string // tasks nobody has started
}

func (u unfinishedWork) empty() bool {
	return len(u.running)+len(u.review)+len(u.doing)+len(u.blocked)+len(u.todo) == 0
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
		if m.life == lifeRunning {
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
		}
		u.running = append(u.running, item)
	}
	return u
}

// holdHooks wraps the hooks an agent was given (the user's own, possibly none) and
// adds the stop guard to the manager's Stop.
type holdHooks struct {
	inner agent.Hooks
	s     *Swarm
	m     *member
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

func (h *holdHooks) AfterCompact(ctx context.Context, agentID, role, reason string) {
	if ch, ok := h.inner.(agent.CompactionHooks); ok {
		ch.AfterCompact(ctx, agentID, role, reason)
	}
}

func (h *holdHooks) BeforeTool(ctx context.Context, c agent.ToolHookCall) agent.ToolHookOutcome {
	if h.inner == nil {
		return agent.ToolHookOutcome{}
	}
	return h.inner.BeforeTool(ctx, c)
}

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
	if ctx.Err() != nil || h.s.isClosed() || h.s.budgetErr() != nil || h.s.agentBudgetSpent(h.m) {
		return agent.StopOutcome{}
	}
	u := h.s.unfinishedWork()
	if u.empty() {
		return agent.StopOutcome{}
	}
	reason := u.reason()
	h.s.emitAs(agentID, events.TypeSwarmHold, map[string]any{"reason": reason})
	return agent.StopOutcome{Veto: true, Reason: reason}
}

// agentBudgetSpent reports whether the member has used its own budget: its next
// request would be refused.
func (s *Swarm) agentBudgetSpent(m *member) bool {
	if s.cfg.AgentBudgetUSD <= 0 || m == nil {
		return false
	}
	_, c := m.a.Usage()
	return c >= s.cfg.AgentBudgetUSD
}
