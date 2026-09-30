package svg

import (
	"encoding/xml"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/vt"
)

func wellFormed(t *testing.T, doc string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(doc))
	for {
		if _, err := d.Token(); err == io.EOF {
			return
		} else if err != nil {
			t.Fatalf("not well-formed XML: %v\n%.400s", err, doc)
		}
	}
}

// frame makes a frame from lines of plain text, one cell per rune.
func frame(at time.Duration, lines ...string) Frame {
	f := Frame{At: at}
	for _, l := range lines {
		var row []Cell
		for _, r := range l {
			row = append(row, Cell{Text: string(r)})
		}
		f.Rows = append(f.Rows, row)
	}
	return f
}

func TestStaticIsWellFormedAndDrawsWhatTheTerminalShowed(t *testing.T) {
	term := vt.New(40, 6)
	term.WriteString("\x1b[1;31mred bold\x1b[0m \x1b[2mdim\x1b[0m \x1b[7mreverse\x1b[0m\r\n")
	term.WriteString("<tag> & \"quoted\" 'single'\r\n")
	term.WriteString("wide: \u754c\u9762 combining: e\u0301\r\n")
	term.WriteString("\x1b[38;2;10;200;30mtruecolor\x1b[0m \x1b[48;5;196mindexed bg\x1b[0m\r\n")
	term.WriteString("\u2580\u2584\u2588\u2592 \u256d\u2500\u256e \u2502 \u2570\u2500\u256f")
	doc := Static(Capture(term, 0), Theme{Title: "sleipnir"})
	wellFormed(t, doc)
	for _, want := range []string{`viewBox=`, `>red</text>`, `>bold</text>`, `>&lt;tag&gt;</text>`, `>&amp;</text>`, `>&quot;quoted&quot;</text>`, `#ff7b72`, `#0ac81e`, `#ff0000`, `class="b"`, `opacity="0.62"`, "sleipnir", `>界</text>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %q:\n%s", want, doc)
		}
	}
	if !strings.Contains(doc, "é") && !strings.Contains(doc, "e\u0301") {
		t.Errorf("a combining mark was lost:\n%s", doc)
	}
	if doc != Static(Capture(term, 0), Theme{Title: "sleipnir"}) {
		t.Error("the same screen must give the same bytes")
	}
}

func TestBlocksAndLinesAreShapesNotGlyphs(t *testing.T) {
	f := frame(0, "\u2580\u2584\u2588\u2592\u2593\u2591\u258c\u2599", "\u256d\u2500\u256e\u2502\u2570\u256f\u254e\u2503")
	doc := Static(f, Theme{})
	wellFormed(t, doc)
	for _, r := range "\u2580\u2584\u2588\u2592\u2593\u2591\u258c\u2599\u256d\u2500\u256e\u2502\u2570\u256f\u254e\u2503" {
		if strings.Contains(doc, string(r)) {
			t.Errorf("%q is in the document as text: fonts leave gaps between rows of it", r)
		}
	}
	if strings.Count(doc, "<rect") < 8 {
		t.Errorf("blocks should be rectangles:\n%s", doc)
	}
	for _, want := range []string{`Q`, `stroke-dasharray`, `stroke-width="2.3"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the lines lack %q:\n%s", want, doc)
		}
	}
	// a full block covers its whole cell (and a little more, so neighbours do not show a seam)
	if !strings.Contains(Static(frame(0, "\u2588"), Theme{}), `width="8.65" height="18.25"`) {
		t.Errorf("a full block must fill the cell:\n%s", Static(frame(0, "\u2588"), Theme{}))
	}
}

// placed is a <text> element of a document: where it is, how wide it is pinned (-1 when it is not) and what it says.
type placed struct {
	x, length float64
	adjust    string
	text      string
}

var textRe = regexp.MustCompile(`<text x="([0-9.]+)" y="[0-9.]+"(?: textLength="([0-9.]+)" lengthAdjust="(\w+)")?[^>]*>([^<]*)</text>`)

func texts(t *testing.T, doc string) []placed {
	t.Helper()
	var out []placed
	for _, m := range textRe.FindAllStringSubmatch(doc, -1) {
		p := placed{length: -1, adjust: m[3], text: m[4]}
		p.x, _ = strconv.ParseFloat(m[1], 64)
		if m[2] != "" {
			p.length, _ = strconv.ParseFloat(m[2], 64)
		}
		out = append(out, p)
	}
	return out
}

