package app

// A question the session asks, and the panels (agents, board, stats) a key opens on demand.

import (
	"fmt"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// ---- a question ----

// ask puts a question on the screen.
func (m *chatModel) ask(q *question) {
	opts, kind := dialogOptions(q.req)
	d := &dialog{q: q, opts: opts, kind: kind, armAt: m.clock().Add(m.c.AnswerAfter), shownAt: m.clock()}
	if strings.EqualFold(q.req.Tool, "write") {
		d.current = currentText(q.req.Paths)
	}
	m.dialogs = append(m.dialogs, d)
	if m.c.Bell != nil {
		m.c.Bell()
	}
	if t := m.callOf(q.req); t != nil {
		t.asking = true
		d.toolKey = t.key
	}
	// A request that is too tall for the live region is written into the scrollback whole, so that what was approved is on record
	// and can be read; the dialog shows its beginning and its end.
	title, body := m.k.requestBody(q.req, m.callOf(q.req), widget.BoxInnerWidth(m.cols, widget.BoxHardWrap()), m.info.Cwd, d.current)
	if _, cut := m.k.fitBody(body, m.dialogRows()); cut > 0 {
		m.syncStream()
		rec := []cell.Line{cell.Styled(m.k.st.warn, m.k.g.ask+" "+title+": needs your answer")}
		m.block(bkNote, append(rec, indentLines(body, cell.Text("  "))...))
	}
}

// dialogRows is how many rows of the live region a question's body may take: what is left of the terminal when the box's
// edges, its three options, the blank row before them, the hint, the status line and the footer are taken out.
func (m *chatModel) dialogRows() int { return max(m.rows-12, 3) }

// callOf is the call in flight that a question is about: the same agent, the same tool. Nothing says which call of a tool asked,
// so of several the oldest that has not been asked about is taken.
func (m *chatModel) callOf(r perm.Request) *toolRun {
	var best *toolRun
	for _, key := range m.toolSeq {
		t := m.tools[key]
		if t == nil || t.agent != r.Agent || !strings.EqualFold(t.name, r.Tool) {
			continue
		}
		if best == nil || (best.asking && !t.asking) {
			best = t
		}
	}
	return best
}

func callOf(t *toolRun) core.Block {
	return core.Block{ToolID: t.id, ToolName: t.name, Input: t.input}
}

// ---- panels on demand ----

// page answers the two commands that are pages, /stats and /agents (the keys ctrl+t and ctrl+g type them): they are drawn from what the
// program already knows, so they answer at once, in a turn or not, and the host is not asked. It reports whether the line was one. The line
// is echoed as typed when echo says so (a key did not type it).
func (m *chatModel) page(line string, echo bool) bool {
	f := strings.Fields(line)
	if len(f) != 1 || (f[0] != "/stats" && f[0] != "/agents") {
		return false
	}
	m.syncStream()
	if echo {
		m.block(bkPrompt, m.k.promptLines(line, m.cols))
	}
	if f[0] == "/stats" {
		m.printStats()
	} else {
		m.printTeam()
	}
	return true
}

// printStats is the stats page, written into the scrollback: what the session cost, how much of its prompts the provider served from its
// cache and what that saved (at list price, which is not the bill), and the prompt stack layer by layer. The chat page carries none
// of it, so that the page stays clean.
func (m *chatModel) printStats() {
	sn := m.snapshot()
	t, st := sn.Totals, m.k.st
	if t.Responses == 0 {
		m.block(bkNote, []cell.Line{cell.Styled(st.dim, "  nothing has been sent yet")})
		return
	}
	pad := func(label string) string { return fmt.Sprintf("  %-7s", label) }
	var cost, cache row
	cost.add(st.dim, pad("cost")).add(cell.Style{}, widget.USD(t.CostUSD)).
		add(st.dim, " "+m.k.g.dot+" "+widget.Tokens(int(t.Tokens.Prompt()))+" in, "+widget.Tokens(int(t.Tokens.Output))+" out "+m.k.g.dot+" "+count(t.Responses, "request", "requests"))
	cache.add(st.dim, pad("cache")).add(hitStyle(st, t.HitRatio()), widget.Percent(t.HitRatio())).add(st.dim, " of the prompts came from the provider's cache")
	if s := t.Savings; s.Known() && s.SavedUSD > 0 {
		lead := " " + m.k.g.dot + " saved " + m.k.g.approx + " "
		if !s.Complete() {
			lead = " " + m.k.g.dot + " saved at least "
		}
		cache.add(st.dim, lead).add(st.good, widget.USD(s.SavedUSD)).add(st.dim, " at list price")
	}
	out := []cell.Line{cell.Styled(st.dim, "  "+m.k.g.compact+" stats"), m.k.fit(cost.line(), m.cols), m.k.fit(cache.line(), m.cols)}
	if a := stackAgent(&liveView{snap: sn}); a != nil {
		layers := promptLayers(a, m.mem.g0est())
		o := widget.NewStackOpts(max(m.cols-4, 20), a.Stack.Read)
		if !a.Stack.Answered {
			o.CachedTokens = 0
		}
		o.BreakAt = breakLayer(a, layers)
		table := widget.StackTable(layers, o, m.k.Palette)
		if left, total, ok := ttlOf(sn, "agent", a.ID); ok {
			table = append(table, m.k.ttlLine(left, total, max(m.cols-4, 8)))
		}
		out = append(out, cell.Styled(st.dim, "  the prompt, layer by layer"))
		out = append(out, indentLines(table, cell.Text("  "))...)
	}
	if !m.k.Unicode {
		for i := range out {
			out[i] = asciiLine(out[i])
		}
	}
	m.block(bkToolBody, out)
}

// printTeam is the agents page, written into the scrollback: every agent of the team, what it is doing, how big its prompt is, what it
// has cost and how much of it the cache served, and the count of the tasks on the board; the table of the cockpit (`sleipnir watch`),
// which shows the same with more room. A single agent has no team to show.
func (m *chatModel) printTeam() {
	st := m.k.st
	if !m.info.Swarm {
		m.block(bkNote, []cell.Line{cell.Styled(st.dim, "  a single agent: /swarm 8 starts a team of eight")})
		return
	}
	sn := m.snapshot()
	rows := agentRows(sn, CockpitOptions{})
	if len(rows) == 0 {
		m.block(bkNote, []cell.Line{cell.Styled(st.dim, fmt.Sprintf("  the team starts with your first goal: a manager, and the %d workers it can spawn", max(m.info.Agents-1, 1)))})
		return
	}
	c := sn.Board.Counts
	var head row
	started := fmt.Sprintf("%d agents started", len(rows))
	if m.info.Agents > len(rows) { // the manager spawns workers when a job wants them: how many of the team have been
		started = fmt.Sprintf("%d of %d agents started", len(rows), m.info.Agents)
	}
	head.add(st.dim, "  "+m.k.g.compact+" "+started).
		add(st.dim, fmt.Sprintf(" %s tasks: %d todo, %d running, %d verifying, %d merged", m.k.g.dot, c.Todo, c.Running, c.Verifying, c.Merged))
	if c.Failed > 0 {
		head.add(st.bad, fmt.Sprintf(", %d failed", c.Failed))
	}
	out := append([]cell.Line{m.k.fit(head.line(), m.cols)}, indentLines(widget.AgentTable(rows, max(m.cols-4, 20), m.frame, m.k.Palette), cell.Text("  "))...)
	if !m.k.Unicode {
		for i := range out {
			out[i] = asciiLine(out[i])
		}
	}
	m.block(bkToolBody, out)
}

// expandLast is ctrl+o: the whole of the newest output that was collapsed, written into the scrollback.
func (m *chatModel) expandLast() {
	if len(m.expand) == 0 {
		m.setHint("nothing is collapsed")
		return
	}
	e := m.expand[len(m.expand)-1]
	m.expand = m.expand[:len(m.expand)-1]
	out := []cell.Line{cell.Styled(m.k.st.dim, "  "+m.k.g.result+" "+clean(e.title)+" (whole output)")}
	for _, l := range e.lines {
		out = append(out, cell.Join(cell.Text("    "), cell.Text(cutCells(l, max(m.cols-4, 1), m.k.g.ellipsis))))
	}
	m.syncStream()
	m.block(bkToolBody, out)
}
