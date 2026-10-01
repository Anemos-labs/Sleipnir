package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/widgettest"
)

func viewText(lines []cell.Line) string { return widgettest.Flatten(lines) }

func numbered(prefix string, n int) []cell.Line {
	out := make([]cell.Line, n)
	for i := range out {
		out[i] = cell.Text(fmt.Sprintf("%s %d", prefix, i+1))
	}
	return out
}

func sampleBands() []widget.ViewBand {
	return []widget.ViewBand{
		{Draw: func(w []int) [][]cell.Line { return [][]cell.Line{numbered("head", 2)} }},
		{Titles: []string{"one", "two"}, Weights: []int{3, 2}, Prio: 1, Draw: func(w []int) [][]cell.Line { return [][]cell.Line{numbered("left", 4), numbered("right", 3)} }},
		{Titles: []string{"list"}, Prio: 2, MinRows: 3,
			Draw: func(w []int) [][]cell.Line { return [][]cell.Line{numbered("row", 12)} },
			Fit:  func(w []int, rows int) [][]cell.Line { return [][]cell.Line{numbered("fit", rows)} }},
		{Titles: []string{"last"}, Prio: 3, Draw: func(w []int) [][]cell.Line { return [][]cell.Line{numbered("tail", 2)} }},
	}
}

// A view is exactly the screen's height and at most its width, for any size, with no control character, however small the screen.
func TestViewFrameFillsTheScreenExactly(t *testing.T) {
	p := widget.DefaultPalette()
	hints := []widget.Hint{{Text: "q quit", Rank: 1}, {Text: "? help", Rank: 2}, {Text: "c cache", Rank: 3}}
	for w := 1; w <= 130; w += 7 {
		for h := 1; h <= 60; h += 5 {
			lines := widget.ViewFrame("task", []string{"● live", "◷ 00:12", "3 agents"}, sampleBands(), hints, w, h, p)
			boxed := w >= 24 && h >= 6
			if len(lines) > h || boxed && len(lines) != h {
				t.Fatalf("%dx%d: %d lines (a box is exactly the height, a list on a tiny screen at most)", w, h, len(lines))
			}
			if got := widgettest.MaxWidth(lines); got > w {
				t.Errorf("%dx%d: a line is %d cells wide", w, h, got)
			}
			if where, bad := widgettest.Control(lines); bad {
				t.Errorf("%dx%d: control character at %s", w, h, where)
			}
		}
	}
	if got := widget.ViewFrame("x", nil, sampleBands(), nil, 0, 10, p); got != nil {
		t.Errorf("no width, no lines: %v", got)
	}
	if got := widget.ViewFrame("x", nil, sampleBands(), nil, 10, 0, p); got != nil {
		t.Errorf("no height, no lines: %v", got)
	}
}

// When the height does not hold every band the ones with the highest Prio go first, the first band never does, and a band that can be
// made shorter is shortened instead of being left out.
func TestViewFrameGivesUpBandsByPrioAndShrinksBeforeDropping(t *testing.T) {
	p := widget.DefaultPalette()
	full := viewText(widget.ViewFrame("task", nil, sampleBands(), nil, 80, 60, p))
	for _, want := range []string{"head 1", "left 4", "right 3", "row 12", "tail 2", "one", "two", "list", "last"} {
		if !strings.Contains(full, want) {
			t.Errorf("a tall screen must hold everything; lacks %q:\n%s", want, full)
		}
	}
	short := viewText(widget.ViewFrame("task", nil, sampleBands(), nil, 80, 14, p))
	if strings.Contains(short, "tail 1") || strings.Contains(short, "last") {
		t.Errorf("the band with the highest Prio goes first:\n%s", short)
	}
	if !strings.Contains(short, "head 1") || !strings.Contains(short, "left 1") {
		t.Errorf("the first band and the low-Prio ones stay:\n%s", short)
	}
	if strings.Contains(short, "row 12") {
		t.Errorf("a list taller than the room must not be shown whole:\n%s", short)
	}
	if !strings.Contains(short, "fit 1") {
		t.Errorf("the list is shortened by its Fit, not left out:\n%s", short)
	}
	for _, rows := range []int{3, 4, 5} {
		got := viewText(widget.ViewFrame("task", nil, sampleBands()[:1], nil, 80, 6+rows, p))
		if !strings.Contains(got, "head 1") {
			t.Errorf("the first band is never given up (%d rows):\n%s", rows, got)
		}
	}
}

// Spare rows are blank and sit above the row of keys, which is always the last line inside the box.
func TestViewFrameKeepsTheKeysAtTheBottom(t *testing.T) {
	p := widget.DefaultPalette()
	lines := viewText(widget.ViewFrame("task", nil, sampleBands()[:1], []widget.Hint{{Text: "q quit", Rank: 1}}, 60, 20, p))
	rows := strings.Split(strings.TrimSuffix(lines, "\n"), "\n")
	if len(rows) != 20 {
		t.Fatalf("%d rows", len(rows))
	}
	if !strings.Contains(rows[18], "q quit") || !strings.HasPrefix(rows[19], "╰") {
		t.Errorf("the keys are the last row before the bottom edge:\n%s", lines)
	}
	if !strings.HasPrefix(rows[0], "╭") || !strings.Contains(rows[0], "SLEIPNIR") {
		t.Errorf("the title bar is the top edge:\n%s", rows[0])
	}
}

// A tiny screen gets a plain list of what the bands hold, cut to the size, never a broken box.
func TestViewFrameOnATinyScreenIsAList(t *testing.T) {
	p := widget.MonoPalette()
	lines := widget.ViewFrame("a task that is long", nil, sampleBands(), nil, 20, 5, p)
	if len(lines) > 5 {
		t.Fatalf("%d lines on a screen of 5", len(lines))
	}
	text := viewText(lines)
	if strings.ContainsAny(text, "╭╰│") {
		t.Errorf("a screen this small is not a box:\n%s", text)
	}
	if !strings.Contains(text, "SLEIPNIR") {
		t.Errorf("the list starts with the name:\n%s", text)
	}
}

// Text in a view that came from outside is cleaned like every widget's.
func TestViewFrameCleansTheTitle(t *testing.T) {
	p := widget.DefaultPalette()
	for _, evil := range widgettest.Hostile() {
		lines := widget.ViewFrame(evil, []string{evil}, sampleBands(), []widget.Hint{{Text: evil, Rank: 1}}, 70, 20, p)
		if where, bad := widgettest.Control(lines); bad {
			t.Fatalf("hostile title %q: control character at %s", evil, where)
		}
	}
}
