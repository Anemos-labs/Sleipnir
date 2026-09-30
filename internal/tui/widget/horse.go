// The eight-legged horse: the mark of Sleipnir, galloping in proportion to the work. Eight legs are eight workers: a leg lifts
// while its worker runs a tool, the gait follows the load, and an idle horse stands (docs/UX.md, "Watch"; swarm.png).
//
// The horse is not drawn here: it is the official mark rasterised into small pixel grids by docs/design/ux/sprite.py, in three
// sizes, and the result is the generated file horsesprite_gen.go. Regenerate it after changing the mark; never edit it by hand.

package widget

import (
	"github.com/reee344/sleipnir/internal/tui/cell"
)

//go:generate python3 ../../../docs/design/ux/sprite.py --go horsesprite_gen.go

// Leg is one of the horse's eight legs, left to right along its belly: four behind and four in front.
type Leg struct {
	// Busy is a worker running a tool: the leg is in the stride, in the gait frame, and bright. An idle leg stands, dim.
	Busy bool
	// Color is the leg's role, an index into Palette.RoleColors (it wraps around, so any number is a colour).
	Color int
}

// Gallop draws the horse galloping to the right, as many terminal rows tall as the sprite size that fits: 16 rows and 46 cells
// wide, or 13 and 36, or 10 and 26 (the largest that fits width; HorseSize says which). legs are the eight legs, left to right;
// a busy leg is in the stride of the gait frame (frame 0 to 3 and then again: a frame is taken modulo 4, so any integer will
// do), an idle leg stands, dim, and the horse stands altogether when no leg is busy. A leg that is not given is idle with the
// role colour of its place. Extra legs are ignored.
//
// Two pixel rows are packed into one cell with the half blocks, so a cell can hold two colours: the body is pale, the mane and
// tail the mark's violet, the eye dark, the hooves dim and every leg in its role's colour. With a palette without colours
// (Palette.Mono) the glyph says what the colour would: the body is solid (█▀▄), the legs are shades (▒ for the four far legs, ▓
// for the four near ones, so that neighbours differ), the mane and tail ░, and busy legs are Bold where idle ones are Dim.
// That is the one widget whose mono text differs from its coloured text; the shape is the same.
//
// The horse is centred in width. A width that holds no sprite gives nothing.
func Gallop(legs []Leg, frame int, width int, p Palette) []cell.Line {
	return GallopIn(legs, frame, width, 0, p)
}

// GallopIn is Gallop for a box that is also limited in height (in rows): it draws the largest sprite that fits both ways. A
// height <= 0 is no limit.
func GallopIn(legs []Leg, frame, width, height int, p Palette) []cell.Line {
	name, _, ok := horsePick(width, height)
	if !ok {
		return nil
	}
	s, ok := horseLoad(name)
	if !ok {
		return nil
	}
	var busy [8]bool
	var colour [8]int
	for k := range colour {
		colour[k] = k
		if k < len(legs) {
			busy[k], colour[k] = legs[k].Busy, legs[k].Color
		}
	}
	pic := s.compose(frame, busy)
	style := func(a hpx) (cell.Color, cell.Attr) {
		switch a.key {
		case 'L':
			c := p.roleCol(colour[a.leg])
			if busy[a.leg] {
				return c, cell.Bold
			}
			f := p.fade(c, 55)
			if f == c { // no 24-bit colour to fade in
				return c, cell.Dim
			}
			return f, 0
		default:
			return horseBaseColour(a, p), 0
		}
	}
	return s.lines(pic, (width-s.cols())/2, p.noColour(), style)
}

// Stand is the horse standing still, the mark itself: every leg upright and in its role's colour (manager, two backend, two
// frontend, tester, reviewer, docs), none dimmed. It is what a terminal without animation shows. A width that holds no sprite
// gives nothing.
func Stand(width int, p Palette) []cell.Line { return StandIn(width, 0, p) }

// StandIn is Stand for a box that is also limited in height: the largest sprite that fits both ways. A height <= 0 is no limit.
func StandIn(width, height int, p Palette) []cell.Line {
	name, _, ok := horsePick(width, height)
	if !ok {
		return nil
	}
	s, ok := horseLoad(name)
	if !ok {
		return nil
	}
	style := func(a hpx) (cell.Color, cell.Attr) {
		if a.key == 'L' {
			return p.roleCol(horseMarkLegs[a.leg]), 0
		}
		return horseBaseColour(a, p), 0
	}
	return s.lines(s.stand, (width-s.cols())/2, p.noColour(), style)
}

// HorseSize is the size in cells of the horse that Gallop and Stand draw in a box of width by height cells (height <= 0: any
// height); (0, 0) when no sprite fits.
func HorseSize(width, height int) (cols, rows int) {
	_, b, ok := horsePick(width, height)
	if !ok {
		return 0, 0
	}
	return b.cols(), b.rows()
}

// horsePick is the largest sprite that fits: its name and its box.
func horsePick(width, height int) (string, horseBox, bool) {
	for _, name := range horseSizeNames {
		b, ok := horseBoxOf(name)
		if ok && b.cols() <= width && (height <= 0 || b.rows() <= height) {
			return name, b, true
		}
	}
	return "", horseBox{}, false
}

// horseBaseColour colours everything but the legs.
func horseBaseColour(a hpx, p Palette) cell.Color {
	switch a.key {
	case 'b':
		return p.HorseBody
	case 'm':
		return p.HorseHair
	case 'e':
		return p.HorseEye
	case 'h':
		return p.HorseHoof
	}
	return cell.Color{}
}
