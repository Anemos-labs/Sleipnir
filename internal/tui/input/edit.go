package input

// The editing primitives. Every change to the buffer goes through replace, which keeps the undo list; everything else (typing,
// deleting, killing, yanking, pasting, completing) is a thin caller of it.

// group says which run of commands an edit belongs to: edits of one group that touch neighbouring text are one undo step.
type group uint8

const (
	grpNone group = iota // never merged
	grpType              // typing: one step for the whole run
	grpBack              // Backspace: a run deletes as one step
	grpFwd               // Delete: likewise
)

const (
	maxUndo      = 500     // undo steps kept (dropped in batches, so up to a eighth more are there for a moment)
	maxUndoRunes = 1 << 20 // and the runes they hold between them: clearing a huge buffer many times must not pile up
	maxKills     = 8       // entries in the kill ring

	// maxBufferRunes is the most the buffer holds (a chip counts as one rune). Typing, completing or pasting past it is dropped:
	// a prompt of 65,536 characters is far past what anyone writes by hand, and what the editor does per key and per redraw
	// grows with the buffer.
	maxBufferRunes = 1 << 16
)

// runesOf is cleaned text as runes, cut to the size of the buffer.
func runesOf(s string) []rune {
	rs := []rune(cleanText(s))
	if len(rs) > maxBufferRunes {
		rs = rs[:maxBufferRunes]
	}
	return rs
}

// undoRec is one undo step: new replaced old at pos, and the cursor was at before, then after.
type undoRec struct {
	pos           int
	old, new      []rune
	before, after int
	grp           group
}

// splice returns buf with buf[from:to] replaced by ins, in place when it fits (an edit at the end of a long buffer costs
// nothing in proportion to it).
func splice(buf []rune, from, to int, ins []rune) []rune {
	n := len(buf) - (to - from) + len(ins)
	if n > cap(buf) {
		out := make([]rune, n, n+n/4+16)
		copy(out, buf[:from])
		copy(out[from:], ins)
		copy(out[from+len(ins):], buf[to:])
		return out
	}
	rest := buf[to:len(buf)]
	buf = buf[:n]
	copy(buf[from+len(ins):], rest) // the tail moves; copy handles the overlap
	copy(buf[from:], ins)
	return buf
}

// replace swaps buf[from:to] for rs, puts the cursor at curAfter and records the change for undo. Arguments out of range are
// clamped: the buffer cannot be damaged through it.
func (e *Editor) replace(from, to int, rs []rune, curAfter int, g group) {
	if to > len(e.buf) {
		to = len(e.buf)
	}
	if from < 0 {
		from = 0
	}
	if from > to {
		from = to
	}
	if room := maxBufferRunes - (len(e.buf) - (to - from)); len(rs) > room { // the buffer is full: take what fits
		rs = rs[:max(room, 0)]
		curAfter = from + len(rs)
	}
	if curAfter < 0 || curAfter > len(e.buf)-(to-from)+len(rs) {
		curAfter = len(e.buf) - (to - from) + len(rs)
	}
	if from == to && len(rs) == 0 {
		return
	}
	rec := undoRec{
		pos: from, old: append([]rune(nil), e.buf[from:to]...), new: append([]rune(nil), rs...),
		before: e.cur, after: curAfter, grp: g,
	}
	e.buf = splice(e.buf, from, to, rec.new)
	if !e.coalesce(rec) {
		e.undo = append(e.undo, rec)
		e.trimUndo()
	}
	e.redo = nil
	e.cur = curAfter
	e.thisGrp = g
	e.dirty = true
}

// trimUndo forgets the oldest undo steps beyond about maxUndo, or beyond maxUndoRunes runes held (the newest step always
// stays). Steps are dropped in batches, so that an edit does not move the whole list.
func (e *Editor) trimUndo() {
	keep, runes := 0, 0
	for i := len(e.undo) - 1; i >= 0 && keep < maxUndo; i-- {
		runes += len(e.undo[i].old) + len(e.undo[i].new)
		if keep > 0 && runes > maxUndoRunes {
			break
		}
		keep++
	}
	drop := len(e.undo) - keep
	if drop == 0 || (drop < maxUndo/8 && runes <= maxUndoRunes) {
		return
	}
	n := copy(e.undo, e.undo[drop:])
	clear(e.undo[n:]) // let go of the text the dropped steps held
	e.undo = e.undo[:n]
}

