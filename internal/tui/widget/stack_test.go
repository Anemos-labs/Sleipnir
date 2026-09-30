package widget_test

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

// showChatLayers is the prompt of the chat sketch: 48.3k tokens in seven layers.
func showChatLayers() []widget.Layer {
	return []widget.Layer{
		{Name: "G0 const", Tokens: 9100, Breakpoint: true, Hash: "3fa9c1d2", Epoch: 0},
		{Name: "G1 shared", Tokens: 12400, Hash: "b71e04aa", Epoch: 0},
		{Name: "G2 role", Tokens: 6000, Breakpoint: true, Hash: "0c5de9f1", Epoch: 1},
		{Name: "G3", Tokens: 3200, Hash: "9a2b7c30", Epoch: 2},
		{Name: "G4", Tokens: 1800, Hash: "e4410d6b", Epoch: 2},
		{Name: "G5 thread", Tokens: 14600, Breakpoint: true, Hash: "51d8a3ee", Epoch: 3},
		{Name: "G6", Tokens: 1200, Hash: "77c0f9b4", Epoch: 3},
	}
}

func showBar(layers []widget.Layer, o widget.StackOpts, p widget.Palette) []cell.Line {
	bar, labels := widget.StackBar(layers, o, p)
	return []cell.Line{bar, labels}
}

func TestStackBarGolden(t *testing.T) {
	p := widget.DefaultPalette()
	layers := showChatLayers()
	total := 0
	for _, l := range layers {
		total += l.Tokens
	}
	var d showtest.Doc
	d.Note("StackBar: █ cached, ▓ where the cache ends, ░ paid, ▏ a provider breakpoint (the last cell of its layer), ⚠ a cache break")
	d.Note("the prompt of the chat sketch: %d tokens in 7 layers; breakpoints on G0, G2 and G5", total)

	for _, w := range []int{20, 40, 50, 80} {
		o := widget.NewStackOpts(w, 44900)
		d.Add(fmt.Sprintf("93%% cached, width %d", w), showBar(layers, o, p), w)
	}

	d.Note("the cached share: none (the first request), half, all")
	for _, c := range []int{0, total / 2, total} {
		o := widget.NewStackOpts(50, c)
		d.Add(fmt.Sprintf("cached %d of %d tokens", c, total), showBar(layers, o, p), 50)
	}

	d.Note("the sweep: a light runs along the bar to the match length; the bright part stops at the light")
	for _, s := range []float64{0, 0.2, 0.4, 0.6, 0.8, 0.93, 1} {
		o := widget.NewStackOpts(50, 44900)
		o.Sweep = s
		d.Add(fmt.Sprintf("sweep %.2f", s), showBar(layers, o, p), 50)
	}

	d.Note("a cache break at G3: the layer is drawn in the alarm style and the label gets a warning sign")
	o := widget.NewStackOpts(50, 22300)
	o.BreakAt = 3
	d.Add("break at G3", showBar(layers, o, p), 50)

	d.Note("a cooling cache fades; a cold one turns █ into ▒")
	for _, wm := range []float64{1, 0.6, 0.3, 0} {
		o := widget.NewStackOpts(50, 44900)
		o.Warm = wm
		d.Add(fmt.Sprintf("warm %.1f", wm), showBar(layers, o, p), 50)
	}
	showGolden(t, "stack_bar", &d)
}

func TestStackBarStyledGolden(t *testing.T) {
	p := showPal()
	layers := showChatLayers()
	var d showtest.Doc
	d.Note("{style|text}: G0..G6 are the layer colours, +d is the Dim attribute, +b Bold; bad is the alarm")
	o := widget.NewStackOpts(40, 29000)
	d.AddText("cached 60%, width 40", showtest.FlattenStyled(showBar(layers, o, p), showNames(p)))
	o.BreakAt = 2
	d.AddText("break at G2", showtest.FlattenStyled(showBar(layers, o, p), showNames(p)))
	o = widget.NewStackOpts(40, 29000)
	o.Sweep = 0.4
	d.AddText("sweep 0.4", showtest.FlattenStyled(showBar(layers, o, p), showNames(p)))
	o = widget.NewStackOpts(40, 29000)
	o.Warm = 0.5
	d.AddText("warm 0.5: faded towards the faint colour", showtest.FlattenStyled(showBar(layers, o, p), showNames(p)))
	showGolden(t, "stack_bar_styled", &d)
}

