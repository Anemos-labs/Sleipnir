package widget

// Tests of the private helpers of the signature widgets. Every name starts with "Show" or "show" so that they cannot collide with
// the internal tests of the text widgets that share this package.

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

func TestShowTokens(t *testing.T) {
	cases := map[int]string{
		-5: "0", 0: "0", 7: "7", 812: "812", 999: "999", 1000: "1.0k", 2400: "2.4k", 2449: "2.4k", 2450: "2.5k", 9999: "10.0k",
		31200: "31.2k", 41200: "41.2k", 99949: "99.9k", 99950: "100k", 312000: "312k", 999499: "999k", 999500: "1.0M", 1_250_000: "1.3M",
		12_345_678: "12.3M", 99_949_999: "99.9M", 99_950_000: "100M", 5_000_000_000: "5000M",
	}
	for n, want := range cases {
		if got := showTokens(n); got != want {
			t.Errorf("showTokens(%d) = %q, want %q", n, got, want)
		}
	}
	if got := showTokens(math.MaxInt); !strings.HasSuffix(got, "M") {
		t.Errorf("a huge count: %q", got)
	}
	// it never gets shorter as the count grows by a thousand, and never writes 1000k
	for n := 0; n < 3_000_000; n += 997 {
		if s := showTokens(n); strings.HasPrefix(s, "1000") || strings.Contains(s, "1000.") {
			t.Fatalf("showTokens(%d) = %q", n, s)
		}
	}
}

func TestShowClockAndElapsed(t *testing.T) {
	cases := []struct {
		d         time.Duration
		clock, el string
	}{
		{-time.Second, "0:00", "00:00"}, {0, "0:00", "00:00"}, {time.Millisecond, "0:01", "00:00"}, {time.Second, "0:01", "00:01"},
		{252 * time.Second, "4:12", "04:12"}, {59*time.Minute + 59*time.Second, "59:59", "59:59"}, {time.Hour, "1:00:00", "1:00:00"},
		{time.Hour + time.Second/2, "1:00:01", "1:00:00"}, {1000 * time.Hour, "99:59:59", "99:59:59"}, {math.MaxInt64, "99:59:59", "99:59:59"},
	}
	for _, c := range cases {
		if got := showClock(c.d); got != c.clock {
			t.Errorf("showClock(%v) = %q, want %q", c.d, got, c.clock)
		}
		if got := showElapsed(c.d); got != c.el {
			t.Errorf("showElapsed(%v) = %q, want %q", c.d, got, c.el)
		}
	}
}

func TestShowRoundingAndClamps(t *testing.T) {
	if showDiv(5, 2) != 3 || showDiv(4, 2) != 2 || showDiv(0, 3) != 0 || showDiv(-4, 3) != 0 || showDiv(4, 0) != 0 || showDiv(1, 3) != 0 || showDiv(2, 3) != 1 {
		t.Error("showDiv rounds half up and is 0 for nothing or a bad divisor")
	}
	for _, f := range []float64{math.NaN(), -1, math.Inf(-1)} {
		if showUnit(f) != 0 || showPermille(f) != 0 {
			t.Errorf("%v is 0", f)
		}
	}
	for _, f := range []float64{1, 2, math.Inf(1), 1e300} {
		if showUnit(f) != 1 || showPermille(f) != 1000 {
			t.Errorf("%v is 1", f)
		}
	}
	if showPermille(0.25) != 250 || showPermille(0.0004) != 0 || showPermille(0.0005) != 1 {
		t.Errorf("permille: %d %d %d", showPermille(0.25), showPermille(0.0004), showPermille(0.0005))
	}
	if showPercent(0.926) != "93%" || showPercent(math.NaN()) != "0%" || showPercent(7) != "100%" {
		t.Errorf("percent: %s %s %s", showPercent(0.926), showPercent(math.NaN()), showPercent(7))
	}
	if showClamp(5, 1, 3) != 3 || showClamp(-5, 1, 3) != 1 || showClamp(2, 1, 3) != 2 {
		t.Error("clamp")
	}
	if showTok(-3) != 0 || showTok(math.MaxInt) != showMaxTokens {
		t.Error("tokens are clamped")
	}
}

