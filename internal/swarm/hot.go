package swarm

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// HotConfig bounds the always-fresh tail.
type HotConfig struct {
	// MaxTokens is the budget. The hot block is billed at the uncached rate on
	// every request of every agent, so it stays small: roughly a tenth of what a
	// warm prefix costs to read.
	MaxTokens int
	// Manager gets a larger budget and a task-centric view.
	ManagerMaxTokens int
}

// DefaultHotConfig returns budgets that keep the tail under ~1k tokens for
// workers.
func DefaultHotConfig() HotConfig { return HotConfig{MaxTokens: 900, ManagerMaxTokens: 2200} }

// Caps on what one rendering can list, so the hot view costs the same whatever the
// age or size of the board (what the board holds is bounded too, see BoardLimits).
const (
	maxHotAlerts      = 6
	maxHotTasks       = 40 // the manager's view
	maxHotTasksWorker = 14
	maxHotFailed      = 3
	maxHotAgents      = 30 // teammates with a line of their own in a worker's view; the others are named (maxHotAgentNames)
	maxHotAgentsMgr   = 64 // the same in the manager's view, which decides about every worker and has the larger budget
	maxHotAgentNames  = 64 // teammates named on the "not shown" line, a few tokens each
	maxHotNotes       = 8
)

// hline is one candidate line with a priority; lower priority numbers survive
// budget pressure longer. Within one priority, a line with a higher drop goes
// first (a less relevant teammate), then the older one. agent names the teammate
// of a team line, who is named on the "not shown" line when the line is dropped.
type hline struct {
	prio  int
	sec   int
	text  string
	drop  int
	agent string
}

