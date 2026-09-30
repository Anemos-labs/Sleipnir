package input

import (
	"reflect"
	"strings"
	"testing"
)

func searchRig(t *testing.T) *rig {
	h := NewHistory()
	for _, s := range []string{"git status", "go test ./...", "git commit -m x", "ls -la", "echo Hello"} {
		h.Add(s)
	}
	return newRig(t, Options{History: h})
}

func TestReverseSearch(t *testing.T) {
	r := searchRig(t)
	evs := r.send(ctrl('r'))
	if !reflect.DeepEqual(evs, []Event{SearchOpened{}, Changed{}}) {
		t.Errorf("opening: %v", evs)
	}
	if st := r.ed.Search(); !st.Active || st.Query != "" || st.Match != "" || st.Index != -1 {
		t.Errorf("fresh search: %+v", st)
	}
	r.send("git")
	if st := r.ed.Search(); st.Query != "git" || st.Match != "git commit -m x" || st.Index != 2 || st.Failing {
		t.Errorf("newest match: %+v", st)
	}
	r.send(ctrl('r'))
	if st := r.ed.Search(); st.Match != "git status" || st.Index != 0 || st.Failing {
		t.Errorf("next older: %+v", st)
	}
	r.send(ctrl('r'))
	if st := r.ed.Search(); st.Match != "git status" || !st.Failing {
		t.Errorf("no older match keeps the last and says so: %+v", st)
	}
	if r.state() != "|" {
		t.Errorf("the buffer is untouched while searching: %q", r.state())
	}
	evs = r.send(kEnter)
	if r.state() != "git status|" {
		t.Errorf("Enter accepts into the buffer: %q", r.state())
	}
	if !reflect.DeepEqual(evs, []Event{SearchClosed{}, Changed{}}) {
		t.Errorf("accept events (Enter accepts, it does not submit): %v", evs)
	}
	if r.ed.Search().Active {
		t.Error("the search is over")
	}
}

func TestSearchTypingContinuesFromTheCurrentMatch(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), "g")
	if st := r.ed.Search(); st.Match != "git commit -m x" {
		t.Fatalf("g: %+v", st)
	}
	r.send("o") // "go": git commit does not contain it, so the search moves on to an older entry
	if st := r.ed.Search(); st.Match != "go test ./..." || st.Failing {
		t.Fatalf("go: %+v", st)
	}
	r.send("z") // "goz": nothing
	if st := r.ed.Search(); !st.Failing || st.Match != "go test ./..." {
		t.Fatalf("goz: %+v", st)
	}
	r.send(kBS) // back to "go": searches again from the newest
	if st := r.ed.Search(); st.Query != "go" || st.Match != "go test ./..." || st.Failing {
		t.Fatalf("backspace: %+v", st)
	}
	r.send(kBS, kBS)
	if st := r.ed.Search(); st.Query != "" || st.Match != "" || st.Index != -1 {
		t.Fatalf("an empty query matches nothing: %+v", st)
	}
	r.send(kBS) // Backspace on an empty query does nothing
	if !r.ed.Search().Active {
		t.Error("still searching")
	}
}

func TestSearchNoMatchAtAll(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), "qqq")
	if st := r.ed.Search(); !st.Failing || st.Match != "" || st.Index != -1 {
		t.Errorf("%+v", st)
	}
	r.send(kEnter) // accepting nothing changes nothing
	if r.state() != "|" || r.ed.Search().Active {
		t.Errorf("%q", r.state())
	}
}

func TestSearchSmartCase(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), "hello")
	if st := r.ed.Search(); st.Match != "echo Hello" {
		t.Errorf("lower-case query ignores case: %+v", st)
	}
	r.send(kEsc, ctrl('r'), "Hello")
	if st := r.ed.Search(); st.Match != "echo Hello" {
		t.Errorf("%+v", st)
	}
	r.send(kEsc, ctrl('r'), "HELLO")
	if st := r.ed.Search(); !st.Failing {
		t.Errorf("an upper-case letter makes the search case-sensitive, so HELLO finds nothing: %+v", st)
	}
}

