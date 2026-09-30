package widget_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

// showSharedLayers is the shared prefix of the storyboard: G0 to G2, 41.2k tokens.
func showSharedLayers() []widget.Layer {
	return []widget.Layer{{Name: "G0", Tokens: 14000}, {Name: "G1", Tokens: 17200}, {Name: "G2", Tokens: 10000}}
}

func TestForkGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Fork: a worker appears (storyboard panel 3): its row, its bar filling in the shared colours, then only the task card is paid")
	for _, pr := range []float64{0, 0.1, 0.25, 0.5, 0.7, 0.8, 1} {
		d.Add("progress "+strconv.FormatFloat(pr, 'f', 2, 64)+", width 56", widget.Fork(showSharedLayers(), 800, pr, 56, p), 56)
	}
	d.Note("other widths")
	for _, w := range []int{40, 30, 20, 14} {
		d.Add("finished, width "+strconv.Itoa(w), widget.Fork(showSharedLayers(), 800, 1, w, p), w)
	}
	d.Note("a big task card is as wide as it is big next to the prefix")
	d.Add("inherits 41.2k, pays 12k", widget.Fork(showSharedLayers(), 12000, 1, 56, p), 56)
	d.Note("no shared prefix: everything is the task card")
	d.Add("nothing shared", widget.Fork(nil, 800, 1, 40, p), 40)
	d.Add("nothing at all", widget.Fork(nil, 0, 1, 40, p), 40)
	showGolden(t, "fork", &d)
}

func TestForkStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("the inherited part is in the layers' colours, the task card is warn (▒)")
	d.AddText("progress 0.4", showtest.FlattenStyled(widget.Fork(showSharedLayers(), 800, 0.4, 44, p), showNames(p)))
	d.AddText("progress 1", showtest.FlattenStyled(widget.Fork(showSharedLayers(), 800, 1, 44, p), showNames(p)))
	showGolden(t, "fork_styled", &d)
}

func TestForkHeightIsConstant(t *testing.T) {
	p := widget.DefaultPalette()
	for _, w := range []int{3, 10, 14, 15, 40, 100} {
		want := -1
		for i := 0; i <= 50; i++ {
			got := len(widget.Fork(showSharedLayers(), 800, float64(i)/50, w, p))
			if want < 0 {
				want = got
			}
			if got != want {
				t.Fatalf("width %d: %d lines at progress %.2f, %d at 0", w, got, float64(i)/50, want)
			}
		}
	}
}

// The worker's bar only ever grows, and the task card is what is left of the bar after the prefix.
func TestForkBarFillsFromTheLeft(t *testing.T) {
	p := widget.MonoPalette()
	prev := 0
	for i := 0; i <= 100; i++ {
		lines := widget.Fork(showSharedLayers(), 800, float64(i)/100, 56, p)
		row := []rune(lines[5].Plain())
		cells := 0
		for _, r := range row[7:] {
			if r != ' ' {
				cells++
			}
		}
		if cells < prev {
			t.Fatalf("progress %.2f: the worker's bar shrank from %d to %d cells:\n%s", float64(i)/100, prev, cells, showtest.Flatten(lines))
		}
		prev = cells
	}
	if prev != 56-7 {
		t.Errorf("a finished worker's bar spans the bar area (49 cells), not %d", prev)
	}
	end := widget.Fork(showSharedLayers(), 800, 1, 56, p)
	if row := end[5].Plain(); !strings.HasSuffix(strings.TrimRight(row, " "), "▒") || strings.Count(row, "▒") < 1 {
		t.Errorf("the task card is ▒ at the right end of the bar: %q", row)
	}
	if !strings.Contains(end[6].Plain(), "inherits 41.2k · pays 800 (the task card)") {
		t.Errorf("caption: %q", end[6].Plain())
	}
	if !strings.Contains(end[3].Plain(), "↓ inherited at the cache READ price") {
		t.Errorf("the arrow line: %q", end[3].Plain())
	}
}

func TestForkCaptionGrows(t *testing.T) {
	p := widget.MonoPalette()
	early := widget.Fork(showSharedLayers(), 800, 0.3, 56, p)
	if strings.Contains(showtest.Flatten(early), "inherits") || strings.Contains(showtest.Flatten(early), "pays") {
		t.Errorf("the caption waits for the bar to fill:\n%s", showtest.Flatten(early))
	}
	mid := widget.Fork(showSharedLayers(), 800, 0.75, 56, p)
	if !strings.Contains(mid[6].Plain(), "inherits 41.2k") || strings.Contains(mid[6].Plain(), "pays") {
		t.Errorf("first what is inherited: %q", mid[6].Plain())
	}
	// tokens count: the task card always gets a cell
	tiny := widget.Fork(showSharedLayers(), 1, 1, 56, p)
	if !strings.Contains(tiny[5].Plain(), "▒") {
		t.Errorf("a one-token task card still gets a cell: %q", tiny[5].Plain())
	}
}

func TestForkEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.Fork(showSharedLayers(), 800, 1, 0, p) != nil || widget.Fork(showSharedLayers(), 800, 1, -4, p) != nil {
		t.Error("a width <= 0 draws nothing")
	}
	if got := showtest.Flatten(widget.Fork(showSharedLayers(), 800, math.NaN(), 50, p)); got != showtest.Flatten(widget.Fork(showSharedLayers(), 800, 0, 50, p)) {
		t.Error("NaN progress is 0")
	}
	if got := showtest.Flatten(widget.Fork(showSharedLayers(), -5, 1, 50, p)); !strings.Contains(got, "pays 0") {
		t.Errorf("negative own tokens are 0:\n%s", got)
	}
	if got := widget.Fork([]widget.Layer{{Name: "G0", Tokens: -4}, {Name: "G1", Tokens: 0}}, 100, 1, 50, p); len(got) == 0 {
		t.Error("layers with no tokens")
	}
	if got := widget.Fork(showSharedLayers(), math.MaxInt, 1, 50, p); len(got) == 0 {
		t.Error("huge own tokens")
	}
}
