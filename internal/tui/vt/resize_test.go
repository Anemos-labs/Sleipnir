package vt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func trimTail(rows []string) []string {
	n := len(rows)
	for n > 0 && rows[n-1] == "" {
		n--
	}
	return rows[:n]
}

func TestResizeReflowsSoftWrappedLines(t *testing.T) {
	v := run(t, 10, 4, "abcdefghijklm") // wraps at the edge: one logical line of 13 cells
	wantRows(t, v, "abcdefghij", "klm")
	wantCursor(t, v, 3, 1)

	v.Resize(5, 4)
	checkInvariants(t, v)
	wantRows(t, v, "abcde", "fghij", "klm")
	wantCursor(t, v, 3, 2)

	v.Resize(10, 4) // the soft wraps are joined again
	checkInvariants(t, v)
	wantRows(t, v, "abcdefghij", "klm")
	wantCursor(t, v, 3, 1)

	v.Resize(20, 4)
	wantRows(t, v, "abcdefghijklm")
	wantCursor(t, v, 13, 0)
}

func TestResizeKeepsHardLineBreaks(t *testing.T) {
	v := run(t, 10, 4, "abc\r\ndefghi\r\nj")
	v.Resize(40, 4) // growing never joins rows that ended in a line feed
	wantRows(t, v, "abc", "defghi", "j")
	v.Resize(4, 6)
	checkInvariants(t, v)
	wantRows(t, v, "abc", "defg", "hi", "j")
	v.Resize(40, 6) // the terminal wrapped "defghi" itself, so it is one line again
	wantRows(t, v, "abc", "defghi", "j")
}

func TestResizeMovesTheCursorWithItsText(t *testing.T) {
	cases := []struct {
		name         string
		cols, rows   int // before
		in           string
		newCols      int // after
		newRows      int
		wantRows     []string
		wantX, wantY int
	}{
		{"in the middle of a wrapped line", 10, 6, "abcdefghijklm\x1b[2D", 5, 6, []string{"abcde", "fghij", "klm"}, 1, 2},
		{"beyond the text stays beyond it", 10, 6, "ab\x1b[8G", 5, 6, []string{"ab"}, 2, 1},
		{"waiting at the end of a full row, wider", 5, 6, "abcde", 8, 6, []string{"abcde"}, 5, 0},
		{"waiting at the end of a full row, narrower", 5, 6, "abcde", 3, 6, []string{"abc", "de"}, 2, 1},
		{"waiting at the end of a full row, only the height changes", 5, 6, "abcde", 5, 7, []string{"abcde"}, 4, 0},
	}
	for _, c := range cases {
		v := run(t, c.cols, c.rows, c.in)
		v.Resize(c.newCols, c.newRows)
		checkInvariants(t, v)
		want := append([]string(nil), c.wantRows...)
		for len(want) < c.newRows {
			want = append(want, "")
		}
		if got := v.Rows(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rows\n got  %q\n want %q", c.name, got, want)
		}
		if x, y, _ := v.Cursor(); x != c.wantX || y != c.wantY {
			t.Errorf("%s: cursor (%d,%d), want (%d,%d)", c.name, x, y, c.wantX, c.wantY)
		}
	}
}

func TestResizeTypingContinuesWhereTheCursorWas(t *testing.T) {
	v := run(t, 5, 4, "abcde") // delayed wrap at the end of a full row
	v.Resize(8, 4)
	v.WriteString("f")
	wantRows(t, v, "abcdef")
	wantCursor(t, v, 6, 0)

	v = run(t, 5, 4, "abcde")
	v.Resize(3, 4)
	v.WriteString("f")
	wantRows(t, v, "abc", "def")
}

func TestResizeWideRunesAreNeverSplit(t *testing.T) {
	v := run(t, 6, 4, "ab中cd")
	v.Resize(3, 4)
	checkInvariants(t, v)
	wantRows(t, v, "ab", "中c", "d")
	if !v.main.rows[0].pad {
		t.Error("the hole left by the wide rune is padding")
	}
	v.Resize(6, 4)
	checkInvariants(t, v)
	wantRows(t, v, "ab中cd")
	v.Resize(1, 4) // a wide rune has no place on a one-column screen
	checkInvariants(t, v)
}

func TestResizeScrollbackTakesPart(t *testing.T) {
	v := New(10, 3)
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(v, "line %d\r\n", i)
	}
	all := trimTail(v.All())
	if len(all) != 7 {
		t.Fatalf("all: %q", all)
	}
	v.Resize(10, 5) // taller: rows come back from the scrollback
	checkInvariants(t, v)
	if got := trimTail(v.All()); !reflect.DeepEqual(got, all) {
		t.Errorf("a taller screen changes nothing that was shown: %q", got)
	}
	v.Resize(10, 2) // shorter: the top of the screen goes into the scrollback
	checkInvariants(t, v)
	if got := trimTail(v.All()); !reflect.DeepEqual(got, all) {
		t.Errorf("a shorter screen changes nothing that was shown: %q", got)
	}
	if x, y, _ := v.Cursor(); y != 1 || x != 0 {
		t.Errorf("the cursor stays on the last row: (%d,%d)", x, y)
	}
	wantRows(t, v, "line 7", "")
}

