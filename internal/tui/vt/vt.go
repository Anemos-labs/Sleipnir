// Package vt is a minimal VT100/xterm emulator: enough of a terminal to see what a renderer put on the screen. It backs the
// renderer's tests (a golden screen is a plain text file) and the headless recorder.
//
// It keeps a screen of styled cells and a scrollback, and understands printable UTF-8 (wide runes take two cells, combining
// marks attach to the cell before), autowrap with the delayed-wrap state of DECAWM, the cursor and erase sequences, SGR,
// the alternate screen, cursor visibility, and records (without acting on them) bracketed paste and synchronized output.
// Operating system commands are consumed, and the ones a renderer must never emit (OSC 52 clipboard, OSC 8 hyperlinks) are
// recorded in Term.Rejected. A sequence it does not know is ignored, never an error: nothing that is written can make it
// panic or leave the cursor outside the screen.
//
// Resize models a terminal that reflows (kitty, WezTerm, iTerm2, VTE, Windows Terminal): lines that were soft-wrapped are
// re-wrapped to the new width, hard line breaks stay. SetReflow(false) models one that does not (xterm).
//
// A Term is not safe for concurrent use and must not be copied.
package vt

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

const (
	maxDim            = 4096 // the largest screen New and Resize make; a larger request is clamped
	defaultScrollback = 5000 // rows kept by default; older rows are dropped
)

// vcell is one screen cell. A wide rune is a wide cell followed by a cont cell (which has no text of its own).
type vcell struct {
	text  string // the rune and the combining marks that follow it; "" is an empty cell, drawn as a space
	style cell.Style
	wide  bool // the left half of a wide rune
	cont  bool // the right half of a wide rune
}

// vrow is a screen row. wrapped says the text continues on the next row (autowrap moved it there, so a reflow may join the
// two); pad says the row was wrapped early because a wide rune did not fit, so its last cell is padding and not content.
type vrow struct {
	cells   []vcell
	wrapped bool
	pad     bool
}

type screen struct{ rows []vrow }

// cursorState is a saved cursor (DECSC, CSI s, and the one ?1049 saves).
type cursorState struct {
	x, y    int
	pending bool
	style   cell.Style
	ok      bool
}

// Term is an emulated terminal.
type Term struct {
	// Rejected lists the operating system commands a renderer must never emit that were seen, as their text without the
	// introducer and terminator ("52;c;..." for the clipboard, "8;;https://..." for a hyperlink). Tests assert it is empty.
	Rejected []string

	cols, rows int
	main, altS screen
	alt        bool
	sb         []vrow // scrollback of the primary screen, oldest first
	sbMax      int
	reflow     bool

	x, y     int
	pending  bool // delayed wrap: the last column is filled and the next printable rune wraps
	sty      cell.Style
	autoWrap bool
	curVis   bool
	saved    cursorState // DECSC / CSI s
	altSave  cursorState // the primary screen's cursor while the alternate screen is up

	paste                           bool
	syncDepth, syncBegins, syncEnds int

	reply []byte
	p     parser
}

// New returns a terminal of cols x rows cells (each clamped to 1..4096), empty, with the cursor visible at the top left.
func New(cols, rows int) *Term {
	cols, rows = clampSize(cols, rows)
	t := &Term{cols: cols, rows: rows, sbMax: defaultScrollback, reflow: true}
	t.init()
	return t
}

func (t *Term) init() {
	t.main = screen{rows: blankRows(t.cols, t.rows)}
	t.altS = screen{}
	t.alt = false
	t.sb = nil
	t.x, t.y, t.pending = 0, 0, false
	t.sty = cell.Style{}
	t.autoWrap, t.curVis = true, true
	t.saved, t.altSave = cursorState{}, cursorState{}
	t.paste = false
	t.syncDepth = 0
	t.p = parser{}
}

func clampSize(cols, rows int) (int, int) {
	return min(max(cols, 1), maxDim), min(max(rows, 1), maxDim)
}

func blankRows(cols, rows int) []vrow {
	out := make([]vrow, rows)
	for i := range out {
		out[i] = vrow{cells: make([]vcell, cols)}
	}
	return out
}

// Reset is the terminal's full reset (ESC c): the screen, the scrollback and every mode are back to their start values. The
// size, the scrollback limit, the reflow setting, Rejected and unread replies are kept.
func (t *Term) Reset() { t.init() }

// Size is the screen's size in cells.
func (t *Term) Size() (cols, rows int) { return t.cols, t.rows }

