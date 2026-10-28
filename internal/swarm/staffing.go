package swarm

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Staffing and review: what the manager's live view says about capacity it leaves unused, and the review gate.
//
// A team has slots by role (team.go). The manager's live view says, under the team's occupancy, how many slots are free while
// tasks that could start have no owner, and which idle worker could take a ready task of its role now. It says so from the board
// and the roster alone, without clocks, so the same board reads the same.
//
// The review gate: in a team with a reviewer slot, the goal is not reported met until a reviewer has looked at the merged result.
// Every accepted implementation task of a writer (not an agreement, not the docs worker's catch-up) that changed the tree is a code merge; a task owned by
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
	// byModel is the newest merge generation an accepted review covered, by the model of the reviewer that wrote it: with a
	// quorum of two (reviewQuorum), merges count as reviewed only up to the generation two different models both covered.
	byModel map[string]int
	// landed are the scopes each merge (by its number) may have touched, for the pools whose reconciled result it supersedes
	// (poolCurrent). A merge with no entry, or with no scopes, may have touched any file.
	landed   map[int][]string
	landedID map[int]string // the task of each merge, by its number
	// began is when a run first held each assignment ("T3#rev"), ended when its work was last submitted, and took how long the
	// work of each merge (by its number) ran from the one to the other: what a review of it is measured against (reviewOverrun).
	// The wait in review for the manager's decision is the manager's time, not the work's.
	began map[string]time.Time
	ended map[string]time.Time
	took  map[int]time.Duration
	// trees is the work tree each review ("T6#rev") began on (noteReviewBegan), and reviewedTree the tree of the review that
	// last moved reviewed: what the accepted review read. A merge after it that changed tests and docs alone leaves it standing
	// (testsSinceReview).
	trees        map[string]string
	reviewedTree string
}

// noteBegan records when a run first took on an assignment, for the duration of the work (took). Later runs of the same
// assignment (a restart, a handover) keep the first time: the work ran since then.
func (g *reviewGate) noteBegan(id string, rev uint64, at time.Time) {
	key := id + "#" + strconv.FormatUint(rev, 10)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.began == nil {
		g.began = map[string]time.Time{}
	}
	if _, ok := g.began[key]; !ok {
		g.began[key] = at
	}
}

// beganAt is when a run first held the assignment of task id at rev, false when none has.
func (g *reviewGate) beganAt(id string, rev uint64) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.began[id+"#"+strconv.FormatUint(rev, 10)]
	return at, ok
}

// tookLocked is how long the work of task t ran: from the first run that held its current assignment to its last submission
// (to now when none is recorded), 0 when no run did. g.mu is held.
func (g *reviewGate) tookLocked(t Task, now time.Time) time.Duration {
	key := t.ID + "#" + strconv.FormatUint(t.Rev, 10)
	at, ok := g.began[key]
	if !ok {
		return 0
	}
	end := now
	if e, ok := g.ended[key]; ok && e.After(at) && e.Before(now) {
		end = e
	}
	if end.Before(at) {
		return 0
	}
	return end.Sub(at)
}

