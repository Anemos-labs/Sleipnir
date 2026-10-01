package vt

import (
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

func run(t *testing.T, cols, rows int, s string) *Term {
	t.Helper()
	v := New(cols, rows)
	v.WriteString(s)
	checkInvariants(t, v)
	return v
}

func wantRows(t *testing.T, v *Term, want ...string) {
	t.Helper()
	got := v.Rows()
	for len(want) < len(got) {
		want = append(want, "")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows:\n got  %q\n want %q", got, want)
	}
}

func wantCursor(t *testing.T, v *Term, x, y int) {
	t.Helper()
	if gx, gy, _ := v.Cursor(); gx != x || gy != y {
		t.Errorf("cursor (%d,%d), want (%d,%d)", gx, gy, x, y)
	}
}

func TestPrintAndControls(t *testing.T) {
	v := run(t, 10, 3, "hello\r\nwor\bld\tX\r\n\x07\x00ok")
	wantRows(t, v, "hello", "wold    X", "ok") // BS then "ld" overwrites the r; HT goes from column 4 to column 8
	wantCursor(t, v, 2, 2)
}

func TestLineFeedDoesNotReturnTheCarriage(t *testing.T) {
	v := run(t, 10, 3, "ab\ncd")
	wantRows(t, v, "ab", "  cd")
}

func TestDelayedWrap(t *testing.T) {
	v := run(t, 5, 3, "abcde")
	wantRows(t, v, "abcde")
	if x, y, _ := v.Cursor(); x != 4 || y != 0 {
		t.Errorf("after filling the row the cursor waits on the last column: (%d,%d)", x, y)
	}
	v.WriteString("f")
	wantRows(t, v, "abcde", "f")
	wantCursor(t, v, 1, 1)

	// A line break right after a full row must not leave a blank row: the wrap was only pending.
	v = run(t, 5, 3, "abcde\r\nfg")
	wantRows(t, v, "abcde", "fg")

	// Moving the cursor clears the pending wrap: the next rune overwrites the last column instead of wrapping.
	v = run(t, 5, 3, "abcde\x1b[Dz")
	wantRows(t, v, "abcze")
}

func TestAutowrapCanBeTurnedOff(t *testing.T) {
	v := run(t, 5, 2, "\x1b[?7labcdefgh")
	wantRows(t, v, "abcdh")
	v.WriteString("\x1b[?7h\r\x1b[Kxyzxyz")
	wantRows(t, v, "xyzxy", "z")
}

func TestScrollbackAndScrolling(t *testing.T) {
	v := run(t, 3, 2, "a\r\nb\r\nc\r\nd")
	if got := v.Scrollback(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("scrollback %q", got)
	}
	wantRows(t, v, "c", "d")
	if got := v.All(); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("all %q", got)
	}
	wantCursor(t, v, 1, 1)

	v.SetScrollbackLimit(1)
	if got := v.Scrollback(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("a lower limit drops the oldest rows: %q", got)
	}
	v.WriteString("\r\ne\r\nf")
	if got := v.Scrollback(); !reflect.DeepEqual(got, []string{"d"}) {
		t.Errorf("the limit holds as rows scroll: %q", got)
	}
}

func TestWideRunesAndMarks(t *testing.T) {
	v := run(t, 6, 3, "a中b")
	wantRows(t, v, "a中b")
	if r, _ := v.Cell(1, 0); r != "中" {
		t.Errorf("cell 1 = %q", r)
	}
	if r, _ := v.Cell(2, 0); r != "" {
		t.Errorf("the right half of a wide rune is empty, got %q", r)
	}
	wantCursor(t, v, 4, 0)

	// A combining mark joins the cell before it and takes no room.
	v = run(t, 6, 1, "e\u0301x")
	if r, _ := v.Cell(0, 0); r != "e\u0301" {
		t.Errorf("combined cell %q", r)
	}
	wantCursor(t, v, 2, 0)
	if v.Rows()[0] != "e\u0301x" {
		t.Errorf("row %q", v.Rows()[0])
	}

	// Variation selector and joiner are marks too.
	v = run(t, 6, 1, "\u2764\ufe0f!")
	wantCursor(t, v, 2, 0)

	// A mark at the very start of a row has nothing to join and is dropped.
	v = run(t, 6, 1, "\u0301a")
	wantRows(t, v, "a")

	// A mark after a run that filled the row joins the last cell, not the first of the next row.
	v = run(t, 3, 2, "abc\u0301")
	if r, _ := v.Cell(2, 0); r != "c\u0301" {
		t.Errorf("mark after delayed wrap: %q", r)
	}
}

