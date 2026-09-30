// The cache sparkline: the hit ratio of every request as a row of bars, with the cache's history marked above it
// (docs/UX.md, "Cache sparkline"; chat.png).

package widget

import (
	"math"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// MarkKind is what a mark above the sparkline says happened at that request.
type MarkKind uint8

const (
	// MarkBreak is a cache break (⚠): the prefix stopped matching where it should have.
	MarkBreak MarkKind = iota
	// MarkCompact is a compaction (◆).
	MarkCompact
	// MarkEpoch is a new epoch (↻): a deliberate change of a stable layer.
	MarkEpoch
)

// Mark is an event drawn above the sample it happened at.
type Mark struct {
	At   int // index into the values of Spark
	Kind MarkKind
}

func (k MarkKind) glyph() string {
	switch k {
	case MarkBreak:
		return "⚠"
	case MarkCompact:
		return "◆"
	case MarkEpoch:
		return "↻"
	}
	return "·"
}

// sparkBars are the eight levels of a bar, lowest first.
const sparkBars = "▁▂▃▄▅▆▇█"

// Spark draws the hit ratios in values (each 0..1, oldest first) as a row of ▁▂▃▄▅▆▇█ under a row of marks, and returns the two
// lines, marks first. A bar is green above 0.85, amber above 0.5 and red below, and the red ones are Bold so that they stand
// out without colour too; the height carries the value whatever the colours do.
//
// There is one cell per value, from the left, while they fit in width. With more values than cells every cell stands for a run
// of neighbours and shows the lowest of them, so that a dip is never averaged away; a mark lands on the cell of the sample it
// is about, and when several marks share a cell the most alarming wins (⚠, then ◆, then ↻). Marks whose sample does not exist
// are ignored.
//
// Out-of-range values are clamped to 0..1 and NaN is a gap (a faint dot). No values draws a lone faint dot on the bar row, and a
// width <= 0 draws nothing.
func Spark(values []float64, width int, marks []Mark, p Palette) []cell.Line {
	if width <= 0 {
		return nil
	}
	n := len(values)
	if n == 0 {
		return []cell.Line{nil, cell.Styled(p.faintSt(), stackTrackDot)}
	}
	cells := n
	if n > width {
		cells = width
	}
	bars := newShowCanvas(cells)
	for k := 0; k < cells; k++ {
		// the cell k is the samples i with i*width/n == k, that is ceil(k*n/width) <= i < ceil((k+1)*n/width); none is left out
		lo, hi := k, k+1
		if n > width {
			lo, hi = (k*n+width-1)/width, ((k+1)*n+width-1)/width
		}
		v, ok := math.Inf(1), false
		for _, x := range values[lo:hi] {
			if x != x { // NaN
				continue
			}
			ok = true
			v = math.Min(v, x)
		}
		if !ok {
			bars.put(k, stackTrackDot, p.faintSt())
			continue
		}
		lv := sparkLevel(v)
		bars.put(k, sparkBars[3*lv:3*lv+3], sparkStyle(v, p)) // each bar is three bytes of UTF-8
	}

	top := newShowCanvas(cells)
	best := make([]int, cells) // the kind of the mark shown in a cell, plus one; 0 is none
	for _, m := range marks {
		if m.At < 0 || m.At >= n {
			continue
		}
		x := m.At
		if n > width {
			x = m.At * width / n
		}
		if k := int(m.Kind) + 1; best[x] == 0 || k < best[x] {
			best[x] = k
		}
	}
	for x, k := range best {
		if k == 0 {
			continue
		}
		kind := MarkKind(k - 1)
		st := p.infoSt().With(cell.Bold)
		switch kind {
		case MarkBreak:
			st = p.badSt()
		case MarkCompact:
			st = p.accentSt().With(cell.Bold)
		}
		top.put(x, kind.glyph(), st)
	}
	return []cell.Line{top.trimmed(), bars.line()}
}

// sparkLevel is the index of the bar that shows v: 0 (▁) to 7 (█).
func sparkLevel(v float64) int {
	if v != v || v <= 0 {
		return 0
	}
	if v >= 1 {
		return 7
	}
	return showClamp(int(v*7.99), 0, 7)
}

// sparkStyle colours a bar by how good the ratio is.
func sparkStyle(v float64, p Palette) cell.Style {
	switch {
	case v > 0.85:
		return p.goodSt()
	case v > 0.5:
		return p.warnSt()
	}
	return showFG(p.Bad).With(cell.Bold)
}