// RenderHot builds one agent's view of the board. It is a pure function of the
// snapshot and the agent, deterministic (sorted, no clocks), and capped to the
// token budget by dropping the least relevant lines first.
func RenderHot(s *Snapshot, agent, role string, isManager bool, cfg HotConfig, est core.Estimator) string {
	budget := cfg.MaxTokens
	if isManager && cfg.ManagerMaxTokens > 0 {
		budget = cfg.ManagerMaxTokens
	}
	if budget == 0 {
		budget = 900
	}
	me, _ := s.Agent(agent)

	mine := map[string]bool{}
	related := map[string]bool{}
	for _, t := range s.Tasks {
		if t.Owner == agent && t.Status != StatusDone && t.Status != StatusFailed {
			mine[t.ID] = true
		}
	}
	for _, t := range s.Tasks {
		for _, d := range t.Deps {
			if mine[t.ID] {
				related[d] = true
			}
			if mine[d] {
				related[t.ID] = true
			}
		}
	}

	var lines []hline
	add := func(prio, sec int, format string, args ...any) {
		lines = append(lines, hline{prio: prio, sec: sec, text: fmt.Sprintf(format, args...)})
	}

	// 0: who am I.
	head := fmt.Sprintf("you: %s (%s)", agent, role)
	if me.Task != "" {
		head += " · " + me.Task
	}
	if me.CtxTokens > 0 {
		head += fmt.Sprintf(" · ctx %dk", (me.CtxTokens+500)/1000)
	}
	add(0, 0, "%s", head)

	// 1: alerts: the newest few (the board keeps at most a handful, and they expire).
	alerts := s.Alerts
	if len(alerts) > maxHotAlerts {
		alerts = alerts[len(alerts)-maxHotAlerts:]
	}
	for _, a := range alerts {
		add(1, 1, "! %s", a.Text)
	}

	// 2: tasks. Workers see their own and related ones; the manager sees open work,
	// most relevant first and capped, so the view (and its cost) does not grow with the
	// age of the session. Rendering stays in board order.
	var open, done int
	var cand []int
	for i, t := range s.Tasks {
		if t.Status == StatusDone {
			done++
			continue
		}
		open++
		if mine[t.ID] || related[t.ID] || isManager {
			cand = append(cand, i)
		}
	}
	capTasks := maxHotTasks
	if !isManager {
		capTasks = maxHotTasksWorker
	}
	hidden := 0
	if len(cand) > capTasks {
		cand, hidden = pickTasks(s, cand, mine, capTasks)
	}
	for _, i := range cand {
		t := s.Tasks[i]
		if mine[t.ID] {
			add(2, 2, "%s", taskLine(t))
		} else {
			add(3, 2, "%s", taskLine(t))
		}
	}

	// 4: teammates, relevant first. Every teammate is either on a line of its own or named
	// on the "not shown" line, so nobody reads a teammate's absence from the view as its
	// absence from the team. The lines go in roster order; relevance decides who gets one
	// (maxHotAgents, maxHotAgentsMgr) and who is named first when the budget drops lines.
	var idle, running, other int
	rank := teammateRanks(s, agent, role, isManager, mine, related)
	var mates []AgentInfo
	for _, a := range s.Agents {
		if a.ID == agent {
			continue
		}
		switch a.State {
		case "idle", "done":
			idle++
		case "running":
			running++
		default:
			other++
		}
		mates = append(mates, a)
	}
	byRank := append([]AgentInfo(nil), mates...)
	sort.SliceStable(byRank, func(i, j int) bool { return rank[byRank[i].ID] < rank[byRank[j].ID] })
	pos := make(map[string]int, len(byRank)) // place by relevance
	at := make(map[string]int, len(mates))   // place on the roster
	for i, a := range byRank {
		pos[a.ID] = i
	}
	for i, a := range mates {
		at[a.ID] = i
	}
	capAgents := maxHotAgents
	if isManager {
		capAgents = maxHotAgentsMgr
	}
	var unlisted []string // teammates beyond the cap, most relevant first
	for _, a := range byRank[min(len(byRank), capAgents):] {
		unlisted = append(unlisted, a.ID)
	}
	for _, a := range mates {
		if pos[a.ID] >= capAgents {
			continue
		}
		prio := 6
		if rank[a.ID] < rankOthers || isManager {
			prio = 4
		}
		lines = append(lines, hline{prio: prio, sec: 3, text: agentLine(a), drop: pos[a.ID], agent: a.ID})
	}
	namesAt := len(lines) // the "not shown" line ends the team section
	lines = append(lines, hline{prio: 1, sec: 3})

	// 5: notes proposed for shared context but not yet merged: the newest few.
	var notes []Note
	for _, n := range s.Notes {
		if n.Scope == "role" && n.Role != role {
			continue
		}
		notes = append(notes, n)
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].ID < notes[j].ID })
	if len(notes) > maxHotNotes {
		hidden += len(notes) - maxHotNotes
		notes = notes[len(notes)-maxHotNotes:]
	}
	for _, n := range notes {
		add(5, 4, "- %s (%s)", n.Text, n.From)
	}

	// Assemble under budget: drop the highest priority numbers first (within one
	// priority, the least relevant teammate first, otherwise the oldest line), until it
	// fits; a teammate whose line is dropped is named on the "not shown" line. Every line is estimated once and
	// dropped in a single pass; a real render then checks the estimate, and only the
	// remainder (section headers, rounding) needs another round, so the cost is
	// O(lines log lines), not a re-render per dropped line.
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	dropped := hidden
	named := func() { // the "not shown" line: capped teammates and those whose lines were dropped
		ids := append([]string(nil), unlisted...)
		for i, l := range lines {
			if l.agent != "" && !keep[i] {
				ids = append(ids, l.agent)
			}
		}
		lines[namesAt].text, keep[namesAt] = notShownLine(ids, pos, at)
	}
	render := func() string {
		named()
		return assemble(lines, keep, s.Version, len(s.Agents), running, idle, other, open, done, agent)
	}
	txt := render()
	if total := est.Tokens(txt); total > budget {
		var order []int
		for i := range lines {
			if lines[i].prio > 1 {
				order = append(order, i)
			}
		}
		sort.SliceStable(order, func(a, b int) bool {
			la, lb := lines[order[a]], lines[order[b]]
			if la.prio != lb.prio {
				return la.prio > lb.prio
			}
			return la.drop > lb.drop
		})
		next := 0
		for total > budget && next < len(order) {
			for total > budget && next < len(order) { // drop by the estimate ...
				i := order[next]
				next++
				keep[i] = false
				if lines[i].agent == "" {
					dropped++ // a teammate's line is not lost: the teammate is named instead
				}
				total -= est.Tokens(lines[i].text) + 1
			}
			txt = render() // ... then look at the real text: headers, rounding and names may need a bit more
			total = est.Tokens(txt)
		}
	}
	if dropped > 0 {
		txt = strings.Replace(txt, "</live>", fmt.Sprintf("(+%d more lines omitted)\n</live>", dropped), 1)
	}
	return txt
}

