package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// boardView is the task board and the merge queue with the room the cockpit cannot give them: the columns, then every task with
// who holds it, what it waits for and how many attempts it took, beside the queue that verifies the finished work before it is
// merged.
func boardView(s Scene) []cell.Line {
	sn := s.Snap
	st := stylesOf(s.Pal)
	bands := []widget.ViewBand{
		{Draw: func(w []int) [][]cell.Line { return [][]cell.Line{boardHeadline(s, w[0])} }},
		{Titles: []string{"task board"}, Prio: 3, Draw: func(w []int) [][]cell.Line {
			return [][]cell.Line{widget.Kanban(kanban(sn), w[0], s.Pal)}
		}},
		{Titles: []string{"tasks", "merge queue"}, Weights: []int{3, 2}, MinRows: 4,
			Draw: func(w []int) [][]cell.Line { return boardPanels(s, w, 60) },
			Fit:  func(w []int, rows int) [][]cell.Line { return boardPanels(s, w, rows) }},
	}
	if len(sn.Board.Alerts) > 0 {
		bands = append(bands, widget.ViewBand{Titles: []string{"alerts"}, Prio: 2, Draw: func(w []int) [][]cell.Line {
			var out []cell.Line
			for i := len(sn.Board.Alerts) - 1; i >= 0 && len(out) < 3; i-- {
				a := sn.Board.Alerts[i]
				var r row
				r.add(st.warn, "⚠ "+clean(a.Kind)).add(st.dim, " · "+clean(a.Text))
				out = append(out, fit(r.line(), w[0]))
			}
			return [][]cell.Line{out}
		}})
	}
	return frame(s, "board", bands)
}

func boardHeadline(s Scene, w int) []cell.Line {
	st := stylesOf(s.Pal)
	c := s.Snap.Board.Counts
	total := c.Todo + c.Running + c.Verifying + c.Merged + c.Failed
	if total == 0 {
		return []cell.Line{cell.Styled(st.dim, "no task on the board: a single agent works without one, a swarm's manager writes it")}
	}
	var r row
	r.add(st.bold, fmt.Sprint(total)).add(st.dim, " "+plural(total, "task", "tasks")+" · ")
	r.add(st.warn, fmt.Sprint(c.Todo)).add(st.dim, " todo · ")
	r.add(st.good, fmt.Sprint(c.Running)).add(st.dim, " running")
	if c.Blocked > 0 {
		r.add(st.dim, fmt.Sprintf(" (%d blocked)", c.Blocked))
	}
	r.add(st.dim, " · ").add(st.info, fmt.Sprint(c.Verifying)).add(st.dim, " verifying · ")
	r.add(st.good, fmt.Sprint(c.Merged)).add(st.dim, " merged")
	if c.Failed > 0 {
		r.add(st.dim, " · ").add(st.bad, fmt.Sprintf("%d failed", c.Failed))
	}
	mc := s.Snap.Merge.Counts
	if s.Snap.Merge.Seen {
		r.add(st.dim, fmt.Sprintf(" · merge queue: %d merged, %d conflicts, %d sent back", mc.Merged, mc.Conflicts, mc.Bounced))
	}
	return []cell.Line{fit(r.line(), w)}
}

// taskGlyph is the glyph of a task's state, the one the board draws.
func taskGlyph(t state.TaskState) (string, func(styles) cell.Style) {
	switch t {
	case state.TaskRunning:
		return "▣", func(st styles) cell.Style { return st.good }
	case state.TaskVerifying:
		return "◌", func(st styles) cell.Style { return st.info }
	case state.TaskMerged:
		return "✓", func(st styles) cell.Style { return st.good }
	case state.TaskFailed:
		return "✗", func(st styles) cell.Style { return st.bad }
	}
	return "▢", func(st styles) cell.Style { return st.warn }
}

// taskTable lists the tasks, running ones first, then the ones that wait, the ones on their way in and the ones that are in;
// limit of them at most, with a line that says how many more there are.
func taskTable(s Scene, w, limit int) []cell.Line {
	st := stylesOf(s.Pal)
	tasks := append([]state.Task(nil), s.Snap.Board.Tasks...)
	order := map[state.TaskState]int{state.TaskFailed: 0, state.TaskRunning: 1, state.TaskVerifying: 2, state.TaskTodo: 3, state.TaskMerged: 4}
	sort.SliceStable(tasks, func(i, j int) bool { return order[tasks[i].State] < order[tasks[j].State] })
	if len(tasks) == 0 {
		return []cell.Line{cell.Styled(st.dim, "none")}
	}
	more := 0
	if len(tasks) > limit {
		more = len(tasks) - (limit - 1)
		tasks = tasks[:limit-1]
	}
	var out []cell.Line
	for _, t := range tasks {
		g, styleOf := taskGlyph(t.State)
		var r row
		r.add(styleOf(st), g).add(cell.Style{}, " ").add(st.bold, clean(t.ID)).space(max(0, 4-len([]rune(t.ID))) + 1)
		owner := clean(t.Owner)
		if owner == "" {
			owner = "—"
		}
		r.add(cell.Style{}, padTo(owner, 6)).space(1)
		titleW := w - r.w
		tail := ""
		switch {
		case len(t.Deps) > 0 && t.State == state.TaskTodo:
			tail = "after " + strings.Join(t.Deps, ",")
		case t.Attempts > 1:
			tail = fmt.Sprintf("try %d", t.Attempts)
		case t.Result != "" && t.State == state.TaskMerged:
			tail = ""
		}
		title := clean(t.Title)
		if t.State == state.TaskMerged && t.Result != "" && title == "" {
			title = clean(t.Result)
		}
		if tail != "" && titleW > len([]rune(tail))+12 {
			titleW -= len([]rune(tail)) + 2
		} else {
			tail = ""
		}
		r.addLine(padRight(cell.Styled(cell.Style{}, title), titleW))
		if tail != "" {
			r.space(2).add(st.dim, tail)
		}
		out = append(out, fit(r.line(), w))
	}
	if more > 0 {
		out = append(out, cell.Styled(st.dim, fmt.Sprintf("… and %d more", more)))
	}
	return out
}

