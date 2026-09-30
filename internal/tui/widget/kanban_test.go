package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

func TestKanbanGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Kanban: todo / running / verifying / merged; cards ▢ waiting ▣ running ◌ verifying ✓ merged ✗ failed")
	cols := showSketchKanban()
	for _, w := range []int{64, 52, 44, 36, 30, 24} {
		d.Add(fmt.Sprintf("the sketch's board, width %d", w), widget.Kanban(cols, w, p), w)
	}
	d.Note("a failed task stays in its column with ✗")
	failed := showSketchKanban()
	failed[2].Cards = append(failed[2].Cards, widget.KanbanCard{ID: "t8", Label: "cli", Failed: true})
	d.Add("t8 failed its verification", widget.Kanban(failed, 52, p), 52)
	d.Note("a long merged column packs its cards; a custom title replaces the name")
	many := showSketchKanban()
	for i := 12; i < 40; i++ {
		many[3].Cards = append(many[3].Cards, widget.KanbanCard{ID: fmt.Sprintf("t%d", i)})
	}
	many[0].Title = "backlog"
	d.Add("thirty-three merged", widget.Kanban(many, 52, p), 52)
	d.Add("two columns", widget.Kanban(cols[:2], 30, p), 30)
	d.Add("empty board", widget.Kanban([]widget.KanbanCol{{Kind: widget.ColTodo}, {Kind: widget.ColRunning}, {Kind: widget.ColMerged}}, 40, p), 40)
	showGolden(t, "kanban", &d)
}

func TestKanbanStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("headers: todo warn, running good, verifying info, merged G1 (all Bold); cards: todo dim, verifying info, merged good, failed bad")
	cols := showSketchKanban()
	cols[2].Cards = append(cols[2].Cards, widget.KanbanCard{ID: "t8", Failed: true})
	d.AddText("width 52", showtest.FlattenStyled(widget.Kanban(cols, 52, p), showNames(p)))
	showGolden(t, "kanban_styled", &d)
}

func TestKanbanGlyphsAndCounts(t *testing.T) {
	p := widget.MonoPalette()
	lines := widget.Kanban(showSketchKanban(), 64, p)
	text := showtest.Flatten(lines)
	for _, want := range []string{"todo 3", "running 4", "verifying 1", "merged 5", "▢ t9", "▢ t10", "▣ t3", "◌ t7", "✓ t1"} {
		if !strings.Contains(text, want) {
			t.Errorf("the board lacks %q:\n%s", want, text)
		}
	}
	// the labels of a column line up
	if !strings.Contains(lines[1].Plain(), "t9  docs") || !strings.Contains(lines[2].Plain(), "t10 e2e") {
		t.Errorf("ids are padded so that the labels line up:\n%s", text)
	}
	// the height is the longest column (a header and four running cards)
	if len(lines) != 5 {
		t.Errorf("%d lines, want 5:\n%s", len(lines), text)
	}
	failed := showSketchKanban()
	failed[1].Cards[0].Failed = true
	got := widget.Kanban(failed, 64, widget.DefaultPalette())
	if !strings.Contains(showtest.Flatten(got), "✗ t3") {
		t.Error("a failed card is ✗")
	}
	for _, l := range got {
		for _, sp := range l {
			if strings.Contains(sp.Text, "✗") && !sp.Style.Has(cell.Bold) {
				t.Error("a failed card is in the alarm style")
			}
		}
	}
}

func TestKanbanNarrowIsACountsLine(t *testing.T) {
	p := widget.DefaultPalette()
	got := widget.Kanban(showSketchKanban(), 20, p)
	if len(got) != 1 {
		t.Fatalf("%d lines", len(got))
	}
	if !strings.HasPrefix(got[0].Plain(), "todo 3 · running 4") || got[0].Width() > 20 {
		t.Errorf("%q", got[0].Plain())
	}
}

func TestKanbanEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.Kanban(nil, 40, p) != nil || widget.Kanban(showSketchKanban(), 0, p) != nil || widget.Kanban(showSketchKanban(), -1, p) != nil {
		t.Error("no columns or no width draws nothing")
	}
	odd := []widget.KanbanCol{{Kind: 99, Cards: []widget.KanbanCard{{ID: "a\x1b[31m\nb", Label: "x\ty"}}}}
	got := widget.Kanban(odd, 30, p)
	if len(got) == 0 {
		t.Fatal("an unknown kind")
	}
	showNoControl(t, "kanban", got)
}
