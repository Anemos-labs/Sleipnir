package widget_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

func TestTTLGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("TTL: the cache warmth clock; ●◕◑◔ by the quarter left, ▰ drains into ▱, amber (Bold) in the last minute, red ○ cold at zero")
	total := 5 * time.Minute
	for _, r := range []time.Duration{5 * time.Minute, 4*time.Minute + 12*time.Second, 3*time.Minute + 41*time.Second, 2*time.Minute + 30*time.Second,
		time.Minute + 20*time.Second, 60 * time.Second, 45 * time.Second, 12 * time.Second, 400 * time.Millisecond, 0} {
		d.Add("remaining "+r.String()+" of 5m", []cell.Line{widget.TTL(r, total, 24, p)}, 24)
	}
	d.Note("a short width gives up the bar, then the word, then the time")
	for _, w := range []int{20, 16, 11, 9, 6, 3, 2, 1} {
		d.Add("4:12 left, width "+strconv.Itoa(w), []cell.Line{widget.TTL(4*time.Minute+12*time.Second, total, w, p)}, w)
	}
	d.Add("cold, width 8", []cell.Line{widget.TTL(0, total, 8, p)}, 8)
	d.Add("an hour-long TTL", []cell.Line{widget.TTL(59*time.Minute+30*time.Second, time.Hour, 24, p), widget.TTL(time.Hour+5*time.Second, 2*time.Hour, 24, p)}, 24)
	showGolden(t, "ttl", &d)
}

func TestTTLStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("green while there is time, amber and Bold in the last minute, red and Bold when cold")
	for _, r := range []time.Duration{4*time.Minute + 12*time.Second, 45 * time.Second, 0} {
		d.AddText("remaining "+r.String(), showtest.FlattenStyled([]cell.Line{widget.TTL(r, 5*time.Minute, 24, p)}, showNames(p)))
	}
	showGolden(t, "ttl_styled", &d)
}

func TestTTLStates(t *testing.T) {
	p := showPal()
	total := 5 * time.Minute
	cases := []struct {
		rem   time.Duration
		plain string
		fg    cell.Color
		bold  bool
	}{
		{4*time.Minute + 12*time.Second, "◕ warm 4:12 ▰▰▰▰▰▰▰▱", p.Good, false},
		{2 * time.Minute, "◑ warm 2:00 ▰▰▰▱▱▱▱▱", p.Good, false},
		{time.Minute, "◔ warm 1:00 ▰▰▱▱▱▱▱▱", p.Good, false}, // amber is below 60 s
		{59 * time.Second, "◔ warm 0:59 ▰▰▱▱▱▱▱▱", p.Warn, true},
		{30 * time.Second, "◔ warm 0:30 ▰▱▱▱▱▱▱▱", p.Warn, true},
		{0, "○ cold ▱▱▱▱▱▱▱▱", p.Bad, true},
		{-time.Second, "○ cold ▱▱▱▱▱▱▱▱", p.Bad, true},
	}
	for _, c := range cases {
		l := widget.TTL(c.rem, total, 40, p)
		if l.Plain() != c.plain {
			t.Errorf("remaining %v: %q, want %q", c.rem, l.Plain(), c.plain)
		}
		if c.fg != l[0].Style.FG || c.bold != l[0].Style.Has(cell.Bold) {
			t.Errorf("remaining %v: style %+v, want colour %v bold %v", c.rem, l[0].Style, c.fg, c.bold)
		}
	}
}

func TestTTLFaceFollowsTheQuarter(t *testing.T) {
	p := widget.MonoPalette()
	total := 4 * time.Minute
	want := map[time.Duration]string{4 * time.Minute: "●", 3 * time.Minute: "◕", 2 * time.Minute: "◑", time.Minute: "◔", 5 * time.Second: "◔", 0: "○"}
	for rem, face := range want {
		if got := widget.TTL(rem, total, 30, p).Plain(); !strings.HasPrefix(got, face) {
			t.Errorf("%v of %v: %q, want the face %s", rem, total, got, face)
		}
	}
}

func TestTTLBarDrainsMonotonically(t *testing.T) {
	p := widget.MonoPalette()
	prev := 100
	for s := 300; s >= 0; s-- {
		l := widget.TTL(time.Duration(s)*time.Second, 5*time.Minute, 30, p)
		on := strings.Count(l.Plain(), "▰")
		if on > prev {
			t.Fatalf("at %ds the bar filled up again (%d > %d): %q", s, on, prev, l.Plain())
		}
		prev = on
		if s > 0 && on == 0 {
			t.Fatalf("at %ds the bar is empty but the cache is warm: %q", s, l.Plain())
		}
		if s == 0 && on != 0 {
			t.Fatalf("a cold cache has an empty bar: %q", l.Plain())
		}
	}
}

func TestTTLEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.TTL(time.Minute, time.Minute, 0, p) != nil || widget.TTL(time.Minute, time.Minute, -3, p) != nil {
		t.Error("a width <= 0 draws nothing")
	}
	// a total that is not positive: the clock cannot drain, it is what remains
	if got := widget.TTL(90*time.Second, 0, 30, p).Plain(); !strings.HasPrefix(got, "● warm 1:30") {
		t.Errorf("no total: %q", got)
	}
	if got := widget.TTL(10*time.Minute, time.Minute, 30, p).Plain(); !strings.HasPrefix(got, "● warm 10:00") {
		t.Errorf("remaining above total: %q", got)
	}
	if got := widget.TTL(0, 0, 30, p).Plain(); !strings.HasPrefix(got, "○ cold") {
		t.Errorf("nothing at all is cold: %q", got)
	}
	if got := widget.TTL(time.Hour+time.Second, 2*time.Hour, 30, p).Plain(); !strings.Contains(got, "1:00:01") {
		t.Errorf("hours: %q", got)
	}
}
