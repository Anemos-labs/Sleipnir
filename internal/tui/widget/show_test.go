package widget_test

// Shared scaffolding of the tests of the signature widgets (stack, spark, ttl, fold, fork, fan, horse, heat, gantt, kanban,
// merge, mail, agent table, dashboard). The tests live in the external test package so that they depend on the public API only
// and cannot collide with the names in the internal tests of the text widgets; every identifier here starts with "show".
//
// Golden files are under testdata/show/ and are rewritten with `go test ./internal/tui/widget -update`; read the diff before
// you commit it: a golden file is a sequence of text pictures, and a changed picture is the change you are reviewing.

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

// showGolden compares the document with testdata/show/<name>.txt.
func showGolden(t *testing.T, name string, d *showtest.Doc) {
	t.Helper()
	showtest.Golden(t, "testdata/show/"+name+".txt", d.String())
}

// showPal is a palette whose colours are all different from each other, so that a styled golden file names every style
// without ambiguity (the real default palette reuses colours: G3 is also "good"). showNames names them.
func showPal() widget.Palette {
	var p widget.Palette
	for i := range p.LayerColors {
		p.LayerColors[i] = cell.RGB(uint8(200+i), 10, 10)
	}
	for i := range p.RoleColors {
		p.RoleColors[i] = cell.RGB(10, uint8(200+i), 10)
	}
	p.Good, p.Bad, p.Warn, p.Info, p.Accent = cell.RGB(10, 10, 201), cell.RGB(10, 10, 202), cell.RGB(10, 10, 203), cell.RGB(10, 10, 204), cell.RGB(10, 10, 205)
	p.Dim, p.Faint = cell.RGB(120, 120, 121), cell.RGB(60, 60, 61)
	p.HorseBody, p.HorseHair, p.HorseEye, p.HorseHoof = cell.RGB(250, 250, 240), cell.RGB(250, 250, 241), cell.RGB(250, 250, 242), cell.RGB(250, 250, 243)
	return p
}

func showNames(p widget.Palette) map[cell.Color]string {
	m := map[cell.Color]string{
		p.Good: "good", p.Bad: "bad", p.Warn: "warn", p.Info: "info", p.Accent: "accent", p.Dim: "dim", p.Faint: "faint",
		p.HorseBody: "body", p.HorseHair: "hair", p.HorseEye: "eye", p.HorseHoof: "hoof",
	}
	for i, c := range p.LayerColors {
		m[c] = fmt.Sprintf("G%d", i)
	}
	for i, c := range p.RoleColors {
		m[c] = fmt.Sprintf("r%d", i)
	}
	return m
}

// showPalettes is the palettes every widget is tried with: the sketches' colours, none at all, and the distinct test colours.
func showPalettes() map[string]widget.Palette {
	return map[string]widget.Palette{"default": widget.DefaultPalette(), "mono": widget.MonoPalette(), "distinct": showPal()}
}

// showRand is a generator with a fixed seed: the tests are deterministic.
func showRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// showWidths are the widths a property test sweeps: every width from 0 to 12, then a spread up to 210 that has a width on
// each side of every threshold where a widget changes its layout (60, 70 and 96 columns for the cockpit, 20 for the bar's
// labels, and so on).
func showWidths() []int {
	var ws []int
	for w := 0; w <= 12; w++ {
		ws = append(ws, w)
	}
	return append(ws, 14, 16, 19, 20, 21, 24, 28, 32, 36, 40, 44, 48, 56, 59, 60, 61, 64, 69, 70, 71, 80, 90, 95, 96, 97, 100, 110, 120, 140, 160, 200, 210)
}

// showNoWider fails the test when a line is wider than w.
func showNoWider(t *testing.T, what string, ls []cell.Line, w int) {
	t.Helper()
	for i, l := range ls {
		if lw := l.Width(); lw > w {
			t.Fatalf("%s: line %d is %d cells wide, limit %d: %q", what, i, lw, w, l.Plain())
		}
	}
}

// showNoColour fails the test when any span of the lines has a colour: what MonoPalette draws is text with Bold, Dim and the like.
func showNoColour(t *testing.T, what string, ls []cell.Line) {
	t.Helper()
	for i, l := range ls {
		for _, sp := range l {
			if sp.Style.FG != (cell.Color{}) || sp.Style.BG != (cell.Color{}) {
				t.Fatalf("%s: line %d has a colour (%+v) without a palette: %q", what, i, sp.Style, sp.Text)
			}
		}
	}
}

// showNoControl fails the test when any text of the lines holds a character that a terminal would act on.
func showNoControl(t *testing.T, what string, ls []cell.Line) {
	t.Helper()
	for i, l := range ls {
		for _, sp := range l {
			for _, r := range sp.Text {
				if r < 0x20 || (r >= 0x7f && r < 0xa0) || r == '\u2028' || r == '\u2029' || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
					t.Fatalf("%s: line %d holds the control character %U: %q", what, i, r, l.Plain())
				}
			}
		}
	}
}
