package render

import (
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
)

// maxHistory is how many printed lines an anchored renderer keeps to repaint a resized screen from.
const maxHistory = 2000

// autoFlushLines is how many printed lines may wait for a frame before Print writes them itself, so that a caller that prints a
// great deal and never flushes does not grow without bound.
const autoFlushLines = 1000

// InlineOption configures NewInline.
type InlineOption func(*Inline)

// KeepLive makes Close leave the last live region on the screen, in the scrollback, instead of erasing it.
func KeepLive() InlineOption { return func(r *Inline) { r.keepLive = true } }

// WithBottomAnchor makes the last row of the live region the last row of the terminal, whatever the region shows: the screen is
// the content, a gap that what is printed fills before anything scrolls, and the region. The first frame scrolls what the terminal
// showed into its scrollback, so that the region has the bottom to itself; a resize clears the screen and paints the last of what was
// printed again at the new size (the renderer keeps it for that). Without it the region sits right under the last printed line and
// moves down as the content grows, and its footer moves with every change of its height.
func WithBottomAnchor() InlineOption { return func(r *Inline) { r.anchor = true } }

// WithStartRow tells an anchored renderer the row of the terminal (counted from 0 at the top) its cursor is on when the first frame is
// drawn, which is a fresh line: the content starts there and what the terminal showed above it stays. Without it (or with a negative
// row) the first frame scrolls what the terminal showed into its scrollback instead, as it cannot know where the content may begin.
func WithStartRow(row int) InlineOption { return func(r *Inline) { r.startRow = row } }

// WithBracketedPaste makes the renderer switch bracketed paste on with its first frame and off in Close, on a terminal that has it
// (Caps.BracketedPaste) and not for plain output. Writes from one place keep the mode and the terminal's state together: a
// program that exits through Close cannot leave the terminal wrapping every paste in markers.
func WithBracketedPaste() InlineOption { return func(r *Inline) { r.paste = true } }

// row is one terminal row of the live region as it was (or will be) drawn.
type row struct {
	enc  string    // the bytes that draw it, starting and ending in the default rendition
	w    int       // the cells it fills
	line cell.Line // what it shows, to work out how a terminal that reflows lays it out after a resize
}

