// The swarm gantt: the last minute of every agent as a lane of activity, with the moments that matter marked on it
// (docs/UX.md, "swarm gantt"; swarm.png).

package widget

import (
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// GanttBuckets is how many buckets a gantt shows: the last minute, at a bucket a second.
const GanttBuckets = 60

// LaneMarkKind is a moment marked on a lane.
type LaneMarkKind uint8

const (
	// LaneMail is a message sent or received (✉).
	LaneMail LaneMarkKind = iota
	// LaneCompact is a compaction (◆).
	LaneCompact
	// LaneStuck is the repeat guard firing (⚠).
	LaneStuck
)

// LaneMark is a moment on a lane, in the lane's buckets.
type LaneMark struct {
	Bucket int
	Kind   LaneMarkKind
}

// Lane is one agent's activity over time.
type Lane struct {
	// Name is the agent's short name, drawn in its role's colour at the left; the first four cells of it are.
	Name string
	// Color is the agent's role, an index into Palette.RoleColors.
	Color int
	// Levels is the activity in each bucket (a bucket is a second), 0 (none) to 8 (all busy); Levels[i] is the bucket First+i.
	// Buckets outside the slice are empty.
	Levels []uint8
	First  int
	Marks  []LaneMark
}

// ganttLevels are the nine levels of a lane cell, empty first.
var ganttLevels = [9]string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// level reads a lane bucket and clamps its intensity to zero through eight, returning zero outside
// the retained range.
func (l Lane) level(bucket int) int {
	i := bucket - l.First
	if i < 0 || i >= len(l.Levels) {
		return 0
	}
	return showClamp(int(l.Levels[i]), 0, 8)
}

// Gantt draws the lanes of agents over the last GanttBuckets buckets ending at nowBucket (the newest, at the right edge), in width
// cells:
//
//	m0 ▃▄▄█▂▃▂      ◆     ▅▅▅▆
//	w1 ████▇▇██▇✉█████████████
//	w7 ▁▂▄▄▅▅▃▂▁▂▂▃▃▄▄▃⚠
//	   -60s          -30s        now
//
// A cell is ▁▂▃▄▅▆▇█ by how busy the agent was in its second, blank when it did nothing; the low levels are Dim, so busy stretches
// stand out, and the lane is in the agent's role colour. Marks replace the cell they fall in: ✉ mail, ◆ a compaction, ⚠ stuck
// (Bold). When the lane area is narrower than a minute every cell shows the busiest bucket it covers, so a short burst is never
// lost; when it is wider a bucket is several cells. Under the lanes is the time axis: -60s, -30s and now.
//
// With no lanes or too little width (the names and ten cells) nothing is drawn.
func Gantt(lanes []Lane, width int, nowBucket int, p Palette) []cell.Line {
	if len(lanes) == 0 {
		return nil
	}
	nameW := 2
	names := make([]string, len(lanes))
	for i, l := range lanes {
		names[i] = showTrunc(showClean(l.Name), 4)
		if w := cell.StringWidth(names[i]); w > nameW {
			nameW = w
		}
	}
	area := width - nameW - 1
	if area < 10 {
		return nil
	}
	nowBucket = showClamp(nowBucket, -1<<40, 1<<40) // far from overflowing whatever it is
	start := nowBucket - GanttBuckets + 1           // the oldest bucket shown

	var out []cell.Line
	for i, l := range lanes {
		c := newShowCanvas(width)
		c.put(0, names[i], p.roleSt(l.Color))
		for x := 0; x < area; x++ {
			// the buckets this cell covers, none of them twice: several when the area is narrower than a minute, one when it is wider
			lo := start + x*GanttBuckets/area
			hi := max(lo+1, start+(x+1)*GanttBuckets/area)
			lv := 0
			for b := lo; b < hi; b++ {
				lv = max(lv, l.level(b))
			}
			if lv == 0 {
				continue
			}
			st := showFG(p.roleCol(l.Color))
			if lv <= 2 {
				st = st.With(cell.Dim)
			}
			c.put(nameW+1+x, ganttLevels[lv], st)
		}
		for _, m := range l.Marks {
			if m.Bucket < start || m.Bucket > nowBucket {
				continue
			}
			x := ((m.Bucket-start+1)*area+GanttBuckets-1)/GanttBuckets - 1 // the last cell of the bucket's span
			glyph, st := "✉", p.warnSt().With(cell.Bold)
			switch m.Kind {
			case LaneCompact:
				glyph, st = "◆", p.accentSt().With(cell.Bold)
			case LaneStuck:
				glyph, st = "⚠", p.badSt()
			}
			c.put(nameW+1+showClamp(x, 0, area-1), glyph, st)
		}
		out = append(out, c.trimmed())
	}

	axis := newShowCanvas(width)
	at := nameW + 1
	axis.put(at, "-60s", p.dimSt())
	if area >= 26 {
		axis.put(at+area/2-2, "-30s", p.dimSt())
	}
	if area >= 16 {
		axis.put(at+area-3, "now", p.dimSt())
	}
	return append(out, axis.trimmed())
}
