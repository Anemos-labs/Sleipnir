package render

import "strconv"

// The escape sequences the renderer writes. There are no others: text that came from outside never becomes one (sanitize.go), so
// every ESC in the output stream starts one of these (plus SGR, composed in sgr.go).
const (
	hideCursor   = "\x1b[?25l"
	showCursor   = "\x1b[?25h"
	syncBegin    = "\x1b[?2026h" // synchronized output: the terminal shows the frame when it ends
	syncEnd      = "\x1b[?2026l"
	pasteOn      = "\x1b[?2004h"
	pasteOff     = "\x1b[?2004l"
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	eraseBelow   = "\x1b[J"  // erase from the cursor to the end of the screen
	eraseToEOL   = "\x1b[K"  // erase from the cursor to the end of the row
	eraseScreen  = "\x1b[2J" // erase the whole screen
	resetSGR     = "\x1b[0m"
)

// emitter builds the bytes of one frame while it keeps track of where the terminal's cursor is, so that every move is the
// shortest relative one. Rows are counted from the top of the live region; the cursor is never left in the delayed-wrap
// state, because a full row is followed at once by a carriage return.
type emitter struct {
	buf      []byte
	cols     int
	row, col int
	maxRow   int // the last row of the region that exists on the terminal, below which a cursor-down move would go nowhere
}

// newEmitter initializes terminal output tracking at the supplied cursor and viewport dimensions.
func newEmitter(buf []byte, cols, row, col, rows int) *emitter {
	return &emitter{buf: buf, cols: cols, row: row, col: col, maxRow: rows - 1}
}

// raw appends bytes without updating the emitter's cursor or style tracking.
func (e *emitter) raw(s string) { e.buf = append(e.buf, s...) }

// origin starts counting again from the cursor: it is on the first row of a region that has no rows yet.
func (e *emitter) origin() { e.row, e.col, e.maxRow = 0, 0, -1 }

// csi writes ESC [ n final, leaving out n when it is 1 (the default).
func (e *emitter) csi(n int, final byte) {
	e.buf = append(e.buf, 0x1b, '[')
	if n != 1 {
		e.buf = strconv.AppendInt(e.buf, int64(n), 10)
	}
	e.buf = append(e.buf, final)
}

// csiUp moves the cursor up n rows (n may be 0) without keeping count: it is used to reach the top of a region before the
// count starts again at origin.
func (e *emitter) csiUp(n int) {
	if n > 0 {
		e.csi(n, 'A')
	}
}

// gotoRow emits relative vertical cursor movement and updates the tracked row.
func (e *emitter) gotoRow(row int) {
	switch {
	case row < e.row:
		e.csi(e.row-row, 'A')
	case row > e.row:
		e.csi(row-e.row, 'B')
	}
	e.row = row
}

// digits returns the length of an integer's decimal representation, including a minus sign if
// present.
func digits(n int) int { return len(strconv.Itoa(n)) }

// gotoCol moves along the row with the shortest of carriage return, cursor forward, cursor back and absolute column.
func (e *emitter) gotoCol(col int) {
	if col == e.col {
		return
	}
	if col == 0 {
		e.buf = append(e.buf, '\r')
		e.col = 0
		return
	}
	d, final := col-e.col, byte('C')
	if d < 0 {
		d, final = -d, 'D'
	}
	relLen := 3 // ESC [ C
	if d != 1 {
		relLen += digits(d) // a distance of 1 is the default and is left out
	}
	if absLen := 3 + digits(col+1); relLen <= absLen { // ESC [ n G
		e.csi(d, final)
	} else {
		e.buf = append(e.buf, 0x1b, '[')
		e.buf = strconv.AppendInt(e.buf, int64(col+1), 10)
		e.buf = append(e.buf, 'G')
	}
	e.col = col
}

// moveTo moves the emitter cursor vertically and then horizontally.
func (e *emitter) moveTo(row, col int) {
	e.gotoRow(row)
	e.gotoCol(col)
}

// text writes a row at the cursor, which must be at its first column, and leaves the cursor at the start of the row again if
// the row filled the line, so that no delayed wrap is pending.
func (e *emitter) text(r row) {
	e.buf = append(e.buf, r.enc...)
	e.col += r.w
	if e.row > e.maxRow {
		e.maxRow = e.row
	}
	if e.col >= e.cols {
		e.buf = append(e.buf, '\r')
		e.col = 0
	}
}

// newline starts a new row below the last one with a carriage return and a line feed, which scrolls the terminal when the
// cursor is on its last line.
func (e *emitter) newline() {
	if n := len(e.buf); n > 0 && e.buf[n-1] == '\r' { // text that filled the row has already returned the carriage
		e.buf = append(e.buf, '\n')
	} else {
		e.buf = append(e.buf, '\r', '\n')
	}
	e.row++
	e.col = 0
	if e.row > e.maxRow {
		e.maxRow = e.row
	}
}
