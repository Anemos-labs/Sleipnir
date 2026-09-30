package widget_test

import (
	"math"
	"strconv"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

// showChatHits is the hit-ratio history of the chat sketch.
func showChatHits() []float64 {
	return []float64{.2, .55, .8, .93, .96, .97, .97, .96, .95, .7, .42, .9, .97, .96, .97, .98, .95, .96, .97, .93}
}

func TestSparkGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Spark: two lines, the marks above and the bars below; ⚠ a cache break, ◆ a compaction, ↻ an epoch")
	hits := showChatHits()
	marks := []widget.Mark{{At: 10, Kind: widget.MarkBreak}, {At: 17, Kind: widget.MarkCompact}, {At: 2, Kind: widget.MarkEpoch}}
	d.Add("the sketch: 20 requests", widget.Spark(hits, 30, marks, p), 30)
	d.Add("only the last 12 cells: the history is resampled, never averaged", widget.Spark(hits, 12, marks, p), 12)
	d.Add("a width of 5", widget.Spark(hits, 5, marks, p), 5)

	d.Note("a long history in a short row: the lowest sample of every run is what the cell shows, so the one bad request (index 97) survives")
	long := make([]float64, 200)
	for i := range long {
		long[i] = 0.97
	}
	long[97] = 0.12
	longMarks := []widget.Mark{{At: 97, Kind: widget.MarkBreak}, {At: 150, Kind: widget.MarkEpoch}}
	for _, w := range []int{50, 20, 7} {
		d.Add("200 requests, width "+strconv.Itoa(w), widget.Spark(long, w, longMarks, p), w)
	}

	d.Note("every bar height, and the colour thresholds (above .85 good, above .5 amber, else red)")
	var ramp []float64
	for v := 0.0; v <= 1.0001; v += 0.0625 {
		ramp = append(ramp, v)
	}
	d.Add("0 to 1 in 17 steps", widget.Spark(ramp, 30, nil, p), 30)

	d.Note("odd input: NaN is a gap, out-of-range values are clamped")
	d.Add("NaN, -1, 2, +Inf", widget.Spark([]float64{math.NaN(), -1, 2, math.Inf(1), 0.5}, 10, nil, p), 10)
	d.Add("no values", widget.Spark(nil, 10, nil, p), 10)
	showGolden(t, "spark", &d)
}

func TestSparkStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("the colour of a bar is its health: above .85 good, above .5 warn, else bad (and Bold)")
	lines := widget.Spark(showChatHits(), 30, []widget.Mark{{At: 10, Kind: widget.MarkBreak}, {At: 17, Kind: widget.MarkCompact}, {At: 2, Kind: widget.MarkEpoch}}, p)
	d.AddText("the sketch", showtest.FlattenStyled(lines, showNames(p)))
	showGolden(t, "spark_styled", &d)
}

// A one-sample dip must still be on screen after downsampling: the cell that stands for it shows the lowest bar, and the
// mark of the sample sits above that same cell.
func TestSparkKeepsADipWhenDownsampling(t *testing.T) {
	p := widget.DefaultPalette()
	for _, n := range []int{50, 97, 200, 1000, 4097} {
		for _, w := range []int{1, 2, 7, 20, 49} {
			if w >= n {
				continue
			}
			for _, at := range []int{0, 1, n / 3, n / 2, n - 2, n - 1} {
				vals := make([]float64, n)
				for i := range vals {
					vals[i] = 0.97
				}
				vals[at] = 0.05
				lines := widget.Spark(vals, w, []widget.Mark{{At: at, Kind: widget.MarkBreak}}, p)
				if len(lines) != 2 {
					t.Fatalf("n=%d w=%d: %d lines", n, w, len(lines))
				}
				bars := []rune(lines[1].Plain())
				if len(bars) != w {
					t.Fatalf("n=%d w=%d: %d cells", n, w, len(bars))
				}
				dips := 0
				dipAt := -1
				for x, r := range bars {
					if r == '▁' {
						dips++
						dipAt = x
					}
				}
				if dips != 1 {
					t.Fatalf("n=%d w=%d at=%d: the dip is on %d cells, want exactly 1: %q", n, w, at, dips, lines[1].Plain())
				}
				marks := []rune(lines[0].Plain())
				if dipAt >= len(marks) || marks[dipAt] != '⚠' {
					t.Fatalf("n=%d w=%d at=%d: the mark is not above the dip (cell %d): %q", n, w, at, dipAt, lines[0].Plain())
				}
			}
		}
	}
}