func TestWideRuneAtTheLastColumnWrapsWithPadding(t *testing.T) {
	v := run(t, 4, 3, "abc中d")
	wantRows(t, v, "abc", "中d")
	if !v.main.rows[0].pad || !v.main.rows[0].wrapped {
		t.Error("the row left short by a wide rune is wrapped padding")
	}
	// On a one-column screen a wide rune has no place at all, and must not loop forever.
	v = run(t, 1, 2, "中a中b")
	wantRows(t, v, "a", "b")
}

func TestOverwritingHalfOfAWideRuneBlanksTheRest(t *testing.T) {
	v := run(t, 6, 1, "中中\ra")
	wantRows(t, v, "a 中")
	if r, _ := v.Cell(1, 0); r != " " {
		t.Errorf("the orphaned right half must be blank, got %q", r)
	}
	v = run(t, 6, 1, "中中\r\x1b[Cb") // writing on the right half blanks the left half
	wantRows(t, v, " b中")
	v = run(t, 6, 1, "中中\x1b[2G\x1b[K") // erasing from the right half blanks the left half too
	wantRows(t, v, "")
	v = run(t, 6, 1, "中中中\x1b[3G\x1b[2X") // erasing exactly one rune
	wantRows(t, v, "中  中")
	v = run(t, 6, 1, "中中中\x1b[2G\x1b[2X") // erasing across two runes takes both whole
	wantRows(t, v, "    中")
}

func TestEraseCharacters(t *testing.T) {
	v := run(t, 10, 2, "abcdef\r\x1b[2C\x1b[2X")
	wantRows(t, v, "ab  ef")
	v = run(t, 10, 2, "abcdef\x1b[3G\x1b[99X")
	wantRows(t, v, "ab")
}

func TestInvalidAndSplitUTF8(t *testing.T) {
	v := run(t, 10, 1, "a\xffb\xe4ac")
	wantRows(t, v, "a\uFFFDb\uFFFDac")

	// A rune cut between two Writes is finished by the next one.
	v = New(10, 1)
	v.Write([]byte("x\xe4\xb8"))
	if v.Idle() {
		t.Error("half a rune is not idle")
	}
	v.Write([]byte("\xadz"))
	wantRows(t, v, "x中z")
	if !v.Idle() {
		t.Error("not idle after a whole rune")
	}
}

