package swarm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
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

	// 1: alerts.
	for _, a := range s.Alerts {
		add(1, 1, "! %s", a.Text)
	}

	// 2: tasks. Workers see their own and related ones; the manager sees all open work.
	var open, done int
	for _, t := range s.Tasks {
		if t.Status == StatusDone {
			done++
			continue
		}
		open++
		switch {
		case mine[t.ID]:
			add(2, 2, "%s", taskLine(t))
		case related[t.ID]:
			add(3, 2, "%s", taskLine(t))
		case isManager:
			add(3, 2, "%s", taskLine(t))
		}
	}

	// 4: teammates, relevant first.
	var idle, running, other int
	owners := map[string]bool{}
	for id := range mine {
		if t, ok := s.Task(id); ok {
			owners[t.Owner] = true
		}
	}
	for id := range related {
		if t, ok := s.Task(id); ok && t.Owner != "" {
			owners[t.Owner] = true
		}
	}
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
		add(prio, 3, "%s", agentLine(a))
	}

	// 5: notes proposed for shared context but not yet merged.
	notes := append([]Note(nil), s.Notes...)
	sort.Slice(notes, func(i, j int) bool { return notes[i].ID < notes[j].ID })
	for _, n := range notes {
		if n.Scope == "role" && n.Role != role {
			continue
		}
		add(5, 4, "- %s (%s)", n.Text, n.From)
	}

	// Assemble under budget: drop highest prio numbers first.
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	render := func() string {
		return assemble(lines, keep, s.Version, len(s.Agents), running, idle, other, open, done, agent)
	}
	txt := render()
	order := make([]int, len(lines))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return lines[order[a]].prio > lines[order[b]].prio })
	dropped := 0
	for _, i := range order {
		if est.Tokens(txt) <= budget || lines[i].prio <= 1 {
			break
		}
		keep[i] = false
		dropped++
		txt = render()
	}
	if dropped > 0 {
		txt = strings.Replace(txt, "</live>", fmt.Sprintf("(+%d more lines omitted)\n</live>", dropped), 1)
	}
	return txt
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
