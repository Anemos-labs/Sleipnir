// The agent table of the cockpit: one row per agent with its state, what it is doing, a small bar of its prompt (what it
// inherited, what is its own, what came from the cache), its size, cost, hit ratio and scope (swarm.png, "agents").

package widget

import (
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// showMoney writes dollars the way the UI does: $0.012, $12.34, $123 ($0.0003 for a cost under a twentieth of a cent, so that it does not read as nothing). Negative and NaN are $0.000.
func showMoney(f float64) string {
	if f != f || f < 0 {
		f = 0
	}
	switch {
	case f > 0 && f < 0.0005: // a real cost must not read as nothing
		return "$" + strconv.FormatFloat(f, 'f', 4, 64)
	case f < 1:
		return "$" + strconv.FormatFloat(f, 'f', 3, 64)
	case f < 100:
		return "$" + strconv.FormatFloat(f, 'f', 2, 64)
	case f < 1e9:
		return "$" + strconv.FormatFloat(f, 'f', 0, 64)
	}
	return "$1e9+"
}

// agentHitStyle colours a hit ratio the way the sketch does: green from 93%, amber from 89%, else red and Bold.
func agentHitStyle(hit float64, p Palette) cell.Style {
	switch {
	case hit >= 0.93:
		return p.goodSt()
	case hit >= 0.89:
		return p.warnSt()
	}
	return p.badSt()
}

// agentBar is an agent's prompt as a small bar of w cells: the part it inherited from the shared prefix in the colours of G0 to G2,
// then its own part in its role's colour, bright as far as the cache served it (hit, the leading share of the prompt), a ▓ where
// that ends and dim ░ after it, so a dark tail is what the agent paid in full. A ▏ marks where the inherited part ends. With no
// tokens it is an empty track.
func agentBar(shared, own int, hit float64, roleColor, w int, p Palette) cell.Line {
	if w <= 0 {
		return nil
	}
	c := newShowCanvas(w)
	shared, own = showTok(shared), showTok(own)
	if shared+own == 0 {
		c.fill(0, w, stackTrackDot, p.faintSt())
		return c.line()
	}
	cells := showShares([]int{shared, own}, w, true)
	split := cells[0]
	lit := showDiv(int64(showPermille(hit))*int64(w), 1000)
	for x := 0; x < w; x++ {
		col := p.roleCol(roleColor)
		if x < split {
			col = p.layerCol(showClamp(x*3/max(1, split), 0, 2))
		}
		switch {
		case x < lit-1 || (x == lit-1 && lit == w):
			c.put(x, "█", showFG(col))
		case x == lit-1:
			c.put(x, "▓", showFG(col))
		default:
			c.put(x, "░", showFG(col).With(cell.Dim))
		}
	}
	if split > 0 && split < w-1 && split != lit-1 { // the tick takes the first cell of the agent's own part
		c.put(split, "▏", showFG(p.roleCol(roleColor)).With(cell.Bold))
	}
	return c.line()
}

// agentTableCol is a column of the agent table.
type agentTableCol struct {
	key   string
	title string
	w     int
	right bool
	// drop is the order in which columns give way when the width is short (the lowest first); 0 is never.
	drop int
}

// AgentTable draws the agents as a header and a row each, width cells wide:
//
//	AGENT        STATE    DOING                 PROMPT                TOK   COST    ⛁  SCOPE
//	m0 manager   ⠹ think  plan: split by ep…    ██████▏████▓░░░░░   53.3k  $0.012  96%  —
//	w1 backend   ⚙ tool   go test ./orders/…    ██████▏███████▓░    72.2k  $0.020  97%  orders/*
//
// The state is a glyph and a word (the glyph of a thinking agent is a spinner that turns with frame), so it reads without
// colour; the bar is the agent's prompt (see the sketch: bright is served from the cache, dim was paid in full, a dark tail is a
// lost cache); the hit ratio is coloured green, amber or red and Bold, and the stuck ones stand out by their state. Idle and
// finished agents are dim.
//
// When the width is short the scope goes first, then the cost, the size and the prompt bar, and the doing column gives up its
// room; everything is cut to fit. No agents, or a width <= 0, draws nothing. Text from outside is cleaned of control characters.
func AgentTable(rows []AgentRow, width int, frame int, p Palette) []cell.Line {
	if width <= 0 || len(rows) == 0 {
		return nil
	}
	agentW, scopeW := 6, 5
	for _, r := range rows {
		agentW = max(agentW, cell.StringWidth(showClean(r.ID))+1+cell.StringWidth(showClean(r.Role)))
		scopeW = max(scopeW, cell.StringWidth(showClean(r.Scope)))
	}
	agentW, scopeW = min(agentW, 14), min(scopeW, 12)
	cols := []agentTableCol{
		{key: "agent", title: "AGENT", w: agentW},
		{key: "state", title: "STATE", w: 7},
		{key: "doing", title: "DOING"},
		{key: "bar", title: "PROMPT", w: showClamp(width/5, 12, 28), drop: 4},
		{key: "tok", title: "TOK", w: 6, right: true, drop: 3},
		{key: "cost", title: "COST", w: 7, right: true, drop: 2},
		{key: "hit", title: "⛁", w: 4, right: true},
		{key: "scope", title: "SCOPE", w: scopeW, drop: 1},
	}
	// the room that is left, once the other columns and the spaces between all of them are paid for, is the doing column's
	room := func() int {
		n := width + 1
		for _, c := range cols {
			n -= c.w + 1
		}
		return n
	}
	const doingMin = 10
	for room() < doingMin {
		best := -1
		for i, c := range cols {
			if c.drop > 0 && (best < 0 || c.drop < cols[best].drop) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		if cols[best].key == "bar" && cols[best].w > 12 { // the bar shrinks before it goes
			cols[best].w = 12
			continue
		}
		cols = append(cols[:best:best], cols[best+1:]...)
	}
	doing := showClamp(room(), 0, 40)
	for i := range cols {
		if cols[i].key == "doing" {
			cols[i].w = doing
		}
	}

	build := func(cells map[string]cell.Line) cell.Line {
		var b showRowBuf
		for i, c := range cols {
			if i > 0 {
				b.space(1)
			}
			l := cells[c.key]
			if l.Width() > c.w {
				l = l.Truncate(c.w, "…")
			}
			if c.right {
				b.space(c.w - l.Width()).addLine(l)
			} else {
				b.addLine(l).space(c.w - l.Width())
			}
		}
		return showFit(horseTrimBlank(b.line()), width)
	}
	head := map[string]cell.Line{}
	for _, c := range cols {
		head[c.key] = cell.Styled(p.dimSt(), c.title)
	}
	out := []cell.Line{build(head)}
	for i, r := range rows {
		state := r.State.valid()
		glyph := state.Glyph()
		if state == StateThinking {
			glyph = showSpin(frame + i)
		}
		muted := state == StateIdle || state == StateDone
		doingSt := cell.Style{}
		if muted {
			doingSt = p.dimSt()
		}
		hit := cell.Styled(p.dimSt(), "—")
		if r.Hit == r.Hit {
			hit = cell.Styled(agentHitStyle(r.Hit, p), strconv.Itoa((showPermille(r.Hit)+5)/10)+"%")
		}
		scope := showClean(r.Scope)
		if scope == "" {
			scope = "—"
		}
		cells := map[string]cell.Line{
			"agent": cell.Join(cell.Styled(p.roleSt(r.RoleColor), showClean(r.ID)), cell.Text(" "+showClean(r.Role))),
			"state": cell.Styled(state.style(p), glyph+" "+state.Word()),
			"doing": cell.Styled(doingSt, showClean(r.Doing)),
			"tok":   cell.Text(showTokens(showTok(r.Shared) + showTok(r.Own))),
			"cost":  cell.Text(showMoney(r.Cost)),
			"hit":   hit,
			"scope": cell.Styled(p.dimSt(), scope),
		}
		for _, c := range cols {
			if c.key == "bar" {
				cells["bar"] = agentBar(r.Shared, r.Own, r.Hit, r.RoleColor, c.w, p)
			}
		}
		out = append(out, build(cells))
	}
	return out
}