func TestCursorMovement(t *testing.T) {
	type mv struct {
		in   string
		x, y int
	}
	cases := []mv{
		{"\x1b[3;5H", 4, 2},
		{"\x1b[H", 0, 0},
		{"\x1b[;7H", 6, 0},
		{"\x1b[4;H", 0, 3},
		{"\x1b[3;5f", 4, 2},
		{"\x1b[99;99H", 9, 4},
		{"\x1b[0;0H", 0, 0},
		{"\x1b[3;3H\x1b[A", 2, 1},
		{"\x1b[3;3H\x1b[2A", 2, 0},
		{"\x1b[3;3H\x1b[99A", 2, 0},
		{"\x1b[3;3H\x1b[B", 2, 3},
		{"\x1b[3;3H\x1b[99B", 2, 4},
		{"\x1b[3;3H\x1b[C", 3, 2},
		{"\x1b[3;3H\x1b[99C", 9, 2},
		{"\x1b[3;3H\x1b[D", 1, 2},
		{"\x1b[3;3H\x1b[0D", 1, 2},
		{"\x1b[3;3H\x1b[99D", 0, 2},
		{"\x1b[3;3H\x1b[E", 0, 3},
		{"\x1b[3;3H\x1b[2F", 0, 0},
		{"\x1b[3;3H\x1b[7G", 6, 2},
		{"\x1b[3;3H\x1b[G", 0, 2},
		{"\x1b[3;3H\x1b[5d", 2, 4},
		{"\x1b[3;3H\x1b[d", 2, 0},
		{"\x1b[99999999999999A\x1b[99999999999999C", 9, 0},
		{"\x1b[3;3H\x1b7\x1b[H\x1b8", 2, 2},
		{"\x1b[3;3H\x1b[s\x1b[H\x1b[u", 2, 2},
		{"\x1b[u", 0, 0}, // nothing was saved
		{"abc\x1b[2D", 1, 0},
		{"\x1b[5;5H\x1bM", 4, 3},         // reverse index
		{"\x1b[5;5H\x1bD", 4, 4},         // index at the bottom scrolls, cursor stays on the last row
		{"\x1b[2;5H\x1bE", 0, 2},         // next line
		{"\x1b[1;1H\x1bM", 0, 0},         // reverse index at the top scrolls down
		{"\x1b[3;3H\x1b[1;2;3;4H", 1, 0}, // extra parameters are ignored
	}
	for _, c := range cases {
		v := run(t, 10, 5, c.in)
		if x, y, _ := v.Cursor(); x != c.x || y != c.y {
			t.Errorf("%q: cursor (%d,%d), want (%d,%d)", c.in, x, y, c.x, c.y)
		}
	}
}

func TestSaveRestoreKeepsTheStyle(t *testing.T) {
	v := run(t, 10, 3, "\x1b[1;31m\x1b7\x1b[0m\x1b[2;1H\x1b8x")
	_, st := v.Cell(0, 0)
	if st.FG != cell.ANSI(1) || !st.Has(cell.Bold) {
		t.Errorf("DECRC restores the rendition: %+v", st)
	}
}

func TestErase(t *testing.T) {
	fill := "aaaaa\r\nbbbbb\r\nccccc\r\nddddd"
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"EL 0", fill + "\x1b[2;3H\x1b[K", []string{"aaaaa", "bb", "ccccc", "ddddd"}},
		{"EL 1", fill + "\x1b[2;3H\x1b[1K", []string{"aaaaa", "   bb", "ccccc", "ddddd"}},
		{"EL 2", fill + "\x1b[2;3H\x1b[2K", []string{"aaaaa", "", "ccccc", "ddddd"}},
		{"ED 0", fill + "\x1b[2;3H\x1b[J", []string{"aaaaa", "bb"}},
		{"ED 1", fill + "\x1b[3;3H\x1b[1J", []string{"", "", "   cc", "ddddd"}},
		{"ED 2", fill + "\x1b[2J", []string{}},
		{"ED 2 keeps the cursor", fill + "\x1b[2;3H\x1b[2Jx", []string{"", "  x"}},
		{"IL", fill + "\x1b[2;1H\x1b[L", []string{"aaaaa", "", "bbbbb", "ccccc"}},
		{"IL 2", fill + "\x1b[2;1H\x1b[2L", []string{"aaaaa", "", "", "bbbbb"}},
		{"DL", fill + "\x1b[2;1H\x1b[M", []string{"aaaaa", "ccccc", "ddddd"}},
		{"DL too many", fill + "\x1b[2;1H\x1b[99M", []string{"aaaaa"}},
		{"SU", fill + "\x1b[2S", []string{"ccccc", "ddddd"}},
		{"SD", fill + "\x1b[2T", []string{"", "", "aaaaa", "bbbbb"}},
	}
	for _, c := range cases {
		v := run(t, 5, 4, c.in)
		want := append([]string(nil), c.want...)
		for len(want) < 4 {
			want = append(want, "")
		}
		if got := v.Rows(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, want)
		}
	}
}

