// The prompt stack bar: G0..G6 sized by tokens and coloured by volatility, bright where the prompt was served from the cache and
// dim where it was paid in full (docs/UX.md, "Prompt stack bar"; the sketches chat.png and story.png).

package widget

import (
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Layer is one layer of the prompt, the unit of the stack bar.
type Layer struct {
	// Name is "G3", or "G3 notes": its first word is the short label drawn under the bar, and when it is G0..G6 it also picks
	// the layer's colour. An empty name is labelled G<position> and coloured by position.
	Name string
	// Tokens is the size of the layer; a layer with no tokens takes no cells.
	Tokens int
	// Breakpoint marks a layer that ends at a provider cache breakpoint.
	Breakpoint bool
	// Hash (a short content hash) and Epoch (the epoch of the layer's last change) are what StackTable shows; the bar ignores them.
	Hash  string
	Epoch int
}

// label is the short name drawn under the bar.
func (l Layer) label(pos int) string {
	f := strings.Fields(showClean(l.Name))
	if len(f) == 0 {
		return "G" + strconv.Itoa(pos)
	}
	return f[0]
}

// colourIndex is which of G0..G6's colours the layer wears: its own number when its label is G<digit>, else its position.
func (l Layer) colourIndex(pos int) int {
	lb := l.label(pos)
	if len(lb) == 2 && (lb[0] == 'G' || lb[0] == 'g') && lb[1] >= '0' && lb[1] <= '9' {
		return int(lb[1] - '0')
	}
	return pos
}

// StackOpts says how a stack bar is drawn. The zero value is not "no options": BreakAt 0 marks layer G0 as broken, Sweep 0
// draws the light at the left edge and Warm 0 is a cold cache. Start from NewStackOpts.
type StackOpts struct {
	// Width is the cells of the whole bar; the layers share them by token count. A Width <= 0 draws nothing.
	Width int
	// CachedTokens is how many leading tokens were served from the cache: the cells they cover are bright, the cell where the
	// prefix ends is a boundary, the rest is dim. More than the prompt is all of it; less than 0 is none.
	CachedTokens int
	// Sweep is the position of the moving light as a fraction of the bar, 0..1 (more is the end of the bar), for the "prefix
	// matching" animation; below 0 (or NaN) there is none. The bright part stops at the light, so advancing Sweep from 0 to the cached fraction draws the
	// match arriving.
	Sweep float64
	// BreakAt is the index in layers of the layer where a cache break happened, or -1: it is drawn in the alarm style and its
	// label gets a warning sign.
	BreakAt int
	// Warm is how warm the cache still is, 1 (just used) to 0 (cold): a cooling bar fades, and a cold one is drawn with shade
	// glyphs instead of solid ones so that it reads without colour.
	Warm float64
}

// NewStackOpts is a width and a cached-token count with no sweep, no break and a warm cache.
func NewStackOpts(width, cachedTokens int) StackOpts {
	return StackOpts{Width: width, CachedTokens: cachedTokens, Sweep: -1, BreakAt: -1, Warm: 1}
}

// The glyphs of the bar: cached, where the cache ends, paid, cooled and the breakpoint tick.
const (
	stackHot      = "█"
	stackEdge     = "▓"
	stackPaid     = "░"
	stackCold     = "▒"
	stackTick     = "▏"
	stackTrackDot = "·"
)

// StackBar draws the layers as one row of o.Width cells and, under it, a row of labels.
//
// The cells are shared out by token count (largest remainder, every layer that has tokens gets at least one cell while there
// are cells to give). The first o.CachedTokens tokens are bright █ in their layer's colour, the cell where they end is ▓ and the
// rest is dim ░, so cached and paid differ by glyph and by brightness, never by colour alone. A breakpoint layer ends with ▏.
// A sweep draws a light ▶▷▹ at its position; a break draws its layer in the alarm style with ⚠ in the label row; a cooling
// cache (o.Warm) fades and a cold one turns █ into ▒.
//
// The labels (G0, G1, ...) are centred under their layers; one that does not fit its layer may spill into free space next to
// it, and when they cannot all be placed the label row is a legend in layer order instead. The bar is exactly o.Width cells;
// the labels are at most that (their right end is trimmed).
//
// With no layers or no tokens the bar is an empty track of faint dots and there are no labels; a Width <= 0 gives two empty
// lines. NaN options are treated as their neutral values.
func StackBar(layers []Layer, o StackOpts, p Palette) (bar, labels cell.Line) {
	w := o.Width
	if w <= 0 {
		return nil, nil
	}
	weights := make([]int, len(layers))
	var total int64
	for i, l := range layers {
		weights[i] = showTok(l.Tokens)
		total += int64(weights[i])
	}
	if total == 0 {
		c := newShowCanvas(w)
		c.fill(0, w, stackTrackDot, p.faintSt())
		return c.line(), nil
	}
	cells := showShares(weights, w, true)

	// which layer owns each cell, and where each layer's run starts
	owner := make([]int, 0, w)
	start := make([]int, len(layers))
	for i, n := range cells {
		start[i] = len(owner)
		for k := 0; k < n; k++ {
			owner = append(owner, i)
		}
	}

	cachedTok := int64(showTok(o.CachedTokens))
	if cachedTok > total {
		cachedTok = total
	}
	lit := showDiv(cachedTok*int64(w), total)
	light := -1
	if o.Sweep >= 0 { // NaN is not >= 0
		light = showClamp(showPermille(o.Sweep)*w/1000, 0, w-1)
		if lit > light {
			lit = light
		}
	}
	warm := showUnit(o.Warm)
	if o.Warm != o.Warm {
		warm = 1
	}
	fadePct := int((1-warm)*60 + 0.5)
	cold := warm < 0.25

	bc := stackCanvas(layers, cells, owner, start, w, lit, o.BreakAt, cold, fadePct, p)
	if light >= 0 {
		bc.put(light, "▶", p.warnSt().With(cell.Bold))
		bc.put(light+1, "▷", p.warnSt())
		bc.put(light+2, "▹", p.warnSt().With(cell.Dim))
	}
	return bc.line(), stackLabels(layers, cells, start, w, o.BreakAt, p)
}

// stackCanvas paints the bar's cells (without the sweep light).
func stackCanvas(layers []Layer, cells, owner, start []int, w, lit, breakAt int, cold bool, fadePct int, p Palette) *showCanvas {
	c := newShowCanvas(w)
	for x := 0; x < w; x++ {
		li := owner[x]
		col := p.layerCol(layers[li].colourIndex(li))
		var glyph string
		var st cell.Style
		switch {
		case x < lit-1 || (x == lit-1 && lit == w):
			glyph, st = stackHot, p.fadeSt(col, fadePct)
		case x == lit-1:
			glyph, st = stackEdge, p.fadeSt(col, fadePct)
		default:
			glyph, st = stackPaid, showFG(col).With(cell.Dim)
		}
		if cold && x < lit {
			glyph = stackCold
		}
		if li == breakAt {
			st = p.badSt()
		}
		c.put(x, glyph, st)
	}
	// breakpoint ticks replace the last cell of their layer, unless that is where the cache ends (the edge outranks the tick)
	for i, l := range layers {
		if !l.Breakpoint || cells[i] < 2 {
			continue
		}
		x := start[i] + cells[i] - 1
		if x == lit-1 && lit < w {
			continue
		}
		st := showFG(p.layerCol(l.colourIndex(i))).With(cell.Bold)
		if i == breakAt {
			st = p.badSt()
		}
		c.put(x, stackTick, st)
	}
	return c
}

// stackLabels places G0.. under their layers, or a legend when they cannot all be placed.
func stackLabels(layers []Layer, cells, start []int, w, breakAt int, p Palette) cell.Line {
	c := newShowCanvas(w)
	prevEnd := -1 // the cell after the last label, plus one of air
	ok := true
	type placed struct {
		x    int
		text string
		st   cell.Style
	}
	var out []placed
	for i, l := range layers {
		if cells[i] == 0 {
			continue
		}
		text := l.label(i)
		st := showFG(p.layerCol(l.colourIndex(i))).With(cell.Bold)
		if i == breakAt {
			text, st = "⚠"+text, p.badSt()
		}
		a, b := start[i], start[i]+cells[i]
		place := func(text string) (int, bool) {
			tw := cell.StringWidth(text)
			if tw > w {
				return 0, false
			}
			x := a
			if tw <= cells[i] {
				x = a + (cells[i]-tw)/2
			}
			x = showClamp(x, 0, w-tw)
			if x < prevEnd+1 {
				x = prevEnd + 1
			}
			if x+tw > w || x >= b || x+tw <= a {
				return 0, false
			}
			return x, true
		}
		x, fits := place(text)
		if !fits && i == breakAt { // the warning matters more than the name
			text = "⚠"
			x, fits = place(text)
		}
		if !fits {
			ok = false
			break
		}
		out = append(out, placed{x, text, st})
		prevEnd = x + cell.StringWidth(text)
	}
	if ok {
		for _, pl := range out {
			c.put(pl.x, pl.text, pl.st)
		}
		return c.trimmed()
	}
	// legend: the labels in layer order, whole ones only; the one of the broken layer has room kept for it, and when there is no
	// room for its name the warning sign alone stands at the end
	c = newShowCanvas(w)
	var alarm string
	for i, l := range layers {
		if i == breakAt && cells[i] > 0 {
			alarm = "⚠" + l.label(i)
		}
	}
	reserve := 0
	if alarm != "" {
		reserve = cell.StringWidth(alarm) + 1
	}
	x := 0
	for i, l := range layers {
		if cells[i] == 0 || i == breakAt {
			continue
		}
		text := l.label(i)
		if x+cell.StringWidth(text) > w-reserve {
			break
		}
		x = c.put(x, text, showFG(p.layerCol(l.colourIndex(i))).With(cell.Bold)) + 1
	}
	if alarm != "" {
		if x+cell.StringWidth(alarm) > w {
			alarm = "⚠"
		}
		c.put(min(x, w-cell.StringWidth(alarm)), alarm, p.badSt())
	}
	return c.trimmed()
}