// Relevance tiers of a teammate to the reader of a live view (teammateRanks).
const (
	rankNeighbour = iota // works with the reader or waits on it
	rankRole             // the reader's role
	rankOthers           // everyone else
)

// teammateRanks says how much the reader needs to see each teammate (rankNeighbour first). For a worker the neighbours are the
// manager, the members and leader of the pools it is in and the owners of their subtasks, the owners of the tasks its own depend
// on or that depend on its own (related), of its task's parent and children, and of tasks blocked on its own; then come the
// workers of its role. For the manager the neighbours are the workers that wait on it (a task in review or blocked), then those
// at work on a task; idle workers come last. mine and related are the reader's open tasks and the tasks linked to them by a
// dependency. It reads the snapshot only.
func teammateRanks(s *Snapshot, agent, role string, isManager bool, mine, related map[string]bool) map[string]int {
	rank := make(map[string]int, len(s.Agents))
	if isManager {
		for _, a := range s.Agents {
			rank[a.ID] = rankOthers
		}
		for _, t := range s.Tasks {
			switch {
			case t.Owner == "":
			case t.Status == StatusReview || t.Status == StatusBlocked:
				rank[t.Owner] = rankNeighbour
			case t.Status == StatusDoing && rank[t.Owner] != rankNeighbour:
				rank[t.Owner] = rankRole
			}
		}
		return rank
	}
	near := map[string]bool{}
	pools := map[string]bool{} // open pool tasks the reader is in
	for _, t := range s.Tasks {
		if t.Pool != nil && t.Status != StatusDone && t.Status != StatusFailed &&
			(t.Owner == agent || slices.Contains(t.Pool.Members, agent) || slices.Contains(t.Pool.Excused, agent)) {
			pools[t.ID] = true
			near[t.Owner] = true
			for _, m := range t.Pool.Members {
				near[m] = true
			}
		}
	}
	parents := map[string]bool{} // parents of the reader's own tasks
	for _, t := range s.Tasks {
		if mine[t.ID] && t.Parent != "" {
			parents[t.Parent] = true
		}
	}
	for _, t := range s.Tasks {
		switch {
		case t.Owner == "":
		case related[t.ID]: // a dependency of the reader's work, or work that depends on it, whatever its status
			near[t.Owner] = true
		case t.Status == StatusDone || t.Status == StatusFailed:
		case pools[t.Parent] || parents[t.ID] || mine[t.Parent] || mine[t.BlockedOn]:
			near[t.Owner] = true
		}
	}
	for _, a := range s.Agents {
		switch {
		case near[a.ID] || a.Role == "manager":
			rank[a.ID] = rankNeighbour
		case a.Role == role:
			rank[a.ID] = rankRole
		default:
			rank[a.ID] = rankOthers
		}
	}
	return rank
}

// notShownLine names the teammates the view has no line for, at most maxHotAgentNames of them (the most relevant: pos is each
// one's place by relevance), in roster order (at). It returns false when there are none.
func notShownLine(ids []string, pos, at map[string]int) (string, bool) {
	if len(ids) == 0 {
		return "", false
	}
	n := len(ids)
	if n > maxHotAgentNames {
		sort.Slice(ids, func(i, j int) bool { return pos[ids[i]] < pos[ids[j]] })
		ids = ids[:maxHotAgentNames]
	}
	sort.Slice(ids, func(i, j int) bool { return at[ids[i]] < at[ids[j]] })
	more := ""
	if n > len(ids) {
		more = fmt.Sprintf(" and %d more", n-len(ids))
	}
	return fmt.Sprintf("+%d not shown: %s%s", n, strings.Join(ids, ", "), more), true
}

