package input

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	menuRows   = 8   // rows of the completion menu that are visible at once
	maxCands   = 200 // candidates the editor keeps from one call to a Completer
	maxCandLen = 512 // bytes of one candidate's Text, Display or Detail
)

// Candidate is one completion.
type Candidate struct {
	Text    string // replaces the text from the completer's `from` to the cursor; a Text that ends in "/" or "=" keeps the menu open (what follows is another choice)
	Display string // what the menu shows; Text when empty
	Detail  string // a dim description to its right
}

// Completer proposes completions. line is the line of the buffer the cursor is on, without its newline (a paste chip is
// U+FFFC), and cursor is the cursor's byte offset in it. It returns from, the byte offset in line where the text to be
// replaced starts (the replaced text is line[from:cursor]), and the candidates, best first.
//
// A Completer has to be quick, since the editor calls it after keystrokes; it must treat line as data and must not act on it.
// If it returns from outside 0..cursor or in the middle of a rune, or no candidates, there is no menu. The editor cleans every
// candidate (a file name can hold control characters) and keeps at most 200.
type Completer interface {
	Complete(line string, cursor int) (from int, cands []Candidate)
}

// CompleterFunc is a Completer that is a function.
type CompleterFunc func(line string, cursor int) (int, []Candidate)

// Complete calls f.
func (f CompleterFunc) Complete(line string, cursor int) (int, []Candidate) { return f(line, cursor) }

// menu is the open completion menu.
type menu struct {
	from  int // buffer index where an accepted candidate starts replacing
	cands []Candidate
	sel   int
	top   int  // first visible row
	moved bool // the user moved the selection: it then stays on its candidate while the list is refiltered
	byTab bool // opened by a Tab press: further Tab presses cycle, as in a shell, instead of accepting
}

// MenuState is a snapshot of the completion menu.
type MenuState struct {
	Open       bool
	From       int // buffer index (runes) where accepting a candidate starts replacing
	Candidates []Candidate
	Selected   int
}

// Completion reports the completion menu.
func (e *Editor) Completion() MenuState {
	if e.menu == nil {
		return MenuState{}
	}
	return MenuState{Open: true, From: e.menu.from, Candidates: append([]Candidate(nil), e.menu.cands...), Selected: e.menu.sel}
}

// completionCtx is the cursor's line as a Completer sees it, the cursor's byte offset in it, and the buffer index of its start.
func (e *Editor) completionCtx() (line string, cursor, ls int) {
	ls = lineStart(e.buf, e.cur)
	le := lineEnd(e.buf, e.cur)
	var b strings.Builder
	for i := ls; i < le; i++ {
		if i == e.cur {
			cursor = b.Len()
		}
		r := e.buf[i]
		if isChip(r) {
			r = '\U0000fffc'
		}
		b.WriteRune(r)
	}
	if e.cur == le {
		cursor = b.Len()
	}
	return b.String(), cursor, ls
}

// complete asks the completer; from is a buffer index. ok is false when there is nothing to show.
func (e *Editor) complete() (from int, cands []Candidate, ok bool) {
	if e.opt.Completer == nil {
		return 0, nil, false
	}
	line, cur, ls := e.completionCtx()
	f, cs := e.opt.Completer.Complete(line, cur)
	if f < 0 || f > cur || (f < len(line) && !utf8.RuneStart(line[f])) || len(cs) == 0 {
		return 0, nil, false
	}
	if len(cs) > maxCands {
		cs = cs[:maxCands]
	}
	for _, c := range cs {
		c.Text = cleanLine(c.Text)
		if c.Text == "" {
			continue
		}
		c.Display = cleanLine(c.Display)
		if c.Display == "" {
			c.Display = c.Text
		}
		c.Detail = cleanLine(c.Detail)
		cands = append(cands, c)
	}
	return ls + utf8.RuneCountInString(line[:f]), cands, len(cands) > 0
}