// Inline is a scrollback printer with a live region.
//
// Print appends lines to the terminal's own scrollback: they go above the live region and are never drawn again, so copy,
// search, tmux and SSH work on them as on any program's output. SetLive sets the live region, the last few lines of the screen
// (status, input box, footer), which is redrawn in place. SetCursor says where the cursor rests in it.
//
// Nothing is written until Flush: Print, SetLive, SetCursor and Resize only record what the screen should become, and Flush
// writes one frame that gets it there, so any number of changes between two flushes cost one frame. A frame that would change
// nothing writes nothing, not even the synchronized-output markers. When only some rows of the live region differ, only those
// rows are rewritten, with relative cursor moves and erase to end of line; when something was printed, the region is erased and
// redrawn below it.
//
// Lines wider than the terminal are wrapped by the renderer (cell.Line.Wrap, with the line's indentation kept on every row),
// so that the number of rows it counts is the number the terminal uses. A printed line is wrapped once, at the width in force
// when it is written; if the terminal later gets narrower, the terminal re-wraps those rows itself, by cell. The live region is
// never taller than the terminal: if it would be, its top rows are dropped.
//
// A resize leaves the renderer unsure what the terminal did to the rows it had drawn (most terminals re-wrap a line that is now
// too wide, xterm cuts it). The next frame therefore erases the old region conservatively, upward by the larger of the rows it
// drew and the rows the same text takes at the new width, then redraws everything. Where a terminal does not reflow, this erases
// as many lines of history above the region as the re-wrap would have added (none when the window only gets wider; a few for a
// region of a few rows and a modest narrowing); erasing too little would leave a ghost of the old region in view.
//
// The cursor is the renderer's business only while there is a live region: with one it is shown at the position SetCursor gave
// and hidden while rows are drawn, or hidden for good if none was given; with none it is left alone, shown, so that a program that
// only prints, or one that dies before Close, leaves the terminal as it found it.
//
// With Caps.Dumb or Caps.Color == ColorNone the renderer is plain: it writes no escape sequence at all, wraps nothing, never
// draws the live region (it only remembers it, and prints it once at Close with KeepLive) and ends lines with "\n" for a
// pipe or "\r\n" for a terminal. That is the output of a pipe or a CI log.
//
// On a real terminal every line ends in "\r\n", because a terminal in raw mode does not add the carriage return.
type Inline struct {
	mu      sync.Mutex
	w       io.Writer
	plain   bool   // no escape sequences at all
	nl      string // the line ending
	syncOut bool
	cm      *colorMap

	keepLive bool
	paste    bool // bracketed paste was asked for and is possible
	anchor   bool // WithBottomAnchor

	// the anchored layout: the row after the last row of content, counted from the top of the terminal, and what was printed
	cend     int
	startRow int         // WithStartRow, or -1
	placed   bool        // the first frame has put the region at the bottom
	hist     []cell.Line // the lines printed, newest last, for a resize to repaint

	cols, rows int

	// what the caller wants the screen to become
	pend    []cell.Line // printed and not yet written, sanitized
	live    []cell.Line // sanitized, not yet wrapped
	caretOn bool
	caretR  int
	caretC  int

	// what the terminal shows, as far as the renderer knows
	drawn   []row
	cr, cc  int  // the terminal's cursor, relative to the top left of the live region
	hidden  bool // the cursor is hidden
	pasteOn bool
	stale   bool // a resize has happened since the region was drawn

	// a page of the whole screen (SetFull), drawn on the alternate screen while it is set
	full     []cell.Line
	fullOn   bool     // the terminal is on the alternate screen
	fullRows []string // the rows as drawn there
	fullCur  [3]int   // the caret as drawn: visible (0 or 1), row, column
	fullWipe bool     // the terminal was resized while the page was up: clear it before drawing

	closed bool
	err    error
	buf    []byte
}

// NewInline returns a renderer that writes to w, which is normally the terminal's output (os.Stdout). caps says what the
// terminal can do (term.Detect); a zero Width or Height is replaced by the default 80x24. NewInline writes nothing.
func NewInline(w io.Writer, caps term.Caps, opts ...InlineOption) *Inline {
	if caps.Width < 1 {
		caps.Width = term.DefaultWidth
	}
	if caps.Height < 1 {
		caps.Height = term.DefaultHeight
	}
	r := &Inline{
		startRow: -1,
		w:        w, cols: caps.Width, rows: caps.Height,
		plain: caps.Dumb || caps.Color == term.ColorNone,
		nl:    "\r\n",
		cm:    newColorMap(caps.Color),
	}
	if caps.Dumb {
		r.nl = "\n" // not a terminal, or one that cannot be told anything: no carriage return to pair with
	}
	r.syncOut = caps.SyncOutput && !r.plain
	for _, o := range opts {
		o(r)
	}
	r.paste = r.paste && caps.BracketedPaste && !r.plain
	r.anchor = r.anchor && !r.plain
	return r
}

// Size is the size the renderer lays out for: the terminal's columns and rows as last given by NewInline or Resize.
func (r *Inline) Size() (cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cols, r.rows
}

// Print appends lines to the scrollback, above the live region. The lines are sanitized now and written at the next Flush (or
// sooner when a great many are waiting). After Close, or after a failed write, Print does nothing.
func (r *Inline) Print(lines ...cell.Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for _, l := range lines {
		s := sanitizeLine(l)
		r.pend = append(r.pend, s)
		if r.anchor {
			r.hist = append(r.hist, s)
		}
	}
	if len(r.hist) > 2*maxHistory {
		r.hist = append([]cell.Line(nil), r.hist[len(r.hist)-maxHistory:]...)
	}
	if len(r.pend) >= autoFlushLines {
		r.flushLocked()
	}
}