// unreviewedWork is the longest duration among the merges no accepted review covers (parts built side by side are read side
// by side: the critical path of the work, not its sum), and their ids in merge order.
func (g *reviewGate) unreviewedWork() (time.Duration, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var longest time.Duration
	var ids []string
	for n := g.reviewed + 1; n <= g.merges; n++ {
		longest = max(longest, g.took[n])
		if id := g.landedID[n]; id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return longest, ids
}

// landedIDs is the set of tasks whose change reached the shared tree in this run.
func (g *reviewGate) landedIDs() map[string]bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]bool, len(g.landedID))
	for _, id := range g.landedID {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

// landLocked counts a change that reached the shared tree as the next merge: id names it, scopes are the files (or globs) it may
// have touched, none when that is not known, and took is how long its work ran. g.mu is held.
func (g *reviewGate) landLocked(id string, scopes []string, took time.Duration) {
	g.merges++
	g.last = id
	if g.landed == nil {
		g.landed, g.landedID, g.took = map[int][]string{}, map[int]string{}, map[int]time.Duration{}
	}
	g.landed[g.merges] = append([]string(nil), scopes...)
	g.landedID[g.merges] = id
	g.took[g.merges] = took
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
// task that is not documentation is a merge, unless the merge queue found nothing to merge. An agreement is neither.
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
	if r, ok := s.mergedFor(t); implementation && ok && r.empty {
		// Nothing reached the shared tree (a tester's check that left no test behind): no merge, so a review running beside it
		// still covers the result it reads.
		implementation = false
	}
	model, quorum := "", 1
	if role == ReviewerRole {
		model, quorum = s.memberModel(t.Owner), s.reviewQuorum()
	}
	// Outside the board lock: they read the work tree. A review accepted after merges that changed tests and docs alone since
	// it began covers them too: a tester's suite accepted while the review waited for its verdict is one.
	stands := implementation && s.testsSinceReview()
	// The merge count is read before the tree: a merge that lands while the tree is read leaves it behind, and the review
	// then covers nothing past its generation.
	reviewStands, treeMerges := false, 0
	if role == ReviewerRole && t.Parent == "" && quorum == 1 {
		s.review.mu.Lock()
		base := s.review.trees[t.ID+"#"+strconv.FormatUint(t.Rev, 10)]
		treeMerges = s.review.merges
		s.review.mu.Unlock()
		reviewStands = s.testsOnlySince(base)
	}
	now := s.deps.Now()
	return func(_ *Snapshot, t Task) error {
		g := &s.review
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case role == ReviewerRole && t.Parent == "":
			generation := g.submitted[t.ID]
			if g.byModel == nil {
				g.byModel = map[string]int{}
			}
			if generation > g.byModel[model] {
				g.byModel[model] = generation
			}
			if covered := quorumCovered(g.byModel, quorum); covered > g.reviewed {
				g.reviewed = covered
				g.reviewedTree = ""
				if quorum == 1 {
					g.reviewedTree = g.trees[t.ID+"#"+strconv.FormatUint(t.Rev, 10)]
				}
				if reviewStands && generation == covered && g.merges == treeMerges {
					g.reviewed = g.merges
				}
			}
		case implementation:
			fresh := g.reviewed > 0 && g.reviewed == g.merges
			g.landLocked(t.ID, t.Files, g.tookLocked(t, now))
			if stands && fresh {
				// Everything the tree gained since the accepted review began is tests and docs: the review stands for it.
				g.reviewed = g.merges
			}
		}
		return nil
	}
}

// noteReviewBegan records the work tree a review began on, the first time a run of a reviewer holds the assignment: what an
// accepted review read (reviewGate.trees). A tree that cannot be read (no git work tree) records nothing, and the review then
// stands for no later merge.
func (s *Swarm) noteReviewBegan(t Task) {
	if s.deps.Root == "" || t.Parent != "" || s.reviewQuorum() != 1 {
		return
	}
	key := t.ID + "#" + strconv.FormatUint(t.Rev, 10)
	g := &s.review
	g.mu.Lock()
	_, seen := g.trees[key]
	g.mu.Unlock()
	if seen {
		return
	}
	tree := s.workTree()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.trees == nil {
		g.trees = map[string]string{}
	}
	if _, ok := g.trees[key]; !ok {
		g.trees[key] = tree
	}
}

// testsSinceReview reports whether every merge so far is reviewed and the work tree has changed, since the accepted review
// began, in test files and docs alone (testFile, docsFile). A tester's suite that lands after the review then leaves the review
// standing: a test cannot change what the product does, and the docs catch-up is not reviewed either. Any other change, a file
// that cannot be classified, or a tree that cannot be read keeps the review due.
func (s *Swarm) testsSinceReview() bool {
	g := &s.review
	g.mu.Lock()
	base, ok := g.reviewedTree, g.reviewed > 0 && g.reviewed == g.merges
	g.mu.Unlock()
	return ok && s.testsOnlySince(base)
}

// testsOnlySince reports whether the work tree has changed since tree base in test files and docs alone. An empty base, a tree
// that cannot be read, or any other change is false.
func (s *Swarm) testsOnlySince(base string) bool {
	if base == "" {
		return false
	}
	now := s.workTree()
	if now == "" {
		return false
	}
	repo, err := gitx.Open(s.deps.Root)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed, err := repo.ChangedPaths(ctx, base, now)
	if err != nil {
		return false
	}
	for _, p := range changed {
		if !testFile(p) && !docsFile(p) {
			return false
		}
	}
	return true
}

// workTree is the id of a git tree holding the shared work tree as a commit would record it, or "" when it cannot be read.
func (s *Swarm) workTree() string {
	if s.deps.Root == "" {
		return ""
	}
	repo, err := gitx.Open(s.deps.Root)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tree, err := repo.SnapshotTree(ctx)
	if err != nil {
		return ""
	}
	return tree
}

