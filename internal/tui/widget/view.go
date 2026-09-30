// The frame of the other full-screen views (cache, mail, board): the box, the title bar, the bands and the row of keys of the
// cockpit, for content that somebody else lays out. The cockpit chooses its own layout (dashboard.go); a view lists its bands,
// says which it would give up first, and gets them back in the same box, so that every screen of the program looks like the
// same program.

package widget

import (
	"github.com/reee344/sleipnir/internal/tui/cell"
)

// ViewBand is a horizontal band of a full-screen view: panels side by side, each titled in the rule above the band.
type ViewBand struct {
	// Titles name the panels in the rule above the band (none for the first band of a view, whose panels title themselves).
	Titles []string
	// Weights share the width among the panels; nil is one panel the whole width.
	Weights []int
	// Min is the narrowest a panel may be, 12 when zero.
	Min int
	// Draw returns the lines of each panel, given how many cells of each the lines may use (the panel less its margins). It is
	// called once, with the widths of the layout that is drawn.
	Draw func(widths []int) [][]cell.Line
	// Prio says which bands go first when the height does not hold them all: the highest goes first. 0 is never given up.
	Prio int
	// Fit, when not nil, lets the band be made shorter instead of being left out: it gets the usable widths again and the most
	// lines a panel may have, and returns the panels (MinRows is the fewest lines worth showing, 1 when zero). A band with a
	// Fit is shrunk when it is its turn to give way, and only left out when even that is not enough.
	Fit     func(widths []int, rows int) [][]cell.Line
	MinRows int
}

// ViewFrame draws a view in the box of the cockpit: the title bar (SLEIPNIR, the task, the stats), the bands with their rules and
// the row of keys, in exactly height lines of width cells. When the bands do not fit the height the ones with the highest Prio
// are dropped until they do; spare rows are blank, above the keys. A screen too small for a box (under 24 by 6) is a plain list of
// what the bands hold, cut to the size.
func ViewFrame(task string, stats []string, bands []ViewBand, hints []Hint, width, height int, p Palette) []cell.Line {
	if width <= 0 || height <= 0 {
		return nil
	}
	if width < 24 || height < 6 || len(bands) == 0 {
		return viewList(task, bands, width, height, p)
	}
	iw := width - 2
	type built struct {
		band   dashBand
		prio   int
		vb     ViewBand
		usable []int
		shrunk bool
	}
	all := make([]built, 0, len(bands)+1)
	for i, vb := range bands {
		weights := vb.Weights
		if len(weights) == 0 {
			weights = []int{1}
		}
		n := len(weights)
		mins := make([]int, n)
		for j := range mins {
			mins[j] = 12
			if vb.Min > 0 {
				mins[j] = vb.Min
			}
		}
		widths := dashSplit(iw, weights, mins)
		usable := make([]int, n)
		for j, w := range widths {
			usable[j] = max(0, w-2)
		}
		var panels [][]cell.Line
		if vb.Draw != nil {
			panels = vb.Draw(usable)
		}
		for len(panels) < n {
			panels = append(panels, nil)
		}
		prio := vb.Prio
		if i == 0 {
			prio = 0
		}
		all = append(all, built{band: dashBand{widths: widths, titles: vb.Titles, lines: panels[:n], rule: i > 0}, prio: prio, vb: vb, usable: usable})
	}
	keys := dashBand{widths: []int{iw}, lines: [][]cell.Line{{dashHints(hints, iw-2, p)}}}
	assemble := func() []dashBand {
		out := make([]dashBand, 0, len(all)+1)
		for _, b := range all {
			out = append(out, b.band)
		}
		return append(out, keys)
	}
	for {
		over := dashMeasure(assemble()) - height
		if over <= 0 {
			break
		}
		worst := -1
		for i, b := range all {
			if b.prio > 0 && (worst < 0 || b.prio >= all[worst].prio) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		b := &all[worst]
		if b.vb.Fit != nil && !b.shrunk {
			floor := max(b.vb.MinRows, 1)
			b.shrunk = true
			if target := max(b.band.tallest()-over, floor); target < b.band.tallest() {
				panels := b.vb.Fit(b.usable, target)
				for len(panels) < len(b.band.widths) {
					panels = append(panels, nil)
				}
				b.band.lines = panels[:len(b.band.widths)]
				continue
			}
		}
		all = append(all[:worst], all[worst+1:]...)
	}
	out := assemble()
	if extra := height - dashMeasure(out); extra > 0 {
		blank := make([]cell.Line, extra, extra+1)
		k := &out[len(out)-1]
		k.lines = [][]cell.Line{append(blank, k.lines[0]...)}
	}
	lines := viewAssemble(task, stats, width, out, p)
	if len(lines) > height { // a band that cannot be given up and does not fit: cut it, the keys stay
		lines = append(lines[:height-2], lines[len(lines)-2:]...)
	}
	return lines
}

// viewAssemble is dashAssemble for a view: the same box with the title bar made from the task and the stats.
func viewAssemble(task string, stats []string, width int, bands []dashBand, p Palette) []cell.Line {
	out := []cell.Line{frameTitleBar(task, stats, width, bands[0].seps(), p)}
	out = append(out, bands[0].render(p)...)
	prev := bands[0].seps()
	for _, b := range bands[1:] {
		if b.rule || len(prev) > 0 {
			out = append(out, dashRule(width, "├", "┤", prev, b.seps(), b.widths, b.titles, p))
		}
		out = append(out, b.render(p)...)
		prev = b.seps()
	}
	return append(out, dashRule(width, "╰", "╯", prev, nil, nil, nil, p))
}

// viewList is a view on a screen too small for a box: the task, then the lines of every band one under the other.
func viewList(task string, bands []ViewBand, width, height int, p Palette) []cell.Line {
	out := make([]cell.Line, 0, height)
	out = append(out, showFit(cell.Join(cell.Styled(p.accentSt().With(cell.Bold), "SLEIPNIR "), cell.Styled(cell.Style{}, showClean(task))), width))
	for _, vb := range bands {
		if vb.Draw == nil {
			continue
		}
		n := max(1, len(vb.Weights))
		widths := make([]int, n)
		for i := range widths {
			widths[i] = max(0, width-2)
		}
		for i, panel := range vb.Draw(widths) {
			if i < len(vb.Titles) && vb.Titles[i] != "" {
				out = append(out, showFit(cell.Styled(cell.Style{Attr: cell.Bold}, showClean(vb.Titles[i])), width))
			}
			for _, l := range panel {
				out = append(out, showFit(l, width))
			}
		}
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}
