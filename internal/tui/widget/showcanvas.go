// A canvas is one row of cells that widgets paint into by column: a bar, a row of labels, marks above a sparkline, the
// lines of a fan. It keeps wide characters and combining marks honest (a wide rune takes two cells and is never cut in half)
// and never grows: anything painted past its width is dropped, which is how every widget keeps its promise that no line is
// wider than the width it was given.

package widget

import (
	"strings"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

type showCanvasCell struct {
	s    string // what is drawn in the cell: a rune and its combining marks, or " "
	st   cell.Style
	cont bool // the second half of the wide rune in the cell before
}

type showCanvas struct{ cells []showCanvasCell }

func newShowCanvas(w int) *showCanvas {
	if w < 0 {
		w = 0
	}
	c := &showCanvas{cells: make([]showCanvasCell, w)}
	for i := range c.cells {
		c.cells[i].s = " "
	}
	return c
}

// clear makes cell x blank, repairing a wide rune that it was half of.
func (c *showCanvas) clear(x int) {
	if x < 0 || x >= len(c.cells) {
		return
	}
	if c.cells[x].cont && x > 0 {
		c.cells[x-1] = showCanvasCell{s: " "}
	}
	if x+1 < len(c.cells) && c.cells[x+1].cont {
		c.cells[x+1] = showCanvasCell{s: " "}
	}
	c.cells[x] = showCanvasCell{s: " "}
}

// put paints s from column x in style st, clipping at both edges, and returns the column after it. A wide rune that does
// not fit in the last cell is left out.
func (c *showCanvas) put(x int, s string, st cell.Style) int {
	last := -1
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		t := s[i : i+n]
		i += n
		if r == utf8.RuneError && n == 1 {
			r, t = '�', "�"
		}
		w := cell.RuneWidth(r)
		if w == 0 { // a combining mark joins the cell before it
			if last >= 0 && last < len(c.cells) {
				c.cells[last].s += t
			}
			continue
		}
		if x >= 0 && x+w <= len(c.cells) {
			for k := 0; k < w; k++ {
				c.clear(x + k)
			}
			c.cells[x] = showCanvasCell{s: t, st: st}
			if w == 2 {
				c.cells[x+1] = showCanvasCell{st: st, cont: true}
			}
			last = x
		} else {
			last = -1
		}
		x += w
	}
	return x
}

// fill paints the one-cell glyph g over n cells from column x.
func (c *showCanvas) fill(x, n int, g string, st cell.Style) {
	for k := 0; k < n; k++ {
		c.put(x+k, g, st)
	}
}

// line is the canvas as a line: runs of one style become one span, and all w cells are kept.
func (c *showCanvas) line() cell.Line {
	var out cell.Line
	var b strings.Builder
	var cur cell.Style
	open := false
	flush := func() {
		if open {
			out = append(out, cell.Span{Text: b.String(), Style: cur})
			b.Reset()
		}
	}
	for _, ce := range c.cells {
		if ce.cont {
			continue
		}
		if !open || ce.st != cur {
			flush()
			cur, open = ce.st, true
		}
		b.WriteString(ce.s)
	}
	flush()
	return out
}

// trimmed is the canvas as a line without the blanks at its right end.
func (c *showCanvas) trimmed() cell.Line {
	n := len(c.cells)
	for n > 0 && c.cells[n-1].s == " " && !c.cells[n-1].cont {
		n--
	}
	cut := &showCanvas{cells: c.cells[:n]}
	return cut.line()
}
