package vt

// Resize changes the size of the terminal (each dimension clamped to 1..4096), as a window resize does.
//
// The primary screen and its scrollback are treated as one list of rows whose last row is the cursor's row or the last row
// with anything in it, whichever is lower. With reflow (the default) rows linked by soft wraps are joined into lines and
// wrapped again at the new width; a hard line break (a row that was ended by a line feed) is never joined, so a row that is
// too wide for the new width is wrapped, and a row that was wrapped by the renderer is not joined when the window grows. The
// cursor keeps its place in its line. Without reflow every row is cut or padded. Then the last rows of the list that fit
// are the screen and the rest is scrollback; if everything fits, nothing is scrollback. The alternate screen is cut or
// padded and never reflowed.
func (t *Term) Resize(cols, rows int) {
	cols, rows = clampSize(cols, rows)
	if cols == t.cols && rows == t.rows {
		return
	}
	cur := cursorState{x: t.x, y: t.y, pending: t.pending}
	if t.alt {
		cur = t.altSave // the primary screen's cursor is the one saved when the alternate screen came up
	}
	ncur := t.resizeMain(cols, rows, cur)
	if t.alt {
		t.altSave.x, t.altSave.y, t.altSave.pending = ncur.x, ncur.y, ncur.pending
		t.altS = screen{rows: cropRows(t.altS.rows, cols, rows)}
	} else {
		t.x, t.y, t.pending = ncur.x, ncur.y, ncur.pending
	}
	t.cols, t.rows = cols, rows
	if t.alt {
		t.moveToKeepPending(t.x, t.y)
	}
	t.saved.x, t.saved.y = min(t.saved.x, cols-1), min(t.saved.y, rows-1)
	t.altSave.x, t.altSave.y = min(t.altSave.x, cols-1), min(t.altSave.y, rows-1)
	t.pending = t.pending && t.x == cols-1
}

func (t *Term) moveToKeepPending(x, y int) {
	p := t.pending
	t.moveTo(x, y)
	t.pending = p && t.x == x
}

func (t *Term) resizeMain(cols, rows int, cur cursorState) cursorState {
	src := make([]vrow, 0, len(t.sb)+len(t.main.rows))
	src = append(src, t.sb...)
	src = append(src, t.main.rows...)
	cy := len(t.sb) + cur.y
	last := len(src) - 1
	for last > cy && rowIsBlank(src[last]) {
		last--
	}
	src = src[:last+1]

	var out []vrow
	var ncy, ncx int
	var npend bool
	if t.reflow {
		out, ncy, ncx, npend = reflowRows(src, cols, cy, cur.x, cur.pending)
	} else {
		out = cropRows(src, cols, len(src))
		ncy, ncx, npend = cy, min(cur.x, cols-1), cur.pending && cur.x <= cols-1
	}

	top := max(len(out)-rows, 0)
	if ncy < top {
		top = ncy
	}
	end := min(len(out), top+rows)
	scr := append([]vrow(nil), out[top:end]...)
	for len(scr) < rows {
		scr = append(scr, vrow{cells: make([]vcell, cols)})
	}
	t.sb = append([]vrow(nil), out[:top]...)
	t.trimScrollback()
	t.main.rows = scr
	return cursorState{x: ncx, y: ncy - top, pending: npend}
}

func rowIsBlank(r vrow) bool {
	if r.wrapped {
		return false
	}
	for _, c := range r.cells {
		if c != (vcell{}) {
			return false
		}
	}
	return true
}

// cropRows cuts or pads every row to cols cells and the list to n rows (xterm's resize). A wide rune whose right half is cut
// is blanked.
func cropRows(src []vrow, cols, n int) []vrow {
	out := make([]vrow, n)
	for i := range out {
		cells := make([]vcell, cols)
		if i < len(src) {
			copy(cells, src[i].cells)
			if cols < len(src[i].cells) && cells[cols-1].wide {
				cells[cols-1] = vcell{}
			}
			if len(src[i].cells) > cols && cells[0].cont {
				cells[0] = vcell{}
			}
			out[i] = vrow{cells: cells, wrapped: src[i].wrapped, pad: false}
		} else {
			out[i] = vrow{cells: cells}
		}
	}
	return out
}

// reflowRows joins the soft-wrapped rows of src into lines and wraps them at cols. The cursor is at column cx of row cy (and in
// the delayed-wrap state when pending); the returned row and column are where it lands.
func reflowRows(src []vrow, cols, cy, cx int, pending bool) (out []vrow, ncy, ncx int, npend bool) {
	for i := 0; i < len(src); {
		j := i
		for j < len(src)-1 && src[j].wrapped {
			j++
		}
		var line []vcell
		curOff := -1
		for k := i; k <= j; k++ {
			if k == cy {
				curOff = len(line) + cx // the cell the cursor is on; one more when it waits at the end of a full row
				if pending {
					curOff++
				}
			}
			line = append(line, src[k].cells[:contentLen(src[k])]...)
		}
		minLen := 0
		if curOff >= 0 {
			minLen = curOff
			if !pending {
				minLen++
			}
		}
		line = fitLine(line, minLen)
		rows, r, c, p := wrapCells(line, cols, curOff)
		if curOff >= 0 {
			ncy, ncx, npend = len(out)+r, c, p
		}
		out = append(out, rows...)
		i = j + 1
	}
	if len(out) == 0 {
		out = []vrow{{cells: make([]vcell, cols)}}
	}
	return out, ncy, ncx, npend
}

// contentLen is how many cells of a row are content: all of them, or one fewer when the last one is wrap padding.
func contentLen(r vrow) int {
	if r.pad {
		return len(r.cells) - 1
	}
	return len(r.cells)
}

// fitLine drops the empty cells at the end of a line, but keeps at least minLen cells (the cursor may be beyond the text).
func fitLine(line []vcell, minLen int) []vcell {
	n := len(line)
	for n > minLen && line[n-1] == (vcell{}) {
		n--
	}
	line = line[:n]
	for len(line) < minLen {
		line = append(line, vcell{})
	}
	return line
}

// wrapCells lays a line out in rows of cols cells, a wide rune never split: one that does not fit at the end of a row starts
// the next, and the hole it leaves is padding. The cursor, at offset curOff of the line (-1: not in it), is returned as a row
// and column; an offset just past the end of the line that fills its row exactly is reported as the delayed-wrap state on that
// row's last column.
func wrapCells(line []vcell, cols, curOff int) (rows []vrow, cr, cc int, pending bool) {
	cur := vrow{cells: make([]vcell, cols)}
	col, off := 0, 0
	found := curOff < 0
	for i := 0; i < len(line); {
		w := 1
		if line[i].wide && i+1 < len(line) && line[i+1].cont {
			w = 2
		}
		if w > cols { // a wide rune on a one-column screen has no place
			i += w
			off += w
			continue
		}
		if col+w > cols {
			cur.wrapped, cur.pad = true, col < cols
			rows = append(rows, cur)
			cur = vrow{cells: make([]vcell, cols)}
			col = 0
		}
		if !found && curOff < off+w {
			cr, cc, found = len(rows), col, true
		}
		cur.cells[col] = line[i]
		if w == 2 {
			cur.cells[col+1] = line[i+1]
		}
		col += w
		i += w
		off += w
	}
	if !found { // the cursor is just past the end of the line
		cr, cc = len(rows), col
		if col >= cols {
			cc, pending = cols-1, true
		}
	}
	rows = append(rows, cur)
	return rows, cr, cc, pending
}
