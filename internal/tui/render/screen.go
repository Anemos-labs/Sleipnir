package render

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
)

// Cell is one cell of a full-screen grid. Text is one character: a rune and the combining marks that follow it. The empty string
// is a blank cell, or the right half of the wide rune in the cell to its left (a wide rune takes two cells: the rune, then an empty
// one). Anything in Text beyond the first character is cut, and control characters make the cell blank: a grid cell is
// sanitized like every other text (sanitize.go).
type Cell struct {
	Text  string
	Style cell.Style
}

// ErrDumb is returned by Screen.Enter on a terminal that cannot be addressed (Caps.Dumb): a full-screen view cannot be shown
// without cursor positioning.
var ErrDumb = errors.New("render: a full-screen view needs a terminal that takes escape sequences")

// ErrNotActive is returned by Screen.Draw and DrawLines when the screen has not been entered (or has been left).
var ErrNotActive = errors.New("render: the full-screen view is not active: call Enter first")

// Screen draws a full-screen view on the alternate screen, where nothing of the scrollback is disturbed and the terminal is
// returned intact by Leave.
//
// Each frame is diffed against the one before, and only the rows that changed are written, from the first changed cell to the
// last, at an absolute position. A frame that changes nothing writes nothing. With Caps.Color == ColorNone the view is drawn
// without colours but keeps bold, reverse and the other attributes, which is how a monochrome view marks what matters; with a
// dumb terminal it cannot be drawn at all (ErrDumb).
//
// A Screen has one mutex and one write path, starts no goroutine, and remembers the first failed Write: after one, every call
// returns that error and writes nothing.
type Screen struct {
	mu      sync.Mutex
	w       io.Writer
	caps    term.Caps
	cm      *colorMap
	syncOut bool

	cols, rows int
	active     bool
	prev       [][]Cell // what the terminal shows; nil means nothing is known and the screen must be cleared
	spare      [][]Cell // a grid that nothing refers to, to build the next frame in
	err        error
	buf        []byte
}

// The view is held as a grid in memory, cell by cell, so its size is bounded: a terminal larger than this (a corrupt size from the
// environment, say) gets a view of this size in its top left corner.
const (
	maxViewCols = 2000
	maxViewRows = 1000
)

func clampView(cols, rows int) (int, int) {
	return min(max(cols, 1), maxViewCols), min(max(rows, 1), maxViewRows)
}

// NewScreen returns a full-screen renderer writing to w, sized from caps (zero sizes become 80x24, and a view is at most 2000
// columns by 1000 rows). It writes nothing until Enter.
func NewScreen(w io.Writer, caps term.Caps) *Screen {
	if caps.Width < 1 {
		caps.Width = term.DefaultWidth
	}
	if caps.Height < 1 {
		caps.Height = term.DefaultHeight
	}
	cols, rows := clampView(caps.Width, caps.Height)
	return &Screen{w: w, caps: caps, cm: newColorMap(caps.Color), syncOut: caps.SyncOutput && !caps.Dumb, cols: cols, rows: rows}
}

// Size is the size the view is laid out for.
func (s *Screen) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

func (s *Screen) write(b []byte) {
	if s.err != nil || len(b) == 0 {
		return
	}
	if _, err := s.w.Write(b); err != nil {
		s.err = err
	}
}

// Enter switches to the alternate screen, hides the cursor and clears the screen. It returns ErrDumb, writing nothing, when the
// terminal cannot be addressed, and the error of a failed Write. Entering twice is harmless.
func (s *Screen) Enter() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.caps.Dumb {
		return ErrDumb
	}
	if s.active {
		return nil
	}
	s.write([]byte(altScreenOn + hideCursor + eraseScreen + "\x1b[H"))
	s.active = true
	s.prev = blankGrid(s.cols, s.rows)
	return s.err
}