// cleanLine is cleanText for text that must stay on one line.
func cleanLine(s string) string {
	s = cleanText(s)
	if strings.ContainsAny(s, "\n\t") {
		s = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' {
				return ' '
			}
			return r
		}, s)
	}
	if len(s) > maxCandLen {
		s = cutUTF8(s, maxCandLen)
	}
	return s
}

// maxWord bounds how far back triggerWord looks: a "word" longer than this (a pasted blob) is not a command or a path, and
// looking further would make every key press in a long line cost the length of the line.
const maxWord = 256

// triggerWord finds the word before the cursor and says whether it is one that opens the menu by itself: a word that starts
// with "/" at the start of a line (slash commands), or with "@" (file paths).
func (e *Editor) triggerWord() (start int, ok bool) {
	s := e.cur
	for s > 0 && e.cur-s < maxWord && e.buf[s-1] != '\n' && !unicode.IsSpace(e.buf[s-1]) && !isChip(e.buf[s-1]) {
		s--
	}
	if e.cur-s >= maxWord {
		return s, false
	}
	if e.commandArgument(s) {
		return s, true // the first argument of a slash command ("/model qw"): the completer says whether it has choices for it
	}
	if s == e.cur {
		return s, false
	}
	switch e.buf[s] {
	case '/':
		return s, s == 0 || e.buf[s-1] == '\n'
	case '@':
		return s, true
	}
	return s, false
}

// commandArgument reports whether the word that starts at s is the first argument of a slash command: the line begins with
// "/name" and one space comes between it and s.
func (e *Editor) commandArgument(s int) bool {
	ls := lineStart(e.buf, s)
	if s-ls < 3 || e.buf[ls] != '/' || e.buf[s-1] != ' ' {
		return false
	}
	for _, r := range e.buf[ls : s-1] {
		if unicode.IsSpace(r) || isChip(r) {
			return false
		}
	}
	return true
}

// afterKey is what every key that reached the editing commands leads to for the menu: an open one is brought up to date, a
// closed one opens when a trigger word has just been typed.
func (e *Editor) afterKey(typed bool) {
	if e.menu != nil {
		e.refreshMenu()
		return
	}
	s, ok := e.triggerWord()
	if e.dismiss >= 0 && (!ok || s != e.dismiss) {
		e.dismiss = -1 // the cursor left the word whose menu was closed
	}
	if typed && ok && e.dismiss != s {
		e.openMenu()
	}
}

// openMenu asks the completer and shows the result, if there is one.
func (e *Editor) openMenu() {
	from, cands, ok := e.complete()
	if !ok {
		e.closeMenu()
		return
	}
	e.showMenu(from, cands)
}

// showMenu shows cands. When the menu was already open and the user had moved the selection, it stays on the same candidate
// if that is still listed; otherwise the best match, the first, is selected, so that typing on always selects what fits best.
func (e *Editor) showMenu(from int, cands []Candidate) {
	if m := e.menu; m != nil && m.from == from && sameCandidates(m.cands, cands) {
		return // nothing changed: the selection and the way the menu was opened stay as they are
	}
	prev := ""
	if e.menu != nil && e.menu.moved && e.menu.sel < len(e.menu.cands) {
		prev = e.menu.cands[e.menu.sel].Text
	}
	wasOpen := e.menu != nil
	m := &menu{from: from, cands: cands}
	if prev != "" {
		for i, c := range cands {
			if c.Text == prev {
				m.sel, m.moved = i, true
				break
			}
		}
	}
	e.menu = m
	e.adjustMenu()
	e.dirty = true
	if !wasOpen {
		e.emit(CompletionOpened{})
	}
}

