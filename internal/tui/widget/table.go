package widget

import (
	"math"
	"slices"
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// Align is the horizontal alignment of a column.
type Align uint8

const (
	AlignLeft  Align = iota // the default: text starts at the left edge of the column
	AlignRight              // numbers
	AlignCenter
)

// Column describes one column of a Table.
type Column struct {
	Title string    // the header text (plain; control characters are removed)
	Head  cell.Line // a styled header; it replaces Title when it is not empty
	// Min and Max bound the column's width in cells. Max 0 means unbounded. Min 0 means the width of the content or 6 cells,
	// whichever is less: a column is cut down to a few readable cells and then dropped, not squeezed to nothing.
	Min, Max int
	// Weight is the column's share of the spare width when the content is narrower than the table: a column with Weight 0 keeps
	// the width of its content, columns with a Weight divide what is left in proportion (up to their Max). A table whose
	// columns all have Weight 0 is only as wide as its content.
	Weight int
	Align  Align
	// Priority decides which columns go first when even the Min widths do not fit: the column with the lowest Priority is
	// dropped first, and among equals the rightmost. The last column is never dropped.
	Priority int
}

// Table is rows of cells under a header.
//
// Widths are display widths (wide characters count 2, marks 0). When the content does not fit, the widest columns are narrowed
// first, each cell cut with an ellipsis, and the narrow ones stay intact; a column never gets narrower than its Min while
// another could give more. When even that does not fit, whole columns are dropped, lowest Priority first (see Column), so a
// narrow terminal shows fewer columns, not unreadable ones. Dropped columns are simply absent from the output; the header row
// always says which remain.
type Table struct {
	Columns []Column
	Rows    [][]cell.Line // a row with fewer cells than columns is padded with empty ones, extra cells are ignored
	Zebra   bool          // every second body row gets the theme's Zebra style (nothing in MonoTheme)
	Grid    bool          // draw │ between columns and ┼ in the rule under the header, instead of two spaces
}

// Render draws the table at most width cells wide: a header row (omitted when no column has a title) and a rule under it in
// Border style, then the rows. Cell text keeps its own styles. Nothing is ever wider than width. No columns, or a width below
// 1, give nil. Control characters in titles and cells are removed.
func (t Table) Render(width int, th Theme) []cell.Line { return t.RenderSelected(width, th, -1) }

// RenderSelected is Render with one row selected: row (an index into Rows; -1 or out of range selects none) is drawn on the
// theme's SelectedRow style and marked with ❯ (> in ASCII) in a two-cell gutter that every row then has, so the selection
// is in the text, not only in the colour.
func (t Table) RenderSelected(width int, th Theme, selected int) []cell.Line {
	if width < 1 || len(t.Columns) == 0 {
		return nil
	}
	g := th.glyphs()
	gutter := 0
	if selected >= 0 && selected < len(t.Rows) {
		gutter = 2
		if width < 6 {
			gutter = 0 // no room for a marker: the style alone has to do
		}
	}
	gap := 2
	if t.Grid {
		gap = 3
	}

	heads := make([]cell.Line, len(t.Columns))
	hasHead := false
	for i, c := range t.Columns {
		h := safeLine(c.Head)
		if len(h) == 0 && c.Title != "" {
			h = cell.Styled(th.Strong, strings.TrimSpace(safeOneLine(c.Title)))
		} else {
			h = overlayStyle(h, th.Strong)
		}
		if h.Width() > 0 {
			hasHead = true
		}
		heads[i] = h
	}
	rows := make([][]cell.Line, len(t.Rows))
	natural := make([]int, len(t.Columns))
	for i := range natural {
		natural[i] = heads[i].Width()
	}
	for r, row := range t.Rows {
		cells := make([]cell.Line, len(t.Columns))
		for i := range cells {
			if i < len(row) {
				cells[i] = safeLine(row[i])
				natural[i] = max(natural[i], cells[i].Width())
			}
		}
		rows[r] = cells
	}

	keep, widths := tableLayout(t.Columns, natural, width-gutter, gap)
	total := gutter + gap*(len(keep)-1)
	for _, w := range widths {
		total += w
	}

	render := func(cells []cell.Line, align func(i int) Align) cell.Line {
		var row cell.Line
		for k, i := range keep {
			if k > 0 {
				if t.Grid {
					row = append(row, cell.Span{Text: " "}, cell.Span{Text: g.vbar, Style: th.Border}, cell.Span{Text: " "})
				} else {
					row = append(row, cell.Span{Text: "  "})
				}
			}
			row = append(row, tableFitCell(cells[i], widths[k], align(i), g.ellipsis)...)
		}
		return row
	}
	colAlign := func(i int) Align { return t.Columns[i].Align }

	var out []cell.Line
	prefix := func(mark string, st cell.Style) cell.Line {
		if gutter == 0 {
			return nil
		}
		return cell.Line{{Text: mark, Style: st}}
	}
	if hasHead {
		out = append(out, append(prefix("  ", cell.Style{}), render(heads, colAlign)...))
		var rule cell.Line
		for k := range keep {
			if k > 0 {
				if t.Grid {
					rule = append(rule, cell.Span{Text: g.rule + g.cross + g.rule, Style: th.Border})
				} else {
					rule = append(rule, cell.Span{Text: "  "})
				}
			}
			rule = append(rule, cell.Span{Text: repeatText(g.rule, widths[k]), Style: th.Border})
		}
		out = append(out, append(prefix("  ", cell.Style{}), rule...))
	}
	markStyle := composeStyle(th.Accent, cell.Style{Attr: cell.Bold})
	for r, cells := range rows {
		row := render(cells, colAlign)
		switch {
		case r == selected && gutter > 0:
			row = append(prefix(g.sel+" ", markStyle), row...)
			row = overlayStyle(row.Pad(total, cell.Style{}), th.SelectedRow)
		case r == selected:
			row = overlayStyle(row.Pad(total, cell.Style{}), th.SelectedRow)
		case t.Zebra && r%2 == 1:
			row = append(prefix("  ", cell.Style{}), row...)
			row = overlayStyle(row.Pad(total, cell.Style{}), th.Zebra)
		default:
			row = append(prefix("  ", cell.Style{}), row...)
		}
		out = append(out, row)
	}
	return clipLines(out, width)
}

// tableFitCell cuts or pads a cell to exactly w cells.
func tableFitCell(l cell.Line, w int, a Align, ellipsis string) cell.Line {
	l = l.Truncate(w, ellipsis)
	free := w - l.Width()
	if free <= 0 {
		return l
	}
	switch a {
	case AlignRight:
		return append(cell.Line{{Text: strings.Repeat(" ", free)}}, l...)
	case AlignCenter:
		left := free / 2
		out := cell.Line{}
		if left > 0 {
			out = append(out, cell.Span{Text: strings.Repeat(" ", left)})
		}
		out = append(out, l...)
		return append(out, cell.Span{Text: strings.Repeat(" ", free-left)})
	}
	return append(append(cell.Line(nil), l...), cell.Span{Text: strings.Repeat(" ", free)})
}

// tableLayout decides which columns stay (indices into cols, in order) and how wide each is. natural[i] is the width of the widest
// content of column i, avail the cells available for columns and the gaps between them. It is deterministic and uses integers
// only. The rules are the ones documented on Column and Table.
func tableLayout(cols []Column, natural []int, avail, gap int) (keep, widths []int) {
	n := len(cols)
	minw := make([]int, n)
	maxw := make([]int, n)
	for i, c := range cols {
		hi := math.MaxInt32
		if c.Max > 0 {
			hi = min(c.Max, 1<<20) // absurd bounds are clamped so that the sums below cannot overflow
		}
		lo := min(c.Min, 1<<20)
		if c.Min <= 0 { // not said: the content's width, up to a few readable cells, and never above an explicit Max
			lo = min(natural[i], defaultMinColumn, hi)
		}
		minw[i] = max(1, lo)
		maxw[i] = max(hi, minw[i])
	}
	for i := 0; i < n; i++ {
		keep = append(keep, i)
	}
	sumMin := func() int {
		s := gap * (len(keep) - 1)
		for _, i := range keep {
			s += minw[i]
		}
		return s
	}
	for len(keep) > 1 && sumMin() > avail {
		drop := 0 // position in keep of the column to drop: lowest priority, then rightmost
		for p, i := range keep {
			if cols[i].Priority < cols[keep[drop]].Priority || (cols[i].Priority == cols[keep[drop]].Priority && p > drop) {
				drop = p
			}
		}
		keep = slices.Delete(keep, drop, drop+1)
	}
	k := len(keep)
	budget := max(0, avail-gap*(k-1))
	want := make([]int, k) // what each kept column would like: its content, clamped to Min..Max
	for p, i := range keep {
		want[p] = max(minw[i], min(natural[i], maxw[i]))
	}
	widths = slices.Clone(want)
	sum := 0
	for _, w := range want {
		sum += w
	}
	switch {
	case sum < budget:
		tableExpand(widths, keep, cols, maxw, budget-sum)
	case sum > budget:
		tableShrink(widths, keep, minw, want, budget)
	}
	return keep, widths
}

// defaultMinColumn is the narrowest a column is cut to when it does not say (Column.Min): five characters and an ellipsis.
const defaultMinColumn = 6

// tableWeight is the column's Weight, bounded so that shares cannot overflow.
func tableWeight(c Column) int { return min(c.Weight, 1<<16) }

// tableExpand hands spare cells to the columns that have a Weight, in proportion, up to their Max.
func tableExpand(widths, keep []int, cols []Column, maxw []int, spare int) {
	for spare > 0 {
		total := 0
		for p, i := range keep {
			if cols[i].Weight > 0 && widths[p] < maxw[i] {
				total += tableWeight(cols[i])
			}
		}
		if total == 0 {
			return
		}
		given := 0
		for p, i := range keep {
			if cols[i].Weight > 0 && widths[p] < maxw[i] {
				give := min(spare*tableWeight(cols[i])/total, maxw[i]-widths[p])
				widths[p] += give
				given += give
			}
		}
		if given == 0 { // the shares rounded to nothing: hand out single cells, heaviest columns first
			for p, i := range keep {
				if spare-given > 0 && cols[i].Weight > 0 && widths[p] < maxw[i] {
					widths[p]++
					given++
				}
			}
			if given == 0 {
				return
			}
		}
		spare -= given
	}
}

// tableShrink narrows the widest columns until the widths fit the budget: it finds the largest cap C such that cutting every
// column to C (but not below its Min) fits, then gives back what rounding left to the columns that were cut.
func tableShrink(widths, keep, minw, want []int, budget int) {
	k := len(widths)
	fit := func(c int) int {
		s := 0
		for p := 0; p < k; p++ {
			s += max(minw[keep[p]], min(want[p], c))
		}
		return s
	}
	if fit(1) > budget { // only possible with one column (Min is honoured while more than one column remain): it gets what is left
		for p := range widths {
			widths[p] = max(1, budget/max(1, k))
		}
		return
	}
	lo, hi := 1, slices.Max(want)
	for lo < hi { // largest cap that still fits
		mid := lo + (hi-lo+1)/2
		if fit(mid) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	sum := 0
	for p := 0; p < k; p++ {
		widths[p] = max(minw[keep[p]], min(want[p], lo))
		sum += widths[p]
	}
	for left := budget - sum; left > 0; {
		moved := false
		for p := 0; p < k && left > 0; p++ {
			if widths[p] == lo && want[p] > lo {
				widths[p]++
				left--
				moved = true
			}
		}
		if !moved {
			return
		}
	}
}
