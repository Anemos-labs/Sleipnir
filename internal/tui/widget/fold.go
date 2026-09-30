// The compaction fold: the thread block shrinks step by step into a small resume, and the spine grows a line for it
// (docs/UX.md, "Fold"; the storyboard story.png, panel 2).

package widget

import (
	"strconv"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

const foldLabelW = 7 // "thread " and "fold ↓ "

// Fold draws a compaction from before tokens to after tokens as progress runs from 0 to 1. The thread is a block of ▓ as wide as
// its tokens, and the fold is a staircase under it: each row is the block a step smaller, the last one the resume (▒, a cell at
// least), so the collapse can be read as a picture at any moment:
//
//	thread ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 31.2k
//	fold ↓ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓
//	       ▓▓▓▓▓▓▓▓▓▓
//	       ▓▓▓▓▓
//	       ▒▒ 2.4k
//
//	spine  ▏resume
//	◆ compacted 31.2k → 2.4k (-92%)
//
// The rows appear one after the other, each starting as wide as the row above and retracting to its own size (an easing that
// is quick at first); the last step is followed by the spine, which grows ▏ and then the word resume letter by letter, and the
// last line counts the tokens down (compacting 31.2k → 12.4k) until it reads compacted with the saving. The number of lines
// depends on the width only, never on progress, so the animation does not move what is under it.
//
// progress is clamped to 0..1 and NaN is 0. after is clamped to 0..before, so a "compaction" that grew the thread shows no
// shrinking. With before <= 0 there is nothing to fold and nothing is drawn; a width too short for the staircase draws the
// last line alone, cut to the width, and a width <= 0 draws nothing.
func Fold(before, after int, progress float64, width int, p Palette) []cell.Line {
	if width <= 0 || before <= 0 {
		return nil
	}
	before = showTok(before)
	after = showClamp(after, 0, before)
	pm := showPermille(progress)

	summary := foldSummary(before, after, pm, width, p)
	first := showTokens(before)
	barMax := width - foldLabelW - 1 - cell.StringWidth(first)
	if barMax < 6 {
		return []cell.Line{summary}
	}
	n := 3
	switch {
	case barMax >= 20:
		n = 5
	case barMax >= 12:
		n = 4
	}

	// the width of every step of the staircase: from the thread to the resume by a curve that falls fast at first
	last := showClamp(showDiv(int64(barMax)*int64(after), int64(before)), 1, barMax)
	steps := make([]int, n)
	for i := range steps {
		t := int64(n - 1 - i)
		steps[i] = last + showDiv(int64(barMax-last)*t*t, int64(n-1)*int64(n-1))
		if i > 0 && steps[i] > steps[i-1] {
			steps[i] = steps[i-1]
		}
	}

	thread, resume := p.layerSt(5), p.layerSt(4)
	const rowsEnd = 800 // the staircase is done at 80%, the spine takes the rest
	slot := rowsEnd / (n - 1)
	var out []cell.Line
	for i := 0; i < n; i++ {
		var b showRowBuf
		w := steps[i]
		visible := i == 0
		if i > 0 {
			if start := (i - 1) * slot; pm > start {
				visible = true
				l := showClamp((pm-start)*1000/slot, 0, 1000) // how far this step has retracted, in thousandths
				w = steps[i-1] - showDiv(int64(steps[i-1]-steps[i])*int64(l), 1000)
			}
		}
		label := ""
		switch {
		case i == 0:
			label = "thread"
		case (i == 2 && i < n-1) || (n < 4 && i == 1):
			if visible {
				label = "fold ↓"
			}
		}
		b.add(p.dimSt(), showPadR(label, foldLabelW))
		if visible {
			if i == n-1 { // the last step is the resume
				b.add(resume, showRepeat("▒", w))
				if pm >= rowsEnd {
					b.add(cell.Style{}, " ").add(cell.Style{Attr: cell.Bold}, showTokens(after))
				}
			} else {
				b.add(thread, showRepeat("▓", w))
				if i == 0 {
					b.add(cell.Style{}, " ").add(cell.Style{Attr: cell.Bold}, first)
				}
			}
		}
		out = append(out, showFit(b.line(), width))
	}
	out = append(out, nil)

	// the spine: its tick appears, then the word grows out of it
	var sp showRowBuf
	sp.add(p.dimSt(), showPadR("spine", foldLabelW))
	if pm >= rowsEnd-100 {
		sp.add(resume.With(cell.Bold), "▏")
	}
	if pm > rowsEnd {
		word := "resume"
		k := showClamp(showDiv(int64(pm-rowsEnd)*int64(len(word)), int64(1000-rowsEnd)), 0, len(word))
		sp.add(cell.Style{Attr: cell.Bold}, word[:k])
	}
	out = append(out, showFit(sp.line(), width), summary)
	return out
}

// foldSummary is the last line: what is being compacted, counting down, and at the end what it came to. When the width is short
// it gives up the saving, then the verb, before it is cut.
func foldSummary(before, after, pm, width int, p Palette) cell.Line {
	head := p.accentSt().With(cell.Bold)
	var verb, to, saving string
	switch {
	case pm >= 1000:
		verb, to = "compacted", showTokens(after)
		if before > after {
			saving = " (-" + strconv.Itoa(showDiv(int64(before-after)*100, int64(before))) + "%)"
		}
	case pm <= 0:
		verb = "compacting"
	default:
		l := int64(1000 - pm)
		verb, to = "compacting", showTokens(after+showDiv(int64(before-after)*l*l, 1000*1000))
	}
	build := func(verb, saving string) cell.Line {
		var b showRowBuf
		text := "◆ "
		if verb != "" {
			text += verb + " "
		}
		text += showTokens(before)
		if to != "" {
			text += " → " + to
		}
		b.add(head, text).add(p.dimSt(), saving)
		return b.line()
	}
	for _, l := range []cell.Line{build(verb, saving), build(verb, ""), build("", "")} {
		if l.Width() <= width {
			return l
		}
	}
	return showFit(build("", ""), width)
}