// showRandLayers is a random prompt: up to nine layers, some empty, some huge, with names of every kind.
func showRandLayers(r *rand.Rand) []widget.Layer {
	names := []string{"", "G0", "G1 const", "G2", "G3 notes", "G4", "G5 thread", "G6", "x", "G9 beyond", "  ", "a\x1b[31mb", "G3\nG4"}
	layers := make([]widget.Layer, r.Intn(10))
	for i := range layers {
		tok := 0
		switch r.Intn(6) {
		case 0: // none
		case 1:
			tok = r.Intn(5)
		case 2:
			tok = 1 << 40
		case 3:
			tok = -r.Intn(100)
		default:
			tok = r.Intn(40000)
		}
		layers[i] = widget.Layer{Name: names[r.Intn(len(names))], Tokens: tok, Breakpoint: r.Intn(3) == 0}
	}
	return layers
}

// cellsPerLayer counts the cells of the bar in each layer's colour (the test palette gives every layer its own).
func showCellsPerLayer(bar cell.Line, p widget.Palette) (perLayer [7]int, other int) {
	for _, sp := range bar {
		n := len([]rune(sp.Text))
		found := false
		for i, c := range p.LayerColors {
			if sp.Style.FG == c {
				perLayer[i] += n
				found = true
			}
		}
		if !found {
			other += n
		}
	}
	return perLayer, other
}

// The cells of the bar add up to the width exactly, every layer that has tokens has a cell while there are cells to give, and
// the cells follow the tokens: what the layers get is what the largest-remainder rule gives.
func TestStackBarCellsSumToTheWidth(t *testing.T) {
	p := showPal()
	r := showRand(11)
	for i := 0; i < 600; i++ {
		layers := showRandLayers(r)
		for k := range layers { // the bar colours by the G-number, so make every layer a different one
			layers[k].Name = fmt.Sprintf("G%d", k%7)
			if k >= 7 {
				layers = layers[:7]
				break
			}
		}
		w := 1 + r.Intn(200)
		o := widget.NewStackOpts(w, r.Intn(60000))
		bar, _ := widget.StackBar(layers, o, p)
		if bar.Width() != w {
			t.Fatalf("width %d: the bar is %d cells: %q", w, bar.Width(), bar.Plain())
		}
		var total int64
		nonEmpty := 0
		for _, l := range layers {
			if l.Tokens > 0 {
				total += int64(min(l.Tokens, 1<<40))
				nonEmpty++
			}
		}
		if total == 0 {
			continue
		}
		per, other := showCellsPerLayer(bar, p)
		if other != 0 {
			t.Fatalf("cells in no layer's colour: %q", bar.Plain())
		}
		sum := 0
		for k, l := range layers {
			sum += per[k]
			if l.Tokens <= 0 && per[k] != 0 {
				t.Fatalf("layer %d has no tokens but %d cells", k, per[k])
			}
			if l.Tokens > 0 && w >= nonEmpty && per[k] < 1 {
				t.Fatalf("width %d: layer %d has %d tokens and no cell: %v", w, k, l.Tokens, per)
			}
		}
		if sum != w {
			t.Fatalf("width %d: the layers have %d cells in all: %v", w, sum, per)
		}
		// a bigger layer never has fewer cells than a smaller one when every layer is big enough to be rounded, not raised
		exact := true
		for _, l := range layers {
			if l.Tokens > 0 && int64(min(l.Tokens, 1<<40))*int64(w) < total {
				exact = false
			}
		}
		if exact {
			for a := range layers {
				for b := range layers {
					if layers[a].Tokens > layers[b].Tokens && layers[b].Tokens > 0 && per[a] < per[b] {
						t.Fatalf("layer %d (%d tokens) has fewer cells than layer %d (%d): %v", a, layers[a].Tokens, b, layers[b].Tokens, per)
					}
				}
			}
		}
	}
}

// The cells the bar calls cached are never more than the cached fraction of the bar plus the one where it ends.
func TestStackBarCachedNeverExceedsTheFraction(t *testing.T) {
	p := widget.MonoPalette()
	r := showRand(12)
	for i := 0; i < 800; i++ {
		layers := showRandLayers(r)
		w := 1 + r.Intn(200)
		var total int64
		for _, l := range layers {
			if l.Tokens > 0 {
				total += int64(min(l.Tokens, 1<<40))
			}
		}
		if total == 0 {
			continue
		}
		cached := r.Intn(int(min(total, 1<<30))+1) - r.Intn(2)*r.Intn(1000) // sometimes a little negative
		o := widget.NewStackOpts(w, cached)
		bar, _ := widget.StackBar(layers, o, p)
		plain := bar.Plain()
		hot := strings.Count(plain, "█") + strings.Count(plain, "▓")
		frac := float64(max(cached, 0)) / float64(total)
		if limit := int(frac*float64(w)) + 1; hot > limit {
			t.Fatalf("w=%d cached %d of %d: %d bright cells, at most %d allowed: %q", w, cached, total, hot, limit, plain)
		}
		if strings.Count(plain, "▓") > 1 {
			t.Fatalf("more than one cell where the cache ends: %q", plain)
		}
		// more cached tokens never light fewer cells
		o2 := widget.NewStackOpts(w, max(cached, 0)+r.Intn(1000))
		bar2, _ := widget.StackBar(layers, o2, p)
		hot2 := strings.Count(bar2.Plain(), "█") + strings.Count(bar2.Plain(), "▓")
		if hot2 < hot-1 { // a breakpoint tick may take the place of one bright cell
			t.Fatalf("more cached tokens, fewer bright cells: %d then %d: %q %q", hot, hot2, plain, bar2.Plain())
		}
	}
	// all cached: no ░ and no edge; none cached: no █
	layers := showChatLayers()
	total := 0
	for _, l := range layers {
		total += l.Tokens
	}
	all, _ := widget.StackBar(layers, widget.NewStackOpts(60, total*2), p)
	if strings.ContainsAny(all.Plain(), "░▓") {
		t.Errorf("everything cached: %q", all.Plain())
	}
	none, _ := widget.StackBar(layers, widget.NewStackOpts(60, -5), p)
	if strings.ContainsAny(none.Plain(), "█▓") {
		t.Errorf("nothing cached: %q", none.Plain())
	}
}