// Leave returns to the normal screen, which is as it was before Enter, and shows the cursor again. It is harmless when the
// screen is not active, and it is what a program does on its way out and from a signal handler's goroutine. It returns the error
// of a failed Write.
func (s *Screen) Leave() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return s.err
	}
	s.active = false
	s.prev = nil
	s.write([]byte(resetSGR + showCursor + altScreenOff))
	return s.err
}

// Resize tells the screen the terminal's new size (from term.WatchResize). The next frame clears the screen and draws every
// cell, because what a terminal does with an alternate screen that changes size is not something to rely on.
func (s *Screen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cols, rows = clampView(cols, rows)
	if cols == s.cols && rows == s.rows {
		return
	}
	s.cols, s.rows = cols, rows
	s.prev, s.spare = nil, nil
}

func blankGrid(cols, rows int) [][]Cell {
	g := make([][]Cell, rows)
	for i := range g {
		g[i] = make([]Cell, cols)
	}
	return g
}

// Draw shows the grid: row y, column x is grid[y][x]. Rows and columns beyond the terminal are ignored, and missing ones are
// blank. It writes only what differs from the previous frame. It returns ErrNotActive before Enter and after Leave, ErrDumb on
// a dumb terminal, and the error of a failed Write.
func (s *Screen) Draw(grid [][]Cell) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	g := s.nextGrid()
	for y := range g {
		var src []Cell
		if y < len(grid) {
			src = grid[y]
		}
		normalize(g[y], src)
	}
	return s.frame(g)
}

// DrawLines shows one line per row, from the top: each line is sanitized and cut at the right edge (a wide rune is never
// split), not wrapped, because a full-screen layout is the widgets' business. Rows past the last line are blank. It fails as Draw
// does.
func (s *Screen) DrawLines(lines []cell.Line) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	g := s.nextGrid()
	for y := range g {
		var l cell.Line
		if y < len(lines) {
			l = sanitizeLine(lines[y]).Truncate(s.cols, "")
		}
		fillFromLine(g[y], l)
	}
	return s.frame(g)
}

func (s *Screen) ready() error {
	switch {
	case s.err != nil:
		return s.err
	case s.caps.Dumb:
		return ErrDumb
	case !s.active:
		return ErrNotActive
	}
	return nil
}

// nextGrid returns a grid to build the next frame in: the one from two frames ago, which nothing refers to any more, or a new one
// when the size has changed.
func (s *Screen) nextGrid() [][]Cell {
	g := s.spare
	s.spare = nil
	if len(g) != s.rows || (len(g) > 0 && len(g[0]) != s.cols) {
		return blankGrid(s.cols, s.rows)
	}
	return g
}

// fillFromLine lays a sanitized line that is no wider than the row out as cells: one per character, a wide rune followed by its
// empty right half, a combining mark joined to the character before it, and the rest of the row blank.
func fillFromLine(row []Cell, l cell.Line) {
	for i := range row {
		row[i] = Cell{}
	}
	x, last := 0, -1
	for _, sp := range l {
		for i := 0; i < len(sp.Text); {
			r, n := utf8.DecodeRuneInString(sp.Text[i:])
			w := cell.RuneWidth(r)
			switch {
			case w == 0:
				if last >= 0 {
					row[last].Text += sp.Text[i : i+n]
				}
			case x+w > len(row):
				return
			default:
				row[x] = Cell{Text: sp.Text[i : i+n], Style: sp.Style}
				last = x
				if w == 2 {
					row[x+1] = Cell{Style: sp.Style}
				}
				x += w
			}
			i += n
		}
	}
}

// normalize fills dst, one row of the view, from the cells of src: every cell clean (one character, or blank), every wide rune
// followed by an empty right half in its own style whatever src says there, and no wide rune cut by the edge.
func normalize(dst, src []Cell) {
	for x := range dst {
		if x < len(src) {
			dst[x] = Cell{Text: firstChar(src[x].Text), Style: src[x].Style}
		} else {
			dst[x] = Cell{}
		}
	}
	for x := 0; x < len(dst); x++ {
		if !isWide(dst[x]) {
			continue
		}
		if x+1 >= len(dst) { // no room for the right half
			dst[x].Text = ""
			continue
		}
		dst[x+1] = Cell{Style: dst[x].Style}
		x++
	}
}

