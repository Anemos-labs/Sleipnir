package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/showtest"
)

func TestGanttGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Gantt: the last minute of every agent; a cell is ▁▂▃▄▅▆▇█ by how busy, marks ✉ mail ◆ compaction ⚠ stuck, the axis is -60s -30s now")
	lanes := showLanes(showSketchAgents())
	for _, w := range []int{70, 50, 30, 18} {
		d.Add(fmt.Sprintf("eight agents, width %d", w), widget.Gantt(lanes, w, 59, p), w)
	}
	d.Note("wider than a minute: a bucket is several cells")
	d.Add("width 100", widget.Gantt(lanes[:3], 100, 59, p), 100)
	d.Note("the window slides with nowBucket: the same lanes 20 seconds later have lost their oldest 20 buckets to the left edge")
	d.Add("now = 79", widget.Gantt(lanes[:3], 50, 79, p), 50)
	d.Add("now = 29 (only half a minute of history)", widget.Gantt(lanes[:3], 50, 29, p), 50)
	d.Note("a lane with a short name, a long name, and levels above 8")
	odd := []widget.Lane{
		{Name: "a", Color: 0, Levels: []uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 200}},
		{Name: "worker-long", Color: 9, Levels: []uint8{8, 8, 8}, First: 57},
	}
	d.Add("odd lanes", widget.Gantt(odd, 40, 59, p), 40)
	showGolden(t, "gantt", &d)
}

func TestGanttStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("the lane is in the agent's role colour; ▁ and ▂ are Dim; marks are Bold (mail warn, compaction accent, stuck bad)")
	lanes := showLanes(showSketchAgents())
	d.AddText("m0, w1 and w7 at width 34", showtest.FlattenStyled(widget.Gantt([]widget.Lane{lanes[0], lanes[1], lanes[7]}, 34, 59, p), showNames(p)))
	showGolden(t, "gantt_styled", &d)
}

// A one-bucket burst must show whatever the width: a narrow gantt takes the busiest bucket of every cell, not an average.
func TestGanttNeverLosesABurst(t *testing.T) {
	p := widget.DefaultPalette()
	for _, w := range []int{14, 20, 30, 37, 47, 59, 64, 80, 120} {
		for b := 0; b < widget.GanttBuckets; b++ {
			lv := make([]uint8, widget.GanttBuckets)
			lv[b] = 8
			lines := widget.Gantt([]widget.Lane{{Name: "a", Levels: lv}}, w, widget.GanttBuckets-1, p)
			if got := strings.Count(lines[0].Plain(), "█"); got < 1 {
				t.Fatalf("width %d: the burst in bucket %d is gone: %q", w, b, lines[0].Plain())
			}
		}
	}
}

// A mark lands in the cell that shows its bucket: with only one busy bucket marked, the mark replaces that very cell.
func TestGanttMarksLandOnTheirBucket(t *testing.T) {
	p := widget.DefaultPalette()
	for _, w := range []int{20, 35, 48, 66, 90} {
		for b := 0; b < widget.GanttBuckets; b += 7 {
			lv := make([]uint8, widget.GanttBuckets)
			lv[b] = 8
			lines := widget.Gantt([]widget.Lane{{Name: "a", Levels: lv, Marks: []widget.LaneMark{{Bucket: b, Kind: widget.LaneCompact}}}}, w, widget.GanttBuckets-1, p)
			row := []rune(lines[0].Plain())
			if !strings.ContainsRune(lines[0].Plain(), '◆') {
				t.Fatalf("width %d bucket %d: the mark is missing: %q", w, b, lines[0].Plain())
			}
			for x, r := range row {
				if r == '◆' && x < 3 {
					t.Fatalf("width %d bucket %d: the mark is in the name column", w, b)
				}
			}
			// the level of the bucket is hidden under the mark, never drawn next to it as a second copy
			if strings.ContainsRune(lines[0].Plain(), '█') && strings.Count(lines[0].Plain(), "█")+strings.Count(lines[0].Plain(), "◆") > (w-3)/widget.GanttBuckets+2 {
				t.Fatalf("width %d bucket %d: the mark and its bucket are in different cells: %q", w, b, lines[0].Plain())
			}
		}
	}
	// marks outside the window are not drawn
	lines := widget.Gantt([]widget.Lane{{Name: "a", Marks: []widget.LaneMark{{Bucket: -1, Kind: widget.LaneMail}, {Bucket: 60, Kind: widget.LaneStuck}}}}, 30, 59, p)
	if strings.ContainsAny(lines[0].Plain(), "✉⚠") {
		t.Errorf("marks outside the last minute are drawn: %q", lines[0].Plain())
	}
}

