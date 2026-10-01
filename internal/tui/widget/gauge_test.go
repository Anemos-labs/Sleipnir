package widget

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

func TestDuration(t *testing.T) {
	ms, s, m, h := time.Millisecond, time.Second, time.Minute, time.Hour
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"}, {1, "<1ms"}, {999 * time.Microsecond, "<1ms"}, {ms, "1ms"}, {812 * ms, "812ms"}, {999 * ms, "999ms"},
		{999*ms + 900*time.Microsecond, "999ms"}, {s, "1.0s"}, {1400 * ms, "1.4s"}, {1449 * ms, "1.4s"}, {1450 * ms, "1.5s"},
		{1960 * ms, "2.0s"}, {9940 * ms, "9.9s"}, {9950 * ms, "10s"}, {10 * s, "10s"}, {42 * s, "42s"}, {59*s + 400*ms, "59s"},
		{59*s + 500*ms, "1m00s"}, {m, "1m00s"}, {2*m + 10*s, "2m10s"}, {2*m + 5*s, "2m05s"}, {59*m + 59*s, "59m59s"},
		{59*m + 59*s + 600*ms, "1h00m"}, {h, "1h00m"}, {h + 3*m, "1h03m"}, {h + 3*m + 29*s, "1h03m"}, {h + 3*m + 30*s, "1h04m"},
		{23*h + 59*m, "23h59m"}, {23*h + 59*m + 40*s, "1d00h"}, {24 * h, "1d00h"}, {49 * h, "2d01h"}, {-1400 * ms, "-1.4s"},
		{-time.Duration(0), "0s"},
	}
	for _, c := range cases {
		if got := Duration(c.d); got != c.want {
			t.Errorf("Duration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	for _, d := range []time.Duration{math.MinInt64, math.MaxInt64, math.MaxInt64 - 1, math.MinInt64 + 1} {
		if got := Duration(d); got == "" || strings.ContainsAny(got, " \n") {
			t.Errorf("Duration(%d) = %q", d, got)
		}
	}
	// never "60s", never "60m": rounding carries into the next unit
	carry := regexp.MustCompile(`(^|\D)(60s|60m($|\d)|100s)`)
	check := func(d time.Duration) {
		if got := Duration(d); carry.MatchString(got) {
			t.Fatalf("Duration(%v) = %q does not carry", d, got)
		}
	}
	for d := time.Duration(0); d < 2*h; d += 331*ms + 7*time.Microsecond {
		check(d)
	}
	for _, edge := range []time.Duration{s, 10 * s, m, h, 24 * h} { // every unit boundary, a little either side, finely
		for d := edge - 2*s; d <= edge+2*s; d += 10 * ms {
			check(max(d, 0))
		}
	}
}

func TestTokens(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"}, {812, "812"}, {999, "999"}, {1000, "1.0k"}, {1234, "1.2k"}, {12100, "12.1k"}, {48300, "48.3k"}, {99999, "100.0k"},
		{123456, "123.5k"}, {999949, "999.9k"}, {999950, "1.0M"}, {1_200_000, "1.2M"}, {999_949_999, "999.9M"}, {999_950_000, "1.0B"},
		{3_400_000_000, "3.4B"}, {-812, "-812"}, {-12100, "-12.1k"},
	}
	for _, c := range cases {
		if got := Tokens(c.n); got != c.want {
			t.Errorf("Tokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	for _, n := range []int{math.MaxInt, math.MinInt, math.MaxInt - 1} {
		if got := Tokens(n); got == "" || got[len(got)-1] != 'B' && got != "-"+Tokens(-(n+1)) {
			t.Errorf("Tokens(%d) = %q", n, got)
		}
	}
}

func TestUSD(t *testing.T) {
	cases := []struct {
		x    float64
		want string
	}{
		{0, "$0.00"}, {1e-9, "<$0.0001"}, {0.00009999, "<$0.0001"}, {0.0001, "$0.0001"}, {0.0021, "$0.0021"}, {0.00999, "$0.0100"},
		{0.01, "$0.01"}, {0.31, "$0.31"}, {1.24, "$1.24"}, {1, "$1.00"}, {999.999, "$1,000.00"}, {1234.5, "$1,234.50"},
		{1e6, "$1,000,000.00"}, {-0.31, "-$0.31"}, {-1e-9, "-<$0.0001"}, {math.NaN(), "$?"}, {math.Inf(1), "$inf"},
		{math.Inf(-1), "-$inf"}, {1e15, "$1e+15"}, {math.MaxFloat64, "$1.8e+308"},
	}
	for _, c := range cases {
		if got := USD(c.x); got != c.want {
			t.Errorf("USD(%v) = %q, want %q", c.x, got, c.want)
		}
	}
	// A real cost is never shown as zero.
	for _, x := range []float64{1e-300, 5e-324, 0.00001} {
		if USD(x) == "$0.00" || USD(x) == "$0.0000" {
			t.Errorf("USD(%v) shows a real cost as zero", x)
		}
	}
}

func TestPercent(t *testing.T) {
	cases := []struct {
		f    float64
		want string
	}{
		{0, "0%"}, {0.001, "<1%"}, {0.0049, "<1%"}, {0.005, "1%"}, {0.934, "93%"}, {0.935, "94%"}, {0.994, "99%"}, {0.996, "99%"},
		{0.99999, "99%"}, {1, "100%"}, {1.004, "100%"}, {2.5, "250%"}, {-0.12, "-12%"}, {math.NaN(), "?%"},
		{math.Inf(1), ">9999%"}, {math.Inf(-1), "->9999%"}, {99.99, "9999%"}, {100, ">9999%"}, {99, "9900%"},
	}
	for _, c := range cases {
		if got := Percent(c.f); got != c.want {
			t.Errorf("Percent(%v) = %q, want %q", c.f, got, c.want)
		}
	}
}

func TestBar(t *testing.T) {
	cases := []struct {
		f    float64
		w    int
		want string
	}{
		{0, 10, "          "}, {1, 10, "██████████"}, {0.5, 10, "█████     "}, {0.55, 10, "█████▌    "}, {0.05, 10, "▌         "},
		{0.0001, 10, "▏         "}, {0.9999, 10, "█████████▉"}, {0.125, 8, "█       "}, {0.1875, 8, "█▌      "},
		{math.NaN(), 4, "    "}, {-1, 4, "    "}, {7, 4, "████"}, {math.Inf(1), 3, "███"}, {0.5, 1, "▌"}, {0.9999, 1, "▉"},
	}
	for _, c := range cases {
		if got := Bar(c.f, c.w); got != c.want {
			t.Errorf("Bar(%v, %d) = %q, want %q", c.f, c.w, got, c.want)
		}
	}
	if Bar(0.5, 0) != "" || Bar(0.5, -3) != "" || BarASCII(0.5, 0) != "" {
		t.Error("a width below 1 gives an empty string")
	}
}

func TestBarIsExactlyWidthCellsAndMonotonic(t *testing.T) {
	blocks := map[rune]int{' ': 0, '▏': 1, '▎': 2, '▍': 3, '▌': 4, '▋': 5, '▊': 6, '▉': 7, '█': 8}
	for w := 1; w <= 30; w++ {
		prev := -1
		for i := 0; i <= 1000; i++ {
			f := float64(i) / 1000
			bar := Bar(f, w)
			if cell.StringWidth(bar) != w || len([]rune(bar)) != w {
				t.Fatalf("Bar(%v, %d) = %q is not %d cells", f, w, bar, w)
			}
			eighths := 0
			for _, r := range bar {
				n, ok := blocks[r]
				if !ok {
					t.Fatalf("Bar(%v, %d) has %q", f, w, r)
				}
				eighths += n
			}
			if eighths < prev {
				t.Fatalf("w=%d: the bar shrank from %d to %d eighths at f=%v", w, prev, eighths, f)
			}
			prev = eighths
			if f > 0 && eighths == 0 {
				t.Fatalf("Bar(%v, %d) shows a value as empty", f, w)
			}
			if f < 1 && eighths == 8*w {
				t.Fatalf("Bar(%v, %d) shows a value below 1 as full", f, w)
			}
		}
	}
}

func TestBarASCII(t *testing.T) {
	if got := BarASCII(0.5, 10); got != "#####     " {
		t.Errorf("%q", got)
	}
	if got := BarASCII(0.001, 10); got != "#         " {
		t.Errorf("never empty for a value: %q", got)
	}
	if got := BarASCII(0.999, 10); got != "######### " {
		t.Errorf("never full below 1: %q", got)
	}
	if got := BarASCII(1, 4); got != "####" {
		t.Errorf("%q", got)
	}
}

func gaugeFill(l cell.Line) (on, off int) {
	for _, sp := range l {
		on += strings.Count(sp.Text, "▰") + strings.Count(sp.Text, "#")
		off += strings.Count(sp.Text, "▱") + strings.Count(sp.Text, "-")
	}
	return
}

func TestGauge(t *testing.T) {
	th := DefaultTheme()
	cases := []struct {
		f      float64
		w      int
		on     int
		colour cell.Style
	}{
		{0, 8, 0, th.Good}, {0.25, 8, 2, th.Good}, {0.5, 8, 4, th.Good}, {0.74, 8, 6, th.Good}, {0.75, 8, 6, th.Warn}, {0.89, 8, 7, th.Warn},
		{0.9, 8, 7, th.Bad}, {0.99, 8, 7, th.Bad}, {1, 8, 8, th.Bad}, {7, 8, 8, th.Bad}, {-2, 8, 0, th.Good}, {math.NaN(), 8, 0, th.Good},
		{math.Inf(1), 8, 8, th.Bad}, {math.Inf(-1), 8, 0, th.Good}, {0.01, 8, 1, th.Good}, {0.999, 8, 7, th.Bad}, {0.5, 1, 1, th.Good},
	}
	for _, c := range cases {
		l := Gauge(c.f, c.w, th)
		on, off := gaugeFill(l)
		if l.Width() != c.w || on != c.on || on+off != c.w {
			t.Errorf("Gauge(%v, %d) = %q: %d on, %d off", c.f, c.w, l.Plain(), on, off)
			continue
		}
		if c.on > 0 && l[0].Style != c.colour {
			t.Errorf("Gauge(%v, %d): filled cells are %+v, want %+v", c.f, c.w, l[0].Style, c.colour)
		}
		if c.on < c.w && l[len(l)-1].Style != th.Faint {
			t.Errorf("Gauge(%v, %d): the empty cells are %+v, want Faint", c.f, c.w, l[len(l)-1].Style)
		}
	}
	if Gauge(0.5, 0, th) != nil || Gauge(0.5, -4, th) != nil {
		t.Error("a width below 1 gives nil")
	}
}

func TestGaugeOptions(t *testing.T) {
	th := DefaultTheme()
	hit := func(f float64) cell.Style { return Gauge(f, 10, th, GaugeHighIsGood())[0].Style }
	if hit(0.95) != th.Good || hit(0.7) != th.Warn || hit(0.3) != th.Bad || hit(0.85) != th.Warn || hit(0.51) != th.Warn || hit(0.5) != th.Bad {
		t.Error("GaugeHighIsGood thresholds")
	}
	fixed := cell.Style{FG: cell.ANSI(5)}
	if Gauge(0.95, 10, th, GaugeStyle(fixed))[0].Style != fixed || Gauge(0.1, 10, th, GaugeStyle(fixed))[0].Style != fixed {
		t.Error("GaugeStyle fixes the colour")
	}
}

func TestGaugeInMonoAndASCII(t *testing.T) {
	mono := MonoTheme()
	l := Gauge(0.5, 10, mono)
	if l.Plain() != "▰▰▰▰▰▱▱▱▱▱" {
		t.Errorf("mono gauge: %q", l.Plain())
	}
	for _, sp := range l {
		if sp.Style.FG.Kind != cell.KindDefault || sp.Style.BG.Kind != cell.KindDefault {
			t.Errorf("mono gauge has colour: %+v", sp.Style)
		}
	}
	if got := Gauge(0.3, 10, DefaultTheme().WithASCII(true)).Plain(); got != "###-------" {
		t.Errorf("ascii gauge: %q", got)
	}
}

func TestGaugeNeverClaimsEmptyOrFull(t *testing.T) {
	th := MonoTheme()
	g := widgettest.NewRand(3)
	for i := 0; i < 2000; i++ {
		w := 2 + g.Intn(60)
		f := float64(g.Intn(1_000_000)) / 1_000_000
		on, _ := gaugeFill(Gauge(f, w, th))
		if f > 0 && on == 0 {
			t.Fatalf("Gauge(%v, %d) is empty", f, w)
		}
		if f < 1 && on == w {
			t.Fatalf("Gauge(%v, %d) is full", f, w)
		}
	}
}

func TestGaugeAndBarAreExactlyWidthCells(t *testing.T) {
	g := widgettest.NewRand(17)
	fracs := []float64{0, 1, 0.5, -1, 2, math.NaN(), math.Inf(1), math.Inf(-1), 1e-300, 0.999999, math.SmallestNonzeroFloat64}
	for i := 0; i < 200; i++ {
		fracs = append(fracs, float64(g.Intn(10001))/10000)
	}
	for _, nt := range allTestThemes() {
		for w := 1; w <= 120; w += 1 + g.Intn(4) {
			for _, f := range fracs {
				l := Gauge(f, w, nt.th)
				checkLines(t, "gauge/"+nt.name, []cell.Line{l}, w)
				if l.Width() != w {
					t.Fatalf("Gauge(%v, %d) is %d cells wide", f, w, l.Width())
				}
			}
		}
	}
}

func TestGaugeGolden(t *testing.T) {
	for _, nt := range []themeCase{{"default", DefaultTheme()}, {"mono", MonoTheme()}, {"ascii", DefaultTheme().WithASCII(true)}} {
		var ls []cell.Line
		for _, f := range []float64{0, 0.02, 0.25, 0.5, 0.74, 0.75, 0.89, 0.9, 0.98, 1, 1.5} {
			ls = append(ls, cell.Join(cell.Text(fmt.Sprintf("%-5s ", Percent(f))), Gauge(f, 12, nt.th), cell.Text(" "),
				Gauge(f, 12, nt.th, GaugeHighIsGood()), cell.Text(" "+Bar(f, 12)+"|")))
		}
		checkGoldenLines(t, "gauge_"+nt.name, nt.th, ls)
	}
}
