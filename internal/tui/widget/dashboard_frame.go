// The frame of the cockpit: one rounded box round the whole screen, cut into bands by horizontal rules, and the bands cut into
// panels by vertical ones, the way swarm.png draws it. A band knows its panels' widths and lines; the rules between bands get the
// right junction characters (┬ ┴ ┼) where the vertical lines of the band above and below meet them.

package widget

import (
	"github.com/reee344/sleipnir/internal/tui/cell"
)

// dashBand is a horizontal band of the cockpit: panels side by side.
type dashBand struct {
	// widths are the panels' widths; they add up, with one cell for each line between two panels, to the inner width of the box.
	widths []int
	// titles are the panels' names, drawn in the rule above the band (the first band has no rule above it: its panels title
	// themselves).
	titles []string
	// lines are each panel's lines, each at most the panel's width less its two cells of margin.
	lines [][]cell.Line
	// rule says whether there is a rule above the band.
	rule bool
}

// seps are the positions, inside the box, of the vertical lines between the panels.
func (b dashBand) seps() []int {
	var xs []int
	x := 0
	for i := 0; i+1 < len(b.widths); i++ {
		x += b.widths[i]
		xs = append(xs, x)
		x++
	}
	return xs
}

// tallest is the number of lines of the tallest panel.
func (b dashBand) tallest() int {
	h := 0
	for _, ls := range b.lines {
		h = max(h, len(ls))
	}
	return h
}

// render is the band's rows, each the width of the box: a line of the box at both ends and between the panels, and in each panel
// the line with a cell of margin on both sides.
func (b dashBand) render(p Palette) []cell.Line {
	edge := cell.Styled(p.faintSt(), "│")
	out := make([]cell.Line, b.tallest())
	for y := range out {
		var row showRowBuf
		row.addLine(edge)
		for i, w := range b.widths {
			if i > 0 {
				row.addLine(edge)
			}
			var l cell.Line
			if y < len(b.lines[i]) {
				l = b.lines[i][y]
			}
			row.space(1).addLine(showPadLine(l, max(0, w-2))).space(min(1, w-1))
		}
		row.addLine(edge)
		out[y] = row.line()
	}
	return out
}

// dashRule is a horizontal line of the box, width cells wide, from the corner or tee left to right. above and below are where
// the vertical lines of the band above and the band below meet it (positions inside the box); titles name the panels of the band
// below, which start at the given widths.
func dashRule(width int, left, right string, above, below []int, widths []int, titles []string, p Palette) cell.Line {
	c := newShowCanvas(width)
	faint := p.faintSt()
	c.put(0, left, faint)
	c.fill(1, width-2, "─", faint)
	c.put(width-1, right, faint)
	at := map[int][2]bool{} // x: [there is a line above, there is one below]
	for _, x := range above {
		v := at[x]
		v[0] = true
		at[x] = v
	}
	for _, x := range below {
		v := at[x]
		v[1] = true
		at[x] = v
	}
	for _, x := range append(append([]int{}, above...), below...) {
		switch v := at[x]; {
		case v[0] && v[1]:
			c.put(x+1, "┼", faint)
		case v[0]:
			c.put(x+1, "┴", faint)
		default:
			c.put(x+1, "┬", faint)
		}
	}
	start := 0
	for i, t := range titles {
		if i < len(widths) {
			if t = showClean(t); t != "" {
				c.put(start+2, showTrunc(" "+t+" ", max(0, widths[i]-3)), cell.Style{Attr: cell.Bold})
			}
			start += widths[i] + 1
		}
	}
	return c.line()
}

// dashSplit shares width among panels by weight, each at least its minimum, with one cell between neighbours taken off first.
// The result adds up to width minus the lines between the panels. A width that cannot give every panel its minimum gives the
// panels at the end none: the caller checks.
func dashSplit(width int, weights, mins []int) []int {
	n := len(weights)
	room := width - (n - 1)
	out := make([]int, n)
	if room <= 0 || n == 0 {
		return out
	}
	w := showShares(weights, room, false)
	for i := range w {
		if w[i] < mins[i] { // take it from the widest panel that can spare it
			need := mins[i] - w[i]
			for need > 0 {
				donor := -1
				for j := range w {
					if j != i && w[j] > mins[j] && (donor < 0 || w[j]-mins[j] > w[donor]-mins[donor]) {
						donor = j
					}
				}
				if donor < 0 {
					break
				}
				w[donor]--
				w[i]++
				need--
			}
		}
	}
	return w
}
