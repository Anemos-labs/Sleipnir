package render

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
)

// screenHarness is a full-screen renderer wired to an emulator of the same size. The bridge does not check for synchronized
// output here: Enter and Leave are mode switches and are not framed; the test of sync output looks at the draw frames itself.
type screenHarness struct {
	tb testing.TB
	b  *bridge
	v  *vt.Term
	s  *Screen
}

func newScreenHarness(tb testing.TB, caps term.Caps) *screenHarness {
	tb.Helper()
	v := vt.New(caps.Width, caps.Height)
	bc := caps
	bc.SyncOutput = false
	b := &bridge{tb: tb, v: v, caps: bc}
	return &screenHarness{tb: tb, b: b, v: v, s: NewScreen(b, caps)}
}

func (sh *screenHarness) draw(lines ...string) {
	sh.tb.Helper()
	if err := sh.s.DrawLines(txts(lines...)); err != nil {
		sh.tb.Fatal(err)
	}
}

func (sh *screenHarness) wantRows(want ...string) {
	sh.tb.Helper()
	got := sh.v.Rows()
	for len(want) < len(got) {
		want = append(want, "")
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		sh.tb.Errorf("screen:\n got  %q\n want %q", got, want)
	}
}

func TestScreenEnterAndLeaveRestoreTheTerminal(t *testing.T) {
	sh := newScreenHarness(t, termCaps(20, 5))
	sh.v.WriteString("$ ls\r\nfile1 file2\r\n$ ")
	sh.v.WriteString("\x1b[31m") // the shell left a rendition set; Leave must not make it worse
	cx, cy, _ := sh.v.Cursor()

	if err := sh.s.Enter(); err != nil {
		t.Fatal(err)
	}
	if !sh.v.AltScreen() {
		t.Fatal("Enter uses the alternate screen")
	}
	if _, _, vis := sh.v.Cursor(); vis {
		t.Error("the cursor is hidden in the view")
	}
	sh.wantRows() // cleared
	sh.draw("title", "", "body line")
	sh.wantRows("title", "", "body line")
	if err := sh.s.Enter(); err != nil { // harmless twice
		t.Fatal(err)
	}
	sh.wantRows("title", "", "body line")

	if err := sh.s.Leave(); err != nil {
		t.Fatal(err)
	}
	if sh.v.AltScreen() {
		t.Fatal("Leave returns to the normal screen")
	}
	if x, y, vis := sh.v.Cursor(); !vis || x != cx || y != cy {
		t.Errorf("the cursor is back where it was and shown: (%d,%d) %v, want (%d,%d)", x, y, vis, cx, cy)
	}
	sh.wantRows("$ ls", "file1 file2", "$")
	if err := sh.s.Leave(); err != nil { // harmless twice
		t.Fatal(err)
	}
	if err := sh.s.DrawLines(txts("late")); !errors.Is(err, ErrNotActive) {
		t.Errorf("drawing after Leave: %v", err)
	}
}

func TestScreenRefusesADumbTerminal(t *testing.T) {
	var out bytes.Buffer
	s := NewScreen(&out, termCaps(20, 5).Plain())
	if err := s.Enter(); !errors.Is(err, ErrDumb) {
		t.Errorf("Enter = %v", err)
	}
	if err := s.DrawLines(txts("x")); !errors.Is(err, ErrDumb) {
		t.Errorf("Draw = %v", err)
	}
	if err := s.Leave(); err != nil {
		t.Errorf("Leave = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a dumb terminal was sent %q", out.String())
	}
}

func TestScreenDrawBeforeEnter(t *testing.T) {
	sh := newScreenHarness(t, termCaps(20, 5))
	if err := sh.s.Draw([][]Cell{{{Text: "x"}}}); !errors.Is(err, ErrNotActive) {
		t.Errorf("Draw = %v", err)
	}
	if sh.b.total != 0 {
		t.Error("nothing is written before Enter")
	}
}