// SetLive sets the live region: zero or more lines shown below everything printed, redrawn in place at each Flush. The lines are
// sanitized now; the caller may reuse the slice.
func (r *Inline) SetLive(lines []cell.Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.live = make([]cell.Line, len(lines))
	for i, l := range lines {
		r.live[i] = sanitizeLine(l)
	}
}

// SetFull puts a page of the whole screen on the terminal's alternate screen, one line to a row from the top (a line wider than the
// terminal is wrapped, and rows past the bottom are dropped), and SetFull(nil) takes it away again: the terminal then shows what it showed
// before, the scrollback and the live region as they were, and what was printed in the meantime is written. While a page is up the live
// region is not drawn and Print only waits. SetCursor places the cursor on the page, from its top left. A plain renderer ignores it.
func (r *Inline) SetFull(lines []cell.Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.plain {
		return
	}
	if lines == nil {
		r.full = nil
		return
	}
	r.full = make([]cell.Line, len(lines))
	for i, l := range lines {
		r.full[i] = sanitizeLine(l)
	}
}

// SetCursor says where the cursor rests, in cells from the top left of the live region, after wrapping (a caller that supplies
// lines wider than the terminal should wrap them first to know where the cursor belongs). A negative row hides the cursor,
// which is also how it starts: a live region that is only a status line has nothing to edit. A row past the region is taken as
// its last row and a column past the terminal as its last column. With no live region the cursor is not the renderer's to
// place and this has no effect until there is one.
func (r *Inline) SetCursor(row, col int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caretOn, r.caretR, r.caretC = row >= 0, row, col
}

// Resize tells the renderer the terminal's new size (from term.WatchResize). Nothing is written until the next Flush. Sizes
// below 1 are taken as 1.
func (r *Inline) Resize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cols, rows = max(cols, 1), max(rows, 1)
	if cols == r.cols && rows == r.rows {
		return
	}
	r.cols, r.rows = cols, rows
	if len(r.drawn) > 0 {
		r.stale = true
	}
	if r.fullOn {
		r.fullRows = nil // the page is drawn again whole: the alternate screen has no memory of what was on it at the old size
		r.fullWipe = true
	}
}

// Flush writes one frame that brings the terminal to what Print, SetLive, SetCursor and Resize have recorded, if there is
// anything to change. It returns the first error a Write has returned, now or earlier; after an error nothing more is written.
func (r *Inline) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushLocked()
}

// Close writes what is pending, then leaves the terminal ready for whatever comes next: the cursor shown, bracketed paste off,
// and the cursor at the start of a fresh line below the live region (KeepLive) or where the region was, which is erased. After
// Close every method does nothing; Close itself may be called again. It returns the first error a Write has returned.
func (r *Inline) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.err
	}
	r.flushLocked()
	if r.plain {
		if r.keepLive && r.err == nil {
			r.writePlain(r.live)
		}
	} else {
		r.full = nil
		r.flushFull() // gives the alternate screen back
		r.closeTerminal()
	}
	r.closed = true
	r.live, r.drawn, r.pend = nil, nil, nil
	return r.err
}

// write is the only place the renderer writes. A failure is remembered and ends all output.
func (r *Inline) write(b []byte) {
	if r.err != nil || len(b) == 0 {
		return
	}
	if _, err := r.w.Write(b); err != nil {
		r.err = err
	}
}

func (r *Inline) flushLocked() error {
	if r.closed {
		return r.err
	}
	if r.err != nil {
		r.pend = nil
		return r.err
	}
	if r.plain {
		lines := r.pend
		r.pend = nil
		r.writePlain(lines)
		return r.err
	}
	r.flushTerminal()
	return r.err
}

// writePlain writes lines as plain text, one per line, without wrapping: a pipe or a log keeps each line whole.
func (r *Inline) writePlain(lines []cell.Line) {
	if len(lines) == 0 {
		return
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Plain())
		b.WriteString(r.nl)
	}
	r.write([]byte(b.String()))
}

