package render

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

func TestPrintAppendsToTheScrollbackAndLeavesTheCursorOnAFreshLine(t *testing.T) {
	h := newHarness(t, termCaps(20, 5))
	h.r.Print(txt("one"), txt("two"))
	if h.written() != 0 {
		t.Fatal("Print must not write: frames are written by Flush")
	}
	h.flush()
	h.wantAll("one", "two")
	if x, y, _ := h.v.Cursor(); x != 0 || y != 2 {
		t.Errorf("cursor (%d,%d), want the start of the next row", x, y)
	}
	for i := 0; i < 10; i++ {
		h.r.Print(txt(fmt.Sprintf("line %d", i)))
	}
	h.flush()
	want := []string{"one", "two"}
	for i := 0; i < 10; i++ {
		want = append(want, fmt.Sprintf("line %d", i))
	}
	h.wantAll(want...)
	if len(h.v.Scrollback()) == 0 {
		t.Error("what scrolled off the screen is in the terminal's scrollback")
	}
}

func TestLiveRegionIsBelowWhatWasPrinted(t *testing.T) {
	h := newHarness(t, termCaps(30, 8))
	h.r.Print(txt("history"))
	h.r.SetLive(txts("status", "input"))
	h.flush()
	h.wantAll("history", "status", "input")
	h.r.Print(txt("more history"))
	h.flush()
	h.wantAll("history", "more history", "status", "input")
	if h.r.closed {
		t.Fatal("closed")
	}
}

func TestAnUnchangedFrameWritesNothing(t *testing.T) {
	for _, sync := range []bool{false, true} {
		c := termCaps(30, 8)
		c.SyncOutput = sync
		h := newHarness(t, c)
		h.r.Print(txt("history"))
		live := []cell.Line{cell.Styled(cell.Style{}.Fg(cell.ANSI(2)), "status"), txt("input")}
		h.r.SetLive(live)
		h.r.SetCursor(1, 3)
		h.flush()
		before := h.written()
		for i := 0; i < 5; i++ {
			h.r.SetLive(live)
			h.r.SetLive(append([]cell.Line(nil), live...)) // a copy with the same content
			h.r.SetCursor(1, 3)
			h.flush()
			h.flush()
		}
		if got := h.written() - before; got != 0 {
			t.Errorf("sync=%v: %d bytes written for frames that change nothing: %q", sync, got, h.b.frames[len(h.b.frames)-1])
		}
		h.wantAll("history", "status", "input")
	}
}

func TestAnEmptyFlushWritesNothing(t *testing.T) {
	h := newHarness(t, termCaps(30, 8))
	h.flush()
	h.r.SetLive(nil)
	h.r.Print()
	h.flush()
	if h.written() != 0 {
		t.Errorf("%d bytes for nothing", h.written())
	}
}

func TestOneChangedLineWritesOnlyThatLine(t *testing.T) {
	h := newHarness(t, termCaps(40, 10))
	h.r.Print(txts("a", "b", "c")...)
	lines := func(tick int) []cell.Line {
		return []cell.Line{
			cell.Styled(cell.Style{}.Fg(cell.ANSI(2)).With(cell.Bold), "status: working on the thing"),
			txt(fmt.Sprintf("tick %03d", tick)),
			cell.Join(cell.Styled(cell.Style{}.Fg(cell.RGB(122, 162, 247)), "> "), txt("input here")),
		}
	}
	h.r.SetLive(lines(0))
	h.r.SetCursor(2, 4)
	h.flush()
	first := h.written()
	for i := 1; i <= 30; i++ {
		before := h.written()
		h.r.SetLive(lines(i))
		h.flush()
		n := h.written() - before
		if n == 0 || n > 48 {
			t.Fatalf("update %d wrote %d bytes: %q", i, n, h.b.frames[len(h.b.frames)-1])
		}
		f := string(h.b.frames[len(h.b.frames)-1])
		if strings.Contains(f, "status") || strings.Contains(f, "input here") {
			t.Fatalf("update %d rewrote a line that did not change: %q", i, f)
		}
		h.wantAll("a", "b", "c", "status: working on the thing", fmt.Sprintf("tick %03d", i), "> input here")
		if x, y, vis := h.v.Cursor(); !vis || x != 4 || y != 5 {
			t.Fatalf("the cursor must come back to the input: (%d,%d) visible %v", x, y, vis)
		}
	}
	if first < 100 {
		t.Errorf("the first frame draws everything and should be much larger than an update, got %d", first)
	}
}

