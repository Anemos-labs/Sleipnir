// The palette of the signature widgets (the prompt stack, the cache sparkline, the TTL clock, the fold and fork animations, the
// horse and the swarm cockpit). It is deliberately small and local to these widgets: the maintainer unifies it with the
// text widgets' Theme later, so nothing here is referenced by name outside the files of these widgets.

package widget

import "github.com/anemos-labs/sleipnir/internal/tui/cell"

// Palette is the colours the signature widgets draw with. A widget never decides state by colour alone: glyphs, words and
// the Bold and Dim attributes carry the same information, so MonoPalette (no colours at all) stays fully readable and draws
// the same characters as DefaultPalette (the one exception is the horse, whose legs and hair change glyph in Mono, see Gallop).
//
// The zero Palette is the same as MonoPalette.
type Palette struct {
	// LayerColors are the colours of prompt layers G0..G6, from cold (stable) to hot (volatile): indigo, blue, teal, green,
	// yellow, orange, red.
	LayerColors [7]cell.Color
	// Good is a hit or a pass, Bad a miss, a stuck agent or an error, Warn a wait or a warning, Info a neutral highlight,
	// Accent the harness itself (violet).
	Good, Bad, Warn, Info, Accent cell.Color
	// Dim is secondary text, Faint borders and empty tracks.
	Dim, Faint cell.Color
	// RoleColors colour agents by role: manager, backend, frontend, tester, reviewer, docs, and two spares.
	RoleColors [8]cell.Color
	// HorseBody, HorseHair (mane and tail), HorseEye (the dark of the eye) and HorseHoof colour the horse around its legs.
	HorseBody, HorseHair, HorseEye, HorseHoof cell.Color
	// Mono says that the terminal shows no colours: the horse then draws shades instead. It is also implied when every colour
	// is the default one.
	Mono bool
}

// DefaultPalette is the palette of the design sketches (docs/design/ux/sketchlib.py PAL and LAYER), for a dark terminal.
func DefaultPalette() Palette {
	h := cell.Hex
	return Palette{
		LayerColors: [7]cell.Color{h("#4c6fd0"), h("#7aa2f7"), h("#2ac3de"), h("#9ece6a"), h("#e0af68"), h("#ff9e64"), h("#f7768e")},
		Good:        h("#9ece6a"),
		Bad:         h("#f7768e"),
		Warn:        h("#e0af68"),
		Info:        h("#7dcfff"),
		Accent:      h("#bb9af7"),
		Dim:         h("#565f89"),
		Faint:       h("#3b4261"),
		RoleColors: [8]cell.Color{
			h("#bb9af7"), h("#7aa2f7"), h("#7dcfff"), h("#9ece6a"), h("#e0af68"), h("#ff9e64"), h("#73daca"), h("#e5a1e0"),
		},
		HorseBody: h("#e9e7f5"),
		HorseHair: h("#8f6bf0"),
		HorseEye:  h("#1a1b26"),
		HorseHoof: h("#3b3850"),
	}
}

// MonoPalette has no colours: every colour is the terminal's own. What colour would say is said by glyphs, words, Bold and
// Dim instead.
func MonoPalette() Palette { return Palette{Mono: true} }

// noColour reports whether p draws without colours.
func (p Palette) noColour() bool { return p.Mono || p == (Palette{}) }

// showFG creates a style that sets only the foreground color.
func showFG(c cell.Color) cell.Style { return cell.Style{FG: c} }

// dimSt is secondary text: the Dim colour, or the Dim attribute when there is no colour to be dim in.
func (p Palette) dimSt() cell.Style {
	if p.Dim == (cell.Color{}) {
		return cell.Style{Attr: cell.Dim}
	}
	return showFG(p.Dim)
}

// faintSt is for borders and empty tracks.
func (p Palette) faintSt() cell.Style {
	if p.Faint == (cell.Color{}) {
		return cell.Style{Attr: cell.Dim}
	}
	return showFG(p.Faint)
}

// goodSt returns the palette's success foreground style.
func (p Palette) goodSt() cell.Style { return showFG(p.Good) }

// badSt returns the palette's error foreground style with bold emphasis.
func (p Palette) badSt() cell.Style { return showFG(p.Bad).With(cell.Bold) }

// warnSt returns the palette's warning foreground style.
func (p Palette) warnSt() cell.Style { return showFG(p.Warn) }

// infoSt returns the palette's informational foreground style.
func (p Palette) infoSt() cell.Style { return showFG(p.Info) }

// accentSt returns the palette's accent foreground style.
func (p Palette) accentSt() cell.Style { return showFG(p.Accent) }

// layerCol is the colour of prompt layer i; an index outside G0..G6 is clamped.
func (p Palette) layerCol(i int) cell.Color {
	return p.LayerColors[showClamp(i, 0, len(p.LayerColors)-1)]
}

// layerSt is the style of a bright (cached) cell of layer i.
func (p Palette) layerSt(i int) cell.Style { return showFG(p.layerCol(i)) }

// roleCol is the colour of role i; an index outside the table wraps around, so any role number gets a colour.
func (p Palette) roleCol(i int) cell.Color {
	n := len(p.RoleColors)
	return p.RoleColors[((i%n)+n)%n]
}

// roleSt is the style of role i's name.
func (p Palette) roleSt(i int) cell.Style { return showFG(p.roleCol(i)).With(cell.Bold) }

// fade mixes colour c towards the Faint colour by t percent (0 keeps c). Only 24-bit colours can be mixed; any other colour
// comes back as it is and the caller adds the Dim attribute instead (fadeSt does both).
func (p Palette) fade(c cell.Color, t int) cell.Color {
	if c.Kind != cell.KindRGB || p.Faint.Kind != cell.KindRGB || t <= 0 {
		return c
	}
	t = showClamp(t, 0, 100)
	mix := func(a, b uint8) uint8 { return uint8((int(a)*(100-t) + int(b)*t + 50) / 100) }
	return cell.RGB(mix(c.R, p.Faint.R), mix(c.G, p.Faint.G), mix(c.B, p.Faint.B))
}

// fadeSt is the style of colour c faded by t percent. Where fading cannot be done in colour (a palette of 16 colours, or none)
// the Dim attribute stands in for it, from 30 percent on: a slight cooling is not worth dimming for.
func (p Palette) fadeSt(c cell.Color, t int) cell.Style {
	if t <= 0 {
		return showFG(c)
	}
	if f := p.fade(c, t); f != c {
		return showFG(f)
	}
	if t >= 30 {
		return showFG(c).With(cell.Dim)
	}
	return showFG(c)
}
