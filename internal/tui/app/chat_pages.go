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

// callOf projects the tool run's identity and input into a core block for presentation.
func callOf(t *toolRun) core.Block {
	return core.Block{ToolID: t.id, ToolName: t.name, Input: t.input}
}

// ---- panels on demand ----

// page opens live panels from session state without queuing a host command.
func (m *chatModel) page(line string) bool {
	f := strings.Fields(line)
	if len(f) != 1 || (f[0] != "/stats" && f[0] != "/agents") {
		return false
	}
	m.syncStream()
	if f[0] == "/stats" {
		m.statsOpen = !m.statsOpen
		m.statsOffset = 0
		if m.statsOpen {
			m.cockpit, m.cockpitBack, m.cockpitTurn = false, false, true
		}
	} else if m.info.Swarm {
		m.toggleCockpit()
	} else {
		m.block(bkNote, []cell.Line{cell.Styled(m.k.st.dim, "  a single agent: /swarm 8 starts a manager and eight workers")})
	}
	return true
}

// statsLines reports provider usage and estimated list-price savings.
func (m *chatModel) statsLines() []cell.Line {
	sn := m.snapshot()
	t, st := sn.Totals, m.k.st
	// Statistics are reading content; reserve muted colors for decorative hints.
	st.dim = cell.Style{}
	if t.Responses == 0 {
		return []cell.Line{cell.Styled(st.dim, "  stats"), cell.Styled(st.dim, "  nothing has been sent yet")}
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
		palette := m.k.Palette
		if !palette.Mono && palette.Dim != (cell.Color{}) {
			palette.Dim = cell.Hex("#9aa5ce")
		}
		table := widget.StackTable(layers, o, palette)
		if left, total, ok := ttlOf(sn, "agent", a.ID); ok {
			label := cell.Text("estimated reuse window: ")
			table = append(table, append(label, m.k.ttlLine(left, total, max(m.cols-4-label.Width(), 1))...))
		}
		out = append(out, cell.Styled(st.dim, "  estimated prompt distribution by layer"))
		out = append(out, indentLines(table, cell.Text("  "))...)
	}
	if !m.k.Unicode {
		for i := range out {
			out[i] = asciiLine(out[i])
		}
	}
	return out
}

// drawStats refreshes from the log without adding snapshots to the transcript.
// Approval dialogs always take precedence over a read-only page.
func (m *chatModel) drawStats() bool {
	if m.question() != nil {
		m.statsOpen = false
	}
	if !m.statsOpen {
		return false
	}
	lines := m.statsLines()
	height := max(1, m.rows-2)
	m.statsOffset = min(m.statsOffset, max(0, len(lines)-height))
	page := append([]cell.Line(nil), lines[m.statsOffset:min(len(lines), m.statsOffset+height)]...)
	for len(page) < height {
		page = append(page, cell.Line{})
	}
	page = append(page, cell.Line{}, m.k.fit(cell.Text("  Esc / Ctrl+T back to chat | Up/Down scroll"), m.cols))
	page = page[:min(len(page), max(1, m.rows))]
	m.scr.SetFull(page)
	m.scr.SetCursor(-1, 0)
	return true
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
