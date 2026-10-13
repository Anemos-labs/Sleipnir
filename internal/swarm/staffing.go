package swarm

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Staffing and review: what the manager's live view says about capacity it leaves unused, and the review gate.
//
// A team has slots by role (team.go). The manager's live view says, under the team's occupancy, how many slots are free while
// tasks that could start have no owner, and which idle worker could take a ready task of its role now. It says so from the board
// and the roster alone, without clocks, so the same board reads the same.
//
// The review gate: in a team with a reviewer slot, the goal is not reported met until a reviewer has looked at the merged result.
// Every accepted implementation task of a writer (not an agreement, not the docs worker's catch-up) is a code merge; a task owned by
// a reviewer and accepted after the last code merge is the review of it. Until there is one, the manager's live view says so, its
// wait is refused once there is nothing else to wait for, and a batch run's final answer is vetoed (hold.go), as many times as the
// agent loop bounds vetoes of one run (maxStopVetoes), so a manager that will not review cannot be held for ever.

// ReviewerRole is the role whose accepted task reviews the merged result.
const ReviewerRole = "reviewer"

// maxStaffingLines bounds the per-role lines the live view adds.
const maxStaffingLines = 3

// reviewGate counts code merges and the review that covers them.
type reviewGate struct {
	mu        sync.Mutex
	merges    int            // accepted implementation tasks of writers
	reviewed  int            // merges counted when a reviewer's task was last accepted
	last      string         // the last of them
	submitted map[string]int // merge generation covered by each submitted review
}

// noteAccepted records an accepted task for the review gate: a reviewer's task covers every merge before it, any other implementation
// task that is not documentation is a merge. An agreement is neither.
func (s *Swarm) noteAccepted(t Task) {
	role := t.Role
	if t.Owner != "" {
		if r := s.roleOf(t.Owner); r != "" {
			role = r
		}
	}
	g := &s.review
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case role == ReviewerRole:
		if generation := g.submitted[t.ID]; generation > g.reviewed {
			g.reviewed = generation
		}
	case t.Kind == TaskKindPlan || s.isDocsTask(t) || s.roles[role].ReadOnly:
	default:
		g.merges++
		g.last = t.ID
	}
}

// reviewSubmission records the merge generation before a review enters review status.
// A later acceptance cannot turn an older report into a review of newer work.
func (s *Swarm) reviewSubmission(t Task) TaskCheck {
	role := t.Role
	if r := s.roleOf(t.Owner); r != "" {
		role = r
	}
	return func(_ *Snapshot, _ Task) error {
		if role == ReviewerRole {
			g := &s.review
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.submitted == nil {
				g.submitted = map[string]int{}
			}
			g.submitted[t.ID] = g.merges
		}
		return nil
	}
}

// implementationTask reports whether a task represents code, rather than an agreement or a read-only report.
func (s *Swarm) implementationTask(t Task) bool {
	role := t.Role
	if t.Owner != "" {
		if r := s.roleOf(t.Owner); r != "" {
			role = r
		}
	}
	return t.Kind != TaskKindPlan && !s.isDocsTask(t) && !s.roles[role].ReadOnly
}

// hasReviewer reports whether the team has a reviewer slot.
func (s *Swarm) hasReviewer() bool {
	n, _ := TeamCounts(s.cfg.Team)
	return n[ReviewerRole] > 0
}

// reviewPending returns the last merged task when the team has a reviewer and no reviewer task was accepted after it, else "".
func (s *Swarm) reviewPending() string {
	if !s.hasReviewer() {
		return ""
	}
	g := &s.review
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.merges > g.reviewed {
		return g.last
	}
	return ""
}

// reviewLine is the review gate's sentence for the manager: what is unreviewed and what to do.
func reviewLine(last string) string {
	return "review: " + last + " and the work merged before it have not been reviewed: before you report the goal met, create a task for the " +
		ReviewerRole + " to review the merged result against the agreement (spawn role=" + ReviewerRole + "), and accept its report"
}

// staffingLines are the manager's live-view lines about unused capacity: free slots while ready tasks have no owner, and idle
// workers whose role has a ready task. Nil when the team does not limit roles or nothing waits.
func (s *Swarm) staffingLines(snap *Snapshot) []string {
	if !s.teamLimited() || snap == nil {
		return nil
	}
	slots, order := TeamCounts(s.cfg.Team)
	used := map[string]int{}
	idle := map[string][]string{}
	s.mu.Lock()
	for _, m := range s.members {
		if m.manager || m.service {
			continue
		}
		used[m.role]++
		m.mu.Lock()
		if m.life == lifeIdle {
			idle[m.role] = append(idle[m.role], m.id)
		}
		m.mu.Unlock()
	}
	s.mu.Unlock()
	var ready []Task
	for _, t := range snap.Tasks {
		if t.Status != StatusTodo || t.Owner != "" {
			continue
		}
		if ok, _ := depsDone(snap, t); ok {
			ready = append(ready, t)
		}
	}
	if len(ready) == 0 {
		return nil
	}
	var free []string
	nfree := 0
	for _, r := range order {
		if n := slots[r] - used[r]; n > 0 {
			free = append(free, r)
			nfree += n
		}
	}
	var out []string
	if nfree > 0 {
		out = append(out, fmt.Sprintf("%d free slot(s) (%s), %d ready task(s) with no owner: start workers (spawn), in parallel where the tasks are independent",
			nfree, strings.Join(free, ", "), len(ready)))
	}
	byRole := map[string][]string{}
	for _, t := range ready {
		if t.Role != "" {
			byRole[t.Role] = append(byRole[t.Role], t.ID)
		}
	}
	roles := make([]string, 0, len(byRole))
	for r := range byRole {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		if len(out) > maxStaffingLines {
			break
		}
		ids := idle[r]
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		out = append(out, fmt.Sprintf("%s idle while %s (%s) can start: give it the task now (spawn agent=%s task=%s)",
			strings.Join(ids, ", "), strings.Join(byRole[r], ", "), r, ids[0], byRole[r][0]))
	}
	return out
}
