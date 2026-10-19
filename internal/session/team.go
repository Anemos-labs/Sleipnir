package session

import (
	"context"
	"fmt"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// MinTeamWorkers is the smallest team a person can ask for: one worker of each
// built-in role. A smaller team misses a role the work may need; a single agent
// (no team) is the other choice.
const MinTeamWorkers = 8

// autoDocsOn reports whether the session asks for auto-docs: Options.AutoDocs, else
// swarm.auto_docs, else true.
func (s *Session) autoDocsOn() bool {
	if s.plugins != nil {
		return s.plugins.On(plugin.AutoDocs) // --auto-docs and swarm.auto_docs are layers of the plugin's state
	}
	if s.opts.AutoDocs != nil {
		return *s.opts.AutoDocs
	}
	if s.cfg != nil && s.cfg.Swarm.AutoDocs != nil {
		return *s.cfg.Swarm.AutoDocs
	}
	return true
}

// workerOrder lists the roles a team can have: the built-in worker roles in
// swarm.WorkerRoleOrder, then the project's roles by name. The manager, the mailman
// and legacy roles are not among them.
func workerOrder(roles swarm.Roles) []string {
	var out []string
	builtin := map[string]bool{}
	for _, r := range swarm.WorkerRoleOrder {
		builtin[r] = true
		if _, ok := roles[r]; ok {
			out = append(out, r)
		}
	}
	var extra []string
	for n := range roles {
		if !builtin[n] && n != "manager" && n != swarm.MailmanRoleName {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// teamFor returns the session's team: Options.Team when it names one, else, when
// Options.DefaultTeam asks for it, the default team of the built-in and project roles
// (one slot per role, see swarm.DefaultTeam). Otherwise, and for a caller that
// restricted the roles (a training task's team), there is no team: roles are limited
// by the worker count alone.
func (s *Session) teamFor(roles swarm.Roles, workers int) ([]TeamSlot, error) {
	o := s.opts
	if len(o.Team) > 0 {
		var bad []string
		for _, t := range o.Team {
			if _, ok := roles[t.Role]; !ok || t.Role == "manager" || t.Role == swarm.MailmanRoleName {
				bad = append(bad, t.Role)
			}
		}
		if len(bad) > 0 {
			return nil, fmt.Errorf("--role-model: no worker role named %s in this session (its roles: %s)", strings.Join(bad, ", "), strings.Join(workerOrder(roles), ", "))
		}
		return o.Team, nil
	}
	if !o.DefaultTeam || o.Roles != nil {
		return nil, nil
	}
	var out []TeamSlot
	for _, r := range swarm.DefaultTeam(workerOrder(roles), workers) {
		out = append(out, TeamSlot{Role: r})
	}
	return out, nil
}

// Team is the session's team, one entry per worker slot in team order: the one it
// was given or the default one; nil for a single agent or a team without slots.
func (s *Session) Team() []TeamSlot { return append([]TeamSlot(nil), s.team...) }

// teamNames is the role of each slot of team.
func teamNames(team []TeamSlot) []string {
	out := make([]string, len(team))
	for i, t := range team {
		out[i] = t.Role
	}
	return out
}

// roleCaps says in a few words what an agent of role may do: whether it edits files,
// what it runs, and whether it reaches the web.
func (s *Session) roleCaps(name string, r swarm.Role) string {
	switch {
	case name == "manager":
		return "edits files and runs commands under the session's permissions · web: fetch, search"
	case r.ReadOnly:
		if _, own := s.ext.profiles[name]; own {
			return "read-only · shell and web: as its definition allows"
		}
		return "read-only · shell: read-only commands and tests · web: fetch, search"
	default:
		return "edits files · shell and web: as the session's mode and rules allow"
	}
}

// roleSummary is the one line that says what role is for: the built-in summary, or
// the project definition's description.
func (s *Session) roleSummary(name string) string {
	if t, ok := swarm.RoleSummaries[name]; ok {
		return t
	}
	for _, d := range s.ext.defs {
		if d.Name == name {
			return d.Description
		}
	}
	return ""
}

// teamCard is the part of the manager's pin that describes its team: each role with
// its slots, what it may do and what it is for, and how documentation is kept. It
// is written once when the session starts, so the pin stays a stable cache prefix;
// the live occupancy of the slots is in the manager's live view.
func (s *Session) teamCard(roles swarm.Roles, team []TeamSlot, autoDocs bool) string {
	counts, order := swarm.TeamCounts(teamNames(team))
	var b strings.Builder
	b.WriteString("\n\nYour team (spawn within these slots; the live view shows which are busy, idle or free):")
	for _, name := range order {
		r := roles[name]
		fmt.Fprintf(&b, "\n- %s ×%d (ids %s-N): %s", name, counts[name], r.Short, s.roleCaps(name, r))
		if name == swarm.DocsRole && autoDocs {
			b.WriteString(" · standing (auto)")
		}
		if s.opts.FixedSteps {
			fmt.Fprintf(&b, " · max %d steps a task", r.MaxSteps)
		} else {
			fmt.Fprintf(&b, " · %d steps a task, more while it keeps editing", r.MaxSteps)
		}
		if t := s.roleSummary(name); t != "" {
			b.WriteString(" · " + t)
		}
	}
	if len(order) > 0 {
		b.WriteString("\nYou: " + s.roleCaps("manager", roles["manager"]) + ".")
	}
	if autoDocs {
		b.WriteString("\n" + managerDocsNote)
	}
	if counts[swarm.ReviewerRole] > 0 {
		b.WriteString("\n" + managerReviewNote)
	}
	if s.managerApprovesOn() {
		b.WriteString("\n" + managerApprovesNote)
	}
	return b.String()
}

// managerReviewNote tells the manager of a team with a reviewer slot about the review gate (swarm staffing.go).
const managerReviewNote = `Review is required. Once the implementation is merged, give the reviewer a task over the merged result (the agreement, the diff, the tests); the goal is held until a reviewer's task is accepted after the last merge, and your report says what the review found.`

// managerApprovesNote tells the manager that its workers' routine questions come to it first (swarm approvals.go).
const managerApprovesNote = `Your workers' routine permission questions come to you first, as "Approval A<n>" messages; answer each at once with approve (yes, no with text, or escalate). Unanswered within a minute, a question goes to the person.`

// delegateApproval is the permission engine's delegate (perm.Config.Delegate): in a team whose manager answers routine questions,
// the swarm puts a worker's question to the manager first; otherwise it settles nothing.
func (s *Session) delegateApproval(ctx context.Context, r perm.Request) (perm.Decision, string) {
	if s.Swarm == nil {
		return perm.Decision{}, ""
	}
	return s.Swarm.DelegateApproval(ctx, r)
}

// ManagerApproves reports whether this session's manager answers its workers' routine permission questions first: a team, with
// --manager-approves and permissions.manager_approves not turning it off.
func (s *Session) ManagerApproves() bool { return len(s.team) > 0 && s.managerApprovesOn() }

// managerApprovesOn reports whether the manager answers its workers' routine permission questions: Options.ManagerApproves, else
// permissions.manager_approves, else true.
func (s *Session) managerApprovesOn() bool {
	if s.plugins != nil {
		return s.plugins.On(plugin.ManagerApproves) // --manager-approves and permissions.manager_approves are layers of it
	}
	if s.opts.ManagerApproves != nil {
		return *s.opts.ManagerApproves
	}
	if s.cfg != nil && s.cfg.Permissions.ManagerApproves != nil {
		return *s.cfg.Permissions.ManagerApproves
	}
	return true
}

// managerDocsNote tells the manager how auto-docs works.
const managerDocsNote = `Documentation is automatic. After the tasks and agreements you accept, the harness gives the docs worker a "docs: catch up" task with the changes (the first once an implementation task is accepted); it keeps README, docs/ and AGENTS.md current (AGENTS.md is also the team's map of the project: commands, layout, conventions, decisions). Do not assign it routine catch-up and do not count its slot as free capacity; give it a task only for documentation nobody has asked for yet (a new guide). Accept its passes after a look. Before you report the goal met, let its last pass finish, and say in your report what it changed.`

// workerDocsNote tells every other worker how documentation is kept.
const workerDocsNote = `

Documentation is kept by the team's docs worker, which runs by itself after every accepted task. Do not edit README or docs/ unless your task says so. AGENTS.md is the team's map of the project (commands, layout, conventions, decisions): read it before you start. When you learn something it should record (a new command, a changed contract, a decision), mail it to a docs worker (dc-N) in one line.`

// docsStandingNote is the docs worker's standing duty under auto-docs.
const docsStandingNote = `

You are the team's standing documentation keeper. The harness gives you a "docs: catch up" task after tasks and agreements are accepted; it lists the changes. For each, read the task (task get) and the code it touched, then keep three readers current: people (README, docs/, examples), agents of later sessions (AGENTS.md: build and test commands, layout, conventions, decisions from the agreements), and this team (AGENTS.md is its map). Record only what is merged or accepted, keep each pass small and factual, and change nothing outside Markdown files and docs/. Teammates may mail you facts to record: fold them into the next pass. Finish each pass with task done and a one-line summary, and mail the manager that line.`

// teamRoles returns roles with the pins of a team session: the team card in the
// manager's, and, under auto-docs, how documentation is kept in every worker's. The
// session's roles are not changed.
func (s *Session) teamRoles(roles swarm.Roles, team []TeamSlot, autoDocs bool) swarm.Roles {
	out := swarm.Roles{}
	for n, r := range roles {
		out[n] = r
	}
	if len(team) == 0 {
		return out
	}
	if m, ok := out["manager"]; ok {
		m.Pin += s.teamCard(roles, team, autoDocs)
		out["manager"] = m
	}
	if !autoDocs {
		return out
	}
	for n, r := range out {
		switch {
		case n == "manager" || n == swarm.MailmanRoleName:
		case n == swarm.DocsRole:
			r.Pin += docsStandingNote
		default:
			r.Pin += workerDocsNote
		}
		out[n] = r
	}
	return out
}
