package render

import (
	"bytes"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
)

// The golden table of colour down-sampling: an RGB colour, the entry of the 256-colour palette the renderer sends to a
// 256-colour terminal, and the standard colour it sends to a 16-colour one. Every row was read, not just recorded: the 256 column
// is the nearest cube or grey entry, and the 16 column keeps the hue (green stays green, red stays red).
var colorGolden = []struct {
	name    string
	r, g, b uint8
	c256    uint8
	c16     uint8
}{
	{"black", 0, 0, 0, 16, 0},
	{"white", 255, 255, 255, 231, 15},
	{"red", 255, 0, 0, 196, 9},
	{"green", 0, 255, 0, 46, 10},
	{"blue", 0, 0, 255, 21, 12},
	{"yellow", 255, 255, 0, 226, 11},
	{"cyan", 0, 255, 255, 51, 14},
	{"magenta", 255, 0, 255, 201, 13},
	{"mid grey", 128, 128, 128, 244, 8},
	{"dark grey 282828", 40, 40, 40, 235, 0},
	{"grey 444444", 68, 68, 68, 238, 8},
	{"silver c0c0c0", 192, 192, 192, 250, 7},
	{"light grey c8c8c8", 200, 200, 200, 251, 7},
	{"near white eeeeee", 238, 238, 238, 255, 15},
	{"orange ffa500", 255, 165, 0, 214, 11},
	{"dark red 8b0000", 139, 0, 0, 88, 1},
	{"teal 008080", 0, 128, 128, 30, 6},
	{"indigo 4b0082", 75, 0, 130, 54, 5},
	{"pink ffc0cb", 255, 192, 203, 218, 9},
	{"brown a52a2a", 165, 42, 42, 124, 1},
	{"olive 808000", 128, 128, 0, 100, 3},
	{"navy 000080", 0, 0, 128, 18, 4},
	{"ui blue 7aa2f7", 0x7a, 0xa2, 0xf7, 111, 12},
	{"ui indigo 6366f1", 0x63, 0x66, 0xf1, 63, 12},
	{"ui teal 1abc9c", 0x1a, 0xbc, 0x9c, 37, 14},
	{"ui green 9ece6a", 0x9e, 0xce, 0x6a, 149, 10},
	{"soft green 98c379", 0x98, 0xc3, 0x79, 108, 10},
	{"ui amber e0af68", 0xe0, 0xaf, 0x68, 179, 11},
	{"ui orange ff9e64", 0xff, 0x9e, 0x64, 215, 11},
	{"ui red f7768e", 0xf7, 0x76, 0x8e, 210, 9},
	{"soft red e06c75", 0xe0, 0x6c, 0x75, 168, 9},
	{"ui violet bb9af7", 0xbb, 0x9a, 0xf7, 141, 13},
	{"violet c678dd", 0xc6, 0x78, 0xdd, 176, 13},
	{"background 1a1b26", 0x1a, 0x1b, 0x26, 234, 0},
	{"panel 24283b", 0x24, 0x28, 0x3b, 236, 0},
	{"selection 33467c", 0x33, 0x46, 0x7c, 60, 4},
	{"cube colour 5f87af", 0x5f, 0x87, 0xaf, 67, 12},
}

func TestColorDownsamplingGolden(t *testing.T) {
	for _, c := range colorGolden {
		if got := nearest256(c.r, c.g, c.b); got != c.c256 {
			t.Errorf("%s: 256-colour entry %d, want %d", c.name, got, c.c256)
		}
		if got := toANSI16(c.r, c.g, c.b); got != c.c16 {
			t.Errorf("%s: 16-colour entry %d, want %d", c.name, got, c.c16)
		}
	}
}