func TestScrollUpFeedsTheScrollbackButInsertDeleteDoNot(t *testing.T) {
	v := run(t, 3, 3, "a\r\nb\r\nc\x1b[2S")
	if got := v.Scrollback(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("SU: %q", got)
	}
	v = run(t, 3, 3, "a\r\nb\r\nc\x1b[1;1H\x1b[M")
	if len(v.Scrollback()) != 0 {
		t.Errorf("DL must not scroll into the scrollback: %q", v.Scrollback())
	}
	v = run(t, 3, 3, "a\r\nb\r\nc\x1b[3J")
	v.WriteString("d\r\ne\r\nf\r\ng")
	if len(v.Scrollback()) != 3 {
		t.Errorf("after ED 3: %q", v.Scrollback())
	}
	v.WriteString("\x1b[3J")
	if len(v.Scrollback()) != 0 {
		t.Errorf("ED 3 clears the scrollback: %q", v.Scrollback())
	}
}

func TestBackColourErase(t *testing.T) {
	v := run(t, 5, 2, "\x1b[44m\x1b[K\x1b[0m\x1b[2;1H\x1b[K")
	if _, st := v.Cell(3, 0); st.BG != cell.ANSI(4) {
		t.Errorf("erasing with a background set leaves it in the cells: %+v", st)
	}
	if _, st := v.Cell(3, 1); st.BG != cell.Default() {
		t.Errorf("erasing with none set leaves none: %+v", st)
	}
}

func TestSGR(t *testing.T) {
	type c = cell.Color
	cases := []struct {
		in   string
		want cell.Style
	}{
		{"\x1b[1m", cell.Style{Attr: cell.Bold}},
		{"\x1b[2m", cell.Style{Attr: cell.Dim}},
		{"\x1b[3m", cell.Style{Attr: cell.Italic}},
		{"\x1b[4m", cell.Style{Attr: cell.Underline}},
		{"\x1b[7m", cell.Style{Attr: cell.Reverse}},
		{"\x1b[9m", cell.Style{Attr: cell.Strike}},
		{"\x1b[1;3;4m", cell.Style{Attr: cell.Bold | cell.Italic | cell.Underline}},
		{"\x1b[1;2m\x1b[22m", cell.Style{}},
		{"\x1b[1;2m\x1b[22;1m", cell.Style{Attr: cell.Bold}},
		{"\x1b[3m\x1b[23m", cell.Style{}},
		{"\x1b[4m\x1b[24m", cell.Style{}},
		{"\x1b[7m\x1b[27m", cell.Style{}},
		{"\x1b[9m\x1b[29m", cell.Style{}},
		{"\x1b[1;31;42m\x1b[0m", cell.Style{}},
		{"\x1b[1;31;42m\x1b[m", cell.Style{}},
		{"\x1b[1;31;42m\x1b[;m", cell.Style{}},
		{"\x1b[31m", cell.Style{FG: c(cell.ANSI(1))}},
		{"\x1b[37m", cell.Style{FG: cell.ANSI(7)}},
		{"\x1b[90m", cell.Style{FG: cell.ANSI(8)}},
		{"\x1b[97m", cell.Style{FG: cell.ANSI(15)}},
		{"\x1b[41m", cell.Style{BG: cell.ANSI(1)}},
		{"\x1b[47m", cell.Style{BG: cell.ANSI(7)}},
		{"\x1b[100m", cell.Style{BG: cell.ANSI(8)}},
		{"\x1b[107m", cell.Style{BG: cell.ANSI(15)}},
		{"\x1b[31m\x1b[39m", cell.Style{}},
		{"\x1b[41m\x1b[49m", cell.Style{}},
		{"\x1b[38;5;200m", cell.Style{FG: cell.Indexed(200)}},
		{"\x1b[38;5;3m", cell.Style{FG: cell.Indexed(3)}}, // an indexed colour stays indexed even below 16
		{"\x1b[48;5;17m", cell.Style{BG: cell.Indexed(17)}},
		{"\x1b[38;2;10;20;30m", cell.Style{FG: cell.RGB(10, 20, 30)}},
		{"\x1b[48;2;255;0;128m", cell.Style{BG: cell.RGB(255, 0, 128)}},
		{"\x1b[38:2::10:20:30m", cell.Style{FG: cell.RGB(10, 20, 30)}},
		{"\x1b[38:2:10:20:30m", cell.Style{FG: cell.RGB(10, 20, 30)}},
		{"\x1b[48:5:99m", cell.Style{BG: cell.Indexed(99)}},
		{"\x1b[1;38;5;9;4m", cell.Style{FG: cell.Indexed(9), Attr: cell.Bold | cell.Underline}},
		{"\x1b[38;2;1;2m", cell.Style{}}, // truncated: ignored, and the rest is consumed
		{"\x1b[38;5m", cell.Style{}},     // likewise
		{"\x1b[38;9;1m1", cell.Style{}},  // an unknown colour mode
		{"\x1b[38;5;999m", cell.Style{FG: cell.Indexed(255)}},
		{"\x1b[5;8;53m", cell.Style{}},           // blink, conceal, overline: no effect
		{"\x1b[58;5;196m\x1b[59m", cell.Style{}}, // underline colour: consumed without touching the foreground
		{"\x1b[4:3m", cell.Style{Attr: cell.Underline}},
		{"\x1b[4m\x1b[4:0m", cell.Style{}},
		{"\x1b[1000m", cell.Style{}},
		{"\x1b[99999999999m", cell.Style{}},
		{"\x1b[31;m", cell.Style{}}, // a trailing empty parameter is 0: reset
	}
	for _, tc := range cases {
		v := run(t, 10, 1, tc.in+"x")
		_, got := v.Cell(0, 0)
		if got != tc.want {
			t.Errorf("%q:\n got  %+v\n want %+v", tc.in, got, tc.want)
		}
	}
}

