package widget

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Gauges and the formatting of the numbers the status line shows. All of it is pure: the same number always gives the same text.

// GaugeOpt changes how Gauge picks its colour.
type GaugeOpt func(*gaugeCfg)

type gaugeCfg struct {
	highIsGood bool
	fixed      bool
	style      cell.Style
}

// GaugeHighIsGood makes a full gauge green and an empty one red (a hit ratio), the reverse of the default (a budget).
func GaugeHighIsGood() GaugeOpt { return func(c *gaugeCfg) { c.highIsGood = true } }

// GaugeStyle draws the filled cells in one style, whatever the value.
func GaugeStyle(st cell.Style) GaugeOpt { return func(c *gaugeCfg) { c.fixed, c.style = true, st } }

// unitFrac clamps a fraction into [0, 1]: NaN and negative numbers are 0, anything above 1 (infinity too) is 1.
func unitFrac(f float64) float64 {
	switch {
	case math.IsNaN(f) || f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// Gauge is a bar of width cells, ▰ for the filled part and ▱ for the rest (# and - when th.ASCII), showing frac in [0, 1]. By
// default it reads as a budget that runs out: the filled cells are green below 75%, amber below 90% and red from there on;
// GaugeHighIsGood reverses that (green above 85%, amber above 50%, red below), GaugeStyle fixes the style. The fill is the value
// itself, so the gauge reads in MonoTheme too; a value that is not zero always shows one cell and one below 1 never shows them
// all, so the bar never claims "empty" or "full" falsely. A NaN or negative frac is 0 and a frac above 1 is 1. width < 1 gives
// nil.
func Gauge(frac float64, width int, th Theme, opts ...GaugeOpt) cell.Line {
	if width < 1 {
		return nil
	}
	var cfg gaugeCfg
	for _, o := range opts {
		o(&cfg)
	}
	f := unitFrac(frac)
	on := int(math.Round(f * float64(width)))
	if f > 0 && on == 0 {
		on = 1
	}
	if f < 1 && on == width && width > 1 {
		on = width - 1
	}
	st := cfg.style
	if !cfg.fixed {
		switch {
		case cfg.highIsGood && f > 0.85, !cfg.highIsGood && f < 0.75:
			st = th.Good
		case cfg.highIsGood && f > 0.5, !cfg.highIsGood && f < 0.9:
			st = th.Warn
		default:
			st = th.Bad
		}
	}
	g := th.glyphs()
	var l cell.Line
	if on > 0 {
		l = append(l, cell.Span{Text: strings.Repeat(g.gaugeOn, on), Style: st})
	}
	if width-on > 0 {
		l = append(l, cell.Span{Text: strings.Repeat(g.gaugeOff, width-on), Style: th.Faint})
	}
	return l
}

var barEighths = [...]string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}

// Bar is a horizontal bar of exactly width cells with the precision of an eighth of a cell: full blocks, then one of ▏▎▍▌▋▊▉,
// then spaces. It has no colour: the caller styles the string (and may draw a track behind it). Like Gauge it never shows
// a non-zero value as empty (at least ▏) or a value below 1 as full. A NaN or negative frac is 0, above 1 is 1; width < 1 gives "".
func Bar(frac float64, width int) string {
	if width < 1 {
		return ""
	}
	f := unitFrac(frac)
	cells := f * float64(width)
	full := int(cells)
	e := int(math.Round((cells - float64(full)) * 8))
	if e == 8 {
		full, e = full+1, 0
	}
	if f > 0 && full == 0 && e == 0 {
		e = 1
	}
	if f < 1 && full >= width {
		full, e = width-1, 7
	}
	if full >= width {
		full, e = width, 0
	}
	return strings.Repeat("█", full) + barEighths[e] + strings.Repeat(" ", width-full-min(e, 1))
}

// BarASCII is Bar for terminals without block characters: # for each filled cell (rounded, and never empty or full by
// rounding), spaces for the rest.
func BarASCII(frac float64, width int) string {
	if width < 1 {
		return ""
	}
	f := unitFrac(frac)
	on := int(math.Round(f * float64(width)))
	if f > 0 && on == 0 {
		on = 1
	}
	if f < 1 && on == width && width > 1 {
		on = width - 1
	}
	return strings.Repeat("#", on) + strings.Repeat(" ", width-on)
}

// Duration formats an elapsed time compactly: "0s", "<1ms", "812ms", "1.4s", "42s", "2m10s", "1h03m", "2d05h". It rounds to the unit it
// shows and carries (59.6 s is "1m00s", never "60s"). A negative duration is written with a leading "-".
func Duration(d time.Duration) string {
	if d < 0 {
		if d == math.MinInt64 {
			d = math.MaxInt64
		} else {
			d = -d
		}
		return "-" + Duration(d)
	}
	switch {
	case d == 0:
		return "0s"
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return strconv.Itoa(int(d/time.Millisecond)) + "ms"
	case d < 10*time.Second:
		tenths := int((d + 50*time.Millisecond) / (100 * time.Millisecond))
		if tenths >= 100 {
			return "10s"
		}
		return fmt.Sprintf("%d.%ds", tenths/10, tenths%10)
	case d < time.Hour:
		if s := int((d + 500*time.Millisecond) / time.Second); s < 60 {
			return strconv.Itoa(s) + "s"
		} else if s < 3600 {
			return fmt.Sprintf("%dm%02ds", s/60, s%60)
		}
	}
	if d < 24*time.Hour {
		if m := int((d + 30*time.Second) / time.Minute); m < 24*60 {
			return fmt.Sprintf("%dh%02dm", m/60, m%60)
		}
	}
	h := int64(d / time.Hour) // not d+30m: that overflows for the largest durations
	if d%time.Hour >= 30*time.Minute {
		h++
	}
	return fmt.Sprintf("%dd%02dh", h/24, h%24)
}

// Tokens formats a token count with a unit: "812", "1.2k", "12.1k", "999.9k", "1.2M", "3.4B". Above 999 it always has one
// decimal, so a column of counts keeps its shape. It rounds to the unit shown and carries (999,960 is "1.0M"). A negative
// count is written with a leading "-".
func Tokens(n int) string {
	if n < 0 {
		if n == math.MinInt {
			n = math.MaxInt
		} else {
			n = -n
		}
		return "-" + Tokens(n)
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	v := min(int64(n), 9e18) // the rounding below must not overflow
	for _, u := range []struct {
		div    int64
		suffix string
	}{{1e3, "k"}, {1e6, "M"}, {1e9, "B"}} {
		tenths := (v + u.div/20) / (u.div / 10)
		if tenths < 10000 || u.suffix == "B" {
			return fmt.Sprintf("%d.%d%s", tenths/10, tenths%10, u.suffix)
		}
	}
	return strconv.Itoa(n) // not reached: the last unit always answers
}

// USD formats a dollar amount for a cost display: "$0.0021" below a cent (four decimals), "$1.24" from a cent up (two decimals,
// thousands separated by commas: "$1,234.50"), "$0.00" for exactly zero and "<$0.0001" for anything positive but smaller than
// that, so a real cost is never shown as zero. A negative amount (a saving) has a leading "-". NaN is "$?" and infinity
// "$inf".
func USD(x float64) string {
	switch {
	case math.IsNaN(x):
		return "$?"
	case math.IsInf(x, 1):
		return "$inf"
	case math.IsInf(x, -1):
		return "-$inf"
	case x < 0:
		return "-" + USD(-x)
	case x == 0:
		return "$0.00"
	case x < 0.0001:
		return "<$0.0001"
	case x < 0.01:
		return fmt.Sprintf("$%.4f", x)
	case x >= 1e15:
		return fmt.Sprintf("$%.3g", x)
	}
	cents := int64(math.Round(x * 100))
	return fmt.Sprintf("$%s.%02d", usdGroupThousands(cents/100), cents%100)
}

func usdGroupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var sb strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		sb.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}

// Percent formats a fraction as a whole percentage: Percent(0.934) is "93%". It is honest at the ends: a positive value that
// would round to 0 is "<1%", and a value below 1 never reads "100%" (it is "99%"). It does not clamp: 2.5 is "250%" (above
// 9999% it is ">9999%"), a negative fraction has a leading "-", and NaN is "?%".
func Percent(frac float64) string {
	switch {
	case math.IsNaN(frac):
		return "?%"
	case frac < 0:
		return "-" + Percent(-frac)
	case frac == 0:
		return "0%"
	}
	p := frac * 100
	switch {
	case p > 9999:
		return ">9999%"
	case p < 0.5:
		return "<1%"
	case p < 100 && math.Round(p) >= 100:
		return "99%"
	}
	return strconv.Itoa(int(math.Round(p))) + "%"
}