func TestRowsShrinkAndGrowInPlace(t *testing.T) {
	h := newHarness(t, termCaps(20, 8))
	h.r.Print(txt("top"))
	states := [][]string{{"a"}, {"a", "b", "c", "d"}, {"a", "x"}, {}, {"p", "q", "r"}, {}, {"z"}}
	for _, s := range states {
		h.r.SetLive(txts(s...))
		h.flush()
		h.wantAll(append([]string{"top"}, s...)...)
		if x, y, _ := h.v.Cursor(); len(s) == 0 && (x != 0 || y != 1) {
			t.Errorf("an empty live region leaves the cursor on the fresh line below the history: (%d,%d)", x, y)
		}
	}
}

func TestRegionAtTheBottomOfAFullScreenGrowsAndShrinks(t *testing.T) {
	h := newHarness(t, termCaps(20, 6))
	for i := 0; i < 10; i++ {
		h.r.Print(txt(fmt.Sprintf("h%d", i)))
	}
	h.r.SetLive(txts("s1"))
	h.flush()
	h.r.SetLive(txts("s1", "s2", "s3", "s4"))
	h.flush()
	all := []string{"h0", "h1", "h2", "h3", "h4", "h5", "h6", "h7", "h8", "h9"}
	h.wantAll(append(all, "s1", "s2", "s3", "s4")...)
	h.r.SetLive(txts("s1"))
	h.flush()
	h.wantAll(append(all, "s1")...)
}

func TestSetCursor(t *testing.T) {
	h := newHarness(t, termCaps(20, 6))
	h.r.SetLive(txts("one", "two", "three"))
	h.flush()
	if _, _, vis := h.v.Cursor(); vis {
		t.Error("the cursor starts hidden: a region with no input has nothing to edit")
	}
	h.r.SetCursor(1, 2)
	h.flush()
	if x, y, vis := h.v.Cursor(); !vis || x != 2 || y != 1 {
		t.Errorf("cursor (%d,%d) visible %v", x, y, vis)
	}
	// Moving it is one short move, with no hiding and showing.
	before := h.written()
	h.r.SetCursor(2, 4)
	h.flush()
	if x, y, vis := h.v.Cursor(); !vis || x != 4 || y != 2 {
		t.Errorf("cursor (%d,%d) visible %v", x, y, vis)
	}
	if f := string(h.b.frames[len(h.b.frames)-1]); strings.Contains(f, "?25") || h.written()-before > 12 {
		t.Errorf("a cursor move is a move and nothing else: %q", f)
	}
	// Beyond the region and the terminal: the last row, the last column.
	h.r.SetCursor(99, 99)
	h.flush()
	if x, y, _ := h.v.Cursor(); x != 19 || y != 2 {
		t.Errorf("clamped cursor (%d,%d)", x, y)
	}
	// Hidden again.
	h.r.SetCursor(-1, 0)
	h.flush()
	if _, _, vis := h.v.Cursor(); vis {
		t.Error("a negative row hides the cursor")
	}
	h.r.SetLive(txts("one", "changed", "three"))
	h.flush()
	if _, _, vis := h.v.Cursor(); vis {
		t.Error("a redraw must not show a cursor that is meant to be hidden")
	}
}

func TestTheCursorIsHiddenWhileRowsAreWrittenAndShownAtTheCaret(t *testing.T) {
	h := newHarness(t, termCaps(20, 6))
	h.r.SetLive(txts("one", "two"))
	h.r.SetCursor(1, 1)
	h.flush()
	h.r.SetLive(txts("one", "TWO"))
	h.flush()
	f := string(h.b.frames[len(h.b.frames)-1])
	hide, show, text := strings.Index(f, hideCursor), strings.Index(f, showCursor), strings.Index(f, "TWO")
	if hide != 0 || show < text || text < hide {
		t.Errorf("hide, draw, show expected: %q", f)
	}
}