func TestModes(t *testing.T) {
	v := run(t, 10, 3, "")
	if _, _, vis := v.Cursor(); !vis {
		t.Error("the cursor starts visible")
	}
	v.WriteString("\x1b[?25l")
	if _, _, vis := v.Cursor(); vis {
		t.Error("CSI ? 25 l hides the cursor")
	}
	v.WriteString("\x1b[?25h")
	if _, _, vis := v.Cursor(); !vis {
		t.Error("CSI ? 25 h shows it")
	}
	if v.BracketedPaste() {
		t.Error("bracketed paste starts off")
	}
	v.WriteString("\x1b[?2004h")
	if !v.BracketedPaste() {
		t.Error("2004 set")
	}
	v.WriteString("\x1b[?2004l")
	if v.BracketedPaste() {
		t.Error("2004 reset")
	}
	// Several modes in one sequence.
	v.WriteString("\x1b[?25;2004l\x1b[?25;2004h")
	if _, _, vis := v.Cursor(); !vis || !v.BracketedPaste() {
		t.Error("several modes in one sequence")
	}
	// Writing still works inside a synchronized frame; the depth is what a test looks at.
	v.WriteString("\x1b[?2026hab")
	if v.SyncDepth() != 1 || v.SyncBegins() != 1 || v.SyncEnds() != 0 {
		t.Errorf("sync: depth %d begins %d ends %d", v.SyncDepth(), v.SyncBegins(), v.SyncEnds())
	}
	wantRows(t, v, "ab")
	v.WriteString("\x1b[?2026l")
	if v.SyncDepth() != 0 || v.SyncEnds() != 1 {
		t.Errorf("sync after the frame: depth %d ends %d", v.SyncDepth(), v.SyncEnds())
	}
	v.WriteString("\x1b[?2026l")
	if v.SyncDepth() != 0 || v.SyncEnds() != 2 {
		t.Errorf("an unmatched end does not go below zero: depth %d ends %d", v.SyncDepth(), v.SyncEnds())
	}
	v.WriteString("\x1b[?2026h\x1b[?2026h")
	if v.SyncDepth() != 2 {
		t.Errorf("a nested begin is visible as depth %d", v.SyncDepth())
	}
}

