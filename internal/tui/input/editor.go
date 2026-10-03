package input

import (
	"strings"
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Event is what the editor reports back from a key. A key can produce several, in order; the concrete types are below, and
// a caller switches on them:
//
//	for _, ev := range ed.Handle(k) {
//		switch ev := ev.(type) {
//		case input.Submit: send(ev.Text)
//		case input.Interrupt: ...
//		}
//	}
type Event interface{ isEvent() }

// Submit is Enter on a non-empty prompt. Text has paste chips expanded to the pasted text and trailing whitespace removed;
// the editor has already cleared itself and stored the text in its history.
type Submit struct{ Text string }

// Interrupt is Ctrl+C. The caller decides what it means (cancel the turn, or quit on a second press). Cleared says the press
// found text in the buffer and cleared it: the usual reading is that such a press only clears.
type Interrupt struct{ Cleared bool }

// EOF is Ctrl+D on an empty buffer.
type EOF struct{}

// Redraw is Ctrl+L: the caller should repaint everything.
type Redraw struct{}

// Changed says something the view shows has changed (text, cursor, menu, search, the Esc hint): draw it again.
type Changed struct{}

// CompletionOpened and CompletionClosed report the completion menu appearing and going away.
type CompletionOpened struct{}
type CompletionClosed struct{}

// SearchOpened and SearchClosed report the reverse history search (Ctrl+R) starting and ending.
type SearchOpened struct{}
type SearchClosed struct{}

// Unhandled is a key the editor has no use for (F-keys, Shift+Tab, PgUp, Esc on an empty buffer, Ctrl+O, ...). The caller may
// give it a meaning of its own.
type Unhandled struct{ Key Key }

// isEvent marks Submit as an editor event without runtime work.
func (Submit) isEvent() {}

// isEvent marks Interrupt as an editor event without runtime work.
func (Interrupt) isEvent() {}

// isEvent marks EOF as an editor event without runtime work.
func (EOF) isEvent() {}

// isEvent marks Redraw as an editor event without runtime work.
func (Redraw) isEvent() {}

// isEvent marks Changed as an editor event without runtime work.
func (Changed) isEvent() {}

// isEvent marks CompletionOpened as an editor event without runtime work.
func (CompletionOpened) isEvent() {}

// isEvent marks CompletionClosed as an editor event without runtime work.
func (CompletionClosed) isEvent() {}

// isEvent marks SearchOpened as an editor event without runtime work.
func (SearchOpened) isEvent() {}

// isEvent marks SearchClosed as an editor event without runtime work.
func (SearchClosed) isEvent() {}

// isEvent marks Unhandled as an editor event without runtime work.
func (Unhandled) isEvent() {}

// Options configures an Editor. The zero value is usable.
type Options struct {
	History     *History  // nil: a private in-memory history
	Completer   Completer // nil: no completion, Tab does nothing
	Placeholder string    // shown, dim, while the buffer is empty
	Prompt      string    // the first line's prefix, at most 16 cells; default "❯ " (continuation lines get as many spaces)
	Theme       *Theme    // nil: DefaultTheme()

	// A paste of at most PasteLines lines and PasteRunes runes (defaults 3 and 200) is inserted as if typed; a larger one
	// becomes a chip. MaxPasteBytes (default 1 MiB) caps the text of one paste; the rest is dropped and "[truncated]" is
	// appended so the loss is visible.
	PasteLines    int
	PasteRunes    int
	MaxPasteBytes int
}

// Editor is the prompt: a multi-line buffer of runes, a cursor, history, kill ring, undo, paste chips, a completion menu and
// a reverse search, driven one Key at a time by Handle. It does no I/O (beyond the history file, through History) and starts no
// goroutines, and it is not safe for concurrent use.
//
// The buffer is always valid UTF-8, free of control characters and escape sequences, and the cursor is always at a cluster
// boundary (between a base character and its combining marks it can never be), so whatever View draws is text and nothing
// else.
//
// Keys, by what they do (the rest are returned as Unhandled):
//
//	characters                     insert
//	Backspace, Delete              delete back, forward (Ctrl+D is Delete, and EOF on an empty buffer)
//	Left, Right, Ctrl+B, Ctrl+F    by cluster
//	Alt+B, Alt+F, Ctrl+Left/Right  by word
//	Home, End, Ctrl+A, Ctrl+E      start, end of the line; Ctrl+Home, Ctrl+End of the buffer
//	Up, Down, Ctrl+P, Ctrl+N       by row or line, then through the history (see SetWidth)
//	Ctrl+R                         reverse search of the history (see Search)
//	Ctrl+K, Ctrl+U                 kill to the end, to the start of the line
//	Ctrl+W, Alt+Backspace, Alt+D   kill the word back, the word forward
//	Ctrl+Y, Alt+Y                  yank the last kill, then older ones
//	Ctrl+T                         transpose
//	Ctrl+_, Ctrl+Z                 undo; redo is Alt+Z, Alt+Ctrl+_ or (where reported) Ctrl+Shift+Z
//	Alt+Enter, Shift+Enter, Ctrl+J newline; so is Enter after a backslash, which it removes
//	Enter                          Submit
//	Esc                            closes the menu or the search; on a non-empty buffer the second press clears it
//	Ctrl+C                         clears a non-empty buffer; always reports Interrupt (but only closes a search)
//	Ctrl+L                         Redraw
//	Tab                            completion, see below; it never inserts a tab character
//
// Completion: a "/" at the start of a line or an "@" at the start of a word opens the menu by itself, and Tab opens it on any
// word. In a menu that typing opened Tab and Enter accept and the arrows, Ctrl+P and Ctrl+N move; in one that Tab opened, Tab
// moves to the next candidate and Enter accepts. Esc closes it, typing filters it.
//
// A word is a run of letters, digits, combining marks and '_'; whitespace and other punctuation separate words and are
// skipped by word motion and kills, as readline does. A paste chip is a word of its own. A tab character can only come from a
// paste or from history; it is kept, and drawn as spaces up to the next multiple of four.
type Editor struct {
	opt  Options
	th   Theme
	hist *History

	buf   []rune
	cur   int
	width int  // the terminal width set by SetWidth, 0 when unknown
	want  int  // preferred display column for Up/Down, -1 when none
	vert  bool // the key being handled was a vertical move inside the buffer (so want is kept)

	chips     []chip // the pastes stored aside since the last Submit; a chip rune's id indexes it
	chipBytes int    // the text they hold

	undo, redo []undoRec
	kills      [][]rune
	ringPos    int
	yankFrom   int
	yankTo     int

	// Per command: what this key did, and what the previous one did (kill sequences merge, yank-pop needs a yank, typing
	// runs are one undo step).
	thisKill, lastKill bool
	thisYank, lastYank bool
	thisGrp, lastGrp   group

	hpos     int    // 0: editing the draft; k: showing the k-th newest history entry
	draft    []rune // what was being typed before history navigation began
	draftCur int

	escArmed bool
	menu     *menu
	dismiss  int // start of the word whose menu the user closed with Esc; -1 when none
	search   *search

	lcache map[string]*lineEntry // laid-out lines for View, see cachedLine

	out   []Event
	dirty bool
}

// NewEditor makes an empty editor.
func NewEditor(o Options) *Editor {
	e := &Editor{opt: o, want: -1, dismiss: -1}
	e.hist = o.History
	if e.hist == nil {
		e.hist = NewHistory()
	}
	e.th = DefaultTheme()
	if o.Theme != nil {
		e.th = *o.Theme
	}
	// What the caller supplies is text too, and gets the same gate: nothing drawn may hold a control character.
	e.opt.Prompt = cell.Text(cleanLine(e.opt.Prompt)).Truncate(16, "").Plain()
	if e.opt.Prompt == "" {
		e.opt.Prompt = "❯ "
	}
	e.opt.Placeholder = cleanLine(e.opt.Placeholder)
	e.th.Marker = cleanLine(e.th.Marker)
	if e.opt.PasteLines <= 0 {
		e.opt.PasteLines = 3
	}
	if e.opt.PasteRunes <= 0 {
		e.opt.PasteRunes = 200
	}
	if e.opt.MaxPasteBytes <= 0 {
		e.opt.MaxPasteBytes = 1 << 20
	}
	return e
}

// History is the history the editor reads and writes.
func (e *Editor) History() *History { return e.hist }

// Text is the buffer as it would be submitted, chips expanded, without trimming.
func (e *Editor) Text() string { return e.expand(e.buf) }

// Display is the buffer as it is drawn: a chip is its label.
func (e *Editor) Display() string {
	var b strings.Builder
	for _, r := range e.buf {
		if isChip(r) {
			b.WriteString(e.chipLabel(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Cursor is the cursor's position as a rune index into the buffer (a chip counts as one rune).
func (e *Editor) Cursor() int { return e.cur }

// Len is the buffer's length in runes.
func (e *Editor) Len() int { return len(e.buf) }

// Empty reports whether the buffer is empty.
func (e *Editor) Empty() bool { return len(e.buf) == 0 }

// EscArmed reports that Esc was pressed once on a non-empty buffer and a second press will clear it; the view shows a hint.
func (e *Editor) EscArmed() bool { return e.escArmed }

// SetText replaces the buffer with s (cleaned like any incoming text) and puts the cursor at its end. Undo history, chips,
// the completion menu, a running search and history navigation are dropped. Use it to load text from outside the keyboard.
func (e *Editor) SetText(s string) {
	e.resetInput()
	e.buf = runesOf(s)
	e.cur = len(e.buf)
	e.dirty = true
}

// Reset empties the editor and forgets undo history, chips, menu, search and navigation. The kill ring and the history stay.
func (e *Editor) Reset() {
	e.resetInput()
	e.dirty = true
}

// resetInput clears the draft, cursor, chips, undo state, history navigation, and transient menus
// while removing chip-backed kills.
func (e *Editor) resetInput() {
	e.buf, e.cur, e.want = nil, 0, -1
	e.chips, e.chipBytes, e.lcache = nil, 0, nil
	e.undo, e.redo = nil, nil
	e.dropChipKills()
	e.hpos, e.draft, e.draftCur = 0, nil, 0
	e.escArmed = false
	e.menu, e.dismiss, e.search = nil, -1, nil
	e.lastKill, e.lastYank, e.lastGrp = false, false, grpNone
}

// emit appends an editor event to the pending output queue.
func (e *Editor) emit(ev Event) { e.out = append(e.out, ev) }

// Handle applies one key and returns what happened, in order. It never fails: a key it does not understand comes back as
// Unhandled, text that is not fit for the buffer is cleaned or dropped, and the buffer and cursor are always left valid.
func (e *Editor) Handle(k Key) []Event {
	e.out = nil
	e.thisKill, e.thisYank, e.thisGrp = false, false, grpNone
	if e.escArmed && !k.Is(Esc, 0) {
		e.escArmed, e.dirty = false, true
	}
	if e.search != nil {
		e.searchKey(k)
	} else {
		e.dispatch(k)
	}
	e.lastKill, e.lastYank, e.lastGrp = e.thisKill, e.thisYank, e.thisGrp
	if !e.vert {
		e.want = -1
	}
	e.vert = false
	if c := snap(e.buf, e.cur); c != e.cur {
		e.cur, e.dirty = c, true
	}
	if e.dirty {
		e.emit(Changed{})
		e.dirty = false
	}
	out := e.out
	e.out = nil
	return out
}

// dispatch handles a key when no search is running.
func (e *Editor) dispatch(k Key) {
	if k.Kind == KindPaste {
		e.closeMenu()
		e.paste(k)
		return
	}
	if e.menu != nil && e.menuKey(k) {
		return
	}
	typed := false
	switch k.Kind {
	case KindRune:
		typed = e.runeKey(k)
	case KindSpecial:
		e.specialKey(k)
	}
	e.afterKey(typed)
}

// unhandled queues a key event for processing by the editor's caller.
func (e *Editor) unhandled(k Key) { e.emit(Unhandled{Key: k}) }

// runeKey handles a character key; it reports whether text was typed.
func (e *Editor) runeKey(k Key) bool {
	r := k.R
	switch m := k.Mod; {
	case m&^Shift == 0:
		return e.typeRune(r)
	case m == Ctrl || m == Ctrl|Shift:
		e.ctrlKey(unicode.ToLower(r), m&Shift != 0, k)
	case m == Alt:
		e.altKey(r, k)
	case m == Ctrl|Alt:
		if r == '_' {
			e.redoCmd()
		} else {
			e.unhandled(k)
		}
	default:
		e.unhandled(k)
	}
	return false
}

func (e *Editor) ctrlKey(r rune, shift bool, k Key) {
	if shift { // only Ctrl+Shift+Z means anything, and only terminals that report modifiers send it
		if r == 'z' {
			e.redoCmd()
		} else {
			e.unhandled(k)
		}
		return
	}
	switch r {
	case 'a':
		e.moveTo(lineStart(e.buf, e.cur))
	case 'e':
		e.moveTo(lineEnd(e.buf, e.cur))
	case 'b':
		e.moveTo(prevBoundary(e.buf, e.cur))
	case 'f':
		e.moveTo(nextBoundary(e.buf, e.cur))
	case 'p':
		e.vertical(-1)
	case 'n':
		e.vertical(1)
	case 'k':
		e.killToEOL()
	case 'u':
		e.killToBOL()
	case 'w':
		e.killWordBack()
	case 'y':
		e.yank()
	case 't':
		e.transpose()
	case 'j':
		e.newline()
	case 'c':
		e.interrupt()
	case 'd':
		if len(e.buf) == 0 {
			e.emit(EOF{})
		} else {
			e.deleteForward()
		}
	case 'l':
		e.emit(Redraw{})
	case 'r':
		e.openSearch()
	case 'z', '_':
		e.undoCmd()
	default: // Ctrl+Space, Ctrl+G, Ctrl+O and the rest are the caller's
		e.unhandled(k)
	}
}

func (e *Editor) altKey(r rune, k Key) {
	switch r {
	case 'b':
		e.moveTo(wordStart(e.buf, e.cur))
	case 'f':
		e.moveTo(wordEnd(e.buf, e.cur))
	case 'd':
		e.killWordForward()
	case 'y':
		e.yankPop()
	case 'z':
		e.redoCmd()
	case '<':
		e.moveTo(0)
	case '>':
		e.moveTo(len(e.buf))
	default:
		e.unhandled(k)
	}
}

func (e *Editor) specialKey(k Key) {
	m := k.Mod
	ctrlAlt := m&(Ctrl|Alt) != 0
	switch k.Code {
	case Enter:
		if m&(Alt|Shift|Ctrl) != 0 {
			e.newline()
		} else {
			e.enter()
		}
	case Tab:
		switch m {
		case 0:
			e.tabComplete()
		default:
			e.unhandled(k)
		}
	case Backspace:
		if ctrlAlt {
			e.killWordBack()
		} else {
			e.backspace()
		}
	case Delete:
		if ctrlAlt {
			e.killWordForward()
		} else {
			e.deleteForward()
		}
	case Left:
		if ctrlAlt {
			e.moveTo(wordStart(e.buf, e.cur))
		} else {
			e.moveTo(prevBoundary(e.buf, e.cur))
		}
	case Right:
		if ctrlAlt {
			e.moveTo(wordEnd(e.buf, e.cur))
		} else {
			e.moveTo(nextBoundary(e.buf, e.cur))
		}
	case Home:
		if m&Ctrl != 0 {
			e.moveTo(0)
		} else {
			e.moveTo(lineStart(e.buf, e.cur))
		}
	case End:
		if m&Ctrl != 0 {
			e.moveTo(len(e.buf))
		} else {
			e.moveTo(lineEnd(e.buf, e.cur))
		}
	case Up, Down:
		if ctrlAlt {
			e.unhandled(k)
			return
		}
		dir := -1
		if k.Code == Down {
			dir = 1
		}
		e.vertical(dir)
	case Esc:
		if m != 0 {
			e.unhandled(k)
		} else {
			e.escape()
		}
	default:
		e.unhandled(k)
	}
}

// escape is the Esc key when no menu or search is open: the first press on a non-empty buffer arms the clear, the second
// clears it; on an empty buffer it is not ours.
func (e *Editor) escape() {
	switch {
	case len(e.buf) == 0:
		e.unhandled(SpecialKey(Esc, 0))
	case e.escArmed:
		e.escArmed = false
		e.clearBuffer()
	default:
		e.escArmed, e.dirty = true, true
	}
}

// interrupt is Ctrl+C.
func (e *Editor) interrupt() {
	e.closeMenu()
	cleared := len(e.buf) > 0
	if cleared {
		e.clearBuffer()
	}
	e.emit(Interrupt{Cleared: cleared})
}

// clearBuffer empties the buffer as an undoable edit (a slip of Ctrl+C is recoverable with Ctrl+_).
func (e *Editor) clearBuffer() {
	if len(e.buf) == 0 {
		return
	}
	e.replace(0, len(e.buf), nil, 0, grpNone)
	e.hpos, e.draft = 0, nil
}

// enter is Enter: submit, or a newline after a trailing backslash.
func (e *Editor) enter() {
	if e.cur > 0 && e.buf[e.cur-1] == '\\' {
		e.replace(e.cur-1, e.cur, []rune{'\n'}, e.cur, grpNone)
		return
	}
	text := strings.TrimRight(e.expand(e.buf), " \t\r\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	_ = e.hist.Add(text) // a history that cannot be written is reported by History.Err; the prompt is still sent
	e.resetInput()
	e.dirty = true
	e.emit(Submit{Text: text})
}

// expand is s with every chip replaced by the text it stands for.
func (e *Editor) expand(rs []rune) string {
	var b strings.Builder
	for _, r := range rs {
		if isChip(r) {
			if id := chipID(r); id < len(e.chips) {
				b.WriteString(e.chips[id].text)
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// colOf is the display column of index i within its line, in cells.
func (e *Editor) colOf(i int) int {
	col := 0
	for p := lineStart(e.buf, i); p < i; p++ {
		col = advance(col, e.buf[p], e.chipWidth(e.buf[p]))
	}
	return col
}

// chipWidth returns a chip label's terminal-cell width or zero for ordinary runes.
func (e *Editor) chipWidth(r rune) int {
	if !isChip(r) {
		return 0
	}
	return cell.StringWidth(e.chipLabel(r))
}