// encodeRows wraps a line to the terminal's width and encodes each row.
func (r *Inline) encodeRows(l cell.Line) []row {
	wrapped := wrapRows(l, r.cols)
	out := make([]row, len(wrapped))
	for i, w := range wrapped {
		enc, cells := r.cm.encode(w)
		out[i] = row{enc: enc, w: cells, line: w}
	}
	return out
}

// layout wraps and encodes the live region. The live region is cut to the terminal's height by dropping its top rows, and the
// caret follows: it is reported off when its row was cut away.
func (r *Inline) layout() (rows []row, tr, tc int, caretOn bool) {
	for _, l := range r.live {
		rows = append(rows, r.encodeRows(l)...)
	}
	cut := 0
	if len(rows) > r.rows {
		cut = len(rows) - r.rows
		rows = rows[cut:]
	}
	if r.caretOn && len(rows) > 0 && r.caretR-cut >= 0 {
		tr = min(r.caretR-cut, len(rows)-1)
		tc = min(max(r.caretC, 0), r.cols-1)
		caretOn = true
	}
	return rows, tr, tc, caretOn
}

func sameRows(a, b []row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].enc != b[i].enc {
			return false
		}
	}
	return true
}

// flushFull draws the page of SetFull on the alternate screen, or leaves it. It reports whether the page holds the screen, in which case
// nothing else is drawn.
func (r *Inline) flushFull() bool {
	if r.full == nil && !r.fullOn {
		return false
	}
	var body []byte
	if r.full == nil { // leaving: the terminal restores the screen, and the cursor is put as the live region wants it
		body = append(body, altScreenOff...)
		if r.hidden {
			body = append(body, hideCursor...)
		} else {
			body = append(body, showCursor...)
		}
		r.fullOn, r.fullRows = false, nil
		r.write(r.framed(body))
		return false
	}
	var rows []row
	for _, l := range r.full {
		rows = append(rows, r.encodeRows(l)...)
	}
	if len(rows) > r.rows {
		rows = rows[:r.rows]
	}
	if !r.fullOn {
		body = append(body, altScreenOn...)
		body = append(body, hideCursor...)
		body = append(body, eraseScreen...)
		r.fullOn, r.fullRows = true, nil
		r.fullCur = [3]int{}
	}
	if r.fullWipe {
		body = append(body, eraseScreen...)
		r.fullWipe = false
	}
	for i, rw := range rows {
		if i >= len(r.fullRows) || r.fullRows[i] != rw.enc {
			body = append(body, "\x1b["+itoa(i+1)+";1H"...)
			body = append(body, rw.enc...)
			body = append(body, eraseToEOL...)
		}
	}
	for i := len(rows); i < len(r.fullRows); i++ { // rows the page no longer has
		body = append(body, "\x1b["+itoa(i+1)+";1H"...)
		body = append(body, eraseToEOL...)
	}
	cur := [3]int{}
	if r.caretOn {
		cur = [3]int{1, min(max(r.caretR, 0), max(len(rows)-1, 0)), min(max(r.caretC, 0), r.cols-1)}
	}
	if len(body) == 0 && cur == r.fullCur {
		return true // the page is as it was
	}
	r.fullRows = r.fullRows[:0]
	for _, rw := range rows {
		r.fullRows = append(r.fullRows, rw.enc)
	}
	if cur[0] == 1 {
		body = append(body, "\x1b["+itoa(cur[1]+1)+";"+itoa(cur[2]+1)+"H"...)
		body = append(body, showCursor...)
	} else {
		body = append(body, hideCursor...)
	}
	r.fullCur = cur
	r.write(r.framed(body))
	return true
}

// framed is a frame between the markers of synchronized output, on a terminal that has them.
func (r *Inline) framed(body []byte) []byte {
	if !r.syncOut {
		return body
	}
	return append(append([]byte(syncBegin), body...), syncEnd...)
}

// itoa formats an integer in decimal for terminal control sequences.
func itoa(n int) string { return strconv.Itoa(n) }

