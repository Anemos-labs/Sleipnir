package app

import (
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

// sliceLine is the cells from to (exclusive) of a line, counted in columns. A wide character that the cut would halve is replaced
// by a space, so that what is left is as wide as it should be.
func sliceLine(l cell.Line, from, to int) cell.Line {
	if to <= from {
		return nil
	}
	var out cell.Line
	col := 0
	for _, sp := range l {
		if col >= to {
			break
		}
		var b strings.Builder
		for _, r := range sp.Text {
			w := cell.RuneWidth(r)
			switch {
			case col+w <= from || col >= to:
			case col < from || col+w > to: // half of a wide character
				for i := max(col, from); i < min(col+w, to); i++ {
					b.WriteByte(' ')
				}
			default:
				b.WriteRune(r)
			}
			col += w
		}
		if b.Len() > 0 {
			out = append(out, cell.Span{Text: b.String(), Style: sp.Style})
		}
	}
	return out
}

// Overlay puts box on base, centred, each line of it over the line of the screen under it, and returns the new screen; base keeps
// its number of lines and each line its width. A box larger than the screen is cut.
func Overlay(base, box []cell.Line, cols int) []cell.Line {
	if len(box) == 0 || len(base) == 0 || cols <= 0 {
		return base
	}
	bw := 0
	for _, l := range box {
		bw = max(bw, l.Width())
	}
	bw = min(bw, cols)
	top := max(0, (len(base)-len(box))/2)
	left := max(0, (cols-bw)/2)
	out := append([]cell.Line(nil), base...)
	for i, bl := range box {
		y := top + i
		if y >= len(out) {
			break
		}
		under := out[y].Pad(cols, cell.Style{})
		out[y] = cell.Join(sliceLine(under, 0, left), sliceLine(bl.Pad(bw, cell.Style{}), 0, bw), sliceLine(under, left+bw, cols))
	}
	return out
}

// helpBox is the list of keys, for ? : the keys of a replay are listed only in one.
func helpBox(p widget.Palette, replay bool) []cell.Line {
	st := stylesOf(p)
	type key struct{ keys, what string }
	rows := []key{
		{"o c m b", "cockpit, cache, mail, board  (1-4 and tab too)"},
		{"↑ ↓", "choose the agent the cache view is about"},
		{"f", "follow the agent that was answered last"},
		{"space", "pause and go on"},
	}
	if replay {
		rows = append(rows, key{"← →", "10 seconds back and forth (shift: a minute)"}, key{"+ -", "faster and slower"}, key{"home end", "to the start and to the end"})
	}
	rows = append(rows, key{"a", "animations on and off"}, key{"ctrl-l", "draw the screen again"}, key{"? esc", "close this"}, key{"q ctrl-c", "quit"})
	kw := 0
	for _, r := range rows {
		kw = max(kw, cell.StringWidth(r.keys))
	}
	var body []cell.Line
	body = append(body, nil)
	for _, r := range rows {
		var l row
		l.space(1).add(st.accent, r.keys).space(kw-cell.StringWidth(r.keys)+2).add(cell.Style{}, r.what).space(1)
		body = append(body, l.line())
	}
	body = append(body, nil)
	w := 0
	for _, l := range body {
		w = max(w, l.Width())
	}
	w = max(w, 24)
	out := make([]cell.Line, 0, len(body)+2)
	title := " keys "
	top := row{}
	top.add(st.faint, "╭─").add(st.bold, title).add(st.faint, strings.Repeat("─", max(0, w-cell.StringWidth(title)-1))+"╮")
	out = append(out, top.line())
	for _, l := range body {
		var r row
		r.add(st.faint, "│").addLine(l.Pad(w, cell.Style{})).add(st.faint, "│")
		out = append(out, r.line())
	}
	var bottom row
	bottom.add(st.faint, "╰"+strings.Repeat("─", w)+"╯")
	return append(out, bottom.line())
}
