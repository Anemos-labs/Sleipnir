package render

import (
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// wrapRows breaks a line into rows of at most cols cells, so that the number of rows the renderer keeps count of is the number
// the terminal really uses. A line that fits is returned as it is. A line that does not is wrapped with cell.Line.Wrap, at
// spaces when it can and inside a word when it must, and keeps its indentation: the leading spaces of the line (at most half the
// width) are put in front of every row, so wrapped code, lists and quotes stay aligned. A wide rune that cannot fit a one-column
// terminal at all is dropped, because a row wider than the terminal would be wrapped by the terminal itself and throw the count
// off.
func wrapRows(l cell.Line, cols int) []cell.Line {
	if cols < 1 {
		cols = 1
	}
	if l.Width() <= cols {
		return []cell.Line{l}
	}
	lead, leadStyle, body := splitIndent(l)
	ind := min(lead, cols/2)
	rows := body.Wrap(cols-ind, 0)
	for i, r := range rows {
		if ind > 0 {
			r = append(cell.Spaces(ind, leadStyle), r...)
		}
		if r.Width() > cols {
			r = r.Truncate(cols, "")
		}
		rows[i] = r
	}
	return rows
}

// splitIndent separates the leading spaces of a line: how many, the style of the first, and the rest.
func splitIndent(l cell.Line) (n int, st cell.Style, rest cell.Line) {
	for i, sp := range l {
		trimmed := strings.TrimLeft(sp.Text, " ")
		if n == 0 && len(trimmed) < len(sp.Text) {
			st = sp.Style
		}
		n += len(sp.Text) - len(trimmed)
		if trimmed != "" {
			rest = append(cell.Line{{Text: trimmed, Style: sp.Style}}, l[i+1:]...)
			return n, st, rest
		}
	}
	return n, st, nil
}

// reflow answers what a terminal that re-wraps its hard lines does to one row of the live region when it is resized to cols
// columns: how many rows the line takes there, and on which of them the cell at offset off falls (a cell past the end of the
// text counts as a blank one, as a cursor parked beyond the text does). It places rune after rune and moves a wide rune that
// does not fit to the next row, which is what a terminal does, and so what internal/tui/vt does.
func reflow(l cell.Line, cols, off int) (height, offRow int) {
	if cols < 1 {
		cols = 1
	}
	row, col, cells := 0, 0, 0
	offRow = -1
	place := func(w int) {
		if col+w > cols {
			row++
			col = 0
		}
		if offRow < 0 && off < cells+w {
			offRow = row
		}
		col += w
		cells += w
	}
	for _, sp := range l {
		for _, r := range sp.Text {
			if w := cell.RuneWidth(r); w > 0 && w <= cols {
				place(w)
			}
		}
	}
	for offRow < 0 {
		place(1)
	}
	return row + 1, offRow
}
