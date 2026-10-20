package swarm

import (
	"fmt"
	"sort"
	"strings"
)

// Rigor is how much planning a manager's task gets before its work starts (task create rigor=…). The harness turns
// it into prerequisites the board already enforces: a read-only plan task whose accepted agreement the work inherits,
// and, for RigorReviewed, a reviewer's plan task over that plan whose verdict and amendments the work inherits too.
const (
	RigorPlan     = "plan"
	RigorReviewed = "reviewed"
)

// rigorPlanner is the read-only role that writes a rigor plan: the architect, else the scout, else the first other
// read-only role the team can staff (not the reviewer, which checks the plan, nor the mailman). "" when there is none.
func (s *Swarm) rigorPlanner() string {
	order := append([]string{"architect", "scout"}, workerRolesSorted(s.roles)...)
	for _, n := range order {
		r, ok := s.roles[n]
		if !ok || !r.ReadOnly || n == ReviewerRole || n == MailmanRoleName || n == "manager" || !s.canStaff(n) {
			continue
		}
		return n
	}
	return ""
}

// canStaff reports whether a worker of role can ever be started: the team has a slot for it, or there is no team.
func (s *Swarm) canStaff(role string) bool {
	if !s.teamLimited() {
		return true
	}
	n, _ := TeamCounts(s.cfg.Team)
	return n[role] > 0
}

// workerRolesSorted is the session's role names in the built-in worker order, then the rest by name.
func workerRolesSorted(roles Roles) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range WorkerRoleOrder {
		if _, ok := roles[n]; ok {
			out = append(out, n)
			seen[n] = true
		}
	}
	var rest []string
	for n := range roles {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// createRigorous creates spec with the prerequisites its rigor asks for and returns every task it created, the work
// task last. Nothing is left on the board when it fails: a prerequisite already created is closed as canceled.
func (s *Swarm) createRigorous(by string, spec TaskSpec, rigor string) ([]Task, error) {
	rigor = strings.ToLower(strings.TrimSpace(rigor))
	if rigor == "" {
		t, err := s.Board.CreateTask(by, spec)
		return []Task{t}, err
	}
	if rigor != RigorPlan && rigor != RigorReviewed {
		return nil, fmt.Errorf("rigor must be plan or reviewed")
	}
	if spec.Parent != "" || spec.Kind == TaskKindPlan {
		return nil, fmt.Errorf("rigor applies to a top-level work task: it adds the plan before the work itself")
	}
	planner := s.rigorPlanner()
	if planner == "" {
		return nil, fmt.Errorf("rigor=%s needs a read-only role to plan (architect or scout) that this team can staff: create the task without rigor, or plan it yourself and put the plan in its description", rigor)
	}
	if _, ok := s.roles[ReviewerRole]; rigor == RigorReviewed && (!ok || !s.canStaff(ReviewerRole)) {
		return nil, fmt.Errorf("rigor=reviewed needs a reviewer slot to check the plan: use rigor=plan, or add a reviewer to the team")
	}
	title := cleanText(spec.Title, maxTitleRunes)
	if title == "" {
		return nil, fmt.Errorf(`task needs a title: pass it in the "title" field`)
	}
	if dup, ok := openTwin(s.Board.Snapshot(), "", title); ok {
		return nil, fmt.Errorf("%s %q is already open with the same title (status %s): use it (task get id=%s) instead of creating another", dup.ID, dup.Title, dup.Status, dup.ID)
	}
	var made []Task
	undo := func(err error) ([]Task, error) {
		for _, t := range made {
			_ = s.Board.Fail(by, t.ID, closeAs(CloseCanceled), "its work task could not be created")
		}
		return nil, err
	}
	what := title
	if d := strings.TrimSpace(spec.Desc); d != "" {
		what += "\n\n" + d
	}
	plan, err := s.Board.CreateTask(by, TaskSpec{
		Title: "Plan: " + title, Role: planner, Kind: TaskKindPlan, Deps: spec.Deps, Files: spec.Files,
		Desc: "Research and plan this work before anyone implements it. Read the code it touches and what exists already (AGENTS.md Capabilities); " +
			"your agreement is the plan the implementer inherits: the approach, the files and interfaces it changes, the risks, and how to verify it (the tests to run or add).\n\nThe work:\n" + what,
	})
	if err != nil {
		return undo(err)
	}
	made = append(made, plan)
	deps := append(append([]string(nil), spec.Deps...), plan.ID)
	if rigor == RigorReviewed {
		rv, err := s.Board.CreateTask(by, TaskSpec{
			Title: "Review plan: " + title, Role: ReviewerRole, Kind: TaskKindPlan, Deps: []string{plan.ID}, Files: spec.Files,
			Desc: "Check the plan in " + plan.ID + "'s agreement before anyone implements it: is it right, complete and the simplest that works, does it reuse what exists, can it be verified? " +
				"Your agreement is the verdict: the amendments the implementer must follow, one per line, as acceptance criteria (\"none\" when the plan stands). A plan that cannot work: say so first, and why.\n\nThe work:\n" + what,
		})
		if err != nil {
			return undo(err)
		}
		made = append(made, rv)
		deps = append(deps, rv.ID)
	}
	spec.Deps = deps
	// First, since a long description is cut at the end.
	follow := "Follow the plan you inherit from " + plan.ID
	if rigor == RigorReviewed {
		follow += " with the review's amendments (" + deps[len(deps)-1] + ") as acceptance criteria"
	}
	follow += "; if it turns out wrong, block the task and mail the manager why instead of improvising."
	if d := strings.TrimSpace(spec.Desc); d != "" {
		follow += "\n\n" + d
	}
	spec.Desc = follow
	work, err := s.Board.CreateTask(by, spec)
	if err != nil {
		return undo(err)
	}
	return append(made, work), nil
}

// rigorChain names the prerequisites createRigorous made, for the manager: "T4 (Plan: …) and T5 (Review plan: …)".
func rigorChain(ts []Task) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = fmt.Sprintf("%s (%s, %s)", t.ID, t.Title, t.Role)
	}
	return strings.Join(parts, " and ")
}