func TestGanttAxisAndNames(t *testing.T) {
	p := widget.MonoPalette()
	lanes := showLanes(showSketchAgents())
	lines := widget.Gantt(lanes, 70, 59, p)
	if len(lines) != len(lanes)+1 {
		t.Fatalf("%d lines for %d lanes and an axis", len(lines), len(lanes))
	}
	axis := lines[len(lines)-1].Plain()
	for _, want := range []string{"-60s", "-30s", "now"} {
		if !strings.Contains(axis, want) {
			t.Errorf("the axis %q lacks %s", axis, want)
		}
	}
	if !strings.HasSuffix(axis, "now") || len([]rune(axis)) != 70 {
		t.Errorf("now is at the right edge of the lane area: %q (%d cells)", axis, len([]rune(axis)))
	}
	for i, l := range lanes {
		if !strings.HasPrefix(lines[i].Plain(), l.Name+" ") {
			t.Errorf("lane %d starts with %q, want its name %q", i, lines[i].Plain(), l.Name)
		}
	}
	// the lane of a busy agent is bolder than that of an idle one
	busy := strings.Count(lines[1].Plain(), "█") + strings.Count(lines[1].Plain(), "▇")
	idle := strings.Count(lines[5].Plain(), "█") + strings.Count(lines[5].Plain(), "▇")
	if busy <= idle {
		t.Errorf("w1 (busy) has %d high cells, w5 (idle) %d", busy, idle)
	}
}

func TestGanttLowLevelsAreDim(t *testing.T) {
	p := showPal()
	// a name takes two cells and a space, so width 63 is a lane of exactly 60 cells: a bucket each
	lines := widget.Gantt([]widget.Lane{{Name: "a", Color: 2, Levels: []uint8{1, 2, 3, 8}, First: 56}}, 63, 59, p)
	var dim, bright int
	for _, sp := range lines[0] {
		for _, r := range sp.Text {
			switch r {
			case '▁', '▂':
				if !sp.Style.Has(cell.Dim) {
					t.Errorf("%c is not Dim", r)
				}
				dim++
			case '▃', '█':
				if sp.Style.Has(cell.Dim) || sp.Style.FG != p.RoleColors[2] {
					t.Errorf("%c is Dim or not in the role colour: %+v", r, sp.Style)
				}
				bright++
			}
		}
	}
	if dim != 2 || bright != 2 {
		t.Errorf("dim %d bright %d, want 2 and 2: %q", dim, bright, lines[0].Plain())
	}
}

func TestGanttEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	lane := widget.Lane{Name: "m0", Levels: []uint8{1, 2, 3}}
	if widget.Gantt(nil, 50, 59, p) != nil || widget.Gantt([]widget.Lane{}, 50, 59, p) != nil {
		t.Error("no lanes draws nothing")
	}
	for _, w := range []int{-3, 0, 1, 5, 12} {
		if got := widget.Gantt([]widget.Lane{lane}, w, 59, p); got != nil {
			t.Errorf("width %d is too short for a lane: %q", w, showtest.Flatten(got))
		}
	}
	// a negative nowBucket and a lane with no levels are fine
	if got := widget.Gantt([]widget.Lane{lane, {Name: "w1"}}, 40, -100, p); len(got) != 3 {
		t.Errorf("%d lines", len(got))
	}
	if got := widget.Gantt([]widget.Lane{{Name: "a\x1b[2J\nb", Levels: []uint8{9}}}, 40, 0, p); len(got) == 0 {
		t.Error("an injected name")
	} else {
		showNoControl(t, "gantt", got)
	}
}
