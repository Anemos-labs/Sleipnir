// Small pure helpers shared by the signature widgets: clamping, integer rounding, the largest-remainder split of a width,
// number formatting, and cleaning of text that came from outside. Everything here is deterministic: no time, no maps
// iterated, no floating point on a rounding boundary where an integer will do (a cell count must not depend on the
// architecture's fused multiply-add).

package widget

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

func showClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// showUnit clamps f to 0..1; NaN is 0.
func showUnit(f float64) float64 {
	switch {
	case f != f, f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// showPermille turns a fraction into whole thousandths, clamped to 0..1000, NaN being 0. Animation is computed in these so
// that a frame does not depend on floating-point rounding past this one conversion.
func showPermille(f float64) int { return int(showUnit(f)*1000 + 0.5) }

// showDiv is num/den rounded half up, for num >= 0 and den > 0 (anything else is 0).
func showDiv(num, den int64) int {
	if num <= 0 || den <= 0 {
		return 0
	}
	return int((2*num + den) / (2 * den))
}

// showMaxTokens bounds token counts so that products with a width cannot overflow.
const showMaxTokens = 1 << 40

func showTok(n int) int {
	if n < 0 {
		return 0
	}
	if n > showMaxTokens {
		return showMaxTokens
	}
	return n
}

// showShares splits total cells among the weights by largest remainder (Hamilton): every weight gets its proportional share
// rounded so that the shares add up to total exactly. A weight <= 0 gets nothing. With minOne every positive weight gets at
// least one cell, taken from the biggest shares; when there are fewer cells than positive weights the biggest weights get
// one each. Ties go to the earlier entry, so the result is deterministic.
func showShares(weights []int, total int, minOne bool) []int {
	out := make([]int, len(weights))
	if total <= 0 {
		return out
	}
	var sum int64
	pos := 0
	for _, w := range weights {
		if w = showTok(w); w > 0 {
			sum += int64(w)
			pos++
		}
	}
	if pos == 0 {
		return out
	}
	if minOne && total < pos {
		for ; total > 0; total-- {
			best := -1
			for i, w := range weights {
				if w > 0 && out[i] == 0 && (best < 0 || w > weights[best]) {
					best = i
				}
			}
			out[best] = 1
		}
		return out
	}
	rem := make([]int64, len(weights))
	used := 0
	for i, w := range weights {
		if w = showTok(w); w > 0 {
			q := int64(total) * int64(w)
			out[i] = int(q / sum)
			rem[i] = q % sum
			used += out[i]
		}
	}
	for ; used < total; used++ {
		best := -1
		for i, w := range weights {
			if w > 0 && (best < 0 || rem[i] > rem[best] || (rem[i] == rem[best] && w > weights[best])) {
				best = i
			}
		}
		out[best]++
		rem[best] = -1 // each entry takes at most one extra cell
	}
	if minOne {
		for i, w := range weights {
			if w <= 0 || out[i] > 0 {
				continue
			}
			donor := -1
			for j := range out {
				if out[j] > 1 && (donor < 0 || out[j] > out[donor]) {
					donor = j
				}
			}
			out[donor]--
			out[i] = 1
		}
	}
	return out
}

// showTokens writes a token count the way the UI does: 812, 2.4k, 31.2k, 312k, 1.3M. Negative counts are 0.
func showTokens(n int) string {
	n = showTok(n)
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 99_950:
		t := (n + 50) / 100 // tenths of a thousand
		return strconv.Itoa(t/10) + "." + strconv.Itoa(t%10) + "k"
	case n < 999_500:
		return strconv.Itoa((n+500)/1000) + "k"
	}
	if t := (n + 50_000) / 100_000; t < 1000 { // tenths of a million
		return strconv.Itoa(t/10) + "." + strconv.Itoa(t%10) + "M"
	}
	return strconv.Itoa((n+500_000)/1_000_000) + "M"
}

// showClock writes a duration as a countdown, m:ss (h:mm:ss from an hour on), rounded up to whole seconds so that a clock
// that has not run out never reads 0:00.
func showClock(d time.Duration) string {
	if d <= 0 {
		return "0:00"
	}
	if max := 100*time.Hour - time.Second; d > max {
		d = max
	}
	s := int((d + time.Second - 1) / time.Second)
	h, m, sec := s/3600, s%3600/60, s%60
	two := func(v int) string {
		if v < 10 {
			return "0" + strconv.Itoa(v)
		}
		return strconv.Itoa(v)
	}
	if h > 0 {
		return strconv.Itoa(h) + ":" + two(m) + ":" + two(sec)
	}
	return strconv.Itoa(m) + ":" + two(sec)
}

// showElapsed writes a wall time as mm:ss (h:mm:ss from an hour on), rounded down.
func showElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if max := 100*time.Hour - time.Second; d > max {
		d = max
	}
	s := int(d / time.Second)
	h, m, sec := s/3600, s%3600/60, s%60
	two := func(v int) string {
		if v < 10 {
			return "0" + strconv.Itoa(v)
		}
		return strconv.Itoa(v)
	}
	if h > 0 {
		return strconv.Itoa(h) + ":" + two(m) + ":" + two(sec)
	}
	return two(m) + ":" + two(sec)
}