func TestSparkOneCellPerValueWhileTheyFit(t *testing.T) {
	p := widget.DefaultPalette()
	vals := showChatHits()
	lines := widget.Spark(vals, 40, nil, p)
	if got := []rune(lines[1].Plain()); len(got) != len(vals) {
		t.Fatalf("%d cells for %d values", len(got), len(vals))
	}
	// the bar height is monotonic in the value
	ramp := []float64{0, 0.13, 0.26, 0.38, 0.51, 0.63, 0.76, 0.9, 1}
	bars := []rune(widget.Spark(ramp, 20, nil, p)[1].Plain())
	for i := 1; i < len(bars); i++ {
		if bars[i] < bars[i-1] {
			t.Errorf("bars fall as the value rises: %q", string(bars))
		}
	}
	if bars[0] != '▁' || bars[len(bars)-1] != '█' {
		t.Errorf("the range is ▁ to █: %q", string(bars))
	}
}

func TestSparkColourThresholds(t *testing.T) {
	p := showPal()
	lines := widget.Spark([]float64{0.86, 0.85, 0.51, 0.5, 0.0}, 10, nil, p)
	want := []cell.Color{p.Good, p.Warn, p.Warn, p.Bad, p.Bad}
	got := lines[1]
	var cols []cell.Color
	for _, sp := range got {
		for range []rune(sp.Text) {
			cols = append(cols, sp.Style.FG)
		}
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("value %d: colour %v, want %v", i, cols[i], want[i])
		}
	}
	for _, sp := range got {
		if sp.Style.FG == p.Bad && !sp.Style.Has(cell.Bold) {
			t.Error("a red bar is Bold so that it stands out without colour")
		}
	}
}

func TestSparkMarksPriorityAndBounds(t *testing.T) {
	p := widget.DefaultPalette()
	vals := []float64{.9, .9, .9, .9}
	marks := []widget.Mark{{At: 1, Kind: widget.MarkEpoch}, {At: 1, Kind: widget.MarkBreak}, {At: 2, Kind: widget.MarkCompact}, {At: 2, Kind: widget.MarkEpoch},
		{At: -1, Kind: widget.MarkBreak}, {At: 4, Kind: widget.MarkBreak}, {At: 99, Kind: widget.MarkBreak}, {At: 3, Kind: widget.MarkEpoch}}
	lines := widget.Spark(vals, 10, marks, p)
	if got := lines[0].Plain(); got != " ⚠◆↻" {
		t.Errorf("marks %q: the most alarming mark on a cell wins, marks off the ends are ignored", got)
	}
}

func TestSparkEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	for _, w := range []int{-5, 0} {
		if got := widget.Spark(showChatHits(), w, nil, p); got != nil {
			t.Errorf("width %d: %v", w, got)
		}
	}
	got := widget.Spark(nil, 5, nil, p)
	if len(got) != 2 || got[1].Plain() != "·" {
		t.Errorf("no values: %q", showtest.Flatten(got))
	}
	got = widget.Spark([]float64{math.NaN(), 0.9}, 5, nil, p)
	if got[1].Plain() != "·█" && got[1].Plain() != "·▇" {
		t.Errorf("NaN is a gap: %q", got[1].Plain())
	}
	got = widget.Spark([]float64{math.NaN(), math.NaN(), 0.9, 0.9}, 2, nil, p)
	if got[1].Plain() != "·▇" && got[1].Plain() != "·█" {
		t.Errorf("a run of NaN is a gap: %q", got[1].Plain())
	}
}
