package vt

import (
	"github.com/reee344/sleipnir/internal/tui/cell"
)

// blank is the cell an erase leaves: empty, in the current background colour (back-colour erase, as xterm does), so a
// renderer that erases while a background is active shows up in the screen's styles.
func (t *Term) blank() vcell { return vcell{style: cell.Style{BG: t.sty.BG}} }

func (t *Term) newRow() vrow {
	r := vrow{cells: make([]vcell, t.cols)}
	if b := t.blank(); b != (vcell{}) {
		for i := range r.cells {
			r.cells[i] = b
		}
	}
	return r
}

// print draws one rune at the cursor.
func (t *Term) print(r rune) {
	w := cell.RuneWidth(r)
	if w == 0 {
		t.attachMark(r)
		return
	}
	if w == 2 && t.cols < 2 {
		return // a wide rune cannot be shown on a one-column screen
	}
	if t.pending {
		t.wrap(false)
	}
	if w == 2 && t.x == t.cols-1 { // only one column left: pad it and continue on the next row
		if !t.autoWrap {
			return
		}
		t.clearRange(t.y, t.x, t.x+1)
		t.wrap(true)
	}
	t.clearRange(t.y, t.x, t.x+w)
	row := &t.g().rows[t.y]
	row.cells[t.x] = vcell{text: string(r), style: t.sty, wide: w == 2}
	if w == 2 {
		row.cells[t.x+1] = vcell{style: t.sty, cont: true}
	}
	t.x += w
	if t.x >= t.cols {
		t.x = t.cols - 1
		t.pending = t.autoWrap
	}
}

// wrap moves to the start of the next row because the text continued: the row is marked soft-wrapped, which is what lets a
// resize join it with the next one.
func (t *Term) wrap(pad bool) {
	row := &t.g().rows[t.y]
	row.wrapped = true
	row.pad = pad
	t.x = 0
	t.lineFeed()
}

// attachMark adds a zero-width rune (a combining mark, a joiner, a variation selector) to the cell that was drawn last.
func (t *Term) attachMark(r rune) {
	x := t.x
	if !t.pending {
		x--
	}
	if x < 0 {
		return
	}
	row := &t.g().rows[t.y]
	if row.cells[x].cont && x > 0 {
		x--
	}
	c := &row.cells[x]
	if c.text == "" {
		c.text = " "
	}
	c.text += string(r)
}

func (t *Term) lineFeed() {
	t.pending = false
	if t.y == t.rows-1 {
		t.scrollUp(1)
	} else {
		t.y++
	}
}

func (t *Term) reverseIndex() {
	t.pending = false
	if t.y == 0 {
		t.scrollDown(1)
	} else {
		t.y--
	}
}

// scrollUp moves every row up n times. On the primary screen the rows that leave the top go to the scrollback.
func (t *Term) scrollUp(n int) {
	g := t.g()
	n = min(max(n, 0), t.rows)
	for i := 0; i < n; i++ {
		if !t.alt {
			t.sb = append(t.sb, g.rows[0])
		}
		copy(g.rows, g.rows[1:])
		g.rows[t.rows-1] = t.newRow()
	}
	t.trimScrollback()
}

func (t *Term) trimScrollback() {
	if over := len(t.sb) - t.sbMax; over > 0 {
		t.sb = append([]vrow(nil), t.sb[over:]...)
	}
}

func (t *Term) scrollDown(n int) {
	g := t.g()
	n = min(max(n, 0), t.rows)
	for i := 0; i < n; i++ {
		copy(g.rows[1:], g.rows[:t.rows-1])
		g.rows[0] = t.newRow()
	}
}

// insertLines opens n blank rows at the cursor row; the rows below move down and the bottom ones are lost.
func (t *Term) insertLines(n int) {
	g := t.g()
	n = min(max(n, 0), t.rows-t.y)
	copy(g.rows[t.y+n:], g.rows[t.y:t.rows-n])
	for i := 0; i < n; i++ {
		g.rows[t.y+i] = t.newRow()
	}
	t.x, t.pending = 0, false
}

