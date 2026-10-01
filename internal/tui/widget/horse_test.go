package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/showtest"
)

// showLegs is eight legs in the colours of the mark, busy as the mask says (bit k is leg k).
func showLegs(mask int) []widget.Leg {
	roles := []int{0, 1, 1, 2, 2, 3, 4, 5}
	legs := make([]widget.Leg, 8)
	for k := range legs {
		legs[k] = widget.Leg{Busy: mask&(1<<k) != 0, Color: roles[k]}
	}
	return legs
}

// showHorseBoxes are the widths that pick each of the three sprite sizes.
var showHorseBoxes = []struct {
	name  string
	width int
}{{"large", 60}, {"medium", 44}, {"small", 32}}

func TestHorseSizesGolden(t *testing.T) {
	var d showtest.Doc
	d.Note("HorseSize(width, height): the largest sprite that fits the box, in cells; (0, 0) when none does")
	for _, w := range []int{80, 60, 52, 51, 50, 44, 40, 38, 37, 32, 30, 29, 28, 27, 10, 0, -5} {
		for _, h := range []int{0, 12, 9} {
			c, r := widget.HorseSize(w, h)
			d.Note("width %3d height %2d: %2d x %2d", w, h, c, r)
		}
	}
	showGolden(t, "horse_sizes", &d)
}

func TestHorseGaitGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Gallop: the eight-legged horse, every leg busy, the four frames of the gait (a frame is taken modulo 4)")
	d.Note("the sprites are generated from the official mark by docs/design/ux/sprite.py; horsesprite_gen.go is generated code")
	all := showLegs(0xff)
	for _, box := range showHorseBoxes {
		for f := 0; f < 4; f++ {
			lines := widget.Gallop(all, f, box.width, p)
			d.Add(fmt.Sprintf("%s, frame %d", box.name, f), lines, box.width)
		}
		d.Add(box.name+", standing (Stand)", widget.Stand(box.width, p), box.width)
	}
	showGolden(t, "horse_gait", &d)
}

func TestHorseLegsGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("a leg lifts while its worker runs a tool: busy legs are in the stride of the frame, idle legs stand")
	d.Note("busy: m0 (leg 0), w3 (leg 3) and w6 (leg 6)")
	mask := 1<<0 | 1<<3 | 1<<6
	for _, box := range showHorseBoxes[1:] {
		for f := 0; f < 4; f += 2 {
			d.Add(fmt.Sprintf("%s, frame %d, legs 0, 3 and 6 busy", box.name, f), widget.Gallop(showLegs(mask), f, box.width, p), box.width)
		}
	}
	d.Add("medium, nobody busy: the same horse as Stand", widget.Gallop(showLegs(0), 3, 44, p), 44)
	showGolden(t, "horse_legs", &d)
}

func TestHorseMonoGolden(t *testing.T) {
	p := widget.MonoPalette()
	var d showtest.Doc
	d.Note("without colours the glyph says what colour would: body █▀▄, far legs ▒ and near legs ▓, mane and tail ░, the eye a hole; Bold and Dim carry busy and idle")
	d.Add("small, frame 1, every leg busy", widget.Gallop(showLegs(0xff), 1, 32, p), 32)
	d.Add("small, frame 1, legs 0, 3 and 6 busy", widget.Gallop(showLegs(1|1<<3|1<<6), 1, 32, p), 32)
	d.Add("small, standing", widget.Stand(32, p), 32)
	d.Add("medium, frame 2", widget.Gallop(showLegs(0xff), 2, 44, p), 44)
	showGolden(t, "horse_mono", &d)
}

func TestHorseStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("styles of the small horse: body, hair, eye and hoof colours; legs r1..r5 by role; a cell of two colours is fg/bg")
	d.Note("busy legs are Bold; idle legs are the role colour faded towards faint (no attribute at 24-bit colour)")
	d.AddText("frame 1, legs 1 and 4 busy", showtest.FlattenStyled(widget.Gallop(showLegs(1<<1|1<<4), 1, 32, p), showNames(p)))
	d.AddText("standing", showtest.FlattenStyled(widget.Stand(32, p), showNames(p)))
	showGolden(t, "horse_styled", &d)
}

func TestHorseFramesDiffer(t *testing.T) {
	p := widget.DefaultPalette()
	for _, box := range showHorseBoxes {
		seen := map[string]int{}
		for f := 0; f < 4; f++ {
			pic := showtest.Flatten(widget.Gallop(showLegs(0xff), f, box.width, p))
			if prev, ok := seen[pic]; ok {
				t.Errorf("%s: frames %d and %d are the same picture", box.name, prev, f)
			}
			seen[pic] = f
		}
		if _, ok := seen[showtest.Flatten(widget.Stand(box.width, p))]; ok {
			t.Errorf("%s: the standing horse is a gait frame", box.name)
		}
	}
}

