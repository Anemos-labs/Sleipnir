package input

// Incremental reverse search through the history (Ctrl+R).
//
// While it runs the buffer is not touched: the view shows the current match in its place and a line with the query. Typing
// adds to the query and finds the newest entry that contains it, starting from the current match, so a match that still fits
// stays; Backspace shortens the query and searches again from the newest entry; Ctrl+R finds the next older match. Enter puts
// the match into the buffer (it does not send it: one more Enter does), Esc, Ctrl+G and Ctrl+C leave the buffer as it was, and
// any other key accepts the match and then does its normal job, as in readline. A query is matched as a substring, ignoring
// case unless the query has an upper-case letter.

// maxQuery is the longest search query, in runes.
const maxQuery = 256

type search struct {
	query   []rune
	idx     int  // History index of the current match, -1 when there is none yet
	failing bool // the query matches nothing (more) in the direction searched; the last match stays shown
}

// SearchState is a snapshot of the reverse search for the view.
type SearchState struct {
	Active  bool
	Query   string
	Match   string // the current match (empty when there is none)
	Index   int    // the match's index in the History, -1 when there is none
	Failing bool   // the last search found nothing: Match is the previous one, if any
}

// Search reports the reverse search.
func (e *Editor) Search() SearchState {
	s := e.search
	if s == nil {
		return SearchState{Index: -1}
	}
	st := SearchState{Active: true, Query: string(s.query), Index: s.idx, Failing: s.failing}
	if s.idx >= 0 {
		st.Match = e.hist.At(s.idx)
	}
	return st
}

func (e *Editor) openSearch() {
	e.closeMenu()
	e.search = &search{idx: -1}
	e.dirty = true
	e.emit(SearchOpened{})
}

func (e *Editor) closeSearch() {
	e.search = nil
	e.dirty = true
	e.emit(SearchClosed{})
}

// find looks for the newest match at or before from, leaving the current one (and marking the search failing) if there is none.
func (e *Editor) find(from int) {
	s := e.search
	if from < 0 {
		s.failing = true
		return
	}
	if i := e.hist.Find(string(s.query), from); i >= 0 {
		s.idx, s.failing = i, false
		return
	}
	s.failing = true
}

func (e *Editor) searchKey(k Key) {
	s := e.search
	newest := e.hist.Len() - 1
	switch {
	case k.Kind == KindPaste:
		e.acceptSearch()
		e.dispatch(k)
	case k.Is(Esc, 0), k.IsRune('g', Ctrl), k.IsRune('c', Ctrl):
		e.closeSearch()
	case k.Is(Enter, 0):
		e.acceptSearch()
	case k.IsRune('r', Ctrl):
		from := newest
		if s.idx >= 0 {
			from = s.idx - 1
		}
		e.find(from)
		e.dirty = true
	case k.Is(Backspace, 0):
		if len(s.query) > 0 {
			s.query = s.query[:len(s.query)-1]
		}
		s.idx, s.failing = -1, false
		if len(s.query) > 0 {
			e.find(newest)
		}
		e.dirty = true
	case k.Kind == KindRune && k.Mod&^Shift == 0:
		add := []rune(cleanLine(string(k.R)))
		if len(add) == 0 || len(s.query)+len(add) > maxQuery {
			return
		}
		s.query = append(s.query, add...)
		from := newest
		if s.idx >= 0 {
			from = s.idx
		}
		e.find(from)
		e.dirty = true
	default:
		e.acceptSearch()
		e.dispatch(k)
	}
}

// acceptSearch ends the search and loads its match into the buffer (as one undoable edit, so the text that was there can
// be brought back with Ctrl+_).
func (e *Editor) acceptSearch() {
	s := e.search
	e.closeSearch()
	if s == nil || s.idx < 0 {
		return
	}
	rs := runesOf(e.hist.At(s.idx))
	e.replace(0, len(e.buf), rs, len(rs), grpNone)
	e.hpos, e.draft = 0, nil
}