// The bar is the same in every palette but for its styles, and never wider than asked.
func TestStackBarPalettesAndWidths(t *testing.T) {
	r := showRand(13)
	for i := 0; i < 60; i++ {
		layers := showRandLayers(r)
		for w := -2; w <= 40; w++ {
			o := widget.NewStackOpts(w, r.Intn(50000))
			o.Sweep = float64(r.Intn(12)-2) / 8
			o.BreakAt = r.Intn(len(layers)+2) - 1
			o.Warm = float64(r.Intn(6)) / 5
			var plains [2]string
			for k, name := range []string{"default", "mono"} {
				bar, labels := widget.StackBar(layers, o, showPalettes()[name])
				if bar.Width() > max(w, 0) || labels.Width() > max(w, 0) {
					t.Fatalf("width %d: bar %d labels %d", w, bar.Width(), labels.Width())
				}
				showNoControl(t, "stack bar", []cell.Line{bar, labels})
				plains[k] = bar.Plain() + "\n" + labels.Plain()
			}
			if plains[0] != plains[1] {
				t.Fatalf("the colours change the text:\n%s\n--\n%s", plains[0], plains[1])
			}
		}
	}
}

func TestStackBarBadInput(t *testing.T) {
	p := widget.DefaultPalette()
	bar, labels := widget.StackBar(nil, widget.NewStackOpts(10, 5), p)
	if bar.Plain() != "··········" || labels != nil {
		t.Errorf("no layers: %q %q", bar.Plain(), labels.Plain())
	}
	bar, labels = widget.StackBar([]widget.Layer{{Name: "G0", Tokens: 0}, {Name: "G1", Tokens: -5}}, widget.NewStackOpts(6, 0), p)
	if bar.Plain() != "······" || labels != nil {
		t.Errorf("no tokens: %q", bar.Plain())
	}
	for _, w := range []int{-10, -1, 0} {
		bar, labels = widget.StackBar(showChatLayers(), widget.NewStackOpts(w, 100), p)
		if bar != nil || labels != nil {
			t.Errorf("width %d draws nothing", w)
		}
	}
	bar, _ = widget.StackBar(showChatLayers(), widget.NewStackOpts(1, 100), p)
	if bar.Width() != 1 {
		t.Errorf("width 1: %q", bar.Plain())
	}
	// fewer cells than layers: the biggest layers get the cells
	bar, _ = widget.StackBar(showChatLayers(), widget.NewStackOpts(3, 0), p)
	if bar.Width() != 3 {
		t.Errorf("three cells for seven layers: %q", bar.Plain())
	}
	o := widget.NewStackOpts(30, 1000)
	o.Sweep, o.Warm = math.NaN(), math.NaN()
	o.BreakAt = 1 << 30
	bar, _ = widget.StackBar(showChatLayers(), o, p)
	if bar.Width() != 30 || strings.ContainsAny(bar.Plain(), "▶▷▹") {
		t.Errorf("NaN options are neutral: %q", bar.Plain())
	}
	// a huge sweep is the end of the bar
	o = widget.NewStackOpts(30, 1000)
	o.Sweep = 1e9
	bar, _ = widget.StackBar(showChatLayers(), o, p)
	if !strings.Contains(bar.Plain(), "▶") {
		t.Errorf("the light is at the end of the bar: %q", bar.Plain())
	}
	o = widget.NewStackOpts(30, 1<<40)
	o.CachedTokens = math.MaxInt
	if bar, _ = widget.StackBar(showChatLayers(), o, p); bar.Width() != 30 {
		t.Errorf("a huge cached count: %q", bar.Plain())
	}
}

