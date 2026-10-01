package widget_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/widget"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/showtest"
)

func TestFoldGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Fold: the compaction of the storyboard, 31.2k tokens of thread folded into a 2.4k resume; read the pictures top to bottom as the animation")
	for _, pr := range []float64{0, 0.25, 0.5, 0.75, 1} {
		d.Add("progress "+strconv.FormatFloat(pr, 'f', 2, 64)+", width 60", widget.Fold(31200, 2400, pr, 60, p), 60)
	}
	d.Note("the same at other widths: fewer steps when it is narrow, only the last line when there is no room for a staircase")
	for _, w := range []int{40, 30, 24, 16, 12} {
		d.Add("progress 0.6, width "+strconv.Itoa(w), widget.Fold(31200, 2400, 0.6, w, p), w)
	}
	for _, w := range []int{30, 16} {
		d.Add("finished, width "+strconv.Itoa(w), widget.Fold(31200, 2400, 1, w, p), w)
	}
	d.Note("a finer sequence, 10 frames at width 44 (the row that is moving retracts from the row above it)")
	for i := 0; i <= 10; i++ {
		pr := float64(i) / 10
		d.Add("progress "+strconv.FormatFloat(pr, 'f', 1, 64), widget.Fold(31200, 2400, pr, 44, p), 44)
	}
	d.Note("odd sizes: nothing gained, everything dropped")
	d.Add("after = before", widget.Fold(5000, 5000, 1, 40, p), 40)
	d.Add("after = 0", widget.Fold(5000, 0, 1, 40, p), 40)
	d.Add("after > before is clamped", widget.Fold(5000, 9000, 1, 40, p), 40)
	showGolden(t, "fold", &d)
}

func TestFoldStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("the thread is G5 (▓), the resume G4 (▒), the summary accent")
	d.AddText("progress 0.6", showtest.FlattenStyled(widget.Fold(31200, 2400, 0.6, 44, p), showNames(p)))
	d.AddText("progress 1", showtest.FlattenStyled(widget.Fold(31200, 2400, 1, 44, p), showNames(p)))
	showGolden(t, "fold_styled", &d)
}

// The height must not depend on the progress, or the animation would push what is under it around.
func TestFoldHeightIsConstant(t *testing.T) {
	p := widget.DefaultPalette()
	for _, w := range []int{5, 14, 24, 40, 60, 100} {
		want := -1
		for i := 0; i <= 100; i++ {
			got := len(widget.Fold(31200, 2400, float64(i)/100, w, p))
			if want < 0 {
				want = got
			}
			if got != want {
				t.Fatalf("width %d: %d lines at progress %.2f, %d at 0", w, got, float64(i)/100, want)
			}
		}
	}
}

// The staircase only ever gets narrower as it goes down and as the progress advances the rows appear in order.
func TestFoldStaircaseShrinks(t *testing.T) {
	p := widget.MonoPalette()
	for _, pr := range []float64{0.3, 0.5, 0.7, 0.8, 0.9, 1} {
		lines := widget.Fold(31200, 2400, pr, 60, p)
		prev := 1 << 30
		for _, l := range lines {
			if n := strings.Count(l.Plain(), "▓") + strings.Count(l.Plain(), "▒"); n > 0 {
				if n > prev {
					t.Fatalf("progress %.2f: a step is wider than the one above it:\n%s", pr, showtest.Flatten(lines))
				}
				prev = n
			}
		}
	}
	first := func(pr float64) int {
		return strings.Count(widget.Fold(31200, 2400, pr, 60, p)[0].Plain(), "▓")
	}
	if first(0) != first(1) || first(0) < 20 {
		t.Errorf("the thread block is the same size throughout: %d and %d", first(0), first(1))
	}
}

func TestFoldEndStates(t *testing.T) {
	p := widget.MonoPalette()
	start := showtest.Flatten(widget.Fold(31200, 2400, 0, 60, p))
	if strings.Count(start, "▓") == 0 || strings.Contains(start, "▒") || strings.Contains(start, "resume") || !strings.Contains(start, "compacting 31.2k") {
		t.Errorf("at progress 0 only the thread shows:\n%s", start)
	}
	end := widget.Fold(31200, 2400, 1, 60, p)
	text := showtest.Flatten(end)
	for _, want := range []string{"31.2k", "▏resume", "◆ compacted 31.2k → 2.4k", "(-92%)", "▒▒"} {
		if !strings.Contains(text, want) {
			t.Errorf("the final state lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(end[len(end)-1].Plain(), "31.2k → 2.4k") {
		t.Errorf("the last line is the summary: %q", end[len(end)-1].Plain())
	}
	// the tokens count down between the two
	mid := showtest.Flatten(widget.Fold(31200, 2400, 0.5, 60, p))
	if !strings.Contains(mid, "compacting 31.2k → ") || strings.Contains(mid, "compacted") {
		t.Errorf("midway it is still compacting:\n%s", mid)
	}
}

func TestFoldEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if got := widget.Fold(0, 0, 0.5, 40, p); got != nil {
		t.Errorf("nothing to fold: %v", got)
	}
	if got := widget.Fold(-5, 3, 0.5, 40, p); got != nil {
		t.Errorf("negative before: %v", got)
	}
	if got := widget.Fold(100, 10, 0.5, 0, p); got != nil {
		t.Errorf("width 0: %v", got)
	}
	if got := widget.Fold(100, 10, 0.5, -3, p); got != nil {
		t.Errorf("width -3: %v", got)
	}
	a := showtest.Flatten(widget.Fold(31200, 2400, math.NaN(), 50, p))
	b := showtest.Flatten(widget.Fold(31200, 2400, 0, 50, p))
	if a != b {
		t.Errorf("NaN progress is 0:\n%s\n--\n%s", a, b)
	}
	if a, b := showtest.Flatten(widget.Fold(31200, 2400, 7, 50, p)), showtest.Flatten(widget.Fold(31200, 2400, 1, 50, p)); a != b {
		t.Errorf("progress above 1 is 1")
	}
	if a, b := showtest.Flatten(widget.Fold(31200, 2400, -1, 50, p)), showtest.Flatten(widget.Fold(31200, 2400, 0, 50, p)); a != b {
		t.Errorf("progress below 0 is 0")
	}
	if got := widget.Fold(31200, -7, 1, 50, p); len(got) == 0 || !strings.Contains(got[len(got)-1].Plain(), "→ 0") {
		t.Errorf("a negative after is 0: %v", got)
	}
	// a huge count must not overflow
	if got := widget.Fold(math.MaxInt, 1, 0.5, 50, p); len(got) == 0 {
		t.Error("huge before")
	}
	// one cell is always left for the resume, however small it is
	got := widget.Fold(1_000_000, 1, 1, 40, p)
	if !strings.Contains(showtest.Flatten(got), "▒") {
		t.Errorf("the resume is at least one cell:\n%s", showtest.Flatten(got))
	}
}