// deleteLines removes n rows at the cursor row; the rows below move up and blank rows enter at the bottom.
func (t *Term) deleteLines(n int) {
	g := t.g()
	n = min(max(n, 0), t.rows-t.y)
	copy(g.rows[t.y:], g.rows[t.y+n:])
	for i := 0; i < n; i++ {
		g.rows[t.rows-n+i] = t.newRow()
	}
	t.x, t.pending = 0, false
}

// clearRange blanks the cells [x0, x1) of row y. A wide rune cut by either end of the range is blanked whole, because half a
// rune is not something a terminal shows.
func (t *Term) clearRange(y, x0, x1 int) {
	x0, x1 = max(x0, 0), min(x1, t.cols)
	if x0 >= x1 {
		return
	}
	row := &t.g().rows[y]
	if x0 > 0 && row.cells[x0].cont {
		row.cells[x0-1] = t.blank()
	}
	if x1 < t.cols && row.cells[x1].cont {
		row.cells[x1] = t.blank()
	}
	b := t.blank()
	for x := x0; x < x1; x++ {
		row.cells[x] = b
	}
	if x1 == t.cols {
		row.pad = false
	}
}

// eraseRow blanks a whole row and cuts its link to the next one.
func (t *Term) eraseRow(y int) {
	t.g().rows[y] = t.newRow()
}

// eraseLine is EL.
func (t *Term) eraseLine(mode int) {
	row := &t.g().rows[t.y]
	switch mode {
	case 0:
		t.clearRange(t.y, t.x, t.cols)
		row.wrapped = false
	case 1:
		t.clearRange(t.y, 0, t.x+1)
	case 2:
		t.eraseRow(t.y)
	}
}

// eraseDisplay is ED.
func (t *Term) eraseDisplay(mode int) {
	switch mode {
	case 0:
		t.eraseLine(0)
		for y := t.y + 1; y < t.rows; y++ {
			t.eraseRow(y)
		}
	case 1:
		for y := 0; y < t.y; y++ {
			t.eraseRow(y)
		}
		t.eraseLine(1)
	case 2:
		for y := 0; y < t.rows; y++ {
			t.eraseRow(y)
		}
	case 3:
		t.sb = nil
	}
}

func (t *Term) moveTo(x, y int) {
	t.x = min(max(x, 0), t.cols-1)
	t.y = min(max(y, 0), t.rows-1)
	t.pending = false
}

func (t *Term) saveCursor() {
	t.saved = cursorState{x: t.x, y: t.y, pending: t.pending, style: t.sty, ok: true}
}

func (t *Term) restoreCursor() {
	if !t.saved.ok {
		t.moveTo(0, 0)
		t.sty = cell.Style{}
		return
	}
	s := t.saved
	t.moveTo(s.x, s.y)
	t.pending = s.pending && t.x == t.cols-1
	t.sty = s.style
}

// setAlt switches between the primary and the alternate screen. The alternate screen starts empty and has no scrollback.
// With saveCursor (?1049) the cursor is saved on the way in and restored on the way out.
func (t *Term) setAlt(on, saveCursor bool) {
	if on == t.alt {
		return
	}
	if on {
		t.altSave = cursorState{x: t.x, y: t.y, pending: t.pending, style: t.sty, ok: saveCursor}
		t.alt = true
		t.altS = screen{rows: blankRows(t.cols, t.rows)}
		t.pending = false
		return
	}
	t.alt = false
	t.altS = screen{}
	if s := t.altSave; s.ok { // without the save (?47, ?1047) the cursor stays where the program left it
		t.moveTo(s.x, s.y)
		t.pending = s.pending && t.x == t.cols-1
		t.sty = s.style
	}
}