// padTo pads or fits a string to the requested terminal-cell width.
func padTo(s string, n int) string {
	w := cell.StringWidth(s)
	if w >= n {
		return fitString(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}

// fitString fits plain text to the requested terminal-cell width using the shared line fitter.
func fitString(s string, n int) string { return fit(cell.Text(s), n).Plain() }

// mailView is the mail with the room the cockpit cannot give it: every message the ring still holds with what it cost the reader,
// beside who writes to whom and how the router dealt with it.
func mailView(s Scene) []cell.Line {
	bands := []widget.ViewBand{
		{Draw: func(w []int) [][]cell.Line { return [][]cell.Line{mailHeadline(s, w[0])} }},
		{Titles: []string{"messages, newest last", "who writes to whom"}, Weights: []int{3, 2}, MinRows: 4,
			Draw: func(w []int) [][]cell.Line { return mailPanels(s, w, 60) },
			Fit:  func(w []int, rows int) [][]cell.Line { return mailPanels(s, w, rows) }},
	}
	return frame(s, "mail", bands)
}

// mailPanels are the newest messages (at most rows of them) and who writes to whom.
func mailPanels(s Scene, w []int, rows int) [][]cell.Line {
	sn := s.Snap
	st := stylesOf(s.Pal)
	recent := sn.Mail.Recent
	if len(recent) > rows {
		recent = recent[len(recent)-rows:]
	}
	ms := make([]widget.Mail, 0, len(recent))
	for _, m := range recent {
		born := -1
		if b, ok := s.Mem.born()[m.Seq]; ok && !s.NoAnim {
			born = b
		}
		ms = append(ms, widget.Mail{From: m.From, To: m.To, Subject: m.Summary, Tokens: m.Tokens, Born: born})
	}
	left := widget.MailFlow(ms, s.Frame, w[0], s.Pal)
	if len(left) == 0 {
		left = paragraph(st.dim, "no message yet: agents write to each other when one needs what another knows", w[0])
	}
	right := pairs(s, w[1])
	if len(right) > rows {
		right = right[:rows]
	}
	return [][]cell.Line{left, right}
}

func mailHeadline(s Scene, w int) []cell.Line {
	st := stylesOf(s.Pal)
	c := s.Snap.Mail.Counts
	var r row
	r.add(st.bold, fmt.Sprint(max(c.Routed, c.Sent))).add(st.dim, " sent · ")
	r.add(st.good, fmt.Sprint(c.Delivered)).add(st.dim, " delivered")
	if c.Ignored > 0 {
		r.add(st.dim, " · ").add(st.warn, fmt.Sprintf("%d not delivered", c.Ignored))
	}
	if c.Digests > 0 {
		r.add(st.dim, fmt.Sprintf(" · %d digests in %d batches", c.Digests, c.Batches))
	}
	if m := s.Snap.Mail.Mailman; m.State != "" {
		r.add(st.dim, " · mailman ").add(cell.Style{}, clean(m.State))
	}
	return []cell.Line{fit(r.line(), w)}
}

// pairs counts the messages of the ring by who sent them to whom, the busiest pair first.
func pairs(s Scene, w int) []cell.Line {
	st := stylesOf(s.Pal)
	type key struct{ from, to string }
	count := map[key]int{}
	tokens := map[key]int{}
	for _, m := range s.Snap.Mail.Recent {
		k := key{m.From, m.To}
		count[k]++
		tokens[k] += m.Tokens
	}
	keys := make([]key, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if count[keys[i]] != count[keys[j]] {
			return count[keys[i]] > count[keys[j]]
		}
		if keys[i].from != keys[j].from {
			return keys[i].from < keys[j].from
		}
		return keys[i].to < keys[j].to
	})
	if len(keys) == 0 {
		return []cell.Line{cell.Styled(st.dim, "—")}
	}
	var out []cell.Line
	for i, k := range keys {
		if i == 8 {
			out = append(out, cell.Styled(st.dim, fmt.Sprintf("… and %d more pairs", len(keys)-i)))
			break
		}
		var r row
		r.add(st.bold, clean(k.from)).add(st.dim, " ➜ ").add(st.bold, clean(k.to))
		right := cell.Styled(st.dim, fmt.Sprintf("×%d · %s tok", count[k], widget.Tokens(tokens[k])))
		out = append(out, alignRight(r.line(), right, w))
	}
	return out
}

// boardPanels are the task list and the merge queue, each at most rows lines tall.
func boardPanels(s Scene, w []int, rows int) [][]cell.Line {
	sn := s.Snap
	q := widget.MergeQueue(mergeItems(sn), s.Frame, w[1], s.Pal)
	if !sn.Merge.Seen {
		q = paragraph(stylesOf(s.Pal).dim, "no worktree isolation in this run: work is written to the checkout directly", w[1])
	}
	if len(q) > rows {
		q = q[:rows]
	}
	return [][]cell.Line{taskTable(s, w[0], rows), q}
}