func TestRegionTallerThanTheScreenIsCutFromTheTop(t *testing.T) {
	h := newHarness(t, termCaps(20, 4))
	h.r.Print(txt("history"))
	h.r.SetLive(txts("1", "2", "3", "4", "5", "6"))
	h.r.SetCursor(5, 0)
	h.flush()
	h.wantAll("history", "3", "4", "5", "6")
	if got := h.v.Rows(); fmt.Sprint(got) != "[3 4 5 6]" {
		t.Errorf("the screen shows the bottom of the region: %q", got)
	}
	if x, y, vis := h.v.Cursor(); !vis || y != 3 || x != 0 {
		t.Errorf("the cursor follows its row: (%d,%d) %v", x, y, vis)
	}
	h.r.SetCursor(0, 0) // its row is cut away
	h.flush()
	if _, _, vis := h.v.Cursor(); vis {
		t.Error("a cursor whose row is not shown is hidden")
	}
	h.r.SetLive(txts("1", "2", "3", "4", "5", "7"))
	h.flush()
	h.wantAll("history", "3", "4", "5", "7")
}

func TestLongLinesAreWrappedByTheRendererAndCounted(t *testing.T) {
	h := newHarness(t, termCaps(10, 8))
	h.r.Print(txt("the quick brown fox jumps over the lazy dog"))
	h.r.SetLive([]cell.Line{txt("live line that is also too long for the terminal"), txt("ok")})
	h.flush()
	h.wantAll("the quick", "brown fox", "jumps over", "the lazy", "dog",
		"live line", "that is", "also too", "long for", "the", "terminal", "ok")
	// The live region is 7 rows; a one-row change is a one-row write.
	before := h.written()
	h.r.SetLive([]cell.Line{txt("live line that is also too long for the terminal"), txt("OK")})
	h.flush()
	if n := h.written() - before; n > 24 {
		t.Errorf("one changed row of seven wrote %d bytes", n)
	}
}

func TestIndentationSurvivesWrapping(t *testing.T) {
	h := newHarness(t, termCaps(14, 8))
	h.r.Print(txt("    foo(bar, baz, qux, quux, corge)"), txt("  - a list item that wraps onto more rows"))
	h.flush()
	h.wantAll("    foo(bar,", "    baz, qux,", "    quux,", "    corge)", "  - a list", "  item that", "  wraps onto", "  more rows")
}

func TestTabsBecomeSpaces(t *testing.T) {
	h := newHarness(t, termCaps(30, 4))
	h.r.Print(txt("a\tb"), txt("12345678\tc"), cell.Join(txt("ab"), txt("\tc")))
	h.flush()
	h.wantAll("a       b", "12345678        c", "ab      c")
}

func TestStylesReachTheScreen(t *testing.T) {
	h := newHarness(t, termCaps(20, 4))
	red := cell.Style{}.Fg(cell.RGB(255, 0, 0)).With(cell.Bold)
	blueBG := cell.Style{}.Bg(cell.ANSI(4)).With(cell.Underline)
	h.r.SetLive([]cell.Line{cell.Join(cell.Styled(red, "ab"), txt("cd"), cell.Styled(blueBG, "ef"))})
	h.flush()
	want := []cell.Style{red, red, {}, {}, blueBG, blueBG}
	for x, w := range want {
		if r, got := h.v.Cell(x, 0); got != w {
			t.Errorf("cell %d %q: style %+v, want %+v", x, r, got, w)
		}
	}
	// A row that ends in a style does not leak it into the row below, or into what is printed next.
	h.r.Print(txt("next"))
	h.flush()
	if _, st := h.v.Cell(0, 0); st != (cell.Style{}) {
		t.Errorf("printed text inherited a style: %+v", st)
	}
	// Erasing a row that ended in a background must not colour the erased cells.
	h.r.SetLive([]cell.Line{cell.Join(cell.Styled(blueBG, "ab"), txt("cd"))})
	h.flush()
	h.r.SetLive([]cell.Line{txt("x")})
	h.flush()
	for x := 1; x < 10; x++ {
		if _, st := h.v.Cell(x, 1); st != (cell.Style{}) {
			t.Errorf("a cell erased after a styled row carries %+v", st)
		}
	}
}