func TestScreenWritesOnlyWhatChanged(t *testing.T) {
	sh := newScreenHarness(t, termCaps(30, 6))
	sh.s.Enter()
	red := cell.Style{}.Fg(cell.ANSI(1))
	frame := func(mid string) [][]Cell {
		g := make([][]Cell, 6)
		for y := range g {
			g[y] = make([]Cell, 30)
		}
		for x, r := range "a fixed title row" {
			g[0][x] = Cell{Text: string(r)}
		}
		for x, r := range mid {
			g[3][5+x] = Cell{Text: string(r), Style: red}
		}
		return g
	}
	sh.s.Draw(frame("status 1"))
	first := sh.b.total
	n := len(sh.b.frames)
	sh.s.Draw(frame("status 1")) // identical
	if len(sh.b.frames) != n {
		t.Errorf("an identical frame wrote %q", sh.b.frames[len(sh.b.frames)-1])
	}
	sh.s.Draw(frame("status 2")) // one cell differs
	f := string(sh.b.frames[len(sh.b.frames)-1])
	if want := "\x1b[4;13H\x1b[31m2\x1b[0m"; f != want {
		t.Errorf("one changed cell:\n got  %q\n want %q", f, want)
	}
	sh.s.Draw(frame("status 2 extended")) // a longer run on one row: one move, one run
	f = string(sh.b.frames[len(sh.b.frames)-1])
	if want := "\x1b[4;14H\x1b[31m extended\x1b[0m"; f != want {
		t.Errorf("a run:\n got  %q\n want %q", f, want)
	}
	if strings.Contains(f, "fixed") || sh.b.total-first > 80 {
		t.Errorf("rows that did not change were written: %q", f)
	}
	sh.wantRows("a fixed title row", "", "", "     status 2 extended")
}

// Random frames, each a few changes away from the last: after every frame the emulator's screen equals the grid cell for cell,
// with styles, wide runes and combining marks.
func TestScreenDiffsAgreeWithTheGridCellForCell(t *testing.T) {
	styles := []cell.Style{{}, cell.Style{}.With(cell.Bold), cell.Style{}.Fg(cell.ANSI(2)), cell.Style{}.Bg(cell.RGB(10, 20, 30)).With(cell.Reverse)}
	texts := []string{" ", "a", "b", "Z", "é", "e\u0301", "中", "あ", "", "~"}
	for seed := int64(1); seed <= 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		cols, rows := 6+rng.Intn(20), 3+rng.Intn(6)
		sh := newScreenHarness(t, termCaps(cols, rows))
		sh.s.Enter()
		grid := make([][]Cell, rows)
		for y := range grid {
			grid[y] = make([]Cell, cols)
		}
		for step := 0; step < 40; step++ {
			for n := 1 + rng.Intn(6); n > 0; n-- {
				y, x := rng.Intn(rows), rng.Intn(cols)
				c := Cell{Text: texts[rng.Intn(len(texts))], Style: styles[rng.Intn(len(styles))]}
				grid[y][x] = c
				if cell.RuneWidth([]rune(c.Text + " ")[0]) == 2 && x+1 < cols {
					grid[y][x+1] = Cell{Style: c.Style}
				}
			}
			if rng.Intn(10) == 0 { // a wide rune in the last column has no room: it is shown as nothing
				grid[rng.Intn(rows)][cols-1] = Cell{Text: "中"}
			}
			if err := sh.s.Draw(grid); err != nil {
				t.Fatal(err)
			}
			want := expectedGrid(grid, cols)
			for y := 0; y < rows; y++ {
				for x := 0; x < cols; x++ {
					gotText, gotStyle := sh.v.Cell(x, y)
					if gotText != want[y][x].Text || gotStyle != want[y][x].Style {
						t.Fatalf("seed %d step %d: cell (%d,%d) is %q %+v, want %q %+v", seed, step, x, y, gotText, gotStyle, want[y][x].Text, want[y][x].Style)
					}
				}
			}
		}
	}
}

