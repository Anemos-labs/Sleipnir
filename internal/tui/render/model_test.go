package render

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// model is what a terminal that reflows must show once the renderer has done its job, computed from what the caller asked for
// and nothing else: no escape sequence is looked at. It is deliberately simple and does not share code with the renderer, except
// cell.Line.Wrap, which is the documented way the renderer wraps a line that is too wide.
//
//   - Printed lines are hard rows of the history, wrapped when they are written to the width then in force.
//   - The terminal re-wraps every hard row to its current width as the window changes (greedy, by cells); widening never merges
//     hard rows. So the history on screen is each hard row laid out at the current width.
//   - The live region is the wrapped lines of SetLive at the current width, cut to the terminal's height from the top.
type model struct {
	cols, rows int
	hist       []string // hard rows, as written, trailing spaces trimmed
	pend       []cell.Line
	live       []cell.Line
	caretOn    bool
	caretR     int
	caretC     int

	cache struct { // histRows for a width and a number of hard rows; hist only ever grows
		cols, n int
		rows    []string
	}
}

func newModel(cols, rows int) *model { return &model{cols: cols, rows: rows} }

func (m *model) print(ls ...cell.Line) { m.pend = append(m.pend, ls...) }

func (m *model) flush() {
	for _, l := range m.pend {
		m.hist = append(m.hist, modelWrap(l, m.cols)...)
	}
	m.pend = nil
}

// modelWrap is the rows a line becomes: itself when it fits, else cell.Line.Wrap.
func modelWrap(l cell.Line, cols int) []string {
	var out []string
	if l.Width() <= cols {
		return []string{strings.TrimRight(l.Plain(), " ")}
	}
	for _, r := range l.Wrap(cols, 0) {
		out = append(out, strings.TrimRight(r.Plain(), " "))
	}
	return out
}

// terminalWrap lays a hard row out the way a terminal does: rune after rune, a zero-width rune joining the one before it, a wide
// rune that does not fit moving to the next row.
func terminalWrap(s string, cols int) []string {
	if cols < 1 {
		cols = 1
	}
	var rows []string
	var cur strings.Builder
	col := 0
	for _, r := range s {
		w := cell.RuneWidth(r)
		switch {
		case w == 0:
			if col > 0 || cur.Len() > 0 {
				cur.WriteRune(r)
			}
		case w > cols:
		default:
			if col+w > cols {
				rows = append(rows, strings.TrimRight(cur.String(), " "))
				cur.Reset()
				col = 0
			}
			cur.WriteRune(r)
			col += w
		}
	}
	return append(rows, strings.TrimRight(cur.String(), " "))
}

func (m *model) histRows() []string {
	if m.cache.cols == m.cols && m.cache.n == len(m.hist) && m.cache.rows != nil {
		return m.cache.rows
	}
	var out []string
	for _, h := range m.hist {
		out = append(out, terminalWrap(h, m.cols)...)
	}
	m.cache.cols, m.cache.n, m.cache.rows = m.cols, len(m.hist), out
	return out
}

// liveRows is the live region as drawn (wrapped, cut from the top to the terminal's height) and how many rows were cut.
func (m *model) liveRows() (rows []string, cut int) {
	for _, l := range m.live {
		rows = append(rows, modelWrap(l, m.cols)...)
	}
	if len(rows) > m.rows {
		cut = len(rows) - m.rows
		rows = rows[cut:]
	}
	return rows, cut
}

// all is everything the terminal should show, scrollback and screen, without the empty rows at the end.
func (m *model) all() []string {
	live, _ := m.liveRows()
	return trimTail(append(m.histRows(), live...))
}

// cursor is where the cursor should be: the row counted from the first row of the scrollback, the column, and whether it is shown.
func (m *model) cursor() (row, col int, visible bool) {
	live, cut := m.liveRows()
	hist := len(m.histRows())
	switch {
	case len(live) == 0: // no live region: the renderer leaves the cursor alone, on the fresh line below the history
		return hist, 0, true
	case !m.caretOn || m.caretR-cut < 0:
		return 0, 0, false
	}
	return hist + min(m.caretR-cut, len(live)-1), min(max(m.caretC, 0), m.cols-1), true
}

// rowDiff shows where two lists of rows first differ, with a few rows of context from both.
func rowDiff(got, want []string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  first difference at row %d (got %d rows, want %d rows)\n", i, len(got), len(want))
	show := func(name string, rows []string) {
		for j := max(i-3, 0); j < min(i+4, len(rows)); j++ {
			mark := " "
			if j == i {
				mark = ">"
			}
			fmt.Fprintf(&b, "  %s %s %3d %q\n", mark, name, j, rows[j])
		}
	}
	show("got ", got)
	show("want", want)
	return b.String()
}

// check compares the terminal with the model.
func (h *harness) check(m *model) {
	h.tb.Helper()
	if got, want := h.all(), m.all(); !reflect.DeepEqual(got, want) {
		h.tb.Errorf("screen and scrollback differ from the model (%dx%d, %d rows of scrollback):\n%s", m.cols, m.rows, len(h.v.Scrollback()), rowDiff(got, want))
		return
	}
	wantRow, wantCol, wantVis := m.cursor()
	x, y, vis := h.v.Cursor()
	if vis != wantVis {
		h.tb.Errorf("cursor visible %v, want %v", vis, wantVis)
		return
	}
	if gotRow := len(h.v.Scrollback()) + y; vis && (gotRow != wantRow || x != wantCol) {
		h.tb.Errorf("cursor at row %d column %d (counted from the first row of the scrollback), want row %d column %d", gotRow, x, wantRow, wantCol)
	}
}
