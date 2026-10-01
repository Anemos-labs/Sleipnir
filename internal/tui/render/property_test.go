package render

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

var testStyles = []cell.Style{
	{},
	cell.Style{}.With(cell.Bold),
	cell.Style{}.Fg(cell.ANSI(1)),
	cell.Style{}.Fg(cell.RGB(10, 200, 30)).With(cell.Underline),
	cell.Style{}.Fg(cell.Indexed(99)).Bg(cell.RGB(200, 0, 200)),
	cell.Style{}.Fg(cell.Hex("#7aa2f7")).With(cell.Dim | cell.Italic),
	cell.Style{}.Bg(cell.ANSI(4)).With(cell.Reverse),
}

// randText is a random line of about n cells: lowercase words, spaces, a wide rune now and then, a precomposed and a
// decomposed accent. It never starts with a space (a wrapped line's leading spaces are the renderer's business, tested apart)
// and never ends with one (trailing spaces are dropped when they show nothing and kept when they do, which the model of these
// tests does not try to know; it has a test of its own).
func randText(rng *rand.Rand, n int) string {
	var sb strings.Builder
	sb.WriteByte(byte('a' + rng.Intn(26)))
	for w := 1; w < n; {
		switch rng.Intn(24) {
		case 0:
			sb.WriteString("中")
			w += 2
		case 1:
			sb.WriteString("é")
			w++
		case 2:
			sb.WriteString("e\u0301")
			w++
		case 3, 4, 5, 6:
			sb.WriteByte(' ')
			w++
		default:
			sb.WriteByte(byte('a' + rng.Intn(26)))
			w++
		}
	}
	sb.WriteByte('z')
	return sb.String()
}

// randLine is a line of up to max cells in one to three random styles.
func randLine(rng *rand.Rand, max int) cell.Line {
	if max < 1 || rng.Intn(10) == 0 {
		return nil
	}
	s := []rune(randText(rng, 1+rng.Intn(max)))
	var l cell.Line
	for len(s) > 0 {
		n := 1 + rng.Intn(len(s))
		if rng.Intn(3) == 0 {
			n = len(s)
		}
		l = append(l, cell.Span{Text: string(s[:n]), Style: testStyles[rng.Intn(len(testStyles))]})
		s = s[n:]
	}
	return l
}

func randLines(rng *rand.Rand, n, max int) []cell.Line {
	out := make([]cell.Line, n)
	for i := range out {
		out[i] = randLine(rng, max)
	}
	return out
}

// Print N lines, then a three-line live region, and change one of its lines a hundred times, resizing now and then: after
// every frame the terminal shows the printed lines and the live region as they should be, and the cursor is in the input line.
func TestLiveRegionUpdatesInPlaceAcrossResizes(t *testing.T) {
	h := newHarness(t, termCaps(40, 12))
	m := newModel(40, 12)
	for i := 0; i < 25; i++ {
		l := txt(fmt.Sprintf("history line %02d", i))
		h.r.Print(l)
		m.print(l)
	}
	sizes := [][2]int{{40, 12}, {30, 12}, {25, 10}, {60, 14}, {40, 12}, {17, 12}, {80, 16}, {40, 12}}
	for i := 0; i < 100; i++ {
		live := txts("status: working", fmt.Sprintf("tick %03d of 100 (the line that changes)", i), "> input")
		if i%2 == 0 { // the changing line also changes length and style, so rows grow and shrink
			live[1] = cell.Join(cell.Styled(testStyles[i%len(testStyles)], fmt.Sprintf("tick %03d", i)), cell.Text(strings.Repeat("·", i%23)))
		}
		h.r.SetLive(live)
		m.live = live
		h.r.SetCursor(2, 2+i%5)
		m.caretOn, m.caretR, m.caretC = true, 2, 2+i%5
		if i%13 == 7 {
			sz := sizes[(i/13)%len(sizes)]
			h.resize(sz[0], sz[1])
			m.cols, m.rows = sz[0], sz[1]
		}
		h.flush()
		m.flush()
		h.check(m)
		if t.Failed() {
			t.Fatalf("stopped at update %d", i)
		}
	}
}

// randomRun plays one random sequence of Print, SetLive, SetCursor, Resize and Flush against the renderer and the model, and
// compares them after every frame. The terminal is between minCols and maxCols wide and minRows and maxRows high, and is resized
// at random when resize is set.
func randomRun(t *testing.T, seed int64, minCols, maxCols, minRows, maxRows int, resize bool) {
	rng := rand.New(rand.NewSource(seed))
	size := func() (int, int) { return minCols + rng.Intn(maxCols-minCols+1), minRows + rng.Intn(maxRows-minRows+1) }
	cols, rows := size()
	h := newHarness(t, termCaps(cols, rows))
	m := newModel(cols, rows)
	for step := 0; step < 60; step++ {
		switch op := rng.Intn(12); {
		case op < 3:
			ls := randLines(rng, rng.Intn(5), 70)
			h.r.Print(ls...)
			m.print(ls...)
		case op < 6:
			ls := randLines(rng, rng.Intn(4), 40)
			h.r.SetLive(ls)
			m.live = ls
		case op == 6 && len(m.live) > 0: // change one line, leave the others: the incremental path
			ls := append([]cell.Line(nil), m.live...)
			ls[rng.Intn(len(ls))] = randLine(rng, 40)
			h.r.SetLive(ls)
			m.live = ls
		case op == 7:
			r, c := rng.Intn(6)-1, rng.Intn(70)
			h.r.SetCursor(r, c)
			m.caretOn, m.caretR, m.caretC = r >= 0, r, c
		case op == 8 && resize:
			cols, rows = size()
			h.resize(cols, rows)
			m.cols, m.rows = cols, rows
		case op == 9 && len(m.live) > 0: // exactly as wide as the terminal
			ls := append([]cell.Line(nil), m.live...)
			ls[rng.Intn(len(ls))] = txt(strings.Repeat("x", cols))
			h.r.SetLive(ls)
			m.live = ls
		}
		if rng.Intn(3) != 0 {
			h.flush()
			m.flush()
			h.check(m)
			if t.Failed() {
				t.Fatalf("stopped at step %d", step)
			}
		}
	}
	h.flush()
	m.flush()
	h.check(m)
}

// Random sequences of Print, SetLive, SetCursor, Resize and Flush always agree with the model.
func TestRandomOperationsAgreeWithTheModel(t *testing.T) {
	for seed := int64(1); seed <= 80; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { randomRun(t, seed, 20, 60, 16, 24, true) })
	}
}

// On a small terminal the live region is often taller than the screen and is cut from the top. A terminal cannot give back rows
// that scrolled away, so these sequences do not resize.
func TestRandomOperationsOnASmallTerminalAgreeWithTheModel(t *testing.T) {
	for seed := int64(1); seed <= 60; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { randomRun(t, 1000+seed, 8, 30, 2, 6, false) })
	}
}