// Largest remainder: the shares add up to the total, each is its quota rounded down or up, and a weight of nothing gets nothing.
func TestShowSharesIsTheLargestRemainderRule(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 3000; i++ {
		weights := make([]int, r.Intn(9))
		var sum int64
		pos := 0
		for k := range weights {
			switch r.Intn(5) {
			case 0:
				weights[k] = 0
			case 1:
				weights[k] = -r.Intn(10)
			case 2:
				weights[k] = 1 << 40
			default:
				weights[k] = r.Intn(50000)
			}
			if weights[k] > 0 {
				sum += int64(weights[k])
				pos++
			}
		}
		total := r.Intn(200)
		for _, minOne := range []bool{false, true} {
			got := showShares(weights, total, minOne)
			if len(got) != len(weights) {
				t.Fatalf("%d shares for %d weights", len(got), len(weights))
			}
			s := 0
			for k, g := range got {
				s += g
				if weights[k] <= 0 && g != 0 {
					t.Fatalf("weight %d got %d cells", weights[k], g)
				}
				if g < 0 {
					t.Fatalf("a negative share: %v", got)
				}
			}
			if pos == 0 || total == 0 {
				if s != 0 {
					t.Fatalf("nothing to share but %v", got)
				}
				continue
			}
			if s != total {
				t.Fatalf("weights %v total %d minOne %v: the shares %v add up to %d", weights, total, minOne, got, s)
			}
			if minOne {
				if total >= pos {
					for k, g := range got {
						if weights[k] > 0 && g < 1 {
							t.Fatalf("weights %v total %d: layer %d has no cell: %v", weights, total, k, got)
						}
					}
				}
				continue
			}
			for k, g := range got { // each share is its exact quota rounded down or up
				if weights[k] <= 0 {
					continue
				}
				q := float64(total) * float64(weights[k]) / float64(sum)
				if float64(g) < math.Floor(q)-1e-9 || float64(g) > math.Ceil(q)+1e-9 {
					t.Fatalf("weights %v total %d: share %d is %d, its quota is %v", weights, total, k, g, q)
				}
			}
		}
	}
	// a fixed case: 41.2k in three layers over 56 cells, every cell accounted for
	if got := showShares([]int{14000, 17200, 10000}, 56, true); got[0]+got[1]+got[2] != 56 || got[0] != 19 || got[1] != 23 || got[2] != 14 {
		t.Errorf("%v", got)
	}
	// fewer cells than layers: the biggest layers get one each, the earlier one on a tie
	if got := showShares([]int{5, 9, 9, 1}, 2, true); got[0] != 0 || got[1] != 1 || got[2] != 1 || got[3] != 0 {
		t.Errorf("%v", got)
	}
}

func TestShowClean(t *testing.T) {
	cases := map[string]string{
		"plain text": "plain text", "": "", "a\x1b[31mb": "a [31mb", "a\nb\r\nc\td": "a b  c d", "\x00\x7f\u009b": "   ", "x\u202ey": "xy",
		"\u200b\u200f\ufeffz": "z", "e\u0301": "e\u0301", "\u0301e": "e", "\u200d\u200dx": "x", "a\u200db": "a\u200db", "\xff": "�",
		"\u2028x\u2029": " x ", "中文 🐎": "中文 🐎", "\U000e0041tag": "tag",
	}
	// the soft hyphen, the Mongolian vowel separator and the variation selectors supplement draw nothing; the markdown and dialog
	// widgets drop them and widgettest.Control says no widget may return them
	for in, want := range map[string]string{"a­b": "ab", "a᠎b": "ab", "selector\U000e0100": "selector"} {
		cases[in] = want
	}
	for in, want := range cases {
		if got := showClean(in); got != want {
			t.Errorf("showClean(%q) = %q, want %q", in, got, want)
		}
	}
	// whatever it is given, the result has no control character and never starts with a mark
	r := rand.New(rand.NewSource(5))
	for i := 0; i < 2000; i++ {
		b := make([]byte, r.Intn(12))
		for k := range b {
			b[k] = byte(r.Intn(256))
		}
		got := showClean(string(b))
		for _, c := range got {
			if c < 0x20 || (c >= 0x7f && c < 0xa0) || showHidden(c) && c != 0x200d {
				t.Fatalf("showClean(%q) = %q holds %U", b, got, c)
			}
		}
		if got != "" && cell.RuneWidth([]rune(got)[0]) == 0 {
			t.Fatalf("showClean(%q) = %q starts with a mark", b, got)
		}
	}
}