// firstChar returns the first character of text after sanitizing it: one rune and the zero-width runes that follow. A control,
// an escape sequence or nothing printable leaves the empty string.
func firstChar(t string) string {
	if t == "" {
		return ""
	}
	if len(t) == 1 && t[0] >= 0x20 && t[0] < 0x7f {
		return t // printable ASCII, nearly every cell of a view
	}
	t, _ = cleanText(t, 0)
	var sb strings.Builder
	for i, first := 0, true; i < len(t); {
		r, n := utf8.DecodeRuneInString(t[i:])
		if cell.RuneWidth(r) > 0 {
			if !first {
				break
			}
			first = false
		}
		sb.WriteString(t[i : i+n])
		i += n
	}
	return sb.String()
}

func charWidth(t string) int {
	r, _ := utf8.DecodeRuneInString(t)
	return cell.RuneWidth(r)
}

// isWide reports whether the cell holds a wide rune. Every wide rune takes at least three bytes, which rules out nearly every
// cell without decoding it.
func isWide(c Cell) bool { return len(c.Text) >= 3 && charWidth(c.Text) == 2 }

// frame writes what differs between the terminal's screen and g, and remembers g.
func (s *Screen) frame(g [][]Cell) error {
	var b []byte
	repaint := s.prev == nil
	if repaint {
		s.prev = blankGrid(s.cols, s.rows)
	}
	for y, row := range g {
		first, last := diffRange(s.prev[y], row)
		if first < 0 {
			continue
		}
		b = append(b, 0x1b, '[')
		b = strconv.AppendInt(b, int64(y+1), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(first+1), 10)
		b = append(b, 'H')
		var cur wireStyle
		for x := first; x <= last; x++ {
			c := row[x]
			if c.Text == "" && x > 0 && isWide(row[x-1]) {
				continue // the right half of a wide rune is drawn with it
			}
			b = append(b, s.cm.wireSGR(cur, c.Style)...)
			cur = s.cm.wire(c.Style)
			if c.Text == "" {
				b = append(b, ' ')
			} else {
				b = append(b, c.Text...)
			}
		}
		if cur != (wireStyle{}) {
			b = append(b, resetSGR...)
		}
	}
	if len(b) == 0 && !repaint {
		s.spare = g // nothing changed: the terminal still shows what prev says
		return s.err
	}
	var out []byte
	if s.syncOut {
		out = append(out, syncBegin...)
	}
	if repaint {
		out = append(out, eraseScreen...)
	}
	out = append(out, b...)
	if s.syncOut {
		out = append(out, syncEnd...)
	}
	s.spare, s.prev = s.prev, g
	s.write(out)
	return s.err
}

// wireSGR is the sequence that takes the terminal from the rendition cur to the style to.
func (m *colorMap) wireSGR(cur wireStyle, to cell.Style) string { return sgr(cur, m.wire(to)) }

// diffRange finds the first and last cells of a row that differ from what is shown, widened so that a wide rune is always written
// whole: a changed right half pulls in its left half, a changed or replaced left half its right half. It returns -1, -1 when
// nothing differs.
func diffRange(prev, row []Cell) (first, last int) {
	first, last = -1, -1
	for x := range row {
		if prev[x] != row[x] {
			if first < 0 {
				first = x
			}
			last = x
		}
	}
	if first < 0 {
		return -1, -1
	}
	if first > 0 && (isWide(row[first-1]) || isWide(prev[first-1])) {
		first--
	}
	if last+1 < len(row) && (isWide(row[last]) || isWide(prev[last])) {
		last++
	}
	return first, last
}
