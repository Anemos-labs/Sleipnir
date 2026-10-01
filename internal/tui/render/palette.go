package render

import (
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
)

// The renderer writes colours the terminal can show. Truecolor passes RGB through; a 256-colour terminal gets the nearest entry
// of the palette and a 16-colour terminal the standard colour that keeps the hue.
//
// 256 colours: the nearest entry by redmean distance, which weighs red and blue by how red the colours are and so tracks
// perception better than plain Euclidean distance while staying in integers; ties go to the lowest index, so the answer is
// deterministic. Only the 6x6x6 cube (levels 0, 95, 135, 175, 215, 255) and the 24 greys (8 + 10 i) are searched: entries 0 to
// 15 are the terminal's theme colours and have no fixed RGB.
//
// 16 colours: "nearest" is the wrong idea here. The standard colours are far apart, and a nearest-RGB search sends every
// desaturated mid-tone (a soft green, a salmon red, a violet) to the grey in the middle, which would turn "green is good, red is
// bad" (docs/UX.md) into grey on grey. So the colour is first judged for chroma: a colour with little of it is a shade of grey
// and gets black, dark grey, silver or white by its lightness; any other keeps its hue, which picks red, yellow, green, cyan,
// blue or magenta, and is the bright variant when it is light enough. The hue sectors are not equal: green takes 75 to 165
// degrees so that the yellow-green of a UI's "good" stays green, and orange and amber (15 to 75) are yellow, the nearest
// the 16 colours have to them.

var cubeLevels = [6]uint8{0, 95, 135, 175, 215, 255}

// paletteRGB is the colour of entry n of the 256-colour palette, for n from 16 up (the cube and the greys); the first 16
// entries depend on the terminal's theme and give black.
func paletteRGB(n uint8) (r, g, b uint8) {
	switch {
	case n < 16:
		return 0, 0, 0
	case n < 232:
		c := n - 16
		return cubeLevels[c/36], cubeLevels[(c/6)%6], cubeLevels[c%6]
	}
	v := 8 + 10*(n-232)
	return v, v, v
}

// distance is the redmean colour distance, squared and scaled; only its order matters.
func distance(r1, g1, b1, r2, g2, b2 int) int {
	rmean := (r1 + r2) / 2
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return ((512+rmean)*dr*dr)>>8 + 4*dg*dg + ((767-rmean)*db*db)>>8
}

// nearest256 is the entry of the 256-colour palette (16 to 255) closest to the colour.
func nearest256(r, g, b uint8) uint8 {
	best, bestD := 16, -1
	for n := 16; n <= 255; n++ {
		pr, pg, pb := paletteRGB(uint8(n))
		if d := distance(int(r), int(g), int(b), int(pr), int(pg), int(pb)); bestD < 0 || d < bestD {
			best, bestD = n, d
		}
	}
	return uint8(best)
}

// toANSI16 is the standard colour (0 to 15) that stands for the colour: see the comment at the top of this file.
func toANSI16(r, g, b uint8) uint8 {
	ri, gi, bi := int(r), int(g), int(b)
	hi, lo := max(ri, gi, bi), min(ri, gi, bi)
	chroma := hi - lo
	if chroma < 32 { // a shade of grey
		switch {
		case hi < 64:
			return 0
		case hi < 150:
			return 8
		case hi < 220:
			return 7
		}
		return 15
	}
	if hi < 48 {
		return 0 // dark enough that no hue shows
	}
	var h int // the hue in degrees, from 0 to 359
	switch hi {
	case ri:
		h = 60 * (gi - bi) / chroma
	case gi:
		h = 60*(bi-ri)/chroma + 120
	default:
		h = 60*(ri-gi)/chroma + 240
	}
	if h < 0 {
		h += 360
	}
	var c uint8
	switch {
	case h >= 345 || h < 15:
		c = 1 // red
	case h < 75:
		c = 3 // yellow
	case h < 165:
		c = 2 // green
	case h < 200:
		c = 6 // cyan
	case h < 255:
		c = 4 // blue
	default:
		c = 5 // magenta
	}
	if hi >= 168 {
		c += 8 // the bright variant
	}
	return c
}

// wireKind says how a colour is written.
type wireKind uint8

const (
	wireDefault wireKind = iota
	wireANSI             // SGR 30-37, 90-97 / 40-47, 100-107
	wireIndexed          // SGR 38;5;n / 48;5;n
	wireRGB              // SGR 38;2;r;g;b / 48;2;r;g;b
)

// wireColor is a colour as it will be written to the terminal, after down-sampling. Two cell colours that map to the same
// wireColor are the same on screen, so the renderer compares these, not the cell colours.
type wireColor struct {
	kind    wireKind
	n       uint8 // wireANSI (0-15), wireIndexed
	r, g, b uint8 // wireRGB
}

// colorMap maps cell colours to what a terminal of one depth can show. The searches are cached (there are 16M RGB values but a
// UI uses a few dozen). It belongs to one renderer and is used under that renderer's lock: there is no shared state.
type colorMap struct {
	depth term.ColorDepth
	cache map[uint32]uint8
}

func newColorMap(d term.ColorDepth) *colorMap {
	return &colorMap{depth: d, cache: map[uint32]uint8{}}
}

// down is the palette entry for an RGB colour: the 256-colour entry, or with to16 the standard colour. Results are cached.
func (m *colorMap) down(r, g, b uint8, to16 bool) uint8 {
	key := uint32(r)<<16 | uint32(g)<<8 | uint32(b)
	if to16 {
		key |= 1 << 24
	}
	if n, ok := m.cache[key]; ok {
		return n
	}
	var n uint8
	if to16 {
		n = toANSI16(r, g, b)
	} else {
		n = nearest256(r, g, b)
	}
	m.cache[key] = n
	return n
}

// resolve is the colour as it will be written at the map's depth.
func (m *colorMap) resolve(c cell.Color) wireColor {
	if m.depth == term.ColorNone {
		return wireColor{} // a view with no colour keeps its attributes (bold, reverse) and nothing else
	}
	switch c.Kind {
	case cell.KindANSI:
		return wireColor{kind: wireANSI, n: c.N & 15}
	case cell.KindIndexed:
		switch m.depth {
		case term.ColorTrueColor, term.ColorANSI256:
			return wireColor{kind: wireIndexed, n: c.N}
		}
		if c.N < 16 {
			return wireColor{kind: wireANSI, n: c.N}
		}
		r, g, b := paletteRGB(c.N)
		return wireColor{kind: wireANSI, n: m.down(r, g, b, true)}
	case cell.KindRGB:
		switch m.depth {
		case term.ColorTrueColor:
			return wireColor{kind: wireRGB, r: c.R, g: c.G, b: c.B}
		case term.ColorANSI256:
			return wireColor{kind: wireIndexed, n: m.down(c.R, c.G, c.B, false)}
		}
		return wireColor{kind: wireANSI, n: m.down(c.R, c.G, c.B, true)}
	}
	return wireColor{}
}