func TestShowTextFitting(t *testing.T) {
	if showTrunc("hello", 10) != "hello" || showTrunc("hello", 4) != "hel…" || showTrunc("hello", 0) != "" || showTrunc("hello", -1) != "" || showTrunc("hello", 1) != "h" {
		t.Errorf("showTrunc: %q %q %q", showTrunc("hello", 4), showTrunc("hello", 1), showTrunc("中文字", 5))
	}
	if got := showTrunc("中文字", 5); cell.StringWidth(got) > 5 {
		t.Errorf("a wide rune is never cut: %q", got)
	}
	if showPadR("ab", 5) != "ab   " || showPadL("ab", 5) != "   ab" || showPadR("abcdef", 3) != "abcdef" || showPadR("中", 3) != "中 " {
		t.Error("padding counts cells")
	}
	if showRepeat("x", 3) != "xxx" || showRepeat("x", 0) != "" || showRepeat("x", -2) != "" {
		t.Error("showRepeat")
	}
	var b showRowBuf
	b.add(cell.Style{}, "a").add(cell.Style{}, "b").add(cell.Style{Attr: cell.Bold}, "c").add(cell.Style{}, "").space(2).space(-3).padTo(8)
	if b.w != 8 || len(b.line()) != 3 || b.line().Plain() != "abc     " {
		t.Errorf("showRowBuf: %d %v %q", b.w, len(b.line()), b.line().Plain())
	}
	if showFit(cell.Text("abcdef"), 4).Plain() != "abc…" || showFit(cell.Text("abc"), 0) != nil || showPadLine(cell.Text("abcdef"), 4).Plain() != "abcd" || showPadLine(cell.Text("ab"), 4).Plain() != "ab  " {
		t.Error("showFit and showPadLine")
	}
	if !showHasText(cell.Text(" a ")) || showHasText(cell.Text("   ")) || showHasText(nil) {
		t.Error("showHasText")
	}
}

func TestShowMoney(t *testing.T) {
	cases := map[float64]string{0: "$0.000", 0.012: "$0.012", 0.9999: "$1.000", 1.5: "$1.50", 12.345: "$12.35", 99.99: "$99.99", 123.4: "$123", -4: "$0.000", math.NaN(): "$0.000", 1e12: "$1e9+"}
	for f, want := range cases {
		if got := showMoney(f); got != want {
			t.Errorf("showMoney(%v) = %q, want %q", f, got, want)
		}
	}
	cases = map[float64]string{0: "$0", 0.31: "$0.31", 20: "$20", 0.0004: "$0.0004", 12.5: "$12.50", 250.7: "$251", math.Inf(1): "$1e9+", math.NaN(): "$0"}
	for f, want := range cases {
		if got := dashMoney(f); got != want {
			t.Errorf("dashMoney(%v) = %q, want %q", f, got, want)
		}
	}
}

// A canvas keeps a wide rune whole, joins a combining mark to its base, drops what falls off its edges and repairs a wide rune
// that something is painted over half of.
func TestShowCanvas(t *testing.T) {
	c := newShowCanvas(6)
	c.put(0, "ab", cell.Style{})
	c.put(2, "中", cell.Style{Attr: cell.Bold})
	c.put(4, "e\u0301", cell.Style{})
	if got := c.line().Plain(); got != "ab中e\u0301 " || c.line().Width() != 6 {
		t.Errorf("wide and combining: %q (%d)", got, c.line().Width())
	}
	c.put(3, "x", cell.Style{}) // over the second half of the wide rune: the first half becomes a blank
	if got := c.line().Plain(); got != "ab x"+"e\u0301 " || c.line().Width() != 6 {
		t.Errorf("painting over half a wide rune leaves a blank for the other half: %q (%d)", got, c.line().Width())
	}
	c.put(1, "\u4e2d", cell.Style{}) // over the b and the blank that was half of the old one
	if got := c.line().Plain(); got != "a\u4e2dx"+"e\u0301 " || c.line().Width() != 6 {
		t.Errorf("a wide rune painted over two cells: %q (%d)", got, c.line().Width())
	}
	c = newShowCanvas(3)
	c.put(2, "中", cell.Style{}) // does not fit
	c.put(-1, "abc", cell.Style{})
	c.put(9, "zzz", cell.Style{})
	if got := c.line().Plain(); got != "bc " || c.line().Width() != 3 {
		t.Errorf("clipping: %q", got)
	}
	c = newShowCanvas(4)
	c.put(0, "x\xffy", cell.Style{})
	if got := c.line().Plain(); got != "x�y " {
		t.Errorf("an invalid byte is a replacement cell: %q", got)
	}
	if got := newShowCanvas(-3).line(); len(got) != 0 {
		t.Errorf("a negative canvas is empty: %v", got)
	}
	c = newShowCanvas(5)
	c.put(1, "ab", cell.Style{})
	if got := c.trimmed().Plain(); got != " ab" {
		t.Errorf("trimmed: %q", got)
	}
	c.fill(3, 9, "·", cell.Style{})
	if got := c.line().Plain(); got != " ab··" {
		t.Errorf("fill stops at the edge: %q", got)
	}
}

