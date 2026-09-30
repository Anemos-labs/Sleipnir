package input

import (
	"strings"
	"unicode"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// EscHint is the line shown under the prompt after one Esc press on a non-empty buffer.
const EscHint = "press Esc again to clear"

// View is what the editor wants on the screen: lines to draw, where the cursor goes, and nothing else. It holds no escape
// sequences and no control characters (the buffer cannot), so the renderer can turn it into bytes without further cleaning.
type View struct {
	// Lines is the input (the prompt prefix on the first row, an indent on the others, soft-wrapped to the width), then the
	// reverse-search line, the completion menu (at most 8 rows) and the Esc hint, each only when it applies.
	Lines []cell.Line
	// CursorRow and CursorCol say where the cursor is, in cells, relative to the first line of View: Lines[CursorRow] is
	// the row, and CursorCol counts cells from its left edge (the prefix included). The cursor is on the cell of the rune it
	// is before, or just past the end of the text.
	CursorRow, CursorCol int
	// InputRows is how many of the leading Lines are the input itself.
	InputRows int
}

// Plain is the view's lines as text, without styles: for tests and logs.
func (v View) Plain() []string {
	out := make([]string, len(v.Lines))
	for i, l := range v.Lines {
		out[i] = l.Plain()
	}
	return out
}

// View lays the editor out for a terminal width cells wide (widths under 4 are treated as 4). It is a pure function of the
// editor's state: it changes nothing, so the caller may call it whenever it draws.
func (e *Editor) View(width int) View {
	if width < 4 {
		width = 4
	}
	pw := cell.StringWidth(e.opt.Prompt)
	textW := max(width-pw, 2)
	var v View
	switch {
	case e.search != nil:
		v = e.searchView(width, pw, textW)
	case len(e.buf) == 0:
		ph := cell.Styled(e.th.Placeholder, e.opt.Placeholder)
		v.Lines = []cell.Line{cell.Join(cell.Styled(e.th.Prompt, e.opt.Prompt), ph).Truncate(width, "…")}
		v.CursorCol = pw
		v.InputRows = 1
	default:
		l := e.layout(textW)
		v.Lines = e.rowLines(l, pw, [2]int{-1, -1})
		v.CursorRow, v.CursorCol = l.cursor.row, pw+l.cursor.col
		v.InputRows = len(v.Lines)
	}
	if e.menu != nil && e.search == nil {
		v.Lines = append(v.Lines, e.menuLines(width)...)
	}
	if e.escArmed && e.search == nil {
		v.Lines = append(v.Lines, cell.Join(cell.Spaces(pw, cell.Style{}), cell.Styled(e.th.Dim, EscHint)).Truncate(width, "…"))
	}
	return v
}

// rowLines turns a layout into styled lines: the prompt on the first row, an indent on the rest. Buffer runes in hl
// (start, end) are drawn with the Selected style: the part of a history match that the query found.
func (e *Editor) rowLines(l layout, pw int, hl [2]int) []cell.Line {
	indent := cell.Span{Text: strings.Repeat(" ", pw)}
	lines := make([]cell.Line, 0, len(l.rows))
	for ri, r := range l.rows {
		ln := cell.Line{indent}
		if ri == 0 {
			ln = cell.Line{{Text: e.opt.Prompt, Style: e.th.Prompt}}
		}
		var run strings.Builder
		var runStyle cell.Style
		flush := func() {
			if run.Len() > 0 {
				ln = append(ln, cell.Span{Text: run.String(), Style: runStyle})
				run.Reset()
			}
		}
		for g := r.g0; g < r.g1; g++ {
			gl := l.glyphs[g]
			if gl.zero {
				continue
			}
			st := e.th.Text
			switch {
			case gl.chip:
				st = e.th.Chip
			case gl.start >= hl[0] && gl.start < hl[1]:
				st = e.th.Selected
			}
			if st != runStyle {
				flush()
				runStyle = st
			}
			run.WriteString(gl.text)
		}
		flush()
		lines = append(lines, ln)
	}
	return lines
}

// searchView shows the history match where the buffer would be and the query on a line of its own, with the cursor after the
// query.
func (e *Editor) searchView(width, pw, textW int) View {
	s := e.search
	var v View
	if s.idx >= 0 {
		rs := runesOf(e.hist.At(s.idx))
		tmp := &Editor{opt: e.opt, th: e.th, buf: rs}
		hl := [2]int{-1, -1}
		if i := indexFold(rs, s.query); i >= 0 && len(s.query) > 0 {
			hl = [2]int{i, i + len(s.query)}
		}
		v.Lines = e.rowLines(tmp.layout(textW), pw, hl)
	} else {
		v.Lines = []cell.Line{cell.Styled(e.th.Prompt, e.opt.Prompt)}
	}
	v.InputRows = len(v.Lines)
	label := "reverse search: "
	ln := cell.Line{{Text: label, Style: e.th.Dim}, {Text: string(s.query), Style: e.th.Text}}
	v.CursorRow, v.CursorCol = len(v.Lines), cell.StringWidth(label)+cell.StringWidth(string(s.query))
	switch {
	case s.failing && s.idx < 0:
		ln = append(ln, cell.Span{Text: "  no match", Style: e.th.Dim})
	case s.failing:
		ln = append(ln, cell.Span{Text: "  no older match", Style: e.th.Dim})
	}
	if v.CursorCol >= width {
		v.CursorCol = width - 1
	}
	v.Lines = append(v.Lines, ln.Truncate(width, "…"))
	return v
}

// indexFold finds needle in hay like History.Find matches: ignoring case unless needle has an upper-case letter.
func indexFold(hay, needle []rune) int {
	if len(needle) == 0 {
		return -1
	}
	fold := true
	for _, r := range needle {
		if unicode.IsUpper(r) {
			fold = false
		}
	}
	eq := func(a, b rune) bool { return a == b || (fold && unicode.ToLower(a) == unicode.ToLower(b)) }
	for i := 0; i+len(needle) <= len(hay); i++ {
		j := 0
		for j < len(needle) && eq(hay[i+j], needle[j]) {
			j++
		}
		if j == len(needle) {
			return i
		}
	}
	return -1
}

// menuLines draws the visible part of the completion menu: at most menuRows rows, the selected one marked and highlighted, a
// dim detail on the right, and an arrow at the edge of the first or last row when there are more above or below.
func (e *Editor) menuLines(width int) []cell.Line {
	m := e.menu
	n := len(m.cands)
	end := min(m.top+menuRows, n)
	dw := 0
	for i := m.top; i < end; i++ {
		dw = max(dw, cell.StringWidth(m.cands[i].Display))
	}
	dw = min(dw, max(8, width/2))
	marker := e.th.Marker
	if marker == "" {
		marker = ">"
	}
	var out []cell.Line
	for i := m.top; i < end; i++ {
		c := m.cands[i]
		st, detail := e.th.Menu, e.th.Dim
		mk := "  "
		if i == m.sel {
			st, detail, mk = e.th.Selected, e.th.Selected, marker+" "
		}
		ln := cell.Line{{Text: mk, Style: st}, {Text: c.Display, Style: st}}.Truncate(width-2, "…")
		ln = ln.Pad(2+dw, st)
		if c.Detail != "" {
			ln = ln.Append(cell.Span{Text: "  ", Style: st}, cell.Span{Text: c.Detail, Style: detail})
		}
		ln = ln.Truncate(width-2, "…").Pad(width-2, st)
		arrow := "  "
		switch {
		case i == m.top && m.top > 0:
			arrow = " ↑"
		case i == end-1 && end < n:
			arrow = " ↓"
		}
		out = append(out, ln.Append(cell.Span{Text: arrow, Style: detail}))
	}
	return out
}