func TestPaletteEntries(t *testing.T) {
	for n, want := range map[uint8][3]uint8{16: {0, 0, 0}, 21: {0, 0, 255}, 196: {255, 0, 0}, 231: {255, 255, 255}, 59: {95, 95, 95}, 232: {8, 8, 8}, 244: {128, 128, 128}, 255: {238, 238, 238}} {
		if r, g, b := paletteRGB(n); [3]uint8{r, g, b} != want {
			t.Errorf("entry %d is (%d,%d,%d), want %v", n, r, g, b, want)
		}
	}
	// An exact palette colour maps to itself: the cube and the greys are what the search ranges over.
	for n := 16; n < 256; n++ {
		r, g, b := paletteRGB(uint8(n))
		got := nearest256(r, g, b)
		gr, gg, gb := paletteRGB(got)
		if [3]uint8{gr, gg, gb} != [3]uint8{r, g, b} {
			t.Errorf("entry %d maps to entry %d, a different colour", n, got)
		}
	}
}

// Whatever the UI does with colour, hue is what tells the roles apart on a terminal with 16 colours: the soft green of "good"
// must not come out as a grey, and red must not come out yellow.
func TestSixteenColoursKeepTheHueOfTheRoles(t *testing.T) {
	roles := []struct {
		name string
		hex  string
		want [2]uint8 // the normal and the bright variant of the expected hue
	}{
		{"indigo", "#6366f1", [2]uint8{4, 12}}, {"blue", "#7aa2f7", [2]uint8{4, 12}}, {"teal", "#1abc9c", [2]uint8{6, 14}},
		{"green", "#9ece6a", [2]uint8{2, 10}}, {"soft green", "#98c379", [2]uint8{2, 10}}, {"dark green", "#2e7d32", [2]uint8{2, 10}},
		{"yellow", "#e0af68", [2]uint8{3, 11}}, {"orange", "#ff9e64", [2]uint8{3, 11}},
		{"red", "#f7768e", [2]uint8{1, 9}}, {"soft red", "#e06c75", [2]uint8{1, 9}}, {"dark red", "#b71c1c", [2]uint8{1, 9}},
		{"violet", "#bb9af7", [2]uint8{5, 13}}, {"purple", "#c678dd", [2]uint8{5, 13}},
	}
	for _, r := range roles {
		c := cell.Hex(r.hex)
		got := toANSI16(c.R, c.G, c.B)
		if got != r.want[0] && got != r.want[1] {
			t.Errorf("%s %s maps to %d, want %d or %d", r.name, r.hex, got, r.want[0], r.want[1])
		}
	}
	// Every hue at full saturation and value: the pure hues land on their colours.
	pure := map[[3]uint8]uint8{{255, 0, 0}: 9, {255, 255, 0}: 11, {0, 255, 0}: 10, {0, 255, 255}: 14, {0, 0, 255}: 12, {255, 0, 255}: 13}
	for rgb, want := range pure {
		if got := toANSI16(rgb[0], rgb[1], rgb[2]); got != want {
			t.Errorf("%v maps to %d, want %d", rgb, got, want)
		}
	}
	// Greys go by lightness and only by lightness, in the order black, dark grey, silver, white.
	rank := map[uint8]int{0: 0, 8: 1, 7: 2, 15: 3}
	last := 0
	for v := 0; v < 256; v++ {
		got, ok := rank[toANSI16(uint8(v), uint8(v), uint8(v))]
		if !ok || got < last {
			t.Fatalf("grey %d is not in lightness order (rank %d after %d)", v, got, last)
		}
		last = got
	}
	// Whatever the input, the answer is one of the 16 and never depends on anything else.
	for r := 0; r < 256; r += 15 {
		for g := 0; g < 256; g += 15 {
			for b := 0; b < 256; b += 15 {
				if toANSI16(uint8(r), uint8(g), uint8(b)) > 15 {
					t.Fatalf("(%d,%d,%d) is outside the 16", r, g, b)
				}
			}
		}
	}
}