func TestResizeWithoutReflowCutsAndPads(t *testing.T) {
	v := New(10, 3)
	v.SetReflow(false)
	v.WriteString("abcdefghij\r\nxy")
	v.Resize(5, 3)
	checkInvariants(t, v)
	wantRows(t, v, "abcde", "xy")
	wantCursor(t, v, 2, 1)
	v.Resize(10, 3) // what was cut is gone
	wantRows(t, v, "abcde", "xy")

	v = New(6, 2)
	v.SetReflow(false)
	v.WriteString("ab中中")
	v.Resize(5, 2) // the second wide rune loses its right half
	checkInvariants(t, v)
	wantRows(t, v, "ab中")
	v.Resize(2, 2)
	checkInvariants(t, v)
	v.Resize(1, 1)
	checkInvariants(t, v)
}

func TestResizeTheAlternateScreenIsCroppedAndThePrimaryReflowed(t *testing.T) {
	v := run(t, 10, 4, "abcdefghijkl\x1b[?1049h\x1b[2;3Halt\x1b[4;10Hz")
	v.Resize(6, 3)
	checkInvariants(t, v)
	if !v.AltScreen() {
		t.Fatal("still the alternate screen")
	}
	if got := v.Rows(); got[1] != "  alt" {
		t.Errorf("alternate screen cropped, not reflowed: %q", got)
	}
	v.WriteString("\x1b[?1049l")
	checkInvariants(t, v)
	if got := trimTail(v.All()); !reflect.DeepEqual(got, []string{"abcdef", "ghijkl"}) {
		t.Errorf("primary screen reflowed while hidden: %q", got)
	}
	// The cursor came back to where it was in the text: just after the last l. That is the start of a third row, because the
	// two rows of six cells are full.
	wantCursor(t, v, 0, 2)
}

func TestResizeNoOpAndClamp(t *testing.T) {
	v := run(t, 10, 4, "abc")
	v.Resize(10, 4)
	wantRows(t, v, "abc")
	v.Resize(0, 0)
	if c, r := v.Size(); c != 1 || r != 1 {
		t.Errorf("%dx%d", c, r)
	}
	checkInvariants(t, v)
	v.Resize(100000, 3)
	if c, r := v.Size(); c != maxDim || r != 3 {
		t.Errorf("%dx%d", c, r)
	}
	checkInvariants(t, v)
}

// Resizing back and forth must not lose, repeat or reorder anything: the text of the terminal, with soft wraps joined, is the
// same after any sequence of resizes (wide runes excepted on a screen too narrow to hold them).
func TestResizeRoundTripsText(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog, and then some more words follow to make it long.\r\n" +
		"short\r\n\r\n" + strings.Repeat("0123456789", 7) + "\r\nlast line"
	v := New(40, 12)
	v.WriteString(text)
	want := logicalText(v)
	for _, sz := range [][2]int{{20, 12}, {7, 30}, {3, 60}, {50, 6}, {12, 12}, {40, 12}, {1, 200}, {80, 3}} {
		v.Resize(sz[0], sz[1])
		checkInvariants(t, v)
		if got := logicalText(v); got != want {
			t.Fatalf("after resizing to %v the text changed:\n got  %q\n want %q", sz, got, want)
		}
	}
}

// logicalText is everything the terminal shows with the soft wraps joined, so that it does not depend on the width.
func logicalText(v *Term) string {
	var b strings.Builder
	rows := append(append([]vrow(nil), v.sb...), v.main.rows...)
	last := len(rows) - 1
	for last >= 0 && rowIsBlank(rows[last]) {
		last--
	}
	for i := 0; i <= last; i++ {
		r := rows[i]
		var row strings.Builder
		for _, c := range r.cells[:contentLen(r)] {
			switch {
			case c.cont:
			case c.text == "":
				row.WriteByte(' ')
			default:
				row.WriteString(c.text)
			}
		}
		if r.wrapped { // a space at the end of a wrapped row is part of the text
			b.WriteString(row.String())
		} else {
			b.WriteString(strings.TrimRight(row.String(), " "))
			b.WriteByte('\n')
		}
	}
	// The last row can be flagged as wrapped only because the cursor, just after a full row, sits on the row below it.
	return strings.TrimRight(b.String(), "\n") + "\n"
}