// docsFile reports whether a path is documentation the docs worker keeps (docsScope): a Markdown file, or a file under docs/.
func docsFile(p string) bool {
	p = strings.TrimSpace(p)
	return strings.EqualFold(path.Ext(p), ".md") || strings.HasPrefix(p, "docs/")
}

// reviewSubmission records the merge generation before a review enters review status (a later acceptance cannot turn an
// older report into a review of newer work), and when any task's work was submitted, where its duration ends (tookLocked).
func (s *Swarm) reviewSubmission(t Task) TaskCheck {
	role := t.Role
	if r := s.roleOf(t.Owner); r != "" {
		role = r
	}
	now := s.deps.Now()
	return func(_ *Snapshot, _ Task) error {
		g := &s.review
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.ended == nil {
			g.ended = map[string]time.Time{}
		}
		g.ended[t.ID+"#"+strconv.FormatUint(t.Rev, 10)] = now
		if role == ReviewerRole {
			if g.submitted == nil {
				g.submitted = map[string]int{}
			}
			g.submitted[t.ID] = g.merges
		}
		return nil
	}
}

// waitingSubmission is a submission the manager has not decided: a task in review that someone else submitted, and how
// long ago. The wait runs from the submission the review gate saw (reviewGate.ended); one it did not see counts from now.
type waitingSubmission struct {
	ID     string
	Rev    uint64
	Waited time.Duration
}

// submissionsWaiting lists the submissions waiting for the manager's decision, longest wait first, then by task id. The
// manager's own tasks are left out: nobody waits on the manager for those but the manager.
func (s *Swarm) submissionsWaiting(snap *Snapshot, now time.Time) []waitingSubmission {
	mgr := s.ManagerID()
	g := &s.review
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []waitingSubmission
	for _, t := range snap.Tasks {
		if t.Status != StatusReview || t.Owner == mgr {
			continue
		}
		w := waitingSubmission{ID: t.ID, Rev: t.Rev}
		if at, ok := g.ended[t.ID+"#"+strconv.FormatUint(t.Rev, 10)]; ok && now.After(at) {
			w.Waited = now.Sub(at)
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Waited != out[j].Waited {
			return out[i].Waited > out[j].Waited
		}
		return lessTaskID(out[i].ID, out[j].ID)
	})
	return out
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

// reviewLine is the review gate's sentence for the manager: what is unreviewed and what to do. After an accepted review it
// names the merges since, so the next review covers them and not the whole result again.
func (s *Swarm) reviewLine(last string) string {
	what, subject := last+" and the work merged before it have", "the merged result"
	if s.reviewedBefore() {
		_, ids := s.review.unreviewedWork()
		if len(ids) == 0 {
			ids = []string{last}
		}
		plural := "s"
		if len(ids) == 1 {
			plural = ""
		}
		what = "the merge" + plural + " since the accepted review (" + strings.Join(ids, ", ") + ") " + map[bool]string{true: "have", false: "has"}[len(ids) > 1]
		subject = "that work (the accepted review stands for the rest)"
	}
	if s.reviewQuorum() > 1 {
		return "review: " + what + " not been reviewed by two reviewers on different models: before you report the goal met, " +
			"give a review of " + subject + " against the agreement to each " + ReviewerRole + " slot that runs on a model the reviews so far did not (spawn role=" + ReviewerRole + " agent=<id>), and accept both reports"
	}
	return "review: " + what + " not been reviewed: before you report the goal met, create a task for the " +
		ReviewerRole + " to review " + subject + " against the agreement (spawn role=" + ReviewerRole + "), and accept its report"
}

// reviewedBefore reports whether an accepted review covers some merge of this run.
func (s *Swarm) reviewedBefore() bool {
	g := &s.review
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reviewed > 0
}

// reviewQuorum is how many reviewers on different models must cover a merge before the goal may be reported met: two when the
// team's reviewer slots run on two or more different models (independent vantages catch what one model misses), else one.
func (s *Swarm) reviewQuorum() int {
	models := map[string]bool{}
	for i, r := range s.cfg.Team {
		if r == ReviewerRole {
			models[s.modelFor(ReviewerRole, i).Model.ID] = true
		}
	}
	if len(models) >= 2 {
		return 2
	}
	return 1
}

// memberModel is the model id agent runs on, "" when it is not a member.
func (s *Swarm) memberModel(agent string) string {
	if m := s.get(agent); m != nil {
		return m.model.ID
	}
	return ""
}

// quorumCovered is the newest merge generation that at least quorum of the models in byModel covered: the quorum-th largest
// value, 0 when fewer models have reviewed.
func quorumCovered(byModel map[string]int, quorum int) int {
	if len(byModel) < quorum {
		return 0
	}
	vs := make([]int, 0, len(byModel))
	for _, v := range byModel {
		vs = append(vs, v)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(vs)))
	return vs[quorum-1]
}

