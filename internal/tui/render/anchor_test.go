package render

import (
	"fmt"
	"reflect"
	"testing"
)

// The chat anchors its live region to the bottom of the window: the last row of the region is the last row of the terminal, whatever
// the region is showing, so the footer never moves. What is printed fills the gap above it, and scrolls when the gap is full.

func rowsOf(h *harness) []string { return h.v.Rows() }

func TestAnchoredTheLiveRegionIsAtTheBottomWhatEverItsHeight(t *testing.T) {
	h := newHarness(t, termCaps(20, 8), WithBottomAnchor())
	h.r.Print(txt("banner"))
	h.r.SetLive(txts("status", "input", "footer"))
	h.flush()
	want := []string{"banner", "", "", "", "", "status", "input", "footer"}
	if got := rowsOf(h); !reflect.DeepEqual(got, want) {
		t.Fatalf("at the start:\n got  %q\n want %q", got, want)
	}
	// the region grows: the footer stays where it is
	h.r.SetLive(txts("tool a", "tool b", "status", "input", "footer"))
	h.flush()
	want = []string{"banner", "", "", "tool a", "tool b", "status", "input", "footer"}
	if got := rowsOf(h); !reflect.DeepEqual(got, want) {
		t.Fatalf("after growing:\n got  %q\n want %q", got, want)
	}
	// and shrinks
	h.r.SetLive(txts("input", "footer"))
	h.flush()
	want = []string{"banner", "", "", "", "", "", "input", "footer"}
	if got := rowsOf(h); !reflect.DeepEqual(got, want) {
		t.Fatalf("after shrinking:\n got  %q\n want %q", got, want)
	}
}

func TestAnchoredWhatIsPrintedFillsTheGapAndThenScrolls(t *testing.T) {
	h := newHarness(t, termCaps(20, 8), WithBottomAnchor())
	h.r.SetLive(txts("input", "footer"))
	h.flush()
	h.r.Print(txt("one"), txt("two"))
	h.flush()
	want := []string{"one", "two", "", "", "", "", "input", "footer"}
	if got := rowsOf(h); !reflect.DeepEqual(got, want) {
		t.Fatalf("two lines:\n got  %q\n want %q", got, want)
	}
	for i := 0; i < 20; i++ {
		h.r.Print(txt(fmt.Sprintf("line %d", i)))
		h.flush()
		if got := rowsOf(h); got[6] != "input" || got[7] != "footer" {
			t.Fatalf("the footer moved after line %d: %q", i, got)
		}
	}
	got := rowsOf(h)
	if got[5] != "line 19" || got[0] != "line 14" {
		t.Errorf("the newest lines are right above the prompt:\n%q", got)
	}
	if len(h.v.Scrollback()) == 0 {
		t.Error("what scrolled off is in the terminal's scrollback")
	}
	// the history is whole, in order
	all := h.all()
	if all[0] == "" {
		t.Errorf("history starts with a blank: %q", all[:3])
	}
}

func TestAnchoredAChangeOfOneRowWritesLittleAndAnUnchangedFrameNothing(t *testing.T) {
	h := newHarness(t, termCaps(20, 8), WithBottomAnchor())
	h.r.SetLive(txts("status", "input", "footer"))
	h.flush()
	n := h.written()
	h.r.SetLive(txts("status*", "input", "footer"))
	h.flush()
	if d := h.written() - n; d == 0 || d > 40 {
		t.Errorf("one changed row wrote %d bytes", d)
	}
	n = h.written()
	h.r.SetLive(txts("status*", "input", "footer"))
	h.flush()
	if d := h.written() - n; d != 0 {
		t.Errorf("an unchanged frame wrote %d bytes", d)
	}
}

func TestAnchoredAResizeRepaintsFromWhatWasPrintedAndKeepsTheFooterAtTheBottom(t *testing.T) {
	h := newHarness(t, termCaps(20, 8), WithBottomAnchor())
	h.r.SetLive(txts("input", "footer"))
	h.r.Print(txt("one"), txt("two"), txt("three"))
	h.flush()
	h.resize(30, 12)
	h.r.SetLive(txts("input", "footer"))
	h.flush()
	got := rowsOf(h)
	if len(got) != 12 || got[10] != "input" || got[11] != "footer" {
		t.Fatalf("after growing: %q", got)
	}
	if got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Errorf("what was printed is repainted: %q", got)
	}
	h.resize(15, 5)
	h.r.SetLive(txts("input", "footer"))
	h.flush()
	got = rowsOf(h)
	if len(got) != 5 || got[3] != "input" || got[4] != "footer" || got[2] != "three" {
		t.Errorf("after shrinking: %q", got)
	}
}

func TestAnchoredClosingLeavesTheCursorBelowTheContent(t *testing.T) {
	h := newHarness(t, termCaps(20, 8), WithBottomAnchor())
	h.r.SetLive(txts("input", "footer"))
	h.r.Print(txt("one"), txt("two"))
	h.flush()
	if err := h.r.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rowsOf(h); got[0] != "one" || got[1] != "two" || got[6] != "" || got[7] != "" {
		t.Errorf("the region is erased and the content stays: %q", got)
	}
	if _, y, _ := h.v.Cursor(); y != 2 {
		t.Errorf("the cursor is on the first row below the content, got row %d", y)
	}
}

// With the row the cursor is on known, what the terminal showed above it stays, and the content starts under it.
func TestAnchoredWithAStartRowKeepsWhatTheTerminalShowed(t *testing.T) {
	caps := termCaps(20, 8)
	h := newHarness(t, caps, WithBottomAnchor(), WithStartRow(2))
	h.v.WriteString("old\r\n$ sleipnir\r\n") // what the shell left, the cursor on the fresh line under it
	h.r.Print(txt("banner"))
	h.r.SetLive(txts("input", "footer"))
	h.flush()
	want := []string{"old", "$ sleipnir", "banner", "", "", "", "input", "footer"}
	if got := rowsOf(h); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	for i := 0; i < 8; i++ {
		h.r.Print(txt(fmt.Sprintf("line %d", i)))
		h.flush()
	}
	got := rowsOf(h)
	if got[6] != "input" || got[7] != "footer" || got[5] != "line 7" {
		t.Errorf("after printing past the bottom: %q", got)
	}
}