// SetReflow chooses what Resize does to soft-wrapped lines: true (the default) re-wraps them like most modern terminals,
// false cuts and pads every row like xterm.
func (t *Term) SetReflow(on bool) { t.reflow = on }

// SetScrollbackLimit sets how many rows of scrollback are kept (at least 0); older rows are dropped.
func (t *Term) SetScrollbackLimit(n int) {
	t.sbMax = max(n, 0)
	t.trimScrollback()
}

// Reply returns, and clears, what the terminal has written back to the program: the answers to cursor position reports
// (CSI 6n) and device status reports (CSI 5n).
func (t *Term) Reply() []byte {
	r := t.reply
	t.reply = nil
	return r
}

// Idle reports whether the parser is between sequences: a frame that ends inside an escape sequence, an operating system
// command or half a UTF-8 rune would swallow the start of the next one.
func (t *Term) Idle() bool { return t.p.state == sGround && len(t.p.utf) == 0 }

// AltScreen reports whether the alternate screen is showing.
func (t *Term) AltScreen() bool { return t.alt }

// BracketedPaste reports whether mode 2004 is set.
func (t *Term) BracketedPaste() bool { return t.paste }

// SyncDepth is how many synchronized-output frames are open: each CSI ? 2026 h adds one and each CSI ? 2026 l takes one
// away (never below zero). A renderer that brackets its frames leaves it at 0 after every Write.
func (t *Term) SyncDepth() int { return t.syncDepth }

// SyncBegins and SyncEnds count the CSI ? 2026 h and CSI ? 2026 l seen so far, so a test can assert one pair per frame.
func (t *Term) SyncBegins() int { return t.syncBegins }
func (t *Term) SyncEnds() int   { return t.syncEnds }

// Cursor is the cursor's column and row on the visible screen (0-based, always inside it) and whether it is shown.
func (t *Term) Cursor() (x, y int, visible bool) { return t.x, t.y, t.curVis }

// Cell returns the text and style of the cell at column x, row y of the visible screen. An empty cell is a space; the right
// half of a wide rune is the empty string. Outside the screen it returns "" and the zero style.
func (t *Term) Cell(x, y int) (text string, style cell.Style) {
	g := t.g()
	if y < 0 || y >= len(g.rows) || x < 0 || x >= t.cols {
		return "", cell.Style{}
	}
	c := g.rows[y].cells[x]
	if c.cont {
		return "", c.style
	}
	if c.text == "" {
		return " ", c.style
	}
	return c.text, c.style
}

// Rows is the visible screen as text, one string per row (always Size's rows of them), trailing spaces trimmed. A wide rune
// is one character of the string and two cells of the screen.
func (t *Term) Rows() []string {
	g := t.g()
	out := make([]string, len(g.rows))
	for i, r := range g.rows {
		out[i] = rowText(r)
	}
	return out
}

// Scrollback is the rows that have scrolled off the top of the primary screen, oldest first, as text.
func (t *Term) Scrollback() []string {
	out := make([]string, len(t.sb))
	for i, r := range t.sb {
		out[i] = rowText(r)
	}
	return out
}

// All is the scrollback followed by the visible rows: everything the terminal has shown, as a user scrolling up would see
// it. Trailing empty rows of the visible screen are kept; a test that does not care trims them.
func (t *Term) All() []string { return append(t.Scrollback(), t.Rows()...) }

// String is the visible screen as text: Rows joined by newlines, without a final newline.
func (t *Term) String() string { return strings.Join(t.Rows(), "\n") }

func (t *Term) g() *screen {
	if t.alt {
		return &t.altS
	}
	return &t.main
}

func rowText(r vrow) string {
	end := len(r.cells) - 1
	for end >= 0 && (r.cells[end].text == "" || r.cells[end].text == " ") {
		end-- // empty cells and spaces at the end are trimmed, so they are not built
	}
	if end < 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(end + 1)
	for _, c := range r.cells[:end+1] {
		switch {
		case c.cont:
		case c.text == "":
			b.WriteByte(' ')
		default:
			b.WriteString(c.text)
		}
	}
	return b.String()
}

// Write feeds the terminal bytes as a program would write them. It always consumes all of p and never fails; a UTF-8 rune or
// an escape sequence cut at the end of p is completed by the next call.
func (t *Term) Write(p []byte) (int, error) {
	for _, b := range p {
		t.feed(b)
	}
	return len(p), nil
}

// WriteString is Write for a string.
func (t *Term) WriteString(s string) (int, error) {
	for i := 0; i < len(s); i++ {
		t.feed(s[i])
	}
	return len(s), nil
}
