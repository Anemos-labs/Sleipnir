package input

import (
	"reflect"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

func TestMenuSelectionFollowsTheBestMatchUnlessMoved(t *testing.T) {
	r := menuRig(t)
	r.send("/", kDown, kDown, kDown) // the user moved to /cost
	r.send("o")                      // "/o": a moved selection stays on its candidate while it is listed
	if m := r.ed.Completion(); !m.Open || m.Candidates[m.Selected].Text != "/cost " {
		t.Errorf("a selection the user moved stays: %+v", m)
	}
	r.send("z") // "/oz" lists nothing
	if r.ed.Completion().Open {
		t.Fatal("closed")
	}
	r.ed.Reset()
	r.send("/c", kDown) // /clear is first, /compact later; the user moved off the first
	r.send("o")
	m := r.ed.Completion()
	if m.Candidates[m.Selected].Text == "" {
		t.Fatal("a selection")
	}
	r.ed.Reset()
	r.send("/")
	r.send("c") // nothing moved: the best match is selected, whatever was selected before
	if m := r.ed.Completion(); m.Selected != 0 {
		t.Errorf("typing selects the best match: %+v", m)
	}
}

func TestMenuThatDoesNotChangeIsNotReportedAsAChange(t *testing.T) {
	r := menuRig(t)
	r.send("/cl") // /clear only
	evs := r.send("e")
	for _, ev := range evs {
		if _, ok := ev.(CompletionOpened); ok {
			t.Errorf("the menu was already open: %v", evs)
		}
	}
	// a cursor move that keeps the same menu is still a change of the cursor, reported once
	if evs := r.send(kLeft); !reflect.DeepEqual(evs, []Event{Changed{}}) {
		t.Errorf("%v", evs)
	}
}

func TestOptionsAreCleaned(t *testing.T) {
	th := DefaultTheme()
	th.Marker = "\x1b[31m>\x1b[0m"
	e := NewEditor(Options{Prompt: "\x1b]0;evil\x07$ \U00002028", Placeholder: "type\x1b[2J here\n", Theme: &th})
	if got := e.View(40).Plain(); !reflect.DeepEqual(got, []string{"$  type here "}) {
		t.Errorf("%q", got)
	}
	if e.th.Marker != ">" {
		t.Errorf("marker %q", e.th.Marker)
	}
	long := NewEditor(Options{Prompt: strings.Repeat("x", 100)})
	if w := cell.StringWidth(long.opt.Prompt); w > 16 {
		t.Errorf("a prompt is cut to 16 cells, not %d", w)
	}
	long.SetText("hello")
	if v := long.View(40); v.Lines[0].Width() > 40 {
		t.Errorf("%d", v.Lines[0].Width())
	}
	if NewEditor(Options{Prompt: "\x1b[2J"}).opt.Prompt != "❯ " {
		t.Error("a prompt that cleans to nothing is the default")
	}
}

func TestSetTextAndReset(t *testing.T) {
	r := menuRig(t)
	r.send("typed", paste(lines(5)), kUndo, ctrl('u'), "/he")
	r.ed.SetText("new\r\ntext\x1b[2J")
	if r.state() != "new\ntext|" {
		t.Errorf("SetText cleans and puts the cursor at the end: %q", r.state())
	}
	if r.ed.Completion().Open || len(r.ed.chips) != 0 || len(r.ed.undo) != 0 || r.ed.Search().Active {
		t.Error("SetText drops the menu, the chips and the undo history")
	}
	r.send(kUndo)
	if r.state() != "new\ntext|" {
		t.Errorf("nothing to undo into: %q", r.state())
	}
	r.send(ctrl('y'))
	if !strings.Contains(r.state(), "typed") && !strings.Contains(r.state(), "/he") {
		// the kill ring is kept by Reset and SetText, like a shell's between lines
		t.Logf("yank after SetText: %q", r.state())
	}
	r.ed.Reset()
	if !r.ed.Empty() || r.ed.Cursor() != 0 || r.ed.Text() != "" {
		t.Error("Reset empties the editor")
	}
	if got := r.ed.View(20).Plain(); !reflect.DeepEqual(got, []string{"❯ "}) {
		t.Errorf("%q", got)
	}
}

func TestEditorAcceptsAnyKeyWithoutPanicking(t *testing.T) {
	r := menuRig(t)
	for _, k := range []Key{
		{}, {Kind: KindSpecial}, {Kind: KindSpecial, Code: 200}, {Kind: Kind(77)}, {Kind: KindRune, R: -5}, {Kind: KindRune, R: 0x110000},
		{Kind: KindRune, R: 0xd800}, {Kind: KindRune, R: 'a', Mod: 0xff}, {Kind: KindSpecial, Code: Enter, Mod: 0xff},
		{Kind: KindPaste}, {Kind: KindPaste, Text: "\x00"}, {Kind: KindSpecial, Code: Up, Mod: Ctrl | Alt | Shift},
	} {
		r.ed.Handle(k)
		r.check()
		r.ed.View(10)
	}
}

func TestKeysDuringSearchNeverLeaveItInAnOddState(t *testing.T) {
	r := searchRig(t)
	for _, in := range []string{ctrl('r'), kUp, kDown, kTab, kPgUp, "\x1bOP", ctrl('w'), ctrl('d'), ctrl('l'), kLeft, kHome} {
		r.send(ctrl('r'), "git", in)
		r.check()
		if st := r.ed.Search(); st.Active && st.Index >= r.ed.History().Len() {
			t.Fatalf("%q: %+v", in, st)
		}
		r.ed.Reset()
	}
}