// coalesce merges rec into the previous undo step when both are the same kind of run and adjacent.
func (e *Editor) coalesce(r undoRec) bool {
	if r.grp == grpNone || e.lastGrp != r.grp || len(e.undo) == 0 {
		return false
	}
	last := &e.undo[len(e.undo)-1]
	if last.grp != r.grp {
		return false
	}
	switch r.grp {
	case grpType:
		if len(r.old) == 0 && len(last.old) == 0 && r.pos == last.pos+len(last.new) {
			last.new = append(last.new, r.new...)
			last.after = r.after
			return true
		}
	case grpBack:
		if len(r.new) == 0 && len(last.new) == 0 && r.pos+len(r.old) == last.pos {
			last.old = append(append([]rune(nil), r.old...), last.old...)
			last.pos = r.pos
			last.after = r.after
			return true
		}
	case grpFwd:
		if len(r.new) == 0 && len(last.new) == 0 && r.pos == last.pos {
			last.old = append(last.old, r.old...)
			last.after = r.after
			return true
		}
	}
	return false
}

func (e *Editor) undoCmd() {
	if len(e.undo) == 0 {
		return
	}
	r := e.undo[len(e.undo)-1]
	if r.pos < 0 || r.pos+len(r.new) > len(e.buf) { // cannot happen; if it ever did, drop the history rather than the buffer
		e.undo, e.redo = nil, nil
		return
	}
	e.undo = e.undo[:len(e.undo)-1]
	e.buf = splice(e.buf, r.pos, r.pos+len(r.new), r.old)
	e.cur = snap(e.buf, r.before)
	e.redo = append(e.redo, r)
	e.afterHistoryJump()
}

func (e *Editor) redoCmd() {
	if len(e.redo) == 0 {
		return
	}
	r := e.redo[len(e.redo)-1]
	if r.pos < 0 || r.pos+len(r.old) > len(e.buf) {
		e.undo, e.redo = nil, nil
		return
	}
	e.redo = e.redo[:len(e.redo)-1]
	e.buf = splice(e.buf, r.pos, r.pos+len(r.old), r.new)
	e.cur = snap(e.buf, r.after)
	e.undo = append(e.undo, r)
	e.afterHistoryJump()
}

// afterHistoryJump ends undo grouping, marks the editor dirty, and closes completion after a
// history change.
func (e *Editor) afterHistoryJump() {
	e.thisGrp = grpNone
	e.dirty = true
	e.closeMenu()
}

// typeRune inserts a typed character after cleaning it (a character that cleans to nothing is dropped).
func (e *Editor) typeRune(r rune) bool {
	s := cleanText(string(r))
	if s == "" || s == "\n" || s == "\t" {
		return false
	}
	rs := []rune(s)
	e.replace(e.cur, e.cur, rs, e.cur+len(rs), grpType)
	return true
}

// newline inserts a newline at the cursor as a separate undo group.
func (e *Editor) newline() { e.replace(e.cur, e.cur, []rune{'\n'}, e.cur+1, grpNone) }

// backspace deletes the preceding editor text unit with backward-delete undo grouping, doing
// nothing at the start.
func (e *Editor) backspace() {
	if e.cur == 0 {
		return
	}
	from := prevBoundary(e.buf, e.cur)
	e.replace(from, e.cur, nil, from, grpBack)
}

// deleteForward deletes the next editor text unit with forward-delete undo grouping, doing nothing
// at the end.
func (e *Editor) deleteForward() {
	if e.cur >= len(e.buf) {
		return
	}
	e.replace(e.cur, nextBoundary(e.buf, e.cur), nil, e.cur, grpFwd)
}

