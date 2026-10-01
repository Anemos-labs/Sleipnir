// The stack in detail, for the ctrl+t panel: one row per layer with its size, share, how much of it came from the cache, its
// short hash and the epoch of its last change.

package widget

import (
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// stackTableCol is one column of the stack table.
type stackTableCol struct {
	key   string
	title string
	w     int
	right bool
	// drop is the order in which columns give way when the width is short: the lowest goes first; 0 never goes.
	drop int
}

// StackTable draws the layers as a table o.Width cells wide: a swatch in the layer's colour, the name, tokens, share of the
// prompt, a small bar of how much of the layer was served from the cache with a word for it (cached, partial, paid), the short
// hash and the epoch of the last change, then a total row. A breakpoint layer is marked ▏, the layer where a cache break
// happened (o.BreakAt) ⚠ and in the alarm style. Only o.Width, o.CachedTokens and o.BreakAt are used.
//
// When the width is short the columns give way in a fixed order (epoch, hash, share, the word, the bar), then the name is cut;
// nothing is ever wider than o.Width. No layers, or a Width <= 0, draws nothing.
func StackTable(layers []Layer, o StackOpts, p Palette) []cell.Line {
	w := o.Width
	if w <= 0 || len(layers) == 0 {
		return nil
	}
	cols := []stackTableCol{
		{key: "mark", title: " ", w: 2},
		{key: "name", title: "layer", w: 6},
		{key: "tokens", title: "tokens", w: 6, right: true},
		{key: "share", title: "share", w: 5, right: true, drop: 3},
		{key: "bar", title: "cache", w: 8, drop: 5},
		{key: "word", title: "", w: 7, drop: 4},
		{key: "hash", title: "hash", w: 8, drop: 2},
		{key: "epoch", title: "epoch", w: 5, right: true, drop: 1},
	}
	for _, l := range layers { // the swatch and a space, then the name, which is cut at 16 cells
		if nw := 2 + showClamp(cell.StringWidth(showClean(l.Name)), 0, 16); nw > cols[1].w {
			cols[1].w = nw
		}
	}
	rowWidth := func(cs []stackTableCol) int {
		n := len(cs) - 1
		for _, c := range cs {
			n += c.w
		}
		return n
	}
	for rowWidth(cols) > w {
		best := -1
		for i, c := range cols {
			if c.drop > 0 && (best < 0 || c.drop < cols[best].drop) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		cols = append(cols[:best:best], cols[best+1:]...)
	}
	for ci := range cols { // still too wide: the name gives way
		if cols[ci].key == "name" {
			if over := rowWidth(cols) - w; over > 0 {
				cols[ci].w = showClamp(cols[ci].w-over, 3, cols[ci].w)
			}
		}
	}

	var total int64
	for _, l := range layers {
		total += int64(showTok(l.Tokens))
	}
	cached := int64(showTok(o.CachedTokens))
	if cached > total {
		cached = total
	}

	row := func(cells map[string]cell.Line) cell.Line {
		var b showRowBuf
		for i, c := range cols {
			if i > 0 {
				b.space(1)
			}
			l := cells[c.key]
			if l.Width() > c.w {
				l = l.Truncate(c.w, "…")
			}
			pad := c.w - l.Width()
			if c.right {
				b.space(pad).addLine(l)
			} else {
				b.addLine(l).space(pad)
			}
		}
		return showFit(b.line(), w)
	}
	txt := cell.Styled

	head := map[string]cell.Line{}
	for _, c := range cols {
		head[c.key] = txt(p.dimSt(), c.title)
	}
	out := []cell.Line{row(head)}

	var before int64
	for i, l := range layers {
		tok := int64(showTok(l.Tokens))
		lc := cached - before
		if lc < 0 {
			lc = 0
		}
		if lc > tok {
			lc = tok
		}
		before += tok
		col := p.layerCol(l.colourIndex(i))
		nameSt, swatch := showFG(col).With(cell.Bold), showFG(col)
		mark := cell.Line{}
		if l.Breakpoint {
			mark = mark.Append(cell.Span{Text: "▏", Style: showFG(col).With(cell.Bold)})
		} else {
			mark = mark.Append(cell.Span{Text: " "})
		}
		if i == o.BreakAt {
			nameSt, swatch = p.badSt(), p.badSt()
			mark = mark.Append(cell.Span{Text: "⚠", Style: p.badSt()})
		}
		name := showClean(l.Name)
		if name == "" {
			name = l.label(i)
		}
		cells := map[string]cell.Line{
			"mark":   mark,
			"name":   stackNameCell(swatch, nameSt, name, cols),
			"tokens": txt(cell.Style{}, showTokens(int(tok))),
			"share":  txt(p.dimSt(), stackShareText(tok, total)),
			"hash":   txt(p.dimSt(), showTrunc(showClean(l.Hash), 8)),
			"epoch":  txt(p.dimSt(), strconv.Itoa(l.Epoch)),
		}
		cells["bar"], cells["word"] = stackCacheCells(col, lc, tok, 8, p)
		out = append(out, row(cells))
	}

	bold := cell.Style{Attr: cell.Bold}
	totalCells := map[string]cell.Line{
		"name":   txt(bold, "total"),
		"tokens": txt(bold, showTokens(int(total))),
		"share":  txt(p.dimSt(), "100%"),
	}
	if total > 0 {
		totalCells["bar"], totalCells["word"] = stackCacheCells(p.Good, cached, total, 8, p)
		totalCells["word"] = txt(bold, showPercent(float64(cached)/float64(total))+" hit")
	}
	out = append(out, row(totalCells))
	return out
}

// stackNameCell is the swatch and the name, cut to the width the name column ended up with.
func stackNameCell(swatch, nameSt cell.Style, name string, cols []stackTableCol) cell.Line {
	nw := 0
	for _, c := range cols {
		if c.key == "name" {
			nw = c.w
		}
	}
	return cell.Join(cell.Styled(swatch, "█ "), cell.Styled(nameSt, name)).Truncate(nw, "…")
}

// stackShareText is a layer's share of the prompt as a whole percentage, "<1%" for one that rounds to nothing.
func stackShareText(tok, total int64) string {
	if total <= 0 || tok <= 0 {
		return "0%"
	}
	if s := showDiv(tok*100, total); s > 0 {
		return strconv.Itoa(s) + "%"
	}
	return "<1%"
}

// stackCacheCells is the small bar of how much of a layer was served from the cache (█ cached, ▓ edge, ░ paid) and the word for it.
func stackCacheCells(col cell.Color, cached, tok int64, w int, p Palette) (bar, word cell.Line) {
	if tok <= 0 {
		return cell.Styled(showFG(col).With(cell.Dim), showRepeat("░", w)), cell.Styled(p.dimSt(), "-")
	}
	hot := showDiv(cached*int64(w), tok)
	var b showRowBuf
	switch {
	case hot >= w:
		b.add(showFG(col), showRepeat("█", w))
		return b.line(), cell.Styled(p.goodSt(), "cached")
	case hot <= 0:
		b.add(showFG(col).With(cell.Dim), showRepeat("░", w))
		return b.line(), cell.Styled(p.dimSt(), "paid")
	}
	b.add(showFG(col), showRepeat("█", hot-1)+"▓").add(showFG(col).With(cell.Dim), showRepeat("░", w-hot))
	return b.line(), cell.Styled(p.warnSt().With(cell.Bold), "partial")
}
