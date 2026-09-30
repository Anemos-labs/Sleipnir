package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

func TestFanGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Fan: the shared prefix and a line to every rider; ┃ read most from the cache, │ a fair share, ╎ less, ╎ dim (and a dim label) nothing")
	riders := []string{"m0", "w1", "w2", "w3", "w4", "w5", "w6", "w7"}
	weights := []int{0, 41200, 41200, 20000, 22000, 0, 0, 30000}
	for _, w := range []int{56, 48, 40, 32, 24} {
		d.Add(fmt.Sprintf("eight riders, width %d", w), widget.Fan(riders, weights, w, p), w)
	}
	d.Note("more riders than fit: the first ones and a count")
	many := make([]string, 50)
	mw := make([]int, 50)
	for i := range many {
		many[i] = fmt.Sprintf("w%d", i)
		mw[i] = 1000 * (i%5 + 1)
	}
	d.Add("50 riders, width 40", widget.Fan(many, mw, 40, p), 40)
	d.Add("50 riders, width 12", widget.Fan(many, mw, 12, p), 12)
	d.Note("the real shared layers (G0-G3) instead of the default three")
	d.Add("shared: the chat prompt's first four layers", widget.FanShared(showChatLayers()[:4], riders[:4], []int{9100, 9100, 0, 4000}, 40, p), 40)
	d.Note("odd input")
	d.Add("no riders", widget.Fan(nil, nil, 30, p), 30)
	d.Add("riders without weights", widget.Fan(riders[:3], nil, 30, p), 30)
	d.Add("long names are cut", widget.Fan([]string{"manager-zero", "worker-one"}, []int{5, 5}, 30, p), 30)
	d.Add("one cell", widget.Fan(riders, weights, 1, p), 1)
	showGolden(t, "fan", &d)
}

func TestFanStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("lines are layer G1's colour: Bold for heavy, Dim for a rider that read nothing; its label is dim too")
	riders := []string{"m0", "w1", "w2", "w3"}
	d.AddText("four riders", showtest.FlattenStyled(widget.Fan(riders, []int{0, 40000, 20000, 3000}, 28, p), showNames(p)))
	showGolden(t, "fan_styled", &d)
}

// A line's weight is a glyph: the heaviest reader gets ┃, nothing read is ╎ dim.
func TestFanLineWeights(t *testing.T) {
	p := showPal()
	lines := widget.Fan([]string{"a", "b", "c", "d"}, []int{100, 50, 10, 0}, 32, p)
	if len(lines) != 6 {
		t.Fatalf("%d lines", len(lines))
	}
	row := lines[2]
	var glyphs []string
	var dims []bool
	for _, sp := range row {
		for _, r := range sp.Text {
			if r != ' ' {
				glyphs = append(glyphs, string(r))
				dims = append(dims, sp.Style.Has(cell.Dim))
			}
		}
	}
	if strings.Join(glyphs, "") != "┃│╎╎" {
		t.Fatalf("glyphs %q, want ┃│╎╎", strings.Join(glyphs, ""))
	}
	if dims[2] || !dims[3] {
		t.Errorf("a rider that read a little is bright, one that read nothing is dim: %v", dims)
	}
	// every rider's label sits under its line
	for x, r := range []rune(lines[2].Plain()) {
		if r != ' ' && []rune(lines[5].Plain())[x] == ' ' {
			t.Errorf("the line at column %d has no label under it:\n%s", x, showtest.Flatten(lines))
		}
	}
}

func TestFanRidersAreSpreadEvenly(t *testing.T) {
	p := widget.MonoPalette()
	lines := widget.Fan([]string{"m0", "w1", "w2", "w3", "w4", "w5", "w6", "w7"}, []int{1, 1, 1, 1, 1, 1, 1, 1}, 56, p)
	var xs []int
	for x, r := range []rune(lines[2].Plain()) {
		if r != ' ' {
			xs = append(xs, x)
		}
	}
	if len(xs) != 8 {
		t.Fatalf("eight lines, got %v", xs)
	}
	for i := 1; i < len(xs); i++ {
		if d := xs[i] - xs[i-1]; d != 7 {
			t.Errorf("the lines are %d cells apart at %d, want 7: %v", d, i, xs)
		}
	}
}

func TestFanEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.Fan([]string{"a"}, []int{1}, 0, p) != nil || widget.Fan([]string{"a"}, []int{1}, -2, p) != nil {
		t.Error("a width <= 0 draws nothing")
	}
	if got := widget.Fan(nil, nil, 30, p); len(got) != 2 {
		t.Errorf("no riders is the bar and its labels: %d lines", len(got))
	}
	// negative weights are nothing, a short weight list is zeros
	got := widget.Fan([]string{"a", "b", "c"}, []int{-5, 7}, 30, p)
	if len(got) != 6 {
		t.Fatalf("%d lines", len(got))
	}
	// an injected label cannot smuggle a control character in
	got = widget.Fan([]string{"a\x1b[31m", "b\nc"}, []int{1, 1}, 30, p)
	showNoControl(t, "fan labels", got)
}