// The break is marked in the label row with the warning sign and its layer is in the alarm style (and only that layer).
func TestStackBarBreak(t *testing.T) {
	p := showPal()
	layers := showChatLayers()
	o := widget.NewStackOpts(60, 20000)
	o.BreakAt = 3
	bar, labels := widget.StackBar(layers, o, p)
	if !strings.Contains(labels.Plain(), "⚠G3") {
		t.Errorf("the label row: %q", labels.Plain())
	}
	alarm, bold := 0, 0
	for _, sp := range bar {
		if sp.Style.FG == p.Bad {
			alarm += len([]rune(sp.Text))
			if sp.Style.Has(cell.Bold) {
				bold += len([]rune(sp.Text))
			}
		}
	}
	per, _ := showCellsPerLayer(bar, p)
	if alarm == 0 || alarm != bold || per[3] != 0 {
		t.Errorf("alarm cells %d (bold %d), G3 cells left in its colour %d: the break layer is all in the alarm style", alarm, bold, per[3])
	}
	// no other layer is touched
	o.BreakAt = -1
	calm, _ := widget.StackBar(layers, o, p)
	calmPer, _ := showCellsPerLayer(calm, p)
	for k := range per {
		if k != 3 && per[k] != calmPer[k] {
			t.Errorf("layer %d changed with the break: %d cells, %d without", k, per[k], calmPer[k])
		}
	}
	// the warning is kept even where the name does not fit
	o = widget.NewStackOpts(9, 0)
	o.BreakAt = 6
	_, labels = widget.StackBar(layers, o, p)
	if !strings.Contains(labels.Plain(), "⚠") {
		t.Errorf("a narrow bar still warns: %q", labels.Plain())
	}
}

// Labels sit under their own layers, or the row is a legend in layer order.
func TestStackBarLabelsSitUnderTheirLayers(t *testing.T) {
	p := showPal()
	layers := showChatLayers()
	for w := 7; w <= 120; w++ {
		bar, labels := widget.StackBar(layers, widget.NewStackOpts(w, 0), p)
		per, _ := showCellsPerLayer(bar, p)
		text := []rune(labels.Plain())
		found := make([]int, len(layers)) // where each label is, -1 when it is not there
		for k := range layers {
			label := []rune(fmt.Sprintf("G%d", k))
			found[k] = -1
			for x := 0; x+len(label) <= len(text); x++ {
				if string(text[x:x+len(label)]) == string(label) {
					found[k] = x
					break
				}
			}
		}
		// no label is cut in two: every G is followed by its digit
		for x, r := range text {
			if r == 'G' && (x+1 >= len(text) || text[x+1] < '0' || text[x+1] > '6') {
				t.Fatalf("width %d: a label is cut: %q", w, labels.Plain())
			}
		}
		// where there is room for all of them they are all there, in order, each over its own layer; in a narrower bar they are a
		// legend, a whole-label prefix in order
		var last = -1
		missing := false
		for k := range layers {
			if found[k] < 0 {
				missing = true
				continue
			}
			if missing {
				t.Fatalf("width %d: G%d is shown but an earlier label is not: %q", w, k, labels.Plain())
			}
			if found[k] <= last {
				t.Fatalf("width %d: labels out of order: %q", w, labels.Plain())
			}
			last = found[k]
		}
		if w >= 20 && missing {
			t.Fatalf("width %d: a label is missing: %q", w, labels.Plain())
		}
		if !missing && !strings.HasPrefix(labels.Plain(), "G0 G1 G2") {
			start := 0
			for k := range layers {
				if found[k] >= start+per[k] || found[k]+2 <= start {
					t.Fatalf("width %d: the label G%d is at %d, its layer is %d..%d: %q", w, k, found[k], start, start+per[k]-1, labels.Plain())
				}
				start += per[k]
			}
		}
	}
}

func TestStackTableGolden(t *testing.T) {
	p := widget.DefaultPalette()
	layers := showChatLayers()
	var d showtest.Doc
	d.Note("StackTable: the ctrl+t panel; columns give way in a fixed order as the width shrinks (epoch, hash, share, word, bar)")
	for _, w := range []int{76, 64, 56, 48, 40, 32, 24, 16} {
		o := widget.NewStackOpts(w, 44900)
		d.Add(fmt.Sprintf("93%% cached, width %d", w), widget.StackTable(layers, o, p), w)
	}
	o := widget.NewStackOpts(64, 0)
	d.Add("nothing cached (the first request)", widget.StackTable(layers, o, p), 64)
	o = widget.NewStackOpts(64, 21500)
	o.BreakAt = 3
	d.Add("a cache break at G3", widget.StackTable(layers, o, p), 64)
	showGolden(t, "stack_table", &d)
}