// flushTerminal writes the frame for a terminal that takes escape sequences.
func (r *Inline) flushTerminal() {
	if r.flushFull() {
		return
	}
	want, tr, tc, caretOn := r.layout()
	full := len(r.pend) > 0 || r.stale
	if r.anchor { // the region is at the bottom: a different height is a different place, and the first frame places it
		full = full || len(want) != len(r.drawn) || !r.placed
	}
	rowsChanged := full || !sameRows(r.drawn, want)
	// The cursor is the renderer's business only while there is a live region: with one, it is shown at the caret or, when
	// the caller has set none, hidden (a status line has nothing to edit); with none, it is left alone and visible, so that a
	// program that only prints, or one that dies before Close, leaves the terminal as it found it.
	wantVisible := len(want) == 0 || caretOn
	pasteChanged := r.paste != r.pasteOn
	if !rowsChanged && wantVisible == !r.hidden && !pasteChanged && (!caretOn || (tr == r.cr && tc == r.cc)) {
		return
	}

	e := newEmitter(r.buf[:0], r.cols, r.cr, r.cc, len(r.drawn))
	if r.syncOut {
		e.raw(syncBegin)
	}
	if pasteChanged {
		if r.paste {
			e.raw(pasteOn)
		} else {
			e.raw(pasteOff)
		}
		r.pasteOn = r.paste
	}
	if rowsChanged && !r.hidden && (len(want) > 0 || len(r.drawn) > 0) {
		e.raw(hideCursor) // a cursor left visible would be seen dancing over the rows as they are written
		r.hidden = true
	}
	switch {
	case full && r.anchor:
		r.drawAnchored(e, want)
	case full:
		r.drawFull(e, want)
	case rowsChanged:
		r.drawDiff(e, want)
	}
	r.drawn = want
	if caretOn {
		e.moveTo(tr, tc)
	}
	switch {
	case wantVisible && r.hidden:
		e.raw(showCursor)
		r.hidden = false
	case !wantVisible && !r.hidden:
		e.raw(hideCursor)
		r.hidden = true
	}
	if r.syncOut {
		e.raw(syncEnd)
	}
	r.cr, r.cc = e.row, e.col
	r.stale, r.pend = false, nil
	r.write(e.buf)
	r.buf = e.buf[:0]
}

// drawFull erases the live region, writes what was printed, then draws the whole region below it.
func (r *Inline) drawFull(e *emitter, want []row) {
	if len(r.drawn) > 0 || r.stale {
		up := r.cr
		if r.stale {
			up = max(up, r.reflowedAbove())
		}
		e.csiUp(up)
		e.raw("\r")
		e.raw(eraseBelow)
	}
	for _, l := range r.pend {
		for _, pr := range r.encodeRows(l) {
			e.raw(pr.enc)
			e.raw("\r\n")
		}
	}
	e.origin()
	for i, rw := range want {
		if i > 0 {
			e.newline()
		}
		e.text(rw)
	}
}

// drawAnchored draws the frame of an anchored renderer that is not a change of rows in place: the first one, one that prints, one that
// changes the height of the region, or one after a resize. The content ends at the row cend; from there the screen is erased, what was
// printed is written, and the region is drawn so that its last row is the last row of the terminal.
func (r *Inline) drawAnchored(e *emitter, want []row) {
	pend, abs := r.pend, 0
	switch {
	case r.placed && r.stale:
		// the window was resized: the terminal may have wrapped or cut what it had, so the screen is cleared and the last of what was
		// printed is written again at the new size
		e.raw(eraseScreen)
		e.raw("\x1b[H")
		pend = r.historyTail(r.rows - len(want))
	case !r.placed && r.startRow >= 0:
		abs = min(r.startRow, r.rows-1) // the content starts where the cursor is, under what the terminal showed
		e.raw("\r")
	case !r.placed:
		// scroll what the terminal shows into its scrollback, to have the bottom to ourselves; the cursor was on a fresh line, which
		// the scroll takes to the top row
		for i := 0; i < r.rows-1; i++ {
			e.raw("\r\n")
		}
		e.csiUp(r.rows - 1)
		e.raw("\r")
	default:
		up := r.rows - len(r.drawn) + r.cr - r.cend // the cursor is in the region, which is at the bottom
		e.csiUp(up)
		e.raw("\r")
		e.raw(eraseBelow)
		abs = r.cend
	}
	r.placed, r.stale = true, false
	for _, l := range pend {
		for _, pr := range r.encodeRows(l) {
			e.raw(pr.enc)
			e.raw("\r\n")
			if abs < r.rows-1 {
				abs++
			}
		}
	}
	n := len(want)
	if n == 0 {
		r.cend = abs
		e.origin()
		return
	}
	top := r.rows - n
	if abs < top {
		e.csi(top-abs, 'B') // the gap: the cursor goes down without scrolling anything
	}
	e.origin()
	for i, rw := range want {
		if i > 0 {
			e.newline()
		}
		e.text(rw)
	}
	r.cend = min(abs, top)
}