func TestAlternateScreen(t *testing.T) {
	v := run(t, 10, 3, "main\r\nscreen\x1b[2;3H\x1b[31m")
	v.WriteString("\x1b[?1049h")
	if !v.AltScreen() {
		t.Fatal("not on the alternate screen")
	}
	wantRows(t, v, "", "", "")
	v.WriteString("\x1b[2J\x1b[Halt one\r\nalt two\r\nalt three\r\nalt four")
	wantRows(t, v, "alt two", "alt three", "alt four")
	if len(v.Scrollback()) != 0 {
		t.Errorf("the alternate screen has no scrollback: %q", v.Scrollback())
	}
	v.WriteString("\x1b[?1049l")
	if v.AltScreen() {
		t.Fatal("still on the alternate screen")
	}
	wantRows(t, v, "main", "screen")
	wantCursor(t, v, 2, 1)
	v.WriteString("x")
	if _, st := v.Cell(2, 1); st.FG != cell.ANSI(1) {
		t.Errorf("the style saved with the cursor is restored: %+v", st)
	}
	// Leaving twice, or entering twice, is harmless.
	v.WriteString("\x1b[?1049l\x1b[?1049h\x1b[?1049h\x1b[?1049l")
	wantRows(t, v, "main", "scxeen")
	checkInvariants(t, v)
}

func TestDeviceReports(t *testing.T) {
	v := run(t, 10, 5, "ab\x1b[3;4H\x1b[6n\x1b[5n")
	if got := string(v.Reply()); got != "\x1b[3;4R\x1b[0n" {
		t.Errorf("reply %q", got)
	}
	if got := v.Reply(); len(got) != 0 {
		t.Errorf("Reply clears: %q", got)
	}
	v.WriteString("\x1b[H\x1b[6n")
	if got := string(v.Reply()); got != "\x1b[1;1R" {
		t.Errorf("reply %q", got)
	}
}

func TestReset(t *testing.T) {
	v := run(t, 10, 3, "abc\r\nd\x1b[?25l\x1b[?2004h\x1b[1;31m\x1b[?1049h")
	v.SetScrollbackLimit(7)
	v.WriteString("\x1bcx")
	if v.AltScreen() || v.BracketedPaste() {
		t.Error("modes are reset")
	}
	if _, _, vis := v.Cursor(); !vis {
		t.Error("the cursor is shown again")
	}
	wantRows(t, v, "x")
	if _, st := v.Cell(0, 0); st != (cell.Style{}) {
		t.Errorf("the rendition is reset: %+v", st)
	}
	if v.sbMax != 7 {
		t.Error("a reset keeps the scrollback limit")
	}
}

func TestNewClampsTheSize(t *testing.T) {
	v := New(0, -5)
	if c, r := v.Size(); c != 1 || r != 1 {
		t.Errorf("%dx%d", c, r)
	}
	v = New(100000, 100000)
	if c, r := v.Size(); c != maxDim || r != maxDim {
		t.Errorf("%dx%d", c, r)
	}
	v = New(3, 1)
	v.WriteString("abc\r\nd")
	wantRows(t, v, "d")
	checkInvariants(t, v)
}

func TestCellOutsideTheScreen(t *testing.T) {
	v := run(t, 3, 2, "ab")
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {3, 0}, {0, 2}} {
		if r, st := v.Cell(p[0], p[1]); r != "" || st != (cell.Style{}) {
			t.Errorf("Cell%v = %q %+v", p, r, st)
		}
	}
	if r, _ := v.Cell(2, 0); r != " " {
		t.Errorf("an empty cell is a space, got %q", r)
	}
}

func TestStringIsTheVisibleRows(t *testing.T) {
	v := run(t, 4, 3, "ab\r\ncd")
	if v.String() != "ab\ncd\n" {
		t.Errorf("%q", v.String())
	}
}

func TestWriteReturnsEverything(t *testing.T) {
	v := New(4, 2)
	if n, err := v.Write([]byte("\x1b[1")); n != 3 || err != nil {
		t.Errorf("%d %v", n, err)
	}
	if n, err := v.WriteString("mx"); n != 2 || err != nil {
		t.Errorf("%d %v", n, err)
	}
	if _, st := v.Cell(0, 0); !st.Has(cell.Bold) {
		t.Error("a sequence split across two writes")
	}
}
