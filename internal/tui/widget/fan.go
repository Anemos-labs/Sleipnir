// The shared-prefix fan: one cached prefix and a line down to every rider that reads it (docs/UX.md, "one prefix, eight riders";
// the top right of the swarm sketch swarm.png).

package widget

import (
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// fanDefaultLayers is the shared prefix when the caller has not said what it is: three layers in the proportions of the sketch.
func fanDefaultLayers() []Layer {
	return []Layer{{Name: "G0", Tokens: 20}, {Name: "G1", Tokens: 16}, {Name: "G2", Tokens: 10}}
}

const fanDrop = 3 // the rows of the lines between the bar and the labels

// Fan draws the shared prefix, G0 to G2, as one bar over the whole width, with a line down from it to each rider's label:
//
//	████████████████████████████████████████████████████
//	         G0                G1              G2
//	   ╎      ┃      │      ╎      │      ╎      ┃
//	   ╎      ┃      │      ╎      │      ╎      ┃
//	   ╎      ┃      │      ╎      │      ╎      ┃
//	  m0     w1     w2     w3     w4     w5     w7
//
// The weight of a line says how many tokens that rider read from the cache: ┃ for most of what the heaviest read, │ for a fair
// share, ╎ for less, and ╎ dim (with a dim label) for a rider that read nothing, a cold one. weights[i] belongs to
// ridersLabels[i]; a missing weight is 0 and extra weights are ignored, and a negative one is 0.
//
// Riders are spread evenly. When there are more than fit (a label and a space each) the first ones are drawn and the last
// column says how many more there are (+42). With no riders it is the bar and its labels only, and a width too short for one
// rider is the bar alone; a width <= 0 draws nothing.
func Fan(ridersLabels []string, weights []int, width int, p Palette) []cell.Line {
	return FanShared(fanDefaultLayers(), ridersLabels, weights, width, p)
}

// FanShared is Fan for the real shared layers: the bar shows them by size and in their colours, all of it read from the cache.
func FanShared(shared []Layer, ridersLabels []string, weights []int, width int, p Palette) []cell.Line {
	if width <= 0 {
		return nil
	}
	layers := make([]Layer, len(shared))
	copy(layers, shared)
	for i := range layers {
		layers[i].Breakpoint = false
	}
	var total int
	for _, l := range layers {
		total += showTok(l.Tokens)
	}
	bar, labels := StackBar(layers, NewStackOpts(width, total), p)
	out := []cell.Line{bar, labels}

	n := len(ridersLabels)
	if n == 0 {
		return out
	}
	names := make([]string, n)
	cw := 2
	for i, s := range ridersLabels {
		names[i] = showTrunc(showClean(s), 8)
		if w := cell.StringWidth(names[i]) + 1; w > cw {
			cw = w
		}
	}
	cols := width / cw
	if cols < 1 {
		return out[:1]
	}
	more := 0
	if n > cols {
		more = n - (cols - 1)
		n = cols - 1
		if n < 1 { // not even room for one rider and the count: the count alone
			n, more = 0, len(ridersLabels)
		}
	}
	slots := n
	if more > 0 {
		slots++
	}

	var top int
	for i := 0; i < n && i < len(weights); i++ {
		if weights[i] > top {
			top = weights[i]
		}
	}
	weight := func(i int) int {
		if i < len(weights) && weights[i] > 0 {
			return weights[i]
		}
		return 0
	}
	line := showFG(p.layerCol(1))
	lines := make([]*showCanvas, fanDrop)
	for k := range lines {
		lines[k] = newShowCanvas(width)
	}
	labelRow := newShowCanvas(width)
	for i := 0; i < slots; i++ {
		cx := (2*i + 1) * width / (2 * slots) // the centre of the column
		if i >= n {                           // the count of those left out
			txt := "+" + showTokens(more)
			labelRow.put(showClamp(cx-cell.StringWidth(txt)/2, 0, width-cell.StringWidth(txt)), txt, p.dimSt())
			continue
		}
		w := weight(i)
		glyph, st, nameSt := "╎", line.With(cell.Dim), p.dimSt()
		switch {
		case w > 0 && int64(w)*3 >= int64(top)*2:
			glyph, st, nameSt = "┃", line.With(cell.Bold), cell.Style{Attr: cell.Bold}
		case w > 0 && int64(w)*5 >= int64(top):
			glyph, st, nameSt = "│", line, cell.Style{Attr: cell.Bold}
		case w > 0:
			glyph, st, nameSt = "╎", line, cell.Style{Attr: cell.Bold}
		}
		for _, c := range lines {
			c.put(cx, glyph, st)
		}
		lw := cell.StringWidth(names[i])
		labelRow.put(showClamp(cx-(lw-1)/2, 0, width-lw), names[i], nameSt)
	}
	for _, c := range lines {
		out = append(out, c.trimmed())
	}
	return append(out, labelRow.trimmed())
}