func TestSearchWithEmptyQueryWalksBackThroughHistory(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), ctrl('r'))
	if st := r.ed.Search(); st.Match != "echo Hello" {
		t.Errorf("first Ctrl+R with no query: newest %+v", st)
	}
	r.send(ctrl('r'))
	if st := r.ed.Search(); st.Match != "ls -la" {
		t.Errorf("%+v", st)
	}
}

func TestSearchCancel(t *testing.T) {
	for _, cancel := range []struct{ name, key string }{{"Esc", kEsc}, {"Ctrl+G", ctrl('g')}, {"Ctrl+C", ctrl('c')}} {
		t.Run(cancel.name, func(t *testing.T) {
			r := searchRig(t)
			r.send("draft text", ctrl('r'), "git")
			evs := withoutChanged(r.send(cancel.key))
			if !reflect.DeepEqual(evs, []Event{SearchClosed{}}) {
				t.Errorf("events: %v (cancelling the search is not an Interrupt)", evs)
			}
			if r.state() != "draft text|" || r.ed.Search().Active {
				t.Errorf("cancel leaves the buffer as it was: %q", r.state())
			}
		})
	}
}

func TestSearchOtherKeyAcceptsThenActs(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), "commit", kLeft)
	if r.state() != "git commit -m |x" {
		t.Errorf("Left accepts the match and moves: %q", r.state())
	}
	r = searchRig(t)
	r.send(ctrl('r'), "ls", ctrl('a'), "X")
	if r.state() != "X|ls -la" {
		t.Errorf("%q", r.state())
	}
	r = searchRig(t)
	r.send("typed", ctrl('r'), "echo", kAltEnter, "more")
	if r.state() != "echo Hello\nmore|" {
		t.Errorf("Alt+Enter accepts and then inserts a newline: %q", r.state())
	}
}

func TestSearchAcceptIsUndoable(t *testing.T) {
	r := searchRig(t)
	r.send("typed", ctrl('r'), "ls", kEnter)
	if r.state() != "ls -la|" {
		t.Fatalf("%q", r.state())
	}
	r.send(kUndo)
	if r.state() != "typed|" {
		t.Errorf("undo brings back what was there before the search: %q", r.state())
	}
}

func TestSearchPasteEndsTheSearch(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), "ls", paste("!!"))
	if r.state() != "ls -la!!|" {
		t.Errorf("%q", r.state())
	}
}

func TestSearchView(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), "commit")
	v := r.ed.View(40)
	plain := v.Plain()
	if len(plain) != 2 || plain[0] != "❯ git commit -m x" || plain[1] != "reverse search: commit" {
		t.Fatalf("%q", plain)
	}
	if v.CursorRow != 1 || v.CursorCol != len("reverse search: commit") || v.InputRows != 1 {
		t.Errorf("the cursor sits after the query: %+v", v)
	}
	// the found text is highlighted
	var hl string
	for _, sp := range v.Lines[0] {
		if sp.Style == r.ed.th.Selected {
			hl += sp.Text
		}
	}
	if hl != "commit" {
		t.Errorf("highlighted %q", hl)
	}
	r.send("zz")
	if got := r.ed.View(40).Plain()[1]; !strings.HasSuffix(got, "no older match") {
		t.Errorf("failing search says so: %q", got)
	}
	r.send(kEsc, ctrl('r'), "zzz")
	v = r.ed.View(40)
	if got := v.Plain(); got[0] != "❯" && strings.TrimSpace(got[0]) != "❯" || !strings.HasSuffix(got[1], "no match") {
		t.Errorf("nothing found: %q", got)
	}
	// narrow view: the search line is cut to the width, the cursor stays inside it
	v = r.ed.View(10)
	if v.Lines[1].Width() > 10 || v.CursorCol >= 10 {
		t.Errorf("narrow: %q cursor %d", v.Plain(), v.CursorCol)
	}
}
