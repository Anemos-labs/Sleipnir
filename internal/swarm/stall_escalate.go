package swarm

import (
	"fmt"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Escalation of a stall that outlives its nudge. A finding is raised once and its agent told once (sweepStalls); a worker that
// ignores the nudge, or a manager that does, would leave the team stuck with nobody looking. A finding that still holds
// stallEscalateAfter after it was raised is escalated once to the manager, the team's diagnosis owner: a packet of what the
// harness knows about it (the task's state, the agent's, how long it held, who was told) and the decisions that end it. The
// swarm.stall event of the escalation has action escalate.

// stallEscalateAfter is how long a finding holds before it is escalated.
var stallEscalateAfter = 10 * time.Minute

// escalateStalls escalates the active findings that have held long enough and were not escalated yet. It is called by
// sweepStalls with st.sweepMu held, after raising and clearing; raised and cleared findings have updated since. Without a
// manager nothing is escalated: a finding stays due until one can receive it.
func (s *Swarm) escalateStalls() {
	st := &s.stalls
	now := s.deps.Now()
	mgr := s.ManagerID()
	var due []Stall
	var held []time.Duration
	st.mu.Lock()
	for k, f := range st.active {
		at, ok := st.since[k]
		if !ok {
			st.since[k] = now
			continue
		}
		if mgr == "" || st.escalated[k] || now.Sub(at) < stallEscalateAfter {
			continue
		}
		st.escalated[k] = true
		due = append(due, f)
	}
	for k := range st.since {
		if _, ok := st.active[k]; !ok {
			delete(st.since, k)
			delete(st.escalated, k)
		}
	}
	sortStalls(due)
	for _, f := range due {
		held = append(held, now.Sub(st.since[f.key()]))
	}
	st.mu.Unlock()
	for i, f := range due {
		if !s.notify(mgr, "request", s.stallPacket(f, held[i])) {
			st.mu.Lock()
			delete(st.escalated, f.key()) // not delivered (the manager stopped): due again at the next sweep
			st.mu.Unlock()
			continue
		}
		ev := stallEvent("escalate", f)
		ev["held_s"] = int(held[i].Seconds())
		s.emit(events.TypeSwarmStall, ev)
	}
}

// stallPacket is the diagnosis of a finding for the manager. Everything in it is harness-written except a task's progress line,
// which is quoted as the agent's words.
func (s *Swarm) stallPacket(f Stall, held time.Duration) string {
	var b strings.Builder
	told := f.Notify
	if told == "" {
		told = "nobody"
	}
	fmt.Fprintf(&b, "Stall diagnosis (%s): %s. It has held for %s; %s was told when it began and it has not cleared.", f.Kind, f.Detail, held.Round(time.Minute), told)
	sn := s.Board.Snapshot()
	if t, ok := sn.Task(f.Task); ok && f.Task != "" {
		fmt.Fprintf(&b, "\nTask %s: %s, owner %s, %d earlier attempts, %d failed verifications in this one.", t.ID, t.Status, orNone(t.Owner), t.Attempts, t.VerificationFailures)
		if t.BlockedOn != "" {
			fmt.Fprintf(&b, " Blocked on %s.", t.BlockedOn)
		}
		if l := cleanText(t.Line, 160); l != "" {
			fmt.Fprintf(&b, " Its last progress line (the agent's words, data): %q.", l)
		}
	}
	if f.Agent != "" {
		for _, a := range sn.Agents {
			if a.ID == f.Agent {
				fmt.Fprintf(&b, "\nAgent %s (%s): %s.", a.ID, a.Role, a.State)
			}
		}
	}
	b.WriteString("\n" + stallRemedy(f.Kind, s.cfg.PersistentPools))
	return b.String()
}

// stallRemedy is what the manager can decide about a finding of kind k. pools says the swarm keeps persistent pools, which refuse
// a handover (as in stallNudge).
func stallRemedy(k StallKind, pools bool) string {
	switch k {
	case StallClaimedNoProgress:
		if pools {
			return "Decide: mail the worker what it is missing, or revise the task smaller (a persistent pool refuses a handover)."
		}
		return "Decide: mail the worker what it is missing, hand the task over to a fresh worker (task handover), or revise the task smaller."
	case StallOrphanedTask:
		return "Decide: fail the task with a reason, and reopen it if the work is still needed."
	case StallBlockedCycle:
		return "Decide: resume one of the tasks with what it needs, or fail one with reason blocked_on."
	case StallReviewStarved:
		return "Decide: read the task (task get) and accept or reject it."
	case StallIdleCapacity:
		return "Decide: give the idle workers the work the objective still needs, or let them stay idle if nothing is left."
	case StallManagerWaitingOnIdle:
		return "Decide: start a worker on the open work, or close the tasks nobody needs."
	}
	return "Decide what ends it, or close the task it concerns."
}

// orNone is s, or "none" when it is empty.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