func TestShowPaletteFading(t *testing.T) {
	p := DefaultPalette()
	c := p.LayerColors[1]
	if p.fade(c, 0) != c || p.fade(c, -5) != c {
		t.Error("no fade keeps the colour")
	}
	half := p.fade(c, 50)
	if half == c || half.Kind != cell.KindRGB {
		t.Errorf("fading mixes towards faint: %+v", half)
	}
	if p.fade(c, 100) != p.Faint || p.fade(c, 500) != p.Faint {
		t.Error("a full fade is the faint colour, more is clamped")
	}
	if !p.fadeSt(c, 10).Has(0) || p.fadeSt(c, 10).FG == c {
		t.Error("a 24-bit colour fades in colour, without an attribute")
	}
	// without a colour to fade, the Dim attribute stands in from 30 percent
	m := MonoPalette()
	if m.fadeSt(cell.Color{}, 20).Has(cell.Dim) || !m.fadeSt(cell.Color{}, 30).Has(cell.Dim) {
		t.Error("Dim stands in for fading from 30 percent on")
	}
	if !m.noColour() || !(Palette{}).noColour() || p.noColour() || !(Palette{Mono: true}).noColour() {
		t.Error("noColour")
	}
	if p.roleCol(-1) != p.RoleColors[7] || p.roleCol(8) != p.RoleColors[0] || p.layerCol(99) != p.LayerColors[6] || p.layerCol(-4) != p.LayerColors[0] {
		t.Error("role colours wrap around, layer colours are clamped")
	}
	if m.dimSt() != (cell.Style{Attr: cell.Dim}) || p.dimSt() != (cell.Style{FG: p.Dim}) || m.faintSt().Attr != cell.Dim {
		t.Error("dim text is Dim without colours")
	}
}

func TestShowLayerNaming(t *testing.T) {
	cases := []struct {
		name   string
		pos    int
		label  string
		colour int
	}{
		{"G3 notes", 0, "G3", 3}, {"G0", 5, "G0", 0}, {"", 2, "G2", 2}, {"   ", 4, "G4", 4}, {"thread", 6, "thread", 6}, {"G9", 1, "G9", 9}, {"G", 1, "G", 1},
		{"g2 x", 0, "g2", 2}, {"G1x", 3, "G1x", 3}, {"\x1b[31mG4", 0, "[31mG4", 0},
	}
	for _, c := range cases {
		l := Layer{Name: c.name}
		if got := l.label(c.pos); got != c.label {
			t.Errorf("label of %q at %d = %q, want %q", c.name, c.pos, got, c.label)
		}
		if got := l.colourIndex(c.pos); got != c.colour {
			t.Errorf("colour of %q at %d = %d, want %d", c.name, c.pos, got, c.colour)
		}
	}
}