func TestHorseFrameWrapsAround(t *testing.T) {
	p := widget.DefaultPalette()
	for _, box := range showHorseBoxes {
		for f := -9; f <= 9; f++ {
			got := showtest.Flatten(widget.Gallop(showLegs(0xff), f, box.width, p))
			want := showtest.Flatten(widget.Gallop(showLegs(0xff), ((f%4)+4)%4, box.width, p))
			if got != want {
				t.Fatalf("%s: frame %d is not frame %d", box.name, f, ((f%4)+4)%4)
			}
		}
	}
}

func TestHorseNobodyBusyIsTheStandingHorse(t *testing.T) {
	p := widget.DefaultPalette()
	for _, box := range showHorseBoxes {
		for f := 0; f < 4; f++ {
			idle := showtest.Flatten(widget.Gallop(showLegs(0), f, box.width, p))
			if stand := showtest.Flatten(widget.Stand(box.width, p)); idle != stand {
				t.Fatalf("%s frame %d: idle horse and Stand differ:\n%s\n--\n%s", box.name, f, idle, stand)
			}
		}
	}
}

func TestHorseSizeFollowsTheBox(t *testing.T) {
	p := widget.DefaultPalette()
	var lastCols, lastRows int
	for _, box := range showHorseBoxes {
		cols, rows := widget.HorseSize(box.width, 0)
		if cols <= 0 || rows <= 0 || cols > box.width {
			t.Fatalf("%s: size %dx%d in width %d", box.name, cols, rows, box.width)
		}
		lines := widget.Gallop(showLegs(0xff), 0, box.width, p)
		if len(lines) != rows {
			t.Errorf("%s: %d lines, HorseSize says %d rows", box.name, len(lines), rows)
		}
		if lastCols > 0 && (cols >= lastCols || rows >= lastRows) {
			t.Errorf("%s (%dx%d) is not smaller than the size before (%dx%d)", box.name, cols, rows, lastCols, lastRows)
		}
		lastCols, lastRows = cols, rows
		// the box is the limit: one cell less and the next size down
		if c2, _ := widget.HorseSize(cols-1, 0); c2 >= cols {
			t.Errorf("%s: width %d still gives the size that needs %d", box.name, cols-1, cols)
		}
		// height limits too
		_, r2 := widget.HorseSize(200, rows-1)
		if r2 >= rows {
			t.Errorf("%s: a height of %d still gives %d rows", box.name, rows-1, r2)
		}
		if got := widget.GallopIn(showLegs(0xff), 0, 200, rows, p); len(got) != rows {
			t.Errorf("%s: GallopIn with room for %d rows drew %d", box.name, rows, len(got))
		}
		if got := widget.StandIn(200, rows, p); len(got) != rows {
			t.Errorf("%s: StandIn with room for %d rows drew %d", box.name, rows, len(got))
		}
	}
	if c, r := widget.HorseSize(200, 3); c != 0 || r != 0 {
		t.Errorf("three rows hold no horse: %dx%d", c, r)
	}
}

// Gallop and Stand are centred in their width and never wider than it.
func TestHorseIsCentredAndFits(t *testing.T) {
	p := widget.DefaultPalette()
	for w := -2; w <= 90; w++ {
		cols, _ := widget.HorseSize(w, 0)
		for _, lines := range [][]cell.Line{widget.Gallop(showLegs(0xff), 2, w, p), widget.Stand(w, p)} {
			if cols == 0 {
				if lines != nil {
					t.Fatalf("width %d holds no horse, got %d lines", w, len(lines))
				}
				continue
			}
			showNoWider(t, fmt.Sprintf("horse width %d", w), lines, w)
			wide := showtest.MaxWidth(lines)
			if left := (w - cols) / 2; wide > left+cols || wide <= left {
				t.Fatalf("width %d: the widest line is %d cells, the horse is %d wide indented by %d", w, wide, cols, left)
			}
		}
	}
}

// The mono horse has the same shape as the coloured one: every cell that is blank in one is blank in the other.
func TestHorseMonoHasTheShapeOfTheColouredHorse(t *testing.T) {
	for _, box := range showHorseBoxes {
		for f := 0; f < 4; f++ {
			for _, mask := range []int{0xff, 0, 1<<1 | 1<<4} {
				c := widget.Gallop(showLegs(mask), f, box.width, widget.DefaultPalette())
				m := widget.Gallop(showLegs(mask), f, box.width, widget.MonoPalette())
				if len(c) != len(m) {
					t.Fatalf("%s: %d rows coloured, %d mono", box.name, len(c), len(m))
				}
				for y := range c {
					cr, mr := []rune(showPad(c[y].Plain(), box.width)), []rune(showPad(m[y].Plain(), box.width))
					for x := range cr {
						if (cr[x] == ' ') != (mr[x] == ' ') {
							t.Fatalf("%s frame %d mask %b: cell (%d,%d) is %q coloured and %q mono", box.name, f, mask, x, y, cr[x], mr[x])
						}
					}
				}
			}
		}
	}
}

