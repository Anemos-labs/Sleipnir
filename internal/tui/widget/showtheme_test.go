package widget_test

// The palette: the default one is the sketches' (docs/design/ux/sketchlib.py PAL and LAYER, and the horse of sprite.py), the mono
// one has no colour at all, and the zero Palette is the mono one.

import (
	"reflect"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

func TestShowDefaultPaletteIsTheSketches(t *testing.T) {
	p := widget.DefaultPalette()
	h := cell.Hex
	for i, hex := range []string{"#4c6fd0", "#7aa2f7", "#2ac3de", "#9ece6a", "#e0af68", "#ff9e64", "#f7768e"} { // LAYER G0..G6
		if p.LayerColors[i] != h(hex) {
			t.Errorf("G%d is %+v, the sketches have %s", i, p.LayerColors[i], hex)
		}
	}
	for name, c := range map[string][2]cell.Color{ // PAL: green, red, yellow, cyan, magenta, dim, faint
		"good": {p.Good, h("#9ece6a")}, "bad": {p.Bad, h("#f7768e")}, "warn": {p.Warn, h("#e0af68")}, "info": {p.Info, h("#7dcfff")},
		"accent": {p.Accent, h("#bb9af7")}, "dim": {p.Dim, h("#565f89")}, "faint": {p.Faint, h("#3b4261")},
		"horse body": {p.HorseBody, h("#e9e7f5")}, "horse hair": {p.HorseHair, h("#8f6bf0")}, "horse eye": {p.HorseEye, h("#1a1b26")},
		"horse hoof": {p.HorseHoof, h("#3b3850")},
	} {
		if c[0] != c[1] || c[0].Kind != cell.KindRGB {
			t.Errorf("%s is %+v, want %+v", name, c[0], c[1])
		}
	}
	// the roles: manager, backend, frontend, tester, reviewer, docs are the sketch's colours, and the two spares differ from all
	for i, hex := range []string{"#bb9af7", "#7aa2f7", "#7dcfff", "#9ece6a", "#e0af68", "#ff9e64"} {
		if p.RoleColors[i] != h(hex) {
			t.Errorf("role %d is %+v, the sketches have %s", i, p.RoleColors[i], hex)
		}
	}
	seen := map[cell.Color]int{}
	for i, c := range p.RoleColors {
		if c.Kind != cell.KindRGB {
			t.Errorf("role %d has no colour", i)
		}
		if j, ok := seen[c]; ok {
			t.Errorf("roles %d and %d have the same colour", j, i)
		}
		seen[c] = i
	}
	if p.Mono {
		t.Error("the default palette is not mono")
	}
}

func TestShowMonoPaletteHasNoColour(t *testing.T) {
	m := widget.MonoPalette()
	if !m.Mono {
		t.Error("MonoPalette says it is not mono")
	}
	if m != (widget.Palette{Mono: true}) {
		t.Errorf("MonoPalette has colours in it: %+v", m)
	}
	// the zero Palette is the same as MonoPalette: what every widget draws with it is the same, styles included
	data := showSketchDashboard(8)
	for _, sz := range showDashSizes {
		if a, b := widget.Dashboard(data, sz.w, sz.h, 4, widget.Palette{}), widget.Dashboard(data, sz.w, sz.h, 4, m); !reflect.DeepEqual(a, b) {
			t.Errorf("the zero palette and MonoPalette draw the cockpit differently at %dx%d", sz.w, sz.h)
		}
	}
	legs := []widget.Leg{{Busy: true}, {}, {Busy: true, Color: 3}}
	if a, b := widget.Gallop(legs, 2, 60, widget.Palette{}), widget.Gallop(legs, 2, 60, m); !reflect.DeepEqual(a, b) {
		t.Error("the zero palette and MonoPalette draw the horse differently")
	}
	for _, l := range widget.Gallop(legs, 2, 60, m) {
		showNoColour(t, "the horse without colours", []cell.Line{l})
	}
}