// historyTail is the last of the printed lines that fit in rows terminal rows at the current width.
func (r *Inline) historyTail(rows int) []cell.Line {
	used, from := 0, len(r.hist)
	for from > 0 {
		h := len(wrapRows(r.hist[from-1], r.cols))
		if used+h > rows {
			break
		}
		used += h
		from--
	}
	return r.hist[from:]
}

// reflowedAbove is how many rows a terminal that re-wraps its hard lines puts above the cursor, at the terminal's current
// width, where the renderer left the cursor: the rows of the region above the cursor's row, each as many times taller as the
// re-wrap makes it, and the rows of the cursor's own row before the cursor. It is at least as many as before the resize.
func (r *Inline) reflowedAbove() int {
	above := 0
	for i := 0; i < r.cr && i < len(r.drawn); i++ {
		h, _ := reflow(r.drawn[i].line, r.cols, 0)
		above += h
	}
	if r.cr < len(r.drawn) {
		_, offRow := reflow(r.drawn[r.cr].line, r.cols, r.cc)
		above += offRow
	}
	return max(above, r.cr)
}

// drawDiff rewrites only the rows that differ, adds rows at the bottom or erases rows that are no longer wanted.
func (r *Inline) drawDiff(e *emitter, want []row) {
	old := r.drawn
	n, m := len(old), len(want)
	for i := 0; i < n && i < m; i++ {
		if old[i].enc == want[i].enc {
			continue
		}
		e.moveTo(i, 0)
		e.text(want[i])
		if old[i].w > want[i].w {
			e.raw(eraseToEOL)
		}
	}
	for i := n; i < m; i++ {
		if i > 0 {
			e.gotoRow(i - 1)
			e.newline()
		}
		e.text(want[i])
	}
	if m < n {
		e.moveTo(m, 0)
		e.raw(eraseBelow)
		e.maxRow = m - 1
	}
}

// closeTerminal leaves the terminal tidy: the region kept on screen with the cursor on a fresh line below it, or erased.
func (r *Inline) closeTerminal() {
	if r.err != nil {
		return
	}
	e := newEmitter(r.buf[:0], r.cols, r.cr, r.cc, len(r.drawn))
	if n := len(r.drawn); n > 0 {
		if r.keepLive {
			e.gotoRow(n - 1)
			e.newline()
		} else {
			e.gotoRow(0)
			if r.anchor {
				e.gotoRow(-(r.rows - len(r.drawn) - r.cend)) // up through the gap to the end of the content
			}
			e.raw("\r")
			e.raw(eraseBelow)
		}
	}
	if r.hidden {
		e.raw(showCursor)
		r.hidden = false
	}
	if r.pasteOn {
		e.raw(pasteOff)
		r.pasteOn = false
	}
	if len(e.buf) == 0 {
		return
	}
	if r.syncOut {
		e.buf = append(append([]byte(syncBegin), e.buf...), syncEnd...)
	}
	r.write(e.buf)
	r.buf = e.buf[:0]
}