func sameCandidates(a, b []Candidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// refreshMenu re-runs the completer after an edit or a cursor move, and closes the menu when the cursor has left the word
// or nothing matches any more.
func (e *Editor) refreshMenu() {
	m := e.menu
	if m == nil {
		return
	}
	if m.from > e.cur || m.from < 0 {
		e.closeMenu()
		return
	}
	for _, r := range e.buf[m.from:e.cur] {
		if unicode.IsSpace(r) || isChip(r) {
			e.closeMenu()
			if s, ok := e.triggerWord(); ok && e.commandArgument(s) {
				e.openMenu() // the command's name is done: its argument has choices of its own, if the completer has any
			}
			return
		}
	}
	e.openMenu()
}

func (e *Editor) closeMenu() {
	if e.menu == nil {
		return
	}
	e.menu = nil
	e.dirty = true
	e.emit(CompletionClosed{})
}

// menuKey handles a key while the menu is open and reports whether it consumed it. Up, Down, Ctrl+P, Ctrl+N and Shift+Tab
// move the selection (wrapping), PgUp and PgDn a page; Enter accepts; Esc closes. Tab accepts in a menu that typing opened
// (a "/" or an "@"), and cycles forward in one that Tab opened, where Enter accepts. Every other key goes on to the editor as
// usual, and the menu follows what it did.
func (e *Editor) menuKey(k Key) bool {
	switch {
	case k.Is(Up, 0), k.IsRune('p', Ctrl), k.Is(Tab, Shift):
		e.menuMove(-1, true)
	case k.Is(Down, 0), k.IsRune('n', Ctrl):
		e.menuMove(1, true)
	case k.Is(PgUp, 0):
		e.menuMove(-menuRows, false)
	case k.Is(PgDn, 0):
		e.menuMove(menuRows, false)
	case k.Is(Tab, 0) && e.menu.byTab:
		e.menuMove(1, true)
	case k.Is(Tab, 0), k.Is(Enter, 0):
		e.acceptMenu()
	case k.Is(Esc, 0):
		if s, ok := e.triggerWord(); ok {
			e.dismiss = s
		}
		e.closeMenu()
	default:
		return false
	}
	return true
}

func (e *Editor) menuMove(d int, wrap bool) {
	m := e.menu
	n := len(m.cands)
	if n == 0 {
		return
	}
	s := m.sel + d
	switch {
	case wrap:
		s = ((s % n) + n) % n
	case s < 0:
		s = 0
	case s >= n:
		s = n - 1
	}
	m.sel, m.moved = s, true
	e.adjustMenu()
	e.dirty = true
}

// adjustMenu scrolls so that the selected row is visible.
func (e *Editor) adjustMenu() {
	m := e.menu
	if m.sel < m.top {
		m.top = m.sel
	}
	if m.sel >= m.top+menuRows {
		m.top = m.sel - menuRows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

// acceptMenu replaces the word with the selected candidate. A candidate that ends in "/" (a directory) leaves the menu open
// on what is inside it, and one that ends in "=" (a role of /roles) on the models it can be given.
func (e *Editor) acceptMenu() {
	m := e.menu
	if m == nil || m.sel >= len(m.cands) || m.from > e.cur {
		e.closeMenu()
		return
	}
	e.applyCandidate(m.from, m.cands[m.sel])
}

func (e *Editor) applyCandidate(from int, c Candidate) {
	rs := []rune(c.Text)
	e.replace(from, e.cur, rs, from+len(rs), grpNone)
	e.closeMenu()
	if strings.HasSuffix(c.Text, "/") || strings.HasSuffix(c.Text, "=") {
		e.openMenu()
	}
}

// tabComplete is Tab with no menu open: ask the completer about the word before the cursor. One candidate is applied at
// once, several open the menu, none does nothing.
func (e *Editor) tabComplete() {
	from, cands, ok := e.complete()
	if !ok {
		return
	}
	if len(cands) == 1 {
		e.applyCandidate(from, cands[0])
		return
	}
	e.dismiss = -1
	e.showMenu(from, cands)
	e.menu.byTab = true
}
