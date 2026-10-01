package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/showtest"
)

func TestMergeQueueGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("MergeQueue: a chip moves through rebase, verify, merge; the spinner turns with the frame; a failed one is bounced back with its reason")
	items := showSketchMerge()
	for f := 0; f < 4; f++ {
		d.Add(fmt.Sprintf("the sketch, frame %d", f), widget.MergeQueue(items, f, 40, p), 40)
	}
	d.Add("the sketch at width 26", widget.MergeQueue(items, 0, 26, p), 26)

	d.Note("a task moving on: t8 rebases, verifies, merges; then it joins the merged list")
	steps := []struct {
		name  string
		items []widget.MergeItem
	}{
		{"t8 queued", []widget.MergeItem{{ID: "t6", Stage: widget.MergeDone}, {ID: "t8", Stage: widget.MergeQueued}}},
		{"t8 rebasing", []widget.MergeItem{{ID: "t6", Stage: widget.MergeDone}, {ID: "t8", Stage: widget.MergeRebase, Note: "git rebase main"}}},
		{"t8 verifying", []widget.MergeItem{{ID: "t6", Stage: widget.MergeDone}, {ID: "t8", Stage: widget.MergeVerify, Note: "go test ./..."}}},
		{"t8 merging", []widget.MergeItem{{ID: "t6", Stage: widget.MergeDone}, {ID: "t8", Stage: widget.MergeMerge, Note: "git merge --ff-only"}}},
		{"t8 merged", []widget.MergeItem{{ID: "t6", Stage: widget.MergeDone}, {ID: "t8", Stage: widget.MergeDone}}},
	}
	for _, s := range steps {
		d.Add(s.name, widget.MergeQueue(s.items, 2, 40, p), 40)
	}

	d.Note("a failure is bounced back to its worker with the reason; a conflict is a failure at the rebase")
	bad := []widget.MergeItem{
		{ID: "t1", Stage: widget.MergeDone},
		{ID: "t2", Stage: widget.MergeVerify, Failed: true, Worker: "w3", Note: "FAIL TestList (0.3s): want 10 rows, got 9"},
		{ID: "t3", Stage: widget.MergeRebase, Failed: true, Worker: "w4", Note: "conflict in orders/list.go"},
		{ID: "t4", Stage: widget.MergeRebase, Note: "git rebase main"},
		{ID: "t5", Stage: widget.MergeQueued},
	}
	d.Add("two failures", widget.MergeQueue(bad, 1, 48, p), 48)
	d.Add("two failures, narrow", widget.MergeQueue(bad, 1, 28, p), 28)
	d.Note("a long merged list wraps under its title")
	var many []widget.MergeItem
	for i := 1; i <= 24; i++ {
		many = append(many, widget.MergeItem{ID: fmt.Sprintf("t%d", i), Stage: widget.MergeDone})
	}
	d.Add("24 merged", widget.MergeQueue(many, 0, 36, p), 36)
	d.Add("nothing queued", widget.MergeQueue(nil, 0, 36, p), 36)
	showGolden(t, "merge", &d)
}

func TestMergeQueueStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("the marker of the head is info and Bold, ✓ good, the spinner info and Bold, ✗ and the bounce bad")
	items := showSketchMerge()
	items = append(items, widget.MergeItem{ID: "t9", Stage: widget.MergeVerify, Failed: true, Worker: "w3", Note: "FAIL TestList"})
	d.AddText("frame 1", showtest.FlattenStyled(widget.MergeQueue(items, 1, 40, p), showNames(p)))
	showGolden(t, "merge_styled", &d)
}

func TestMergeQueueSpinnerTurnsAndWraps(t *testing.T) {
	p := widget.MonoPalette()
	seen := map[string]bool{}
	for f := -12; f < 30; f++ {
		lines := widget.MergeQueue(showSketchMerge(), f, 40, p)
		first := lines[0].Plain()
		spin := string([]rune(first)[len([]rune(first))-1])
		seen[spin] = true
		if want := widget.MergeQueue(showSketchMerge(), f+10, 40, p)[0].Plain(); first != want {
			t.Fatalf("frame %d and %d are not the same frame", f, f+10)
		}
	}
	if len(seen) != 10 {
		t.Errorf("the spinner has %d different frames, want 10", len(seen))
	}
}

func TestMergeQueueStagesAndMarkers(t *testing.T) {
	p := widget.MonoPalette()
	lines := widget.MergeQueue(showSketchMerge(), 0, 50, p)
	text := showtest.Flatten(lines)
	for _, want := range []string{"▸ t7 rebase ✓ ─ verify", "    go test ./orders/...", "  t8 rebase", "merged t1 t2 t4 t5 t6", "conflicts 0 · bounced back 0"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "▸") != 1 {
		t.Errorf("only the first task in progress is marked:\n%s", text)
	}
	// counts
	bad := []widget.MergeItem{
		{ID: "a", Stage: widget.MergeVerify, Failed: true, Worker: "w1", Note: "FAIL"},
		{ID: "b", Stage: widget.MergeRebase, Failed: true, Note: "conflict"},
		{ID: "c", Stage: widget.MergeRebase, Failed: true},
	}
	got := showtest.Flatten(widget.MergeQueue(bad, 0, 60, p))
	for _, want := range []string{"✗ a rebase ✓ ─ verify ✗", "↩ back to w1: FAIL", "↩ bounced back: conflict", "conflicts 2 · bounced back 3", "merged none"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}

func TestMergeQueueFailureIsLoudWithoutColour(t *testing.T) {
	bad := []widget.MergeItem{{ID: "t2", Stage: widget.MergeVerify, Failed: true, Worker: "w3", Note: "FAIL"}}
	lines := widget.MergeQueue(bad, 0, 40, widget.MonoPalette())
	for _, l := range lines[:2] {
		for _, sp := range l {
			if strings.Contains(sp.Text, "✗") || strings.Contains(sp.Text, "↩") {
				if !sp.Style.Has(cell.Bold) {
					t.Errorf("%q is not Bold", sp.Text)
				}
			}
		}
	}
}

func TestMergeQueueEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.MergeQueue(showSketchMerge(), 0, 0, p) != nil || widget.MergeQueue(showSketchMerge(), 0, -4, p) != nil {
		t.Error("a width <= 0 draws nothing")
	}
	got := widget.MergeQueue([]widget.MergeItem{{ID: "a\x1b[31m\nb", Stage: 77, Note: "x\r\ny", Worker: "w\x00"}, {ID: "z", Stage: widget.MergeVerify, Failed: true, Note: "\u202e"}}, 5, 30, p)
	showNoControl(t, "merge", got)
	if len(widget.MergeQueue(nil, 0, 10, p)) == 0 {
		t.Error("an empty queue says so")
	}
}