func TestShowSpinAndCounts(t *testing.T) {
	seen := map[string]bool{}
	for i := -25; i < 25; i++ {
		s := showSpin(i)
		seen[s] = true
		if cell.StringWidth(s) != 1 {
			t.Errorf("frame %d is %d cells wide", i, cell.StringWidth(s))
		}
	}
	if len(seen) != 10 || showSpin(10) != showSpin(0) || showSpin(-1) != showSpin(9) {
		t.Errorf("the spinner has %d frames and wraps around", len(seen))
	}
	for n, want := range map[int]string{0: "no", 1: "one", 8: "eight", 12: "twelve", 13: "13", -1: "-1", 50: "50"} {
		if got := dashCount(n); got != want {
			t.Errorf("dashCount(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{0: "0 agents", 1: "1 agent", 2: "2 agents", 8: "8 agents", 3000: "3000 agents", -4: "0 agents"} {
		if got := dashAgents(n); got != want {
			t.Errorf("dashAgents(%d) = %q, want %q", n, got, want)
		}
	}
}

// The agents the table keeps when it cannot show them all: the stuck ones first, the others in the order given, and what is kept
// comes out in the order given.
func TestShowDashPick(t *testing.T) {
	agents := make([]AgentRow, 10)
	for i := range agents {
		agents[i].ID = string(rune('a' + i))
	}
	agents[7].State, agents[9].State = StateStuck, StateStuck
	for k, want := range map[int][]int{-1: nil, 0: nil, 1: {7}, 2: {7, 9}, 3: {0, 7, 9}, 5: {0, 1, 2, 7, 9}, 10: {0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 99: {0, 1, 2, 3, 4, 5, 6, 7, 8, 9}} {
		got := dashPick(agents, k)
		if len(got) != len(want) {
			t.Errorf("dashPick(%d) = %v, want %v", k, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("dashPick(%d) = %v, want %v", k, got, want)
				break
			}
		}
	}
	if dashPick(nil, 3) != nil && len(dashPick(nil, 3)) != 0 {
		t.Error("no agents")
	}
}

func TestShowDashSplit(t *testing.T) {
	for w := 10; w < 200; w += 7 {
		got := dashSplit(w, []int{25, 30, 19, 26}, []int{22, 28, 20, 23})
		sum := len(got) - 1
		for _, g := range got {
			sum += g
		}
		if sum != w {
			t.Fatalf("width %d: %v fill %d", w, got, sum)
		}
		if w >= 22+28+20+23+3 {
			for i, m := range []int{22, 28, 20, 23} {
				if got[i] < m {
					t.Fatalf("width %d: panel %d is %d, its minimum is %d: %v", w, i, got[i], m, got)
				}
			}
		}
	}
	if got := dashSplit(0, []int{1, 1}, []int{1, 1}); got[0] != 0 || got[1] != 0 {
		t.Errorf("no room: %v", got)
	}
}

func TestShowHorseGait(t *testing.T) {
	legs := make([]Leg, 8)
	if dashGait(10, legs) != 10*2/8 {
		t.Error("an idle horse crawls")
	}
	for i := range legs {
		legs[i].Busy = true
	}
	if dashGait(8, legs) != 10 {
		t.Errorf("a loaded horse runs: %d", dashGait(8, legs))
	}
	// the gait repeats every 32 frames at every pace, for negative frames too, and a huge frame does not overflow
	for busy := 0; busy <= 8; busy++ {
		l := make([]Leg, 8)
		for i := 0; i < busy; i++ {
			l[i].Busy = true
		}
		for _, f := range []int{-100, -33, -1, 0, 5, 31, 32, 1000} {
			if a, b := dashGait(f, l), dashGait(f+32, l); a != b {
				t.Errorf("%d busy legs: the gait at frame %d is %d and at frame %d %d", busy, f, a, f+32, b)
			}
		}
		for _, f := range []int{math.MinInt, math.MaxInt, 1 << 62} {
			if g := dashGait(f, l); g < 0 || g > 38 {
				t.Errorf("%d busy legs: the gait at frame %d is %d", busy, f, g)
			}
		}
	}
	got := dashLegs([]AgentRow{{RoleColor: 3, State: StateTool}, {RoleColor: 4, State: StateThinking}, {State: StateEdit}, {}, {}})
	if !got[0].Busy || got[1].Busy || !got[2].Busy || got[3].Busy || got[0].Color != 3 || got[1].Color != 4 || got[5].Color != 5 || got[7].Color != 7 {
		t.Errorf("legs: %+v (a thinking agent is not running a tool; a leg with no agent keeps the colour of its place)", got)
	}
	// more than eight agents share the legs round robin: the ninth is on leg 0, the tenth on leg 1
	nine := make([]AgentRow, 10)
	nine[8].State, nine[9].State = StateTool, StateDone
	if got = dashLegs(nine); !got[0].Busy || got[1].Busy {
		t.Errorf("the ninth agent runs a tool on leg 0 and the tenth is done on leg 1: %+v", got)
	}
}
