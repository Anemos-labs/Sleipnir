package input

import "github.com/reee344/sleipnir/internal/tui/cell"

// Cursor movement and history navigation.

// moveTo puts the cursor at i, moved to a cluster boundary.
func (e *Editor) moveTo(i int) {
	if c := snap(e.buf, i); c != e.cur {
		e.cur, e.dirty = c, true
	}
}

// SetWidth tells the editor how wide the terminal is, so that Up and Down move by display row: a long line that wraps over
// five rows is walked row by row, and only from its first or last row does the next press go to the history. The width is the
// one the caller passes to View (values under 4 count as 4). Without it (or with a width of 0 or less) they move by buffer line,
// which is also the right answer for text that does not wrap. Call it again after the terminal is resized.
func (e *Editor) SetWidth(width int) { e.width = width }

// vertical is Up or Down: a row (or, without a width, a line) up or down in the buffer, and from the first or last one on,
// a step through the history.
func (e *Editor) vertical(dir int) {
	moved := false
	if e.width > 0 {
		moved = e.moveRow(dir)
	} else {
		moved = e.moveLine(dir)
	}
	if moved {
		e.vert = true
		return
	}
	e.histMove(dir)
}

// moveRow moves the cursor to the display row above or below it, to the cell in it that is nearest the column the run of
// vertical moves began at. It reports false when there is no such row.
func (e *Editor) moveRow(dir int) bool {
	width := max(e.width, 4)
	l := e.layout(max(width-cell.StringWidth(e.opt.Prompt), 2))
	target := l.cursor.row + dir
	if target < 0 || target >= len(l.rows) {
		return false
	}
	if e.want < 0 {
		e.want = l.cursor.col
	}
	e.moveTo(indexInRow(l, target, e.want))
	e.dirty = true
	return true
}

// indexInRow is the buffer index for a cursor that is on row r of the layout at display column col: before the glyph whose cells
// contain col, or, when the row ends before col, on its last glyph (on a row that wraps on) or at the end of its line.
func indexInRow(l layout, r, col int) int {
	row := l.rows[r]
	idx := -1
	for g := row.g0; g < row.g1; g++ {
		gl := l.glyphs[g]
		if gl.zero {
			continue
		}
		if p := l.pos[g]; col < p.col+gl.w {
			return gl.start
		}
		idx = gl.start
	}
	if row.last || idx < 0 {
		return row.le
	}
	return idx
}

// moveLine moves the cursor to the previous or next buffer line, keeping the display column it had when the run of vertical
// moves began (a short line in between does not make it forget). It reports false when there is no such line.
func (e *Editor) moveLine(dir int) bool {
	start := lineStart(e.buf, e.cur)
	var ts, te int
	if dir < 0 {
		if start == 0 {
			return false
		}
		te = start - 1
		ts = lineStart(e.buf, te)
	} else {
		end := lineEnd(e.buf, e.cur)
		if end >= len(e.buf) {
			return false
		}
		ts = end + 1
		te = lineEnd(e.buf, ts)
	}
	if e.want < 0 {
		e.want = e.colOf(e.cur)
	}
	e.moveTo(e.indexAtCol(ts, te, e.want))
	e.dirty = true
	return true
}

// indexAtCol is the cluster boundary in buf[start:end] whose cell contains display column col (the end of the line when the
// line is shorter).
func (e *Editor) indexAtCol(start, end, col int) int {
	c := 0
	for i := start; i < end; {
		next := nextBoundary(e.buf, i)
		w := c
		for p := i; p < next; p++ {
			w = advance(w, e.buf[p], e.chipWidth(e.buf[p]))
		}
		if col < w {
			return i
		}
		c, i = w, next
	}
	return end
}

// histMove walks the history: dir < 0 is older (Up), dir > 0 newer (Down). The text being typed when the walk began is
// remembered and comes back when the walk returns past the newest entry. Editing a recalled entry is allowed, but moving
// to another entry discards the edit.
func (e *Editor) histMove(dir int) {
	n := e.hist.Len()
	switch {
	case dir < 0:
		if e.hpos >= n {
			return
		}
		if e.hpos == 0 {
			e.draft, e.draftCur = append([]rune(nil), e.buf...), e.cur
		}
		e.hpos++
		e.showEntry(e.hist.At(n-e.hpos), true)
	default:
		if e.hpos == 0 {
			return
		}
		e.hpos--
		if e.hpos == 0 {
			d, c := e.draft, e.draftCur
			e.draft = nil
			e.setBuffer(d, c)
			return
		}
		e.showEntry(e.hist.At(n-e.hpos), false)
	}
}

// showEntry loads a history entry into the buffer with the cursor at the end of its first line (walking up: one more Up
// moves on through the history) or of its last (walking down).
func (e *Editor) showEntry(text string, firstLine bool) {
	rs := runesOf(text)
	c := len(rs)
	if firstLine {
		c = lineEnd(rs, 0)
	}
	e.setBuffer(rs, c)
}

// setBuffer replaces the whole buffer without recording undo (the old text is not in the new one's history: undo starts over).
func (e *Editor) setBuffer(rs []rune, cur int) {
	e.buf = rs
	e.cur = snap(e.buf, cur)
	e.undo, e.redo = nil, nil
	e.thisGrp = grpNone
	e.dirty = true
	e.closeMenu()
}