// showPercent is a ratio as a whole percentage, "93%"; NaN and negative ratios are 0%, more than 1 is 100%.
func showPercent(f float64) string { return strconv.Itoa((showPermille(f)+5)/10) + "%" }

// showClean makes text from outside (an agent's name, a mail subject, a tool's output, a file name) safe to lay out in a cell:
// control characters (escape sequences included, a line break, a tab) become spaces, text direction overrides and other
// invisible format characters disappear, invalid UTF-8 becomes U+FFFD. Nothing else changes, so combining marks and emoji
// keep working, except that the text never begins with a mark (a mark with no base before it in the same run is dropped by
// whoever cuts the line, and which runs there are depends on the colours). The result never contains a character that a
// terminal would act on.
func showClean(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune('�')
		case unicode.IsControl(r), r == '\u2028', r == '\u2029':
			b.WriteByte(' ')
		case r == '\u200d': // the joiner of emoji sequences
			if b.Len() > 0 {
				b.WriteRune(r)
			}
		case showHidden(r):
		case unicode.IsGraphic(r):
			if b.Len() == 0 && cell.RuneWidth(r) == 0 {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// showHidden is the characters that change how text is displayed without drawing anything: direction marks and overrides,
// zero-width spaces, invisible operators, the byte-order mark.
func showHidden(r rune) bool {
	switch {
	case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x206f, r == 0xfeff:
		return true
	case r >= 0xe0000 && r <= 0xe007f: // tag characters
		return true
	}
	return false
}

// showTrunc cuts s to w cells with an ellipsis (and is s itself when it fits); w <= 0 is "".
func showTrunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if cell.StringWidth(s) <= w {
		return s
	}
	return cell.Text(s).Truncate(w, "…").Plain()
}

// showPadR pads s with spaces on the right to w cells; a wider s is returned as it is.
func showPadR(s string, w int) string {
	if n := w - cell.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// showPadL pads s with spaces on the left to w cells; a wider s is returned as it is.
func showPadL(s string, w int) string {
	if n := w - cell.StringWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

func showRepeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}

// showHasText reports whether a line shows anything but spaces.
func showHasText(l cell.Line) bool {
	for _, sp := range l {
		if strings.TrimSpace(sp.Text) != "" {
			return true
		}
	}
	return false
}

// showRowBuf builds one line from pieces of text, merging neighbours of the same style and counting the cells used.
type showRowBuf struct {
	spans cell.Line
	w     int
}

func (b *showRowBuf) add(st cell.Style, s string) *showRowBuf {
	if s == "" {
		return b
	}
	if n := len(b.spans); n > 0 && b.spans[n-1].Style == st {
		b.spans[n-1].Text += s
	} else {
		b.spans = append(b.spans, cell.Span{Text: s, Style: st})
	}
	b.w += cell.StringWidth(s)
	return b
}

func (b *showRowBuf) addLine(l cell.Line) *showRowBuf {
	for _, sp := range l {
		b.add(sp.Style, sp.Text)
	}
	return b
}

// space adds n spaces (nothing when n <= 0).
func (b *showRowBuf) space(n int) *showRowBuf { return b.add(cell.Style{}, showRepeat(" ", n)) }

// padTo adds spaces until the row is w cells wide.
func (b *showRowBuf) padTo(w int) *showRowBuf { return b.space(w - b.w) }

func (b *showRowBuf) line() cell.Line { return b.spans }

// showFit cuts a line to w cells with an ellipsis; w <= 0 gives nil.
func showFit(l cell.Line, w int) cell.Line {
	if w <= 0 {
		return nil
	}
	return l.Truncate(w, "…")
}

// showPadLine pads a line with unstyled spaces to exactly w cells (cutting it when it is wider).
func showPadLine(l cell.Line, w int) cell.Line {
	if w <= 0 {
		return nil
	}
	if l.Width() > w {
		l = l.Truncate(w, "")
	}
	return l.Pad(w, cell.Style{})
}
