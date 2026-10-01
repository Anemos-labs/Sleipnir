// The cache warmth clock: how long the provider will keep the prefix before it goes cold (docs/UX.md, "TTL drain").

package widget

import (
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

const ttlBarCells = 8

// TTL draws the clock of the cache: a clock face that empties as the time runs out, the word warm and the time left, and a bar
// of ▰ that drains into ▱:
//
//	◕ warm 4:12 ▰▰▰▰▰▰▱▱
//
// It is green while there is time, amber and Bold from the last minute, and at zero red and Bold: ○ cold, with an empty bar. The
// face is ●◕◑◔ by the quarter of the total that is left (a clock with a little time left is ◔, never the empty face of a cold one),
// so the state reads without colour by face, word, time and bar.
//
// remaining is clamped to 0..total; a total that is not positive is taken to be remaining (a clock that cannot drain). When
// the width is short the bar goes first, then the word, then the time; a width <= 0 draws nothing.
func TTL(remaining, total time.Duration, width int, p Palette) cell.Line {
	if width <= 0 {
		return nil
	}
	if remaining < 0 {
		remaining = 0
	}
	const ttlMax = 1000 * time.Hour // no provider keeps a cache longer; it keeps the arithmetic far from overflowing
	remaining = min(remaining, ttlMax)
	if total <= 0 || total < remaining {
		total = remaining
	}
	total = min(total, ttlMax)
	cold := remaining <= 0

	var face, word string
	var st cell.Style
	switch {
	case cold:
		face, word, st = "○", "cold", p.badSt()
	case remaining < time.Minute:
		face, word, st = ttlFace(remaining, total), "warm", p.warnSt().With(cell.Bold)
	default:
		face, word, st = ttlFace(remaining, total), "warm", p.goodSt()
	}
	clock := showClock(remaining)

	filled := 0
	if !cold {
		filled = showDiv(int64(remaining)*ttlBarCells, int64(total))
		if filled == 0 {
			filled = 1
		}
	}
	build := func(withWord, withTime bool, bar int) cell.Line {
		var b showRowBuf
		b.add(st, face)
		if withWord {
			b.add(cell.Style{}, " ").add(st, word)
		}
		if withTime && !cold {
			b.add(cell.Style{}, " ").add(cell.Style{Attr: ttlAttr(st)}, clock)
		}
		if bar > 0 {
			on := showClamp(showDiv(int64(filled)*int64(bar), ttlBarCells), 0, bar)
			if filled > 0 && on == 0 {
				on = 1
			}
			b.add(cell.Style{}, " ").add(st, showRepeat("▰", on)).add(p.faintSt(), showRepeat("▱", bar-on))
		}
		return b.line()
	}
	bar := ttlBarCells
	for _, try := range []struct {
		word, time bool
		bar        int
	}{{true, true, bar}, {true, true, bar / 2}, {true, true, 0}, {false, true, 0}, {false, false, 0}} {
		if l := build(try.word, try.time, try.bar); l.Width() <= width {
			return l
		}
	}
	return nil
}

// ttlAttr makes the time as bold as the clock when the clock is warning.
func ttlAttr(st cell.Style) cell.Attr { return st.Attr & cell.Bold }

// ttlFace is the clock face for the quarter of the total that is left.
func ttlFace(remaining, total time.Duration) string {
	switch q := showDiv(int64(remaining)*4, int64(total)); {
	case q >= 4:
		return "●"
	case q == 3:
		return "◕"
	case q == 2:
		return "◑"
	}
	return "◔"
}
