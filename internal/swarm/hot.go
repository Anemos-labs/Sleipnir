package swarm

import (
	"fmt"
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
	maxHotAgents      = 30
	maxHotNotes       = 8
)

// hline is one candidate line with a priority; lower priority numbers survive
// budget pressure longer.
type hline struct {
	prio int
	sec  int
	text string
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
		lines = append(lines, hline{prio, sec, fmt.Sprintf(format, args...)})
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

	// 4: teammates, relevant first.
	var idle, running, other int
	owners := map[string]bool{}
	for _, t := range s.Tasks {
		if t.Owner != "" && (mine[t.ID] || related[t.ID]) {
			owners[t.Owner] = true
		}
	}
	shown := 0
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
		prio := 6
		if owners[a.ID] || a.Role == role {
			prio = 4
		}
		if isManager {
			prio = 4
		}
		if shown >= maxHotAgents {
			hidden++
			continue
		}
		shown++
		add(prio, 3, "%s", agentLine(a))
	}

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
	// priority, the oldest lines first), until it fits. Every line is estimated once and
	// dropped in a single pass; a real render then checks the estimate, and only the
	// remainder (section headers, rounding) needs another round, so the cost is
	// O(lines log lines), not a re-render per dropped line.
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	render := func() string {
		return assemble(lines, keep, s.Version, len(s.Agents), running, idle, other, open, done, agent)
	}
	txt := render()
	dropped := hidden
	if total := est.Tokens(txt); total > budget {
		var order []int
		for i := range lines {
			if lines[i].prio > 1 {
				order = append(order, i)
			}
		}
		sort.SliceStable(order, func(a, b int) bool { return lines[order[a]].prio > lines[order[b]].prio })
		pos := 0
		for total > budget && pos < len(order) {
			for total > budget && pos < len(order) { // drop by the estimate ...
				i := order[pos]
				pos++
				keep[i] = false
				dropped++
				total -= est.Tokens(lines[i].text) + 1
			}
			txt = render() // ... then look at the real text: headers and rounding may need a bit more
			total = est.Tokens(txt)
		}
	}
	if dropped > 0 {
		txt = strings.Replace(txt, "</live>", fmt.Sprintf("(+%d more lines omitted)\n</live>", dropped), 1)
	}
	return txt
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func taskLine(t Task) string {
	s := fmt.Sprintf("%s %s", t.ID, t.Status)
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