// expectedGrid is what the terminal must show for a grid: an empty cell is a space, a wide rune is followed by an empty right
// half in the same style, and a wide rune in the last column, which has no room, is a blank.
func expectedGrid(grid [][]Cell, cols int) [][]Cell {
	out := make([][]Cell, len(grid))
	for y, row := range grid {
		out[y] = make([]Cell, cols)
		for x := 0; x < cols; x++ {
			c := row[x]
			switch {
			case x > 0 && isWide(out[y][x-1]): // whatever the grid says there
				out[y][x] = Cell{Style: out[y][x-1].Style}
			case c.Text == "":
				out[y][x] = Cell{Text: " ", Style: c.Style}
			case isWide(c) && x+1 >= cols:
				out[y][x] = Cell{Text: " ", Style: c.Style}
			default:
				out[y][x] = c
			}
		}
	}
	return out
}

func TestScreenResizeRepaintsEverything(t *testing.T) {
	sh := newScreenHarness(t, termCaps(20, 4))
	sh.s.Enter()
	sh.draw("one", "two", "three", "four")
	sh.v.Resize(30, 6)
	sh.s.Resize(30, 6)
	n := len(sh.b.frames)
	sh.draw("one", "two", "three", "four", "five", "six")
	if len(sh.b.frames) != n+1 || !bytes.Contains(sh.b.frames[n], []byte(eraseScreen)) {
		t.Errorf("a resize is followed by a clear and a full draw: %q", sh.b.frames[n:])
	}
	sh.wantRows("one", "two", "three", "four", "five", "six")
	if c, r := sh.s.Size(); c != 30 || r != 6 {
		t.Errorf("%dx%d", c, r)
	}
	// Smaller: what does not fit is cut, not wrapped.
	sh.v.Resize(6, 2)
	sh.s.Resize(6, 2)
	sh.draw("a long first row", "second row", "third")
	sh.wantRows("a long", "second")
}

func TestScreenLinesAreCutAtTheEdgeAndNeverWrapped(t *testing.T) {
	sh := newScreenHarness(t, termCaps(8, 3))
	sh.s.Enter()
	sh.draw("12345678", "123456789", "1234567中中")
	sh.wantRows("12345678", "12345678", "1234567")
	if len(sh.v.Scrollback()) != 0 {
		t.Error("the view never scrolls")
	}
}

func TestScreenBottomRightCellDoesNotScroll(t *testing.T) {
	sh := newScreenHarness(t, termCaps(5, 3))
	sh.s.Enter()
	g := [][]Cell{make([]Cell, 5), make([]Cell, 5), make([]Cell, 5)}
	g[2][4] = Cell{Text: "Z"}
	g[0][0] = Cell{Text: "A"}
	sh.s.Draw(g)
	sh.wantRows("A", "", "    Z")
	g[2][3] = Cell{Text: "Y"}
	sh.s.Draw(g)
	sh.wantRows("A", "", "   YZ")
}

func TestScreenColorNoneKeepsAttributesAndDropsColours(t *testing.T) {
	c := termCaps(20, 3)
	c.Color = term.ColorNone
	sh := newScreenHarness(t, c)
	sh.s.Enter()
	st := cell.Style{}.Fg(cell.RGB(255, 0, 0)).Bg(cell.ANSI(4)).With(cell.Bold | cell.Reverse)
	sh.s.DrawLines([]cell.Line{cell.Styled(st, "selected")})
	if _, got := sh.v.Cell(0, 0); got != (cell.Style{}.With(cell.Bold | cell.Reverse)) {
		t.Errorf("style %+v: bold and reverse stay, colours go", got)
	}
}