// killRange removes buf[from:to] and adds it to the kill ring. Consecutive kills build one entry: a forward kill appends, a
// backward kill prepends, as in Emacs, so Ctrl+W pressed three times yanks back as one piece.
func (e *Editor) killRange(from, to int, forward bool) {
	if from >= to || from < 0 || to > len(e.buf) {
		return
	}
	text := append([]rune(nil), e.buf[from:to]...)
	if e.lastKill && len(e.kills) > 0 {
		top := &e.kills[len(e.kills)-1]
		if forward {
			*top = append(*top, text...)
		} else {
			*top = append(text, *top...)
		}
	} else {
		e.kills = append(e.kills, text)
		if len(e.kills) > maxKills {
			e.kills = append([][]rune(nil), e.kills[len(e.kills)-maxKills:]...)
		}
	}
	e.ringPos = 0
	e.thisKill = true
	e.replace(from, to, nil, from, grpNone)
}

// killToEOL kills through the end of the line, including the newline when already at line end.
func (e *Editor) killToEOL() {
	end := lineEnd(e.buf, e.cur)
	if e.cur == end && end < len(e.buf) {
		end++ // at the end of a line, the newline goes: the next line joins this one
	}
	e.killRange(e.cur, end, true)
}

// killToBOL kills back to line start, including the preceding newline when already at line start.
func (e *Editor) killToBOL() {
	start := lineStart(e.buf, e.cur)
	if e.cur == start && start > 0 {
		start--
	}
	e.killRange(start, e.cur, false)
}

// killWordBack removes text from the previous word boundary to the cursor into the kill buffer.
func (e *Editor) killWordBack() { e.killRange(wordStart(e.buf, e.cur), e.cur, false) }

// killWordForward removes text from the cursor to the next word boundary into the kill buffer.
func (e *Editor) killWordForward() { e.killRange(e.cur, wordEnd(e.buf, e.cur), true) }

// yank inserts the newest kill at the cursor.
func (e *Editor) yank() {
	if len(e.kills) == 0 {
		return
	}
	text := e.kills[len(e.kills)-1]
	e.ringPos = 0
	e.yankFrom = e.cur
	e.replace(e.cur, e.cur, text, e.cur+len(text), grpNone)
	e.yankTo = e.cur
	e.thisYank = true
}

// yankPop replaces the text just yanked with the next older kill. It does nothing unless the previous key was a yank.
func (e *Editor) yankPop() {
	if !e.lastYank || len(e.kills) < 2 || e.yankFrom < 0 || e.yankTo > len(e.buf) || e.yankFrom > e.yankTo {
		return
	}
	e.ringPos = (e.ringPos + 1) % len(e.kills)
	text := e.kills[len(e.kills)-1-e.ringPos]
	e.replace(e.yankFrom, e.yankTo, text, e.yankFrom+len(text), grpNone)
	e.yankTo = e.yankFrom + len(text)
	e.thisYank = true
}

// dropChipKills forgets kills that hold chips: chips die with the prompt they were pasted into, and a yank of one into the
// next prompt would be a reference to nothing.
func (e *Editor) dropChipKills() {
	keep := e.kills[:0]
	for _, k := range e.kills {
		has := false
		for _, r := range k {
			if isChip(r) {
				has = true
				break
			}
		}
		if !has {
			keep = append(keep, k)
		}
	}
	e.kills = keep
}

// transpose swaps the two clusters around the cursor and moves past them; at the end of a line it swaps the two before it.
// It never moves text across a line break.
func (e *Editor) transpose() {
	c := e.cur
	var a, b, d int
	if c == len(e.buf) || e.buf[c] == '\n' {
		b = prevBoundary(e.buf, c)
		a = prevBoundary(e.buf, b)
		d = c
	} else {
		b = c
		a = prevBoundary(e.buf, c)
		d = nextBoundary(e.buf, c)
	}
	if b <= 0 || a >= b || b >= d || a < lineStart(e.buf, c) || e.buf[a] == '\n' || e.buf[b] == '\n' {
		return
	}
	swapped := append(append([]rune(nil), e.buf[b:d]...), e.buf[a:b]...)
	e.replace(a, d, swapped, d, grpNone)
}
