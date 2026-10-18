package swarm

import (
	"fmt"
	"slices"
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
// wait is refused once there is nothing else to wait for, and completion is vetoed (hold.go). Configured pools continue the same
// manager context beyond the per-run veto limit until the review settles, cancellation or budget exhaustion.

// ReviewerRole is the role whose accepted task reviews the merged result.
const ReviewerRole = "reviewer"

// maxStaffingLines bounds the per-role lines the live view adds.
const maxStaffingLines = 3

// reviewGate counts code merges and the review that covers them.
type reviewGate struct {
	mu        sync.Mutex
	merges    int            // changes that reached the shared tree: accepted implementation tasks, manager changes, revisions
	reviewed  int            // merges counted when a reviewer's task was last accepted
	last      string         // the last of them
	submitted map[string]int // merge generation covered by each submitted review
	// landed are the scopes each merge (by its number) may have touched, for the pools whose reconciled result it supersedes
	// (poolCurrent). A merge with no entry, or with no scopes, may have touched any file.
	landed map[int][]string
}

// landLocked counts a change that reached the shared tree as the next merge: id names it, scopes are the files (or globs) it may
// have touched, none when that is not known. g.mu is held.
func (g *reviewGate) landLocked(id string, scopes []string) {
	g.merges++
	g.last = id
	if g.landed == nil {
		g.landed = map[int][]string{}
	}
	g.landed[g.merges] = append([]string(nil), scopes...)
}

// touchedSince returns the first merge after generation that may have touched a file of scopes, or 0 when none did. A count
// below generation (a team restored with a fresh count) is a change too.
func (g *reviewGate) touchedSince(generation int, scopes []string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.merges < generation {
		return generation
	}
	for n := generation + 1; n <= g.merges; n++ {
		landed := g.landed[n]
		if len(landed) == 0 || len(scopes) == 0 {
			return n
		}
		for _, a := range landed {
			for _, b := range scopes {
				if scopesOverlap(a, b) {
					return n
				}
			}
		}
	}
	return 0
}

// noteAccepted records an accepted task for the review gate: a reviewer's task covers every merge before it, any other implementation
// task that is not documentation is a merge. An agreement is neither.
func (s *Swarm) noteAccepted(t Task) {
	_ = s.noteAcceptedCheck(t)(nil, t)
}

// noteAcceptedCheck runs last under the board lock, before acceptance is published.
// Resolve role metadata outside that lock to avoid taking the swarm lock under it.
func (s *Swarm) noteAcceptedCheck(t Task) TaskCheck {
	role := t.Role
	if t.Owner != "" {
		if r := s.roleOf(t.Owner); r != "" {
			role = r
		}
	}
	implementation := t.Pool == nil && t.Kind != TaskKindPlan && !s.isDocsTask(t) && !s.roles[role].ReadOnly
	return func(_ *Snapshot, t Task) error {
		g := &s.review
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case role == ReviewerRole && t.Parent == "":
			if generation := g.submitted[t.ID]; generation > g.reviewed {
				g.reviewed = generation
			}
		case implementation:
			g.landLocked(t.ID, t.Files)
		}
		return nil
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

// staffingLines are the manager's live-view lines about unused capacity: free slots while ready tasks have no owner, idle workers
// whose role has a ready task, and the ready tasks already queued for a free member of their role. It agrees with the spawn tool: a
// queued task (spawnqueue.go) is not offered again, and an idle member a pool keeps (poolKeeps: its leader, the owner of an open
// subtask, any member of a pool still agreeing on its plan) is not offered, since a spawn on it would only be queued. Nil when the
// team does not limit roles or nothing waits.
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
	queued := make(map[string]bool, len(s.queued))
	for id := range s.queued {
		queued[id] = true
	}
	s.mu.Unlock()
	held := map[string]string{} // idle member -> the parent whose pool keeps it
	for _, ids := range idle {
		for _, id := range ids {
			if h := poolHolder(snap, id); h != "" {
				held[id] = h
			}
		}
	}
	var ready []Task
	waiting := map[string][]string{} // role -> ready tasks already queued for a free member of it
	for _, t := range snap.Tasks {
		if t.Status != StatusTodo || t.Owner != "" {
			continue
		}
		if ok, _ := depsDone(snap, t); !ok {
			continue
		}
		if queued[t.ID] {
			waiting[t.Role] = append(waiting[t.Role], t.ID)
			continue
		}
		ready = append(ready, t)
	}
	var out []string
	roleLines := 0
	if len(ready) > 0 {
		out, roleLines = offerLines(ready, idle, held, slots, used, order)
	}
	return append(out, queuedLines(waiting, maxStaffingLines-roleLines)...)
}

// queuedLines tell the manager, per role and at most room lines, which ready tasks already wait in the spawn queue for a free member
// of that role, so it waits instead of spawning them again.
func queuedLines(waiting map[string][]string, room int) []string {
	var out []string
	for _, r := range roleKeys(waiting) {
		if len(out) >= room {
			break
		}
		who := "member"
		if r != "" {
			who = r + " member"
		}
		out = append(out, fmt.Sprintf("%s queued for the next free %s: the harness starts them; do not spawn them again (wait)",
			strings.Join(waiting[r], ", "), who))
	}
	return out
}

// offerLines are the live-view lines that offer capacity for the ready tasks no one has and no spawn waits for: the free slots, and
// per role the idle members free to take one now. held maps an idle member to the parent whose pool keeps it: such a member is offered
// only that parent, and only when it is one of the ready tasks (a parent returned to the board keeps its pool, and spawn reuses it),
// never another task. It returns the lines and how many of them are per-role lines, the count maxStaffingLines bounds; the free-slot
// line is outside the bound.
func offerLines(ready []Task, idle map[string][]string, held map[string]string, slots, used map[string]int, order []string) ([]string, int) {
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
	isReady := map[string]bool{}
	for _, t := range ready {
		if t.Role != "" {
			byRole[t.Role] = append(byRole[t.Role], t.ID)
			isReady[t.ID] = true
		}
	}
	roleLines := 0
	for _, r := range roleKeys(byRole) {
		var spare []string
		own := map[string][]string{} // ready parent -> idle members its own pool keeps
		for _, id := range idle[r] {
			switch h := held[id]; {
			case h == "":
				spare = append(spare, id)
			case isReady[h]:
				own[h] = append(own[h], id)
			}
		}
		for _, p := range roleKeys(own) {
			if roleLines >= maxStaffingLines {
				break
			}
			ids := own[p]
			sort.Strings(ids)
			out = append(out, fmt.Sprintf("%s idle in the pool of %s (%s), which can start: give it the task now (spawn agent=%s task=%s)",
				strings.Join(ids, ", "), p, r, ids[0], p))
			roleLines++
		}
		tasks := slices.DeleteFunc(slices.Clone(byRole[r]), func(id string) bool { return len(own[id]) > 0 })
		if len(spare) == 0 || len(tasks) == 0 || roleLines >= maxStaffingLines {
			continue
		}
		sort.Strings(spare)
		out = append(out, fmt.Sprintf("%s idle while %s (%s) can start: give it the task now (spawn agent=%s task=%s)",
			strings.Join(spare, ", "), strings.Join(tasks, ", "), r, spare[0], tasks[0]))
		roleLines++
	}
	return out, roleLines
}

// roleKeys returns the keys (roles) of m in order.
func roleKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
