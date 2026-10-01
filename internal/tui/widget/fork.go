// The cache fork: a worker appears already filled with the prefix it inherits, and only its task card is paid in full
// (docs/UX.md, "Fork"; the storyboard story.png, panel 3).

package widget

import (
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

const forkLabelW = 7 // "shared " and "new    "

// Fork draws a worker being spawned, as progress runs from 0 to 1:
//
//	shared ██████████████████████████████████████████
//	41.2k          G0              G1         G2
//	            ╎              ╎            ╎
//	       ↓ inherited at the cache READ price
//
//	new    ██████████████████████████████████████████▒
//	       inherits 41.2k · pays 0.8k (the task card)
//
// The top bar is the shared prefix (shared, bright, in its layers' colours). At first only the worker's row label is there; then
// its bar fills from the left in the same colours, which is the inheritance, read at the cache READ price; then the task card,
// the worker's own tokens, appears as ▒ in the paid style, and the caption says what was inherited and what was paid. Both bars
// are drawn to one scale, so the task card is as wide as it is big next to the prefix (at least a cell). The line count depends
// on the width only.
//
// Layers with no tokens are left out, ownTokens below 0 is 0, NaN progress is 0 and progress is clamped to 0..1. Without shared
// tokens the top bar is an empty track and the worker's bar is all task card. A width that leaves no room for the bars draws the
// caption alone, finished whatever the progress; a width <= 0 draws nothing.
func Fork(shared []Layer, ownTokens int, progress float64, width int, p Palette) []cell.Line {
	if width <= 0 {
		return nil
	}
	weights := make([]int, len(shared))
	var prefix int64
	for i, l := range shared {
		weights[i] = showTok(l.Tokens)
		prefix += int64(weights[i])
	}
	own := showTok(ownTokens)
	pm := showPermille(progress)

	barW := width - forkLabelW
	if barW < 8 {
		return []cell.Line{forkCaption(prefix, own, 1000, width, p)}
	}

	// both bars to one scale: the prefix takes its share of the worker's bar, the task card the rest (a cell at least)
	ownCells := 0
	switch {
	case own > 0 && prefix == 0:
		ownCells = barW
	case own > 0:
		ownCells = showClamp(showDiv(int64(own)*int64(barW), int64(own)+prefix), 1, barW-1)
	}
	sharedCells := barW - ownCells

	layers := make([]Layer, len(shared))
	copy(layers, shared)
	for i := range layers {
		layers[i].Breakpoint = false
	}
	var bar, labels cell.Line
	if sharedCells > 0 {
		bar, labels = StackBar(layers, NewStackOpts(sharedCells, int(prefix)), p)
	}

	lead := func(text string, st cell.Style) cell.Line {
		return cell.Styled(st, showPadR(showTrunc(text, forkLabelW), forkLabelW))
	}
	var out []cell.Line

	// the prefix, its size and its layers
	out = append(out, cell.Join(lead("shared", p.dimSt()), bar))
	out = append(out, cell.Join(lead(showTokens(int(prefix)), cell.Style{Attr: cell.Bold}), labels))

	// the connectors and what they mean appear with the fill
	inheriting := pm >= 100 && sharedCells > 0
	conn := newShowCanvas(width)
	arrow := newShowCanvas(width)
	if inheriting {
		shares := showShares(weights, sharedCells, true)
		x := forkLabelW
		for i, n := range shares {
			if n > 0 {
				conn.put(x+n/2, "╎", showFG(p.layerCol(layers[i].colourIndex(i))))
			}
			x += n
		}
		for _, text := range []string{"↓ inherited at the cache READ price", "↓ inherited at READ price", "↓ inherited"} {
			if forkLabelW+cell.StringWidth(text) <= width {
				arrow.put(forkLabelW, text, p.goodSt().With(cell.Bold))
				break
			}
		}
	}
	out = append(out, conn.trimmed(), arrow.trimmed(), nil)

	// the worker's bar: fills from the left in the shared colours, then the task card
	var row showRowBuf
	row.addLine(lead("new", p.infoSt().With(cell.Bold)))
	if pm >= 100 {
		const fillEnd, cardEnd = 700, 900
		shown := sharedCells
		if pm < fillEnd {
			shown = showClamp(sharedCells*(pm-100)/(fillEnd-100), 0, sharedCells)
		}
		switch {
		case shown >= sharedCells:
			row.addLine(bar)
		case shown > 0: // still filling: the last cell is the edge of the fill
			row.addLine(bar.Truncate(shown-1, "")).add(forkLastStyle(bar.Truncate(shown, "")), "▓")
		}
		if pm > fillEnd && ownCells > 0 {
			n := ownCells
			if pm < cardEnd {
				n = showClamp(showDiv(int64(ownCells)*int64(pm-fillEnd), cardEnd-fillEnd), 1, ownCells)
			}
			row.add(p.warnSt(), showRepeat("▒", n))
		}
	}
	out = append(out, showFit(row.line(), width))
	out = append(out, forkCaption(prefix, own, pm, width, p))
	return out
}

// forkLastStyle is the style of the last span of a line.
func forkLastStyle(l cell.Line) cell.Style {
	if len(l) == 0 {
		return cell.Style{}
	}
	return l[len(l)-1].Style
}

// forkCaption is the line under the worker's bar: what it inherits and what it pays. It grows with the progress: the inherited
// part once the bar has filled, the paid part once the task card is there. It sits under the bar, and when it does not fit it
// gives up the explanation, then the indent, then the paid part, before it is cut.
func forkCaption(prefix int64, own, pm, width int, p Palette) cell.Line {
	build := func(indent int, explain, paid bool) cell.Line {
		var b showRowBuf
		b.space(indent)
		if pm >= 700 {
			b.add(p.goodSt(), "inherits "+showTokens(int(prefix)))
		}
		if pm >= 900 && paid {
			b.add(p.dimSt(), " · ").add(p.warnSt(), "pays "+showTokens(own))
			if explain {
				b.add(p.dimSt(), " (the task card)")
			}
		}
		return b.line()
	}
	for _, try := range []struct {
		indent        int
		explain, paid bool
	}{{forkLabelW, true, true}, {forkLabelW, false, true}, {0, false, true}, {0, false, false}} {
		if l := build(try.indent, try.explain, try.paid); l.Width() <= width {
			return l
		}
	}
	return showFit(build(0, false, false), width)
}