func TestScreenColoursAreDownsampled(t *testing.T) {
	c := termCaps(20, 3)
	c.Color = term.ColorANSI16
	sh := newScreenHarness(t, c)
	sh.s.Enter()
	sh.s.DrawLines([]cell.Line{cell.Styled(cell.Style{}.Fg(cell.RGB(152, 195, 121)), "good")})
	if _, got := sh.v.Cell(0, 0); got.FG != cell.ANSI(10) {
		t.Errorf("a soft green at 16 colours: %+v", got)
	}
}

func TestScreenSyncOutputBracketsEveryDrawFrame(t *testing.T) {
	c := termCaps(20, 3)
	c.SyncOutput = true
	sh := newScreenHarness(t, c)
	sh.s.Enter()
	sh.draw("a")
	sh.draw("b")
	sh.draw("b") // nothing changed: no frame at all
	sh.v.Resize(10, 3)
	sh.s.Resize(10, 3)
	sh.draw("c")
	frames := sh.b.frames[1:] // the first write is Enter
	if len(frames) != 3 {
		t.Fatalf("%d draw frames", len(frames))
	}
	for _, f := range frames {
		if !bytes.HasPrefix(f, []byte(syncBegin)) || !bytes.HasSuffix(f, []byte(syncEnd)) || bytes.Count(f, []byte(syncBegin)) != 1 {
			t.Errorf("not a synchronized frame: %q", f)
		}
	}
	if sh.v.SyncBegins() != 3 || sh.v.SyncEnds() != 3 || sh.v.SyncDepth() != 0 {
		t.Errorf("begins %d ends %d depth %d", sh.v.SyncBegins(), sh.v.SyncEnds(), sh.v.SyncDepth())
	}
}

func TestScreenWriteErrorIsSticky(t *testing.T) {
	boom := errors.New("closed")
	w := &failingWriter{failAfter: 2, err: boom}
	s := NewScreen(w, termCaps(10, 3))
	if err := s.Enter(); err != nil {
		t.Fatal(err)
	}
	if err := s.DrawLines(txts("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.DrawLines(txts("b")); !errors.Is(err, boom) {
		t.Fatalf("Draw = %v", err)
	}
	writes := w.writes
	if err := s.DrawLines(txts("c")); !errors.Is(err, boom) {
		t.Errorf("Draw = %v", err)
	}
	if err := s.Leave(); !errors.Is(err, boom) {
		t.Errorf("Leave = %v", err)
	}
	if err := s.Enter(); !errors.Is(err, boom) {
		t.Errorf("Enter = %v", err)
	}
	if w.writes != writes {
		t.Errorf("%d writes after the failure", w.writes-writes)
	}
}

func TestScreenGridCellsAreSingleCharacters(t *testing.T) {
	sh := newScreenHarness(t, termCaps(10, 2))
	sh.s.Enter()
	sh.s.Draw([][]Cell{{{Text: "abc"}, {Text: "\x1b[2J"}, {Text: "e\u0301x"}, {Text: "\n"}, {Text: "\u0301"}, {Text: "中"}, {Text: "garbage"}, {Text: "z"}}})
	// "abc" is cut to "a"; the escape and the controls are blank cells; the accent with nothing before it is blank; "中"
	// takes its cell and the next, whose "garbage" is overwritten by the right half of the wide rune.
	if got, want := sh.v.Rows()[0], "a e\u0301  中z"; got != want {
		t.Errorf("row %q, want %q", got, want)
	}
	if len(sh.v.Rejected) != 0 {
		t.Errorf("rejected %q", sh.v.Rejected)
	}
}

func TestScreenConcurrentUse(t *testing.T) {
	sh := newScreenHarness(t, termCaps(20, 4))
	if err := sh.s.Enter(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if err := sh.s.DrawLines(txts(fmt.Sprintf("goroutine %d frame %d", g, i))); err != nil {
					t.Error(err)
				}
				sh.s.Size()
			}
		}()
	}
	wg.Wait()
	if err := sh.s.Leave(); err != nil { // e.g. a signal handler's goroutine
		t.Fatal(err)
	}
	if sh.v.AltScreen() {
		t.Error("still on the alternate screen")
	}
}