// staffingLines are the manager's live-view lines about unused capacity: free slots while ready tasks have no owner, idle workers
// whose role has a ready task, and the ready tasks already queued for a free member of their role. It agrees with the spawn tool: a
// queued task (spawnqueue.go) is not offered again, and an idle member a pool keeps (poolKeeps: its leader, the owner of an open
// subtask, any member of a pool still agreeing on its plan) is not offered, since a spawn on it would only be queued. While a task is
// open, idle members of a role with nothing ready or queued for it are named in one line asking the manager to plan independent work
// for them (unplannedLine); it counts against maxStaffingLines. Nil when the team does not limit roles or nothing waits.
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
	q := queuedLines(waiting, maxStaffingLines-roleLines)
	for i, r := range roleKeys(waiting) {
		if i < len(q) && len(waiting[r]) > 0 {
			if line := spillLine(s.freeWriters(r), r, waiting[r][0]); line != "" {
				q[i] += "; " + line
			}
		}
	}
	out = append(out, q...)
	if roleLines+len(q) < maxStaffingLines && openTask(snap) {
		if line := unplannedLine(s.unplannedIdle(snap, idle, held, ready, waiting)); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// emptyBoardAfter is how many requests the manager of a configured team makes in one run, with no open task on the board and no
// worker at work, before its live view says that the team waits on it (emptyBoardLine).
const emptyBoardAfter = 6

// soloRun counts the manager's requests in its current run while the board has no open task and no worker runs. mu guards it.
type soloRun struct {
	mu sync.Mutex
	n  int
}

// reset starts the count again, for a new run of the manager.
func (c *soloRun) reset() {
	c.mu.Lock()
	c.n = 0
	c.mu.Unlock()
}

// emptyBoardLine is the manager's live-view line for a run that reads and plans alone while the whole team waits: more than
// emptyBoardAfter requests with no open task and no worker at work. A manager that maps the whole code base before its first task
// spends the team's time on reading each worker does for its own task. The staffing lines say nothing then, since nothing is open.
// The count starts again with each run of the manager (runManager) and whenever a task is open or a worker runs; "" while it is below the bound,
// and for a team that does not limit roles.
func (s *Swarm) emptyBoardLine(snap *Snapshot) string {
	if !s.teamLimited() {
		return ""
	}
	busy := openTask(snap) || s.workerRunning()
	s.solo.mu.Lock()
	defer s.solo.mu.Unlock()
	if busy {
		s.solo.n = 0
		return ""
	}
	s.solo.n++
	if s.solo.n <= emptyBoardAfter {
		return ""
	}
	return fmt.Sprintf("nothing is on the board and none of the team's %d worker slots is at work after %d requests of yours in this run: unless this request is one you "+
		"handle yourself, create its tasks now (task create, one area each, with dependencies) and spawn them; each worker reads the code "+
		"its task touches, so you need only enough of it to scope the tasks", len(s.cfg.Team), s.solo.n-1)
}

// workerRunning reports whether a member other than the manager and the service agents is at work.
func (s *Swarm) workerRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.members {
		if m.manager || m.service {
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

// unplannedIdle returns, by role, the idle members nothing is planned for: not kept by a pool (held), owning no open task, and of
// a role with no ready unowned task and no queued spawn. The docs worker is left out when the harness keeps it at work (AutoDocs).
func (s *Swarm) unplannedIdle(snap *Snapshot, idle map[string][]string, held map[string]string, ready []Task, waiting map[string][]string) map[string][]string {
	planned := map[string]bool{}
	for _, t := range ready {
		planned[t.Role] = true
	}
	for r := range waiting {
		planned[r] = true
	}
	out := map[string][]string{}
	for r, ids := range idle {
		if planned[r] || r == DocsRole && s.AutoDocs() {
			continue
		}
		for _, id := range ids {
			if held[id] == "" && !ownsOpenTask(snap, id) {
				out[r] = append(out[r], id)
			}
		}
	}
	return out
}

// unplannedLine is the live-view line that names the idle members nothing is planned for (unplannedIdle), by role in role order and
// by id within a role, and tells the manager to plan independent work for them or leave them idle; "" when there are none.
func unplannedLine(byRole map[string][]string) string {
	var groups []string
	first := ""
	for _, r := range roleKeys(byRole) {
		ids := slices.Clone(byRole[r])
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		if first == "" {
			first = ids[0]
		}
		group := strings.Join(ids, ", ")
		if r != "" {
			group += " (" + r + ")"
		}
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return ""
	}
	return fmt.Sprintf("idle with nothing planned for them: %s: plan the next independent work for them (task create with file scopes "+
		"disjoint from the open tasks, then spawn agent=%s task=<id>), or leave them idle if nothing independent remains",
		strings.Join(groups, "; "), first)
}

// queuedLines tell the manager, per role and at most room lines, which ready tasks already wait in the spawn queue for a free member
// of that role, so it waits instead of spawning them again.
func queuedLines(waiting map[string][]string, room int) []string {
	var out []string
	for _, r := range roleKeys(waiting) {
		if len(out) >= room {
			break
		}
		who, again := "member", "do not spawn them again"
		if r != "" {
			who, again = r+" member", "do not spawn them on "+r+" again"
		}
		out = append(out, fmt.Sprintf("%s queued for the next free %s: the harness starts them; %s (wait)",
			strings.Join(waiting[r], ", "), who, again))
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

// freeWriters are the roles of the team, other than except, in team order, with a slot or an idle member free for a task now:
// roles that write code, so neither a read-only role, nor docs, nor the tester, whose tasks are tests of their own. A task queued
// behind the one member of its role can start on one of them. A manager that gave four independent modules to backend, the team's
// one backend slot, ran them one after another while the frontend and devops slots stayed empty.
func (s *Swarm) freeWriters(except string) []string {
	if !s.teamLimited() || !s.isolated() && s.activeWriters() >= s.cfg.MaxWriters {
		return nil // at the writer limit no writer is admitted, whatever its role's slots
	}
	slots, order := TeamCounts(s.cfg.Team)
	snap := s.Board.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	used, idle := map[string]int{}, map[string]bool{}
	for _, m := range s.members {
		if m.manager || m.service {
			continue
		}
		used[m.role]++
		m.mu.Lock()
		free := m.life == lifeIdle && m.task == ""
		m.mu.Unlock()
		// An idle member that a pool keeps, or that owns an open solo task, is not free for another task (spawn's busy).
		if free && poolHolder(snap, m.id) == "" && soloTaskOf(snap, m.id, "") == "" {
			idle[m.role] = true
		}
	}
	var out []string
	for _, r := range order {
		if r == except || r == DocsRole || r == "tester" || s.roles[r].ReadOnly {
			continue
		}
		if slots[r] > used[r] || idle[r] {
			out = append(out, r)
		}
	}
	return out
}

// spillLine is what a queued task's spawn and the live view add when other writer roles are free (freeWriters): "" when none is.
func spillLine(free []string, role, id string) string {
	if len(free) == 0 {
		return ""
	}
	return fmt.Sprintf("free now: %s: one of them can take %s at once if it needs nothing only %s knows (spawn role=%s task=%s)",
		strings.Join(free, ", "), id, role, free[0], id)
}

// reviewerModelHint is a one-line suggestion for a team whose reviewers all run on a model its writers use while the team has another
// model as well: a reviewer on a model no writer runs on does not share the writers' blind spots, nor a backdoor trained into their
// weights (docs/SECURITY.md). It is a suggestion and never a default: a team on a single model gets none (not everyone has two, or
// wants them), and neither does one whose reviewers run on two models (the review quorum already asks for both) or on a model no
// writer uses. "" when there is nothing to suggest.
func (s *Swarm) reviewerModelHint() string {
	writers, reviewers := map[string]bool{}, map[string]bool{}
	all := map[string]string{} // model id -> how --role-model names it (provider/model)
	for i, r := range s.cfg.Team {
		rm := s.modelFor(r, i)
		id := rm.Model.ID
		all[id] = id
		if rm.Provider != nil {
			if name := rm.Provider.Profile().Name; name != "" {
				all[id] = name + "/" + id
			}
		}
		switch {
		case r == ReviewerRole:
			reviewers[id] = true
		case !s.roles[r].ReadOnly:
			writers[id] = true
		}
	}
	if len(reviewers) != 1 || len(all) < 2 {
		return ""
	}
	var rv string
	for id := range reviewers {
		rv = id
	}
	if !writers[rv] {
		return ""
	}
	var others []string
	for id, ref := range all {
		if id != rv {
			others = append(others, ref)
		}
	}
	sort.Strings(others)
	return fmt.Sprintf("tip: the reviewers run on %s, as writers do; a reviewer on another model (--role-model reviewer=%s) catches "+
		"what the writers' model misses, a backdoor in its weights included", rv, others[0])
}