func TestColorMapResolvesAtEveryDepth(t *testing.T) {
	cases := []struct {
		name  string
		depth term.ColorDepth
		in    cell.Color
		want  wireColor
	}{
		{"default stays default", term.ColorANSI16, cell.Default(), wireColor{}},
		{"ansi is ansi at 16", term.ColorANSI16, cell.ANSI(3), wireColor{kind: wireANSI, n: 3}},
		{"ansi is ansi at 256", term.ColorANSI256, cell.ANSI(11), wireColor{kind: wireANSI, n: 11}},
		{"ansi is ansi at truecolor", term.ColorTrueColor, cell.ANSI(9), wireColor{kind: wireANSI, n: 9}},
		{"indexed at 256", term.ColorANSI256, cell.Indexed(200), wireColor{kind: wireIndexed, n: 200}},
		{"indexed at truecolor", term.ColorTrueColor, cell.Indexed(99), wireColor{kind: wireIndexed, n: 99}},
		{"a low indexed is an ansi colour at 16", term.ColorANSI16, cell.Indexed(5), wireColor{kind: wireANSI, n: 5}},
		{"a palette colour at 16 keeps its hue", term.ColorANSI16, cell.Indexed(196), wireColor{kind: wireANSI, n: 9}},
		{"a grey of the ramp at 16", term.ColorANSI16, cell.Indexed(244), wireColor{kind: wireANSI, n: 8}},
		{"rgb at truecolor", term.ColorTrueColor, cell.RGB(1, 2, 3), wireColor{kind: wireRGB, r: 1, g: 2, b: 3}},
		{"rgb at 256", term.ColorANSI256, cell.RGB(255, 0, 0), wireColor{kind: wireIndexed, n: 196}},
		{"rgb at 16", term.ColorANSI16, cell.RGB(255, 0, 0), wireColor{kind: wireANSI, n: 9}},
		{"nothing at none", term.ColorNone, cell.RGB(255, 0, 0), wireColor{}},
		{"not even ansi at none", term.ColorNone, cell.ANSI(1), wireColor{}},
	}
	for _, c := range cases {
		if got := newColorMap(c.depth).resolve(c.in); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestNearestColourIsCachedPerRenderer(t *testing.T) {
	a, b := newColorMap(term.ColorANSI256), newColorMap(term.ColorANSI16)
	if len(a.cache) != 0 {
		t.Fatal("a new map starts empty")
	}
	c := cell.RGB(10, 200, 30)
	first := a.resolve(c)
	if len(a.cache) != 1 {
		t.Fatalf("the search result is cached: %d entries", len(a.cache))
	}
	a.cache[uint32(10)<<16|uint32(200)<<8|30] = 99 // a cache hit is used, not recomputed
	if got := a.resolve(c); got.n != 99 || got == first {
		t.Errorf("the cached entry was not used: %+v", got)
	}
	if len(b.cache) != 0 {
		t.Error("renderers do not share a cache: there is no global state")
	}
	// The 16 and the 256 answers for one colour do not collide.
	both := newColorMap(term.ColorANSI256)
	both.down(10, 200, 30, false)
	both.down(10, 200, 30, true)
	if len(both.cache) != 2 {
		t.Errorf("one key for two answers: %d entries", len(both.cache))
	}
}

func TestSGRTransitions(t *testing.T) {
	red := wireColor{kind: wireANSI, n: 1}
	green := wireColor{kind: wireANSI, n: 2}
	brightBlue := wireColor{kind: wireANSI, n: 12}
	blueBG := wireColor{kind: wireANSI, n: 4}
	idx := wireColor{kind: wireIndexed, n: 99}
	rgbRed, rgbBlue := wireColor{kind: wireRGB, r: 255}, wireColor{kind: wireRGB, b: 255}
	bold := wireStyle{attr: cell.Bold}
	cases := []struct {
		name     string
		from, to wireStyle
		want     string
	}{
		{"nothing changes", bold, bold, ""},
		{"default to bold", wireStyle{}, bold, "\x1b[1m"},
		{"bold to default", bold, wireStyle{}, "\x1b[0m"},
		{"bold to bold and underline", bold, wireStyle{attr: cell.Bold | cell.Underline}, "\x1b[4m"},
		{"underline off", wireStyle{attr: cell.Bold | cell.Underline}, bold, "\x1b[24m"},
		{"dim off keeps bold: a reset is shorter", wireStyle{attr: cell.Bold | cell.Dim}, bold, "\x1b[0;1m"},
		{"bold off keeps dim: a reset is shorter", wireStyle{attr: cell.Bold | cell.Dim}, wireStyle{attr: cell.Dim}, "\x1b[0;2m"},
		{"bold to dim", bold, wireStyle{attr: cell.Dim}, "\x1b[0;2m"},
		{"dim off keeps bold and colours: 22 clears both, so bold is set again", wireStyle{fg: red, bg: blueBG, attr: cell.Bold | cell.Dim}, wireStyle{fg: red, bg: blueBG, attr: cell.Bold}, "\x1b[22;1m"},
		{"bold off keeps dim and colours", wireStyle{fg: red, bg: blueBG, attr: cell.Bold | cell.Dim}, wireStyle{fg: red, bg: blueBG, attr: cell.Dim}, "\x1b[22;2m"},
		{"italic off", wireStyle{attr: cell.Italic | cell.Bold}, bold, "\x1b[23m"},
		{"reverse and strike on", wireStyle{}, wireStyle{attr: cell.Reverse | cell.Strike}, "\x1b[7;9m"},
		{"foreground", wireStyle{}, wireStyle{fg: red}, "\x1b[31m"},
		{"foreground to another", wireStyle{fg: red}, wireStyle{fg: green}, "\x1b[32m"},
		{"foreground to default keeps the rest", wireStyle{fg: red, attr: cell.Bold}, bold, "\x1b[39m"},
		{"bright colours", wireStyle{}, wireStyle{fg: brightBlue, bg: blueBG}, "\x1b[94;44m"},
		{"background to default", wireStyle{fg: red, bg: blueBG}, wireStyle{fg: red}, "\x1b[49m"},
		{"indexed", wireStyle{}, wireStyle{fg: idx, bg: idx}, "\x1b[38;5;99;48;5;99m"},
		{"truecolor", wireStyle{fg: rgbRed}, wireStyle{fg: rgbBlue}, "\x1b[38;2;0;0;255m"},
		{"a reset is shorter than undoing this", wireStyle{fg: red, bg: blueBG, attr: cell.Bold | cell.Italic | cell.Underline}, bold, "\x1b[0;1m"},
		{"everything at once from default", wireStyle{}, wireStyle{fg: red, bg: blueBG, attr: cell.Bold | cell.Underline}, "\x1b[1;4;31;44m"},
	}
	for _, c := range cases {
		if got := sgr(c.from, c.to); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Whatever sgr writes must leave a terminal in the style it was asked for: feed it to the emulator and read the style back.
func TestSGRTransitionsReachTheirStyle(t *testing.T) {
	m := newColorMap(term.ColorTrueColor)
	var z cell.Style
	styles := []cell.Style{
		{}, z.With(cell.Bold), z.With(cell.Bold | cell.Dim), z.With(cell.Dim), z.Fg(cell.ANSI(1)), z.Fg(cell.ANSI(9)).Bg(cell.ANSI(4)),
		z.Fg(cell.Indexed(99)).With(cell.Italic | cell.Underline), z.Fg(cell.RGB(1, 2, 3)).Bg(cell.RGB(4, 5, 6)).With(cell.Reverse | cell.Strike),
		z.Bg(cell.Indexed(17)), z.With(cell.Bold | cell.Italic | cell.Underline | cell.Reverse | cell.Strike | cell.Dim),
	}
	for _, from := range styles {
		for _, to := range styles {
			h := newHarness(t, termCaps(10, 2))
			h.v.WriteString(sgr(wireStyle{}, m.wire(from)) + "a")
			h.v.WriteString(sgr(m.wire(from), m.wire(to)) + "b")
			if _, got := h.v.Cell(1, 0); got != to {
				t.Errorf("%+v -> %+v reached %+v", from, to, got)
			}
		}
	}
}

func colorOf(t *testing.T, depth term.ColorDepth, s cell.Style) cell.Style {
	t.Helper()
	c := termCaps(10, 3)
	c.Color = depth
	h := newHarness(t, c)
	h.r.SetLive([]cell.Line{cell.Styled(s, "x")})
	h.flush()
	_, got := h.v.Cell(0, 0)
	return got
}

func TestColoursAreDownsampledToTheTerminal(t *testing.T) {
	orange := cell.Style{}.Fg(cell.RGB(255, 165, 0)).Bg(cell.RGB(0, 0, 128)).With(cell.Bold)
	cases := []struct {
		depth term.ColorDepth
		want  cell.Style
	}{
		{term.ColorTrueColor, orange},
		{term.ColorANSI256, cell.Style{}.Fg(cell.Indexed(214)).Bg(cell.Indexed(18)).With(cell.Bold)},
		{term.ColorANSI16, cell.Style{}.Fg(cell.ANSI(11)).Bg(cell.ANSI(4)).With(cell.Bold)},
	}
	for _, c := range cases {
		if got := colorOf(t, c.depth, orange); got != c.want {
			t.Errorf("%v: %+v, want %+v", c.depth, got, c.want)
		}
	}
	// A terminal colour (ANSI) and an indexed one are not turned into each other.
	if got := colorOf(t, term.ColorTrueColor, cell.Style{}.Fg(cell.ANSI(2))); got.FG != cell.ANSI(2) {
		t.Errorf("ansi at truecolor: %+v", got)
	}
	if got := colorOf(t, term.ColorANSI16, cell.Style{}.Fg(cell.Indexed(196))); got.FG != cell.ANSI(9) {
		t.Errorf("indexed at 16: %+v", got)
	}
}

func TestStylesThatLookTheSameAreNotRewritten(t *testing.T) {
	c := termCaps(20, 4)
	c.Color = term.ColorANSI16
	h := newHarness(t, c)
	h.r.SetLive([]cell.Line{cell.Styled(cell.Style{}.Fg(cell.RGB(255, 0, 0)), "text")})
	h.flush()
	before := h.written()
	h.r.SetLive([]cell.Line{cell.Styled(cell.Style{}.Fg(cell.RGB(250, 10, 10)), "text")}) // another red that is the same red at 16
	h.flush()
	if h.written() != before {
		t.Errorf("a colour that maps to the same one rewrote the row: %q", h.b.frames[len(h.b.frames)-1])
	}
}

// With no colour at all the renderer writes no escape byte, whatever it is given (v).
func TestColorNoneOutputContainsNoEscapeByte(t *testing.T) {
	hostile := "a\x1b]52;c;aGVsbG8=\x07b\x1b[2Jc\x1b]8;;http://x\x07d\x1b]8;;\x07\u009b31m\rfoo\b\b\bbar"
	styled := cell.Join(cell.Styled(cell.Style{}.Fg(cell.RGB(1, 2, 3)).With(cell.Bold|cell.Underline), "styled "), cell.Styled(cell.Style{}.Bg(cell.ANSI(4)), "bg "), txt(hostile))
	for _, c := range []term.Caps{
		{Color: term.ColorNone, Width: 20, Height: 6, Unicode: true},                     // NO_COLOR on a terminal
		{Color: term.ColorNone, Width: 20, Height: 6, Dumb: true},                        // a pipe
		{Color: term.ColorTrueColor, Width: 20, Height: 6, Dumb: true, SyncOutput: true}, // a dumb terminal claiming colour
		{Color: term.ColorNone, Width: 20, Height: 6, SyncOutput: true, BracketedPaste: true},
	} {
		var out bytes.Buffer
		r := NewInline(&out, c, WithBracketedPaste(), KeepLive())
		r.Print(styled, txt("中文 and e\u0301 and a long line that has to be wrapped somewhere"))
		r.SetLive([]cell.Line{styled, txt("> input")})
		r.SetCursor(1, 3)
		r.Flush()
		r.Resize(12, 5)
		r.SetLive([]cell.Line{txt("changed"), styled})
		r.Flush()
		r.Print(txt("more"))
		r.Close()
		if out.Len() == 0 {
			t.Fatalf("%+v: nothing was written", c)
		}
		if i := bytes.IndexByte(out.Bytes(), 0x1b); i >= 0 {
			t.Errorf("%+v: an escape byte at %d in %q", c, i, out.String())
		}
		for _, b := range out.Bytes() {
			if b < 0x20 && b != '\n' && b != '\r' {
				t.Errorf("%+v: control byte %#x in %q", c, b, out.String())
				break
			}
		}
		if bytes.Contains(out.Bytes(), []byte("\u009b")) {
			t.Errorf("%+v: a C1 control in the output", c)
		}
	}
}