func showPad(s string, w int) string {
	if n := w - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// In mono the glyphs carry the parts: legs are shade blocks, the body is solid, the hair is the lightest, and the stand
// has eight separate leg columns of ▒.
func TestHorseMonoGlyphs(t *testing.T) {
	m := widget.MonoPalette()
	for _, box := range showHorseBoxes {
		text := showtest.Flatten(widget.Stand(box.width, m))
		for _, g := range []string{"█", "▀", "▄", "▒", "░", "▓"} {
			if !strings.Contains(text, g) {
				t.Errorf("%s: the mono horse has no %s:\n%s", box.name, g, text)
			}
		}
		for _, l := range widget.Stand(box.width, m) {
			for _, sp := range l {
				if sp.Style.FG != (cell.Color{}) || sp.Style.BG != (cell.Color{}) {
					t.Fatalf("%s: a mono horse has a colour: %+v", box.name, sp.Style)
				}
			}
		}
		// busy legs are Bold and idle legs Dim, which is how the state reads without colour
		lines := widget.Gallop(showLegs(1<<2), 0, box.width, m)
		bold, dim := false, false
		for _, l := range lines {
			for _, sp := range l {
				if strings.Contains(sp.Text, "▒") {
					bold = bold || sp.Style.Has(cell.Bold)
					dim = dim || sp.Style.Has(cell.Dim) || sp.Style == (cell.Style{})
				}
			}
		}
		if !bold {
			t.Errorf("%s: the busy leg is not Bold", box.name)
		}
		_ = dim
	}
}

// The state of a leg is in its style: busy legs are Bold in their role colour, idle legs are faded and not Bold.
func TestHorseLegStyles(t *testing.T) {
	p := showPal()
	lines := widget.Gallop(showLegs(1<<3), 1, 44, p)
	busyColour, idleColour := p.RoleColors[2], p.RoleColors[1] // leg 3 is a frontend (role 2), leg 1 a backend (role 1)
	var sawBusy, sawIdle, idleBold bool
	for _, l := range lines {
		for _, sp := range l {
			if sp.Style.FG == busyColour && sp.Style.Has(cell.Bold) {
				sawBusy = true
			}
			if sp.Style.FG != (cell.Color{}) && sp.Style.FG != busyColour && sp.Style.FG != p.HorseBody && sp.Style.FG != p.HorseHair &&
				sp.Style.FG != p.HorseEye && sp.Style.FG != p.HorseHoof {
				sawIdle = true
				idleBold = idleBold || sp.Style.Has(cell.Bold)
			}
			if sp.Style.FG == idleColour {
				t.Fatalf("an idle leg is drawn in its full role colour: %+v", sp.Style)
			}
		}
	}
	if !sawBusy || !sawIdle || idleBold {
		t.Errorf("busy legs Bold (%v), idle legs faded (%v) and not Bold (%v)", sawBusy, sawIdle, !idleBold)
	}
	// Stand is the mark: no leg is faded
	stand := widget.Stand(44, p)
	full := 0
	for _, l := range stand {
		for _, sp := range l {
			for _, rc := range p.RoleColors {
				if sp.Style.FG == rc || sp.Style.BG == rc {
					full++
				}
			}
		}
	}
	if full == 0 {
		t.Error("the standing horse has no leg in a role colour")
	}
}

func TestHorseFewerOrMoreLegs(t *testing.T) {
	p := widget.DefaultPalette()
	all := showtest.Flatten(widget.Gallop(showLegs(0xff), 2, 44, p))
	// legs that are not given are idle, extra legs are ignored, a colour that is out of range wraps
	three := showLegs(0xff)[:3:3]
	if got := showtest.Flatten(widget.Gallop(three, 2, 44, p)); got == all || got == "" {
		t.Error("three busy legs are not the whole stride")
	}
	extra := append(showLegs(0xff), widget.Leg{Busy: false, Color: 3}, widget.Leg{Busy: false})
	if got := showtest.Flatten(widget.Gallop(extra, 2, 44, p)); got != all {
		t.Error("legs after the eighth are ignored")
	}
	odd := []widget.Leg{{Busy: true, Color: -5}, {Busy: true, Color: 1000}, {Color: 1 << 40}}
	if got := widget.Gallop(odd, 0, 44, p); len(got) == 0 {
		t.Error("odd colours")
	}
	if got := widget.Gallop(nil, 0, 44, p); len(got) == 0 {
		t.Error("no legs at all is an idle horse")
	}
}