// pickTasks chooses which of the candidate tasks (indexes into s.Tasks, in board order)
// to show when there are more than max: the agent's own first, then work in progress
// (doing, review, blocked), then todo, and only the newest few failed ones. It returns
// them in board order and how many were left out.
func pickTasks(s *Snapshot, cand []int, mine map[string]bool, max int) ([]int, int) {
	rank := func(i int) int {
		t := s.Tasks[i]
		switch {
		case mine[t.ID]:
			return 0
		case t.Status == StatusDoing:
			return 1
		case t.Status == StatusReview:
			return 2
		case t.Status == StatusBlocked:
			return 3
		case t.Status == StatusTodo:
			return 4
		}
		return 5 // failed
	}
	sorted := append([]int(nil), cand...)
	sort.SliceStable(sorted, func(a, b int) bool {
		ra, rb := rank(sorted[a]), rank(sorted[b])
		if ra != rb {
			return ra < rb
		}
		if ra == 5 {
			return sorted[a] > sorted[b] // newest failures first
		}
		return sorted[a] < sorted[b]
	})
	kept := make([]int, 0, max)
	failed := 0
	for _, i := range sorted {
		if len(kept) >= max {
			break
		}
		if rank(i) == 5 {
			if failed >= maxHotFailed {
				continue
			}
			failed++
		}
		kept = append(kept, i)
	}
	sort.Ints(kept)
	return kept, len(cand) - len(kept)
}

func assemble(lines []hline, keep []bool, version uint64, agents, running, idle, other, open, done int, self string) string {
	var sb strings.Builder
	sb.WriteString(`<live board="v` + strconv.FormatUint(version, 10) + `">` + "\n")
	last := -1
	for i, l := range lines {
		if !keep[i] {
			continue
		}
		if l.sec != last {
			switch l.sec {
			case 2:
				sb.WriteString("tasks:\n")
			case 3:
				fmt.Fprintf(&sb, "team (%d others: %d running, %d idle", agents-boolInt(self != "" && agents > 0), running, idle)
				if other > 0 {
					fmt.Fprintf(&sb, ", %d other", other)
				}
				sb.WriteString("):\n")
			case 4:
				sb.WriteString("proposed shared notes (not yet merged):\n")
			}
			last = l.sec
		}
		if l.sec >= 2 {
			sb.WriteString("  ")
		}
		sb.WriteString(l.text)
		sb.WriteByte('\n')
	}
	fmt.Fprintf(&sb, "board: %d open, %d done\n", open, done)
	sb.WriteString("</live>")
	return sb.String()
}

// boolInt encodes true as one and false as zero.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// taskLine formats task identity, status (with its closure reason, or the task it is
// blocked on), owner, and bounded title and progress text, including dependencies for
// pending tasks.
func taskLine(t Task) string {
	s := fmt.Sprintf("%s %s", t.ID, t.Status)
	if !t.Closure.IsZero() {
		s += ":" + t.Closure.String()
	}
	if t.BlockedOn != "" {
		s += " on " + t.BlockedOn
	}
	if t.Owner != "" {
		s += " " + t.Owner
	}
	s += fmt.Sprintf(" %q", oneLine(t.Title, 60))
	if len(t.Deps) > 0 && t.Status == StatusTodo {
		s += " (needs " + strings.Join(t.Deps, ",") + ")"
	}
	if t.Line != "" {
		s += " — " + oneLine(t.Line, 90)
	}
	return s
}

// agentLine formats an agent's identity, state, optional task, and bounded activity description.
func agentLine(a AgentInfo) string {
	s := fmt.Sprintf("%s %s %s", a.ID, a.Role, a.State)
	if a.Task != "" {
		s += " " + a.Task
	}
	if a.Line != "" {
		s += " — " + oneLine(a.Line, 70)
	}
	return s
}
