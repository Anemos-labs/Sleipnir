package widget

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Pipe tables: parsed here, laid out by the Table widget (table.go), which does the column arithmetic.

type mdTable struct {
	align []Align
	head  []string
	rows  [][]string
}

// mdSplitRow splits a table row at its unescaped pipes; a leading and a trailing pipe are optional, cells are trimmed. An
// escaped pipe stays as two characters: the inline parser turns it into a pipe.
func mdSplitRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	var cells []string
	var cur strings.Builder
	endedOnPipe := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
			endedOnPipe = false
		case c == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			endedOnPipe = true
		default:
			cur.WriteByte(c)
			endedOnPipe = false
		}
	}
	if !endedOnPipe && (cur.Len() > 0 || len(cells) > 0 || len(s) > 0) {
		cells = append(cells, strings.TrimSpace(cur.String()))
	}
	return cells
}

// mdDelimRow parses the row under a table header: cells of dashes with optional colons that set the alignment.
func mdDelimRow(line string) ([]Align, bool) {
	if !strings.Contains(line, "|") || mdIndent(line) >= 4 {
		return nil, false
	}
	cells := mdSplitRow(line)
	if len(cells) == 0 {
		return nil, false
	}
	align := make([]Align, len(cells))
	for i, c := range cells {
		left := strings.HasPrefix(c, ":")
		right := len(c) > 1 && strings.HasSuffix(c, ":")
		core := strings.TrimSuffix(strings.TrimPrefix(c, ":"), ":")
		if core == "" || strings.Trim(core, "-") != "" {
			return nil, false
		}
		switch {
		case left && right:
			align[i] = AlignCenter
		case right:
			align[i] = AlignRight
		}
	}
	return align, true
}

// mdTableStarts reports whether lines[i] is the header of a table: a row with pipes over a delimiter row with as many cells.
func mdTableStarts(lines []string, i int) bool {
	if i+1 >= len(lines) || !strings.Contains(lines[i], "|") {
		return false
	}
	align, ok := mdDelimRow(lines[i+1])
	return ok && len(mdSplitRow(lines[i])) == len(align)
}

// mdTableInterrupts reports whether lines[i], a line after the first of a paragraph, ends the paragraph by being the header of
// a table. The answer must agree with mdParse, which decides what block starts on a line that ends a paragraph: four spaces of
// indentation start code there, and a list marker starts a list even when it is too weak to interrupt a paragraph, so neither
// can be a header row. (Were the two to disagree, a paragraph would end before a line that does not start a table, and then
// grow back over it when the row under it changed: a stream could not tell which paragraphs are final.)
func mdTableInterrupts(lines []string, i, depth int) bool {
	return mdTableStarts(lines, i) && mdIndent(lines[i]) < 4 && !(depth < mdMaxDepth && mdIsMarker(lines[i]))
}

func mdTableAt(lines []string, i int) (*mdTable, int, bool) {
	if !mdTableStarts(lines, i) {
		return nil, 0, false
	}
	align, _ := mdDelimRow(lines[i+1])
	t := &mdTable{align: align, head: mdSplitRow(lines[i])}
	j := i + 2
	for ; j < len(lines); j++ {
		ln := lines[j]
		if mdBlank(ln) || !strings.Contains(ln, "|") {
			break
		}
		if _, _, ok := mdATX(ln); ok {
			break
		}
		if _, ok := mdFenceOpen(ln); ok {
			break
		}
		row := mdSplitRow(ln)
		for len(row) < len(align) {
			row = append(row, "")
		}
		t.rows = append(t.rows, row[:len(align)])
	}
	return t, j, true
}

// table renders a pipe table with the Table widget: cells keep their inline formatting, columns are as wide as their widest
// cell, and when that is too wide the widest columns are cut with an ellipsis. No column is ever dropped: a table is data, and
// a model's table with a column missing would mislead (Table drops columns only when even three cells each do not fit, and
// then the header row shows which are left).
func (r *mdRenderer) table(t *mdTable, w int) []cell.Line {
	n := len(t.align)
	one := func(src string) cell.Line {
		var l cell.Line
		for i, seg := range r.inlineLines(src, r.base) {
			if i > 0 {
				l = append(l, cell.Span{Text: " ", Style: r.base})
			}
			l = append(l, seg...)
		}
		return l
	}
	natural := make([]int, n)
	cols := make([]Column, n)
	for i := 0; i < n; i++ {
		cols[i].Head = one(t.head[i])
		cols[i].Align = t.align[i]
		natural[i] = cols[i].Head.Width()
	}
	rows := make([][]cell.Line, len(t.rows))
	for ri, row := range t.rows {
		rows[ri] = make([]cell.Line, n)
		for i := 0; i < n; i++ {
			rows[ri][i] = one(row[i])
			natural[i] = max(natural[i], rows[ri][i].Width())
		}
	}
	for i := range cols {
		cols[i].Min = min(3, natural[i])
	}
	return Table{Columns: cols, Rows: rows, Grid: true}.Render(w, r.th)
}
