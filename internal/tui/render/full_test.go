package render

import (
	"reflect"
	"testing"
)

// The chat can show a page of the whole screen (the cockpit of a team): SetFull puts it on the terminal's alternate screen, redraws only
// the rows that change, and SetFull(nil) gives the terminal back as it was, the scrollback and the live region of the chat untouched.
func TestAFullScreenPageIsOnTheAlternateScreenAndTheChatComesBack(t *testing.T) {
	h := newHarness(t, termCaps(20, 6))
	h.r.Print(txt("history"))
	h.r.SetLive(txts("status", "input"))
	h.r.SetCursor(1, 2)
	h.flush()
	before := h.all()

	h.r.SetFull(txts("COCKPIT", "agents", "board", "", "", "keys"))
	h.r.SetCursor(5, 0)
	h.flush()
	if !h.v.AltScreen() {
		t.Fatal("a full-screen page is drawn on the alternate screen")
	}
	if got, want := h.v.Rows(), []string{"COCKPIT", "agents", "board", "", "", "keys"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the page shows %q, want %q", got, want)
	}

	// a frame that changes one row writes little, and nothing at all when nothing changes
	n := h.written()
	h.r.SetFull(txts("COCKPIT", "agents*", "board", "", "", "keys"))
	h.r.SetCursor(5, 0)
	h.flush()
	if d := h.written() - n; d == 0 || d > 60 {
		t.Errorf("a changed row wrote %d bytes", d)
	}
	n = h.written()
	h.r.SetFull(txts("COCKPIT", "agents*", "board", "", "", "keys"))
	h.r.SetCursor(5, 0)
	h.flush()
	if d := h.written() - n; d != 0 {
		t.Errorf("an unchanged page wrote %d bytes", d)
	}

	// what is printed meanwhile waits for the chat
	h.r.Print(txt("said while away"))
	h.flush()
	if h.v.Rows()[0] != "COCKPIT" {
		t.Error("a line printed while the page is up must not be written into it")
	}

	h.r.SetFull(nil)
	h.flush()
	if h.v.AltScreen() {
		t.Fatal("the alternate screen is left when the page is")
	}
	h.wantAll("history", "said while away", "status", "input")
	if len(before) != 3 {
		t.Fatalf("setup: %q", before)
	}
}

// A chat that ends with the page up gives the terminal back too.
func TestClosingWithAFullScreenPageUpLeavesTheAlternateScreen(t *testing.T) {
	h := newHarness(t, termCaps(20, 6))
	h.r.SetLive(txts("input"))
	h.flush()
	h.r.SetFull(txts("COCKPIT"))
	h.flush()
	if err := h.r.Close(); err != nil {
		t.Fatal(err)
	}
	if h.v.AltScreen() {
		t.Error("Close leaves the alternate screen")
	}
}

// A window resized while the page is up gets the page again, whole, at its new size.
func TestAFullScreenPageFollowsTheSizeOfTheWindow(t *testing.T) {
	h := newHarness(t, termCaps(20, 6))
	h.r.SetFull(txts("top", "a", "b", "c", "d", "bottom"))
	h.flush()
	h.resize(30, 8)
	h.r.SetFull(txts("TOP", "a", "b", "c", "d", "e", "f", "BOTTOM"))
	h.flush()
	rows := h.v.Rows()
	if len(rows) != 8 || rows[0] != "TOP" || rows[7] != "BOTTOM" {
		t.Errorf("after growing: %q", rows)
	}
	h.resize(20, 5)
	h.r.SetFull(txts("T", "b", "c", "d", "B"))
	h.flush()
	rows = h.v.Rows()
	if len(rows) != 5 || rows[0] != "T" || rows[4] != "B" {
		t.Errorf("after shrinking: %q", rows)
	}
}