func TestSGRIsEmittedOnlyForWhatChangedAndResetAtTheEndOfTheRow(t *testing.T) {
	h := newHarness(t, termCaps(40, 4))
	bold := cell.Style{}.With(cell.Bold)
	boldRed := bold.Fg(cell.ANSI(1))
	h.r.Print(cell.Join(cell.Styled(bold, "a"), cell.Styled(boldRed, "b"), cell.Styled(bold, "c"), txt("d")), cell.Styled(boldRed, "e"))
	h.flush()
	got := string(h.b.frames[0])
	want := "\x1b[1ma\x1b[31mb\x1b[39mc\x1b[0md\r\n\x1b[1;31me\x1b[0m\r\n"
	if got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
}

func TestResizeRedrawsTheRegionAtTheNewWidthWithoutGhosts(t *testing.T) {
	h := newHarness(t, termCaps(40, 16))
	m := newModel(40, 16)
	hist := txts("history one", "history two")
	live := txts("a status line that is thirty-eight", "> an input line that is long enough")
	h.r.Print(hist...)
	m.print(hist...)
	h.r.SetLive(live)
	m.live = live
	h.r.SetCursor(1, 12)
	m.caretOn, m.caretR, m.caretC = true, 1, 12
	h.flush()
	m.flush()
	h.check(m)
	for _, w := range []int{20, 12, 60, 40, 9, 80, 41, 39} {
		h.resize(w, 16)
		m.cols = w
		if before := h.written(); before != h.written() {
			t.Fatal("a resize alone must not write")
		}
		h.flush()
		m.flush()
		h.check(m)
		if t.Failed() {
			t.Fatalf("after resizing to %d columns", w)
		}
	}
}

// On a terminal that does not reflow (xterm), a resize leaves the rows of the region cut at the new width, so the renderer's
// conservative erase goes up by more rows than the region has: the region is right and nothing of it is left behind, and what
// it costs is the lines of history directly above the region, at most as many as it over-counted. This test pins that cost.
func TestResizeOnATerminalThatDoesNotReflowLosesAtMostTheRowsItOverCounted(t *testing.T) {
	h := newHarness(t, termCaps(40, 14))
	h.v.SetReflow(false)
	for i := 0; i < 6; i++ {
		h.r.Print(txt(fmt.Sprintf("h%d", i)))
	}
	h.r.SetLive(txts("status line of thirty-eight cells....", "> input line of thirty-eight cells....."))
	h.r.SetCursor(1, 5)
	h.flush()
	h.resize(20, 14)
	h.flush()
	rows := h.all()
	wantLive := []string{"status line of", "thirty-eight", "cells....", "> input line of", "thirty-eight", "cells....."}
	if len(rows) < len(wantLive) || fmt.Sprint(rows[len(rows)-len(wantLive):]) != fmt.Sprint(wantLive) {
		t.Fatalf("the region is wrong after the resize: %q", rows)
	}
	history := rows[:len(rows)-len(wantLive)]
	// The cursor was on row 1 of the region; at width 20 the rows above it take 2 rows, so the renderer went up 2 and not 1.
	if len(history) != 5 || fmt.Sprint(history) != "[h0 h1 h2 h3 h4]" {
		t.Errorf("history after the resize: %q (one row over-erased is the documented cost)", history)
	}
}

