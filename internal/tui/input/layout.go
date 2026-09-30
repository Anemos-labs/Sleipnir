package input

import (
	"strconv"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// Layout puts the buffer on rows of a given width so that the view can draw it and say exactly where the cursor is.
//
// Line breaking is cell.Line.Wrap's (words kept whole, long words broken, wide runes never split). Wrap is lossy in one way a
// cursor cannot afford: it drops spaces at a break and trailing spaces, so the text that comes back does not say where every
// rune went. Layout therefore uses Wrap as the authority on which glyph lands on which row, and then aligns its answer with
// the glyphs it asked about, one by one. A space that Wrap dropped is given the place it would have been drawn at (right after
// the last glyph of the row, or the start of the next row when there is no room), so a cursor on it, or after the end of the
// text, always has a cell: when the row is full that is the start of a row of its own, which the layout then has. The text
// never reflows because the cursor moved: rows are made from the text alone, and the cursor is placed on them afterwards.
// To keep the spaces that matter visible, the leading spaces of a line and the spaces inside a chip label go to Wrap as
// no-break spaces, which it treats as letters, and are drawn as spaces.

const nbsp = "\U000000a0"

// dglyph is one glyph, the unit Wrap works in: a rune and the zero-width runes after it.
type dglyph struct {
	text, ow   string // what is drawn, and what Wrap is asked to lay out (they differ for a pinned space)
	w          int
	start, end int  // the runes it stands for: indexes in the line, and in the buffer once the layout has put the line in place
	space      bool // a plain space that Wrap may drop
	fold       bool // zero-width runes that follow attach to it
	chip       bool
	zero       bool // a zero-width rune with nothing to attach to: draws nothing
	cont       bool // not the first glyph of its buffer rune (the rest of a tab or of a chip label)
}

type cellPos struct{ row, col int }

// lineGlyphs expands buffer[ls:le] into glyphs: tabs become spaces up to the next tab stop, a chip becomes its label.
func (e *Editor) lineGlyphs(ls, le int) []dglyph {
	var gs []dglyph
	col := 0
	leading := true // still in the run of spaces that starts the line
	for i := ls; i < le; i++ {
		r := e.buf[i]
		o := i - ls // glyphs know their place in the line, so a line laid out once can be reused wherever it sits
		switch {
		case r == '\t':
			k := tabWidth - col%tabWidth
			for j := 0; j < k; j++ {
				g := dglyph{text: " ", ow: " ", w: 1, start: o, end: o + 1, space: true, cont: j > 0}
				if leading {
					g.ow, g.space = nbsp, false
				}
				gs = append(gs, g)
			}
			col += k
		case isChip(r):
			leading = false
			for j, lr := range e.chipLabel(r) {
				g := dglyph{text: string(lr), ow: string(lr), w: cell.RuneWidth(lr), start: o, end: o + 1, chip: true, cont: j > 0}
				if lr == ' ' {
					g.ow = nbsp
				}
				col += g.w
				gs = append(gs, g)
			}
		case isMark(r):
			if n := len(gs); n > 0 && gs[n-1].fold {
				g := &gs[n-1]
				g.text, g.ow, g.end, g.space = g.text+string(r), g.ow+string(r), o+1, false
			} else {
				gs = append(gs, dglyph{start: o, end: o + 1, zero: true})
			}
		default:
			g := dglyph{text: string(r), ow: string(r), w: cell.RuneWidth(r), start: o, end: o + 1, fold: true}
			if r == ' ' {
				if leading {
					g.ow = nbsp
				} else {
					g.space = true
				}
			} else {
				leading = false
			}
			col += g.w
			gs = append(gs, g)
		}
	}
	return gs
}

// splitGlyphs cuts a wrapped row into glyphs the way cell does: a zero-width rune joins the one before it.
func splitGlyphs(s string) []string {
	var out []string
	for _, r := range s {
		if cell.RuneWidth(r) == 0 {
			if len(out) > 0 {
				out[len(out)-1] += string(r)
			}
			continue
		}
		out = append(out, string(r))
	}
	return out
}

// lineLayout is one buffer line on rows: which glyphs are on which row, where each glyph is (a glyph Wrap dropped has the
// place it would have had), and where a cursor after the last glyph would be.
type lineLayout struct {
	rows [][2]int  // per row, the glyphs [g0,g1) drawn on it
	pos  []cellPos // per glyph: row within the line, column within the row's text; the row may be len(rows), one past the end
	end  cellPos   // the cell after the whole line
}

// wrapLine lays glyphs out on rows w cells wide, with Wrap's breaks; if Wrap's answer cannot be explained by the glyphs (a
// bug here, or a change in cell.Wrap, which the tests look for) it lays them out by hand instead, so the cursor stays right.
func wrapLine(gs []dglyph, w int) lineLayout {
	if ll, ok := alignLine(gs, w); ok {
		return ll
	}
	return naiveWrap(gs, w)
}

func alignLine(gs []dglyph, w int) (lineLayout, bool) {
	var b []byte
	for _, g := range gs {
		if !g.zero {
			b = append(b, g.ow...)
		}
	}
	texts := cell.Text(string(b)).Wrap(w, 0)
	ll := lineLayout{pos: make([]cellPos, len(gs))}
	gi, lastRow, lastEnd, dropped := 0, 0, 0, 0
	where := func() cellPos { // where a glyph of width 1 would go after the last one drawn
		if col := lastEnd + dropped; col < w {
			return cellPos{lastRow, col}
		}
		return cellPos{lastRow + 1, 0}
	}
	drop := func(j int) { // a glyph Wrap did not draw: put it where it would have been
		ll.pos[j] = where()
		if !gs[j].zero {
			dropped += gs[j].w
		}
	}
	for r, t := range texts {
		first, cols, n := -1, 0, 0
		for k, s := range splitGlyphs(t.Plain()) {
			for gi < len(gs) && (gs[gi].zero || (k == 0 && gs[gi].space && gs[gi].ow != s)) {
				drop(gi)
				gi++
			}
			if gi >= len(gs) || gs[gi].ow != s {
				return lineLayout{}, false
			}
			if first < 0 {
				first = gi
			}
			ll.pos[gi] = cellPos{r, cols}
			cols += gs[gi].w
			lastRow, lastEnd, dropped = r, cols, 0
			gi++
			n++
		}
		if n > 1 && cols > w {
			return lineLayout{}, false
		}
		if first < 0 {
			first = gi
		}
		ll.rows = append(ll.rows, [2]int{first, gi})
	}
	for ; gi < len(gs); gi++ {
		if !gs[gi].zero && !gs[gi].space {
			return lineLayout{}, false
		}
		drop(gi)
	}
	ll.end = where()
	return ll, true
}

// naiveWrap breaks between glyphs at the row width, with no word logic.
func naiveWrap(gs []dglyph, w int) lineLayout {
	ll := lineLayout{pos: make([]cellPos, len(gs))}
	row, col, first := 0, 0, 0
	for i, g := range gs {
		if g.zero {
			ll.pos[i] = cellPos{row, col}
			continue
		}
		if col > 0 && col+g.w > w {
			ll.rows = append(ll.rows, [2]int{first, i})
			row, col, first = row+1, 0, i
		}
		ll.pos[i] = cellPos{row, col}
		col += g.w
	}
	ll.rows = append(ll.rows, [2]int{first, len(gs)})
	ll.end = cellPos{row, col}
	if col >= w {
		ll.end = cellPos{row + 1, 0}
	}
	return ll
}

// layout is the whole buffer on rows.
type layout struct {
	glyphs []dglyph // every line's glyphs, with start and end as buffer indexes
	rows   []lrow
	pos    []cellPos // per glyph: global row, column within the row's text
	at     []int     // per buffer index 0..len: the glyph that starts there, or -1
	cursor cellPos   // where the cursor is: global row, column within the row's text
}

type lrow struct {
	g0, g1 int  // glyphs g0..g1 are drawn on this row
	line   int  // the buffer line it belongs to
	ls, le int  // that line's first rune and the newline that ends it (or the end of the buffer)
	last   bool // the last row of its line
}

// lineEntry is one buffer line laid out at one width. It depends on nothing but the line's text, the width and the chip labels
// (which do not change while a prompt is being written), so it is kept from one layout to the next: typing in a long prompt
// lays out the one line that changed, not all of them. Only the lines of the latest layout are kept, so the cache is never
// larger than the buffer, however long the user types.
type lineEntry struct {
	gs []dglyph
	ll lineLayout
}

// cachedLine returns the layout of buffer[ls:le] at width w, from the cache of the previous layout if the same line was in it,
// and records it in next, the cache of this one.
func (e *Editor) cachedLine(ls, le, w int, next map[string]*lineEntry) *lineEntry {
	key := strconv.Itoa(w) + ":" + string(e.buf[ls:le])
	c, ok := e.lcache[key]
	if !ok {
		gs := e.lineGlyphs(ls, le)
		c = &lineEntry{gs: gs, ll: wrapLine(gs, w)}
	}
	next[key] = c
	return c
}

// layout lays the buffer out on rows whose text area is w cells wide (w at least 2) and finds the cursor. When the cursor is
// at the end of a full row, the layout has one more, empty, row for it to stand on.
func (e *Editor) layout(w int) layout {
	if w < 2 {
		w = 2
	}
	l := layout{at: make([]int, len(e.buf)+1)}
	for i := range l.at {
		l.at[i] = -1
	}
	next := make(map[string]*lineEntry)
	defer func() { e.lcache = next }()
	for ls, line := 0, 0; ; line++ {
		le := lineEnd(e.buf, ls)
		c := e.cachedLine(ls, le, w, next)
		base, rowBase := len(l.glyphs), len(l.rows)
		for j, g := range c.gs {
			g.start, g.end = g.start+ls, g.end+ls
			if !g.cont && l.at[g.start] < 0 {
				l.at[g.start] = base + j
			}
			l.glyphs = append(l.glyphs, g)
		}
		rows := c.ll.rows
		if e.cur >= ls && e.cur <= le {
			cp := c.ll.end
			if e.cur < le {
				for j, g := range c.gs {
					if g.start+ls == e.cur && !g.cont {
						cp = c.ll.pos[j]
						break
					}
				}
			}
			for cp.row >= len(rows) { // the cursor stands on a row of its own
				rows = append(rows[:len(rows):len(rows)], [2]int{len(c.gs), len(c.gs)})
			}
			l.cursor = cellPos{cp.row + rowBase, cp.col}
		}
		for _, p := range c.ll.pos {
			l.pos = append(l.pos, cellPos{p.row + rowBase, p.col})
		}
		for i, r := range rows {
			l.rows = append(l.rows, lrow{g0: base + r[0], g1: base + r[1], line: line, ls: ls, le: le, last: i == len(rows)-1})
		}
		if le >= len(e.buf) {
			break
		}
		ls = le + 1
	}
	return l
}