func TestTextIsPinnedToTheCellGrid(t *testing.T) {
	doc := Static(frame(0, "  ab  cd"), Theme{CellW: 10})
	wellFormed(t, doc)
	// each word is at its own column, pinned to its own cells: "ab" from column 2, "cd" from column 6
	got := texts(t, doc)
	want := []placed{{20, 20, "spacing", "ab"}, {60, 20, "spacing", "cd"}}
	if len(got) != len(want) {
		t.Fatalf("got %d text elements, want 2:\n%s", len(got), doc)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("text %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The picture that found this: a word followed by padding was stretched across the padding ("m a n a g e r"), and a symbol
// wider than the cell in the viewer's font pushed the words after it into each other ("rpm212/240"). Nothing a word or a symbol
// does may change where another one is, and no word is pinned to more cells than it has letters.
func TestWordsAndSymbolsDoNotMoveEachOther(t *testing.T) {
	const cw = 10
	row := "m0 manager   \u2819 think  docs   \u2699 tool $0.012  96%"
	doc := Static(frame(0, row), Theme{CellW: cw})
	wellFormed(t, doc)
	got := texts(t, doc)
	var want []placed
	// expected placement, derived independently: split on spaces, symbols alone
	i := 0
	rs := []rune(row)
	for i < len(rs) {
		switch {
		case rs[i] == ' ':
			i++
		case rs[i] > 0x7e:
			if rs[i] == 0x2819 { // braille: a shape, not text
				i++
				continue
			}
			want = append(want, placed{float64(i * cw), cw, "spacingAndGlyphs", string(rs[i])})
			i++
		default:
			j := i
			for j < len(rs) && rs[j] > ' ' && rs[j] < 0x7f {
				j++
			}
			p := placed{float64(i * cw), float64((j - i) * cw), "spacing", string(rs[i:j])}
			if j-i == 1 {
				p.length, p.adjust = -1, ""
			}
			want = append(want, p)
			i = j
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d text elements, want %d:\n%+v\n%+v", len(got), len(want), got, want)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Errorf("text %d is %+v, want %+v", k, got[k], want[k])
		}
	}
}

func TestBrailleAndProgressBarsAreShapes(t *testing.T) {
	doc := Static(frame(0, "\u2819\u28ff\u2800\u25b0\u25b1"), Theme{})
	wellFormed(t, doc)
	for _, r := range "\u2819\u28ff\u25b0\u25b1" {
		if strings.Contains(doc, string(r)) {
			t.Errorf("%q is in the document as text", r)
		}
	}
	// \u2819 has dots 1, 4 and 5; \u28ff has all eight; the blank pattern has none
	if n := strings.Count(doc, "<circle"); n != 3+8 {
		t.Errorf("%d dots drawn, want 11:\n%s", n, doc)
	}
	if strings.Count(doc, "<path") != 2 {
		t.Errorf("want one filled and one outlined parallelogram:\n%s", doc)
	}
}

func TestDecorationsAreLinesThatCrossTheSpaces(t *testing.T) {
	f := frame(0, "ab cd")
	for i := range f.Rows[0] {
		f.Rows[0][i].Style = cell.Style{}.With(cell.Underline)
	}
	doc := Static(f, Theme{CellW: 10})
	wellFormed(t, doc)
	if strings.Contains(doc, "text-decoration") || strings.Contains(doc, `class="u"`) {
		t.Errorf("a text decoration stops at the gaps between words:\n%s", doc)
	}
	if !strings.Contains(doc, `width="50" height="1.1"`) {
		t.Errorf("the underline should be one line across the five cells:\n%s", doc)
	}
}

func TestColoursReachTheDocument(t *testing.T) {
	th := DefaultTheme()
	for _, tc := range []struct {
		c    cell.Color
		want string
	}{
		{cell.ANSI(1), th.ANSI[1]}, {cell.ANSI(15), th.ANSI[15]}, {cell.Indexed(196), "#ff0000"}, {cell.Indexed(16), "#000000"},
		{cell.Indexed(231), "#ffffff"}, {cell.Indexed(232), "#080808"}, {cell.Indexed(255), "#eeeeee"}, {cell.Indexed(5), th.ANSI[5]},
		{cell.RGB(1, 2, 255), "#0102ff"}, {cell.Default(), "DEFAULT"},
	} {
		if got := th.hex(tc.c, "DEFAULT"); got != tc.want {
			t.Errorf("%+v = %s, want %s", tc.c, got, tc.want)
		}
	}
	f := frame(0, "x")
	f.Rows[0][0].Style = cell.Style{FG: cell.ANSI(2), BG: cell.ANSI(4)}.With(cell.Reverse)
	doc := Static(f, Theme{})
	if !strings.Contains(doc, `fill="`+th.ANSI[2]+`"`) || !strings.Contains(doc, `fill="`+th.ANSI[4]+`"`) {
		t.Errorf("reverse video must swap the colours:\n%s", doc)
	}
}

func TestHostileTextCannotBreakTheDocument(t *testing.T) {
	f := frame(0, "a\x00b\x1b[31mc\u0085d</text><script>alert(1)</script>")
	doc := Static(f, Theme{Title: `"><script>`})
	wellFormed(t, doc)
	if strings.Contains(doc, "<script") || strings.Contains(doc, "\x1b") || strings.Contains(doc, "\x00") {
		t.Errorf("the document carries what it was given:\n%s", doc)
	}
}

func TestAnimatedShowsEachVersionOfARowWhenItWasThere(t *testing.T) {
	frames := []Frame{
		frame(0, "one  ", "same "),
		frame(1*time.Second, "two  ", "same "),
		frame(3*time.Second, "three", "same "),
	}
	doc := Animated(frames, Theme{}, Options{Hold: time.Second}) // 4 seconds in all
	wellFormed(t, doc)
	if strings.Count(doc, "same") != 1 {
		t.Errorf("a row that never changes is drawn once, without an animation:\n%s", doc)
	}
	for _, want := range []string{
		"0%{opacity:1}25%{opacity:0}",               // "one": the first quarter
		"0%{opacity:0}25%{opacity:1}75%{opacity:0}", // "two": the middle half
		"0%{opacity:0}75%{opacity:1}",               // "three": the last quarter, to the end
		"animation-duration:4s", "animation-iteration-count:infinite",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the animation lacks %q:\n%s", want, doc)
		}
	}
	once := Animated(frames, Theme{}, Options{Once: true})
	if !strings.Contains(once, "animation-iteration-count:1;") || !strings.Contains(once, "animation-fill-mode:forwards") {
		t.Error("Once must play one time and stay on the last frame")
	}
	if doc != Animated(frames, Theme{}, Options{Hold: time.Second}) {
		t.Error("the same frames must give the same bytes")
	}
}

func TestAnimatedReusesKeyframesAndRowsThatComeBack(t *testing.T) {
	frames := []Frame{frame(0, "a", "x"), frame(time.Second, "b", "x"), frame(2*time.Second, "a", "y"), frame(3*time.Second, "b", "y")}
	doc := Animated(frames, Theme{}, Options{Hold: time.Second})
	wellFormed(t, doc)
	// row 0 alternates a b a b: two versions, each visible twice, not four copies
	if n := strings.Count(doc, ">a</text>"); n != 1 {
		t.Errorf("a row that comes back is drawn once, got %d copies of it:\n%s", n, doc)
	}
	if !strings.Contains(doc, "0%{opacity:1}25%{opacity:0}50%{opacity:1}75%{opacity:0}") {
		t.Errorf("a version that is visible twice needs both intervals:\n%s", doc)
	}
}

func TestAnimatedCursorMoves(t *testing.T) {
	a, b := frame(0, "ab"), frame(time.Second, "ab")
	a.CursorOn, a.CursorX = true, 0
	b.CursorOn, b.CursorX = true, 1
	doc := Animated([]Frame{a, b}, Theme{}, Options{Hold: time.Second})
	wellFormed(t, doc)
	if strings.Count(doc, `opacity="0.7"`) != 2 {
		t.Errorf("one cursor rectangle per place:\n%s", doc)
	}
}

func TestEmptyAnimationIsStillADocument(t *testing.T) {
	wellFormed(t, Animated(nil, Theme{}, Options{}))
	wellFormed(t, Static(Frame{}, Theme{}))
}