// The renderer's count of the rows of the live region must be the terminal's, or every relative cursor move that follows lands
// on the wrong row. Lines that are awkward for the count: as wide as the terminal, one cell wider, wide runes that fill the row
// exactly or overflow it by one cell, combining marks, and a line that wraps at every space.
func TestTheRendererCountsTheRowsTheTerminalUses(t *testing.T) {
	h := newHarness(t, termCaps(12, 10))
	for _, s := range []string{"short", strings.Repeat("x", 12), strings.Repeat("y", 13), strings.Repeat("中", 6),
		strings.Repeat("中", 7), strings.Repeat("e\u0301", 12), strings.Repeat("e\u0301", 13), "a b c d e f g h i j k l m n o p"} {
		h.r.SetLive(txts("top", s, "bottom"))
		h.r.SetCursor(99, 0)
		h.flush()
		rows := len(h.r.drawn)
		if got := h.all(); len(got) != rows {
			t.Errorf("%q: the renderer counts %d rows, the terminal shows %d: %q", s, rows, len(got), got)
		}
		if _, y, _ := h.v.Cursor(); len(h.v.Scrollback())+y != rows-1 {
			t.Errorf("%q: the cursor is on row %d, the last of the %d rows is %d", s, len(h.v.Scrollback())+y, rows, rows-1)
		}
	}
}

func TestPrintedLinesThatFillTheWidthDoNotLeaveBlankRows(t *testing.T) {
	h := newHarness(t, termCaps(8, 10))
	h.r.Print(txt("12345678"), txt("abcdefgh"), txt(""), txt("x"))
	h.r.SetLive(txts("ABCDEFGH", "12345678"))
	h.flush()
	h.wantAll("12345678", "abcdefgh", "", "x", "ABCDEFGH", "12345678")
	h.r.SetLive(txts("ABCDEFGH", "1234567"))
	h.flush()
	h.wantAll("12345678", "abcdefgh", "", "x", "ABCDEFGH", "1234567")
	h.r.SetLive(txts("ABCDEFGH", "12345678"))
	h.flush()
	h.wantAll("12345678", "abcdefgh", "", "x", "ABCDEFGH", "12345678")
}

func TestManyPrintsWithoutFlushAreWrittenInBatches(t *testing.T) {
	h := newHarness(t, termCaps(20, 5))
	for i := 0; i < 3*autoFlushLines+7; i++ {
		h.r.Print(txt(fmt.Sprintf("l%d", i)))
	}
	if len(h.b.frames) < 3 {
		t.Errorf("the queue of printed lines must be bounded: %d frames", len(h.b.frames))
	}
	if h.r.pendLen() >= autoFlushLines {
		t.Errorf("%d lines waiting", h.r.pendLen())
	}
	h.flush()
	all := h.all()
	if len(all) != 3*autoFlushLines+7 || all[0] != "l0" || all[len(all)-1] != fmt.Sprintf("l%d", 3*autoFlushLines+6) {
		t.Errorf("%d rows, first %q last %q", len(all), all[0], all[len(all)-1])
	}
}

func (r *Inline) pendLen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pend)
}

func TestSizeAndResizeClamp(t *testing.T) {
	r := NewInline(&bytes.Buffer{}, termCaps(0, 0))
	if c, rows := r.Size(); c != 80 || rows != 24 {
		t.Errorf("an unknown size is 80x24, got %dx%d", c, rows)
	}
	r.Resize(-3, 0)
	if c, rows := r.Size(); c != 1 || rows != 1 {
		t.Errorf("%dx%d", c, rows)
	}
	r.Resize(100, 50)
	if c, rows := r.Size(); c != 100 || rows != 50 {
		t.Errorf("%dx%d", c, rows)
	}
}

func TestATinyTerminalDoesNotBreakAnything(t *testing.T) {
	for _, sz := range [][2]int{{1, 1}, {1, 5}, {2, 2}, {3, 1}} {
		h := newHarness(t, termCaps(sz[0], sz[1]))
		h.r.Print(txt("hello world"), txt("中文"))
		h.r.SetLive(txts("status line", "中x", "input"))
		h.r.SetCursor(2, 9)
		h.flush()
		h.resize(sz[1]+3, sz[0]+3)
		h.flush()
		h.resize(sz[0], sz[1])
		h.r.SetLive(nil)
		h.flush()
		if err := h.r.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
