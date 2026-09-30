// The cockpit as a list: what a terminal narrower than 60 columns or shorter than 8 rows gets, and what a swarm looks like to a
// screen reader: one line for each thing, most important first, nothing side by side.

package widget

import (
	"strconv"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// dashList is the one-column cockpit: the title, the clock and the money, the states counted, the agents one to a line, then the
// board, the merge queue, the governor and the latest feed lines, as many of them as the height allows (the feed goes first, then
// the board, the queue and the governor, and the agents are shortened last). No line is wider than width.
func dashList(d DashboardData, width, height int, frame int, p Palette) []cell.Line {
	var out []cell.Line
	var title showRowBuf
	title.add(p.accentSt().With(cell.Bold), "SLEIPNIR")
	if t := showClean(d.Title); t != "" {
		title.add(p.dimSt(), " ▸ ").add(cell.Style{}, t)
	}
	out = append(out, showFit(title.line(), width))
	if height == 1 {
		return out
	}

	var stats showRowBuf
	stats.add(cell.Style{}, "◷ "+showElapsed(d.Elapsed)+" · "+dashAgents(len(d.Agents)))
	if d.Budget > 0 {
		stats.add(cell.Style{}, " · "+dashMoney(d.Spend)+"/"+dashMoney(d.Budget))
	} else {
		stats.add(cell.Style{}, " · "+dashMoney(d.Spend))
	}
	stats.add(cell.Style{}, " · ⛁ "+showPercent(d.HitRatio))
	out = append(out, showFit(stats.line(), width))
	if height == 2 {
		return out
	}

	// what is reserved for the lines under the agents
	tail, feed := 0, 0
	switch {
	case height >= 16:
		tail, feed = 3, 3
	case height >= 12:
		tail, feed = 3, 0
	case height >= 9:
		tail = 1
	}
	counts := 0
	if height >= 4 {
		counts = 1
	}
	room := max(1, height-len(out)-counts-tail-feed)

	if counts > 0 {
		var b showRowBuf
		n := heatCounts(dashStatesOf(d.Agents))
		for _, s := range heatOrder {
			if n[s] > 0 {
				if b.w > 0 {
					b.space(1)
				}
				b.add(s.style(p), s.Glyph()+strconv.Itoa(n[s]))
			}
		}
		out = append(out, showFit(b.line(), width))
	}

	shown := len(d.Agents)
	more := 0
	if shown > room {
		shown = max(1, room-1)
		more = len(d.Agents) - shown
		if room == 1 {
			shown, more = 1, 0
		}
	}
	for _, k := range dashPick(d.Agents, shown) {
		a := d.Agents[k]
		state := a.State.valid()
		glyph := state.Glyph()
		if state == StateThinking {
			glyph = showSpin(frame + k)
		}
		var b showRowBuf
		hit := showPercent(a.Hit)
		b.add(p.roleSt(a.RoleColor), showClean(a.ID)).add(cell.Style{}, " ").add(state.style(p), glyph).add(cell.Style{}, " ")
		doing := showTrunc(showClean(a.Doing), max(0, width-b.w-cell.StringWidth(hit)-1))
		b.add(dashMute(state, p), doing)
		if pad := width - b.w - cell.StringWidth(hit); pad >= 1 {
			b.space(pad).add(agentHitStyle(a.Hit, p), hit)
		}
		out = append(out, showFit(b.line(), width))
	}
	if more > 0 {
		out = append(out, showFit(cell.Styled(p.dimSt(), "… and "+strconv.Itoa(more)+" more"), width))
	}

	if tail >= 1 {
		var b showRowBuf
		for i, c := range d.Kanban {
			if i > 0 {
				b.add(p.dimSt(), " · ")
			}
			h, _ := c.Kind.style(p)
			b.add(h, c.title()+" "+strconv.Itoa(c.count()))
		}
		out = append(out, showFit(b.line(), width))
	}
	if tail >= 3 {
		out = append(out, showFit(dashMergeSummary(d.Merge, frame, p), width))
		var b showRowBuf
		g := d.Governor
		b.add(p.dimSt(), "rpm ")
		if g.RPMLimit > 0 {
			b.add(cell.Style{}, strconv.Itoa(g.RPM)+"/"+strconv.Itoa(g.RPMLimit))
		} else {
			b.add(cell.Style{}, strconv.Itoa(g.RPM))
		}
		if d.PrefixTTL > 0 || d.PrefixLeft > 0 {
			b.add(p.dimSt(), " · prefix ").add(p.warnSt(), showClock(d.PrefixLeft))
		}
		out = append(out, showFit(b.line(), width))
	}
	if feed > 0 {
		out = append(out, dashFeed(d.Feed, feed, width, p)...)
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

func dashStatesOf(agents []AgentRow) []AgentState {
	out := make([]AgentState, len(agents))
	for i, a := range agents {
		out[i] = a.State
	}
	return out
}

// dashMute is the style of what an agent is doing: dim when it is idle or done.
func dashMute(s AgentState, p Palette) cell.Style {
	if s == StateIdle || s == StateDone {
		return p.dimSt()
	}
	return cell.Style{}
}

// dashMergeSummary is the merge queue in one line: what is moving and how many are in.
func dashMergeSummary(items []MergeItem, frame int, p Palette) cell.Line {
	var b showRowBuf
	b.add(p.dimSt(), "merge ")
	merged, shown := 0, 0
	for _, it := range items {
		if it.Stage >= MergeDone && !it.Failed {
			merged++
			continue
		}
		if shown > 0 {
			b.add(p.dimSt(), " · ")
		}
		shown++
		st := cell.Style{}
		tail := " " + it.Stage.word()
		switch {
		case it.Failed:
			st, tail = p.badSt(), " "+it.Stage.word()+" ✗"
		case it.Stage != MergeQueued:
			tail += " " + showSpin(frame)
		}
		b.add(st, showClean(it.ID)+tail)
	}
	if shown > 0 {
		b.add(p.dimSt(), " · ")
	}
	b.add(p.goodSt(), strconv.Itoa(merged)+" merged")
	return b.line()
}
