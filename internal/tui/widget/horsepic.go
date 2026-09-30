// The horse as pictures: the generated sprites (horsesprite_gen.go, from docs/design/ux/sprite.py) parsed into pixel grids, busy
// legs taken from the gait frame and idle ones from the standing picture, and two pixel rows packed into one terminal row with
// the half blocks. Nothing here is drawn from shapes: the sprites are the official mark rasterised, and this file only arranges
// them.

package widget

import (
	"github.com/reee344/sleipnir/internal/tui/cell"
)

// hpx is one pixel of a sprite. key is ' ' (nothing), 'b' body, 'm' mane and tail, 'e' eye, 'L' a leg, 'h' a hoof; leg is which of
// the eight legs an 'L' or an 'h' belongs to (-1 when no leg is near a hoof).
type hpx struct {
	key byte
	leg int8
}

func (a hpx) empty() bool { return a.key == ' ' || a.key == 0 }

// hpic is a sprite as a grid of pixels.
type hpic struct {
	w, h int
	px   []hpx
}

func (p hpic) at(x, y int) hpx {
	if x < 0 || y < 0 || x >= p.w || y >= p.h {
		return hpx{key: ' '}
	}
	return p.px[y*p.w+x]
}

// horseSizeNames are the sprite sizes, largest first; the generated file is a map and a map is never iterated where order shows.
var horseSizeNames = [...]string{"large", "medium", "small"}

// horseDrawOrder is the order the generator paints the legs in: the far ones (behind the body) first, then the near ones.
var horseDrawOrder = [8]int{0, 2, 4, 6, 1, 3, 5, 7}

// horseMarkLegs is the role colour of each leg in the mark: a manager, two backend, two frontend, a tester, a reviewer, docs.
var horseMarkLegs = [8]int{0, 1, 1, 2, 2, 3, 4, 5}

// horseBox is the box that holds every picture of a size, in pixels: x0 <= x < x1 and y0 <= y < y1, with both ends of y on a terminal
// row (two pixel rows). It is worked out from the raw sprite without parsing it, so that choosing a size costs nothing.
type horseBox struct{ x0, x1, y0, y1 int }

func (b horseBox) cols() int { return b.x1 - b.x0 }
func (b horseBox) rows() int { return (b.y1 - b.y0) / 2 }

// horseBoxOf is the box of a size; ok is false when there is no such size or it has no pixels.
func horseBoxOf(name string) (horseBox, bool) {
	sp, found := horseSprites[name]
	if !found {
		return horseBox{}, false
	}
	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	scan := func(rows []string) {
		for y, r := range rows {
			for x := 0; x < len(r); x++ {
				if c := r[x]; c == 'b' || c == 'm' || c == 'e' || c == 'h' || (c >= '0' && c <= '7') {
					minX, maxX = min(minX, x), max(maxX, x)
					minY, maxY = min(minY, y), max(maxY, y)
				}
			}
		}
	}
	for i := range sp.frames {
		scan(sp.frames[i])
	}
	scan(sp.stand)
	if maxX < 0 {
		return horseBox{}, false
	}
	return horseBox{x0: minX, x1: maxX + 1, y0: minY &^ 1, y1: (maxY + 2) &^ 1}, true
}

// horseSet is one size of the horse, parsed.
type horseSet struct {
	horseBox
	frames [4]hpic
	stand  hpic
	body   hpic // everything but the legs: the same in every picture, with what a near leg covers filled in from the others
}

// horseLoad parses one size; ok is false when there is no such size or it has no pixels.
func horseLoad(name string) (s horseSet, ok bool) {
	box, found := horseBoxOf(name)
	if !found {
		return s, false
	}
	s.horseBox = box
	sp := horseSprites[name]
	w, h := 0, 0
	all := append(append([][]string{}, sp.frames[:]...), sp.stand)
	for _, rows := range all {
		if len(rows) > h {
			h = len(rows)
		}
		for _, r := range rows {
			if len(r) > w {
				w = len(r)
			}
		}
	}
	if w == 0 || h == 0 {
		return s, false
	}
	for i := range sp.frames {
		s.frames[i] = horseParse(sp.frames[i], w, h)
	}
	s.stand = horseParse(sp.stand, w, h)

	// the body: a pixel is body if any picture shows body there (a near leg hides it only in some of them)
	s.body = hpic{w: w, h: h, px: make([]hpx, w*h)}
	rank := func(k byte) int {
		switch k {
		case 'm':
			return 3
		case 'e':
			return 2
		case 'b':
			return 1
		}
		return 0
	}
	pics := append(append([]hpic{}, s.frames[:]...), s.stand)
	for i := range s.body.px {
		for _, pic := range pics {
			if a := pic.px[i]; rank(a.key) > rank(s.body.px[i].key) {
				s.body.px[i] = hpx{key: a.key, leg: -1}
			}
		}
	}
	return s, true
}

// horseParse reads rows of keys into a picture w by h and gives every hoof to the leg nearest to it.
func horseParse(rows []string, w, h int) hpic {
	p := hpic{w: w, h: h, px: make([]hpx, w*h)}
	for y, r := range rows {
		for x := 0; x < len(r) && x < w; x++ {
			switch c := r[x]; {
			case c >= '0' && c <= '7':
				p.px[y*w+x] = hpx{key: 'L', leg: int8(c - '0')}
			case c == 'b' || c == 'm' || c == 'e':
				p.px[y*w+x] = hpx{key: c, leg: -1}
			case c == 'h':
				p.px[y*w+x] = hpx{key: 'h', leg: -1}
			default:
				p.px[y*w+x] = hpx{key: ' ', leg: -1}
			}
		}
	}
	const reach = 4
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if p.px[y*w+x].key != 'h' {
				continue
			}
			best, bestD := int8(-1), 1<<30
			for dy := -reach; dy <= reach; dy++ {
				for dx := -reach; dx <= reach; dx++ {
					if a := p.at(x+dx, y+dy); a.key == 'L' {
						if d := dx*dx + dy*dy; d < bestD || (d == bestD && a.leg < best) {
							best, bestD = a.leg, d
						}
					}
				}
			}
			p.px[y*w+x].leg = best
		}
	}
	return p
}

// horseCompose is the picture of the horse with the busy legs as they are in the gait frame and the idle ones as they stand. Legs
// are painted in the generator's order, near over far, and the body hides a far leg and is hidden by a near one; the hair and
// the eye are on top of everything, as they are in the generated pictures. With every leg busy it is the frame, with none it is
// the standing picture, exactly.
func (s horseSet) compose(frame int, busy [8]bool) hpic {
	f := s.frames[((frame%4)+4)%4]
	all, none := true, true
	for _, b := range busy {
		all = all && b
		none = none && !b
	}
	switch {
	case all:
		return f
	case none:
		return s.stand
	}
	out := hpic{w: s.body.w, h: s.body.h, px: append([]hpx(nil), s.body.px...)}
	for _, k := range horseDrawOrder {
		src := s.stand
		if busy[k] {
			src = f
		}
		for i, a := range src.px {
			if int(a.leg) != k || (a.key != 'L' && a.key != 'h') {
				continue
			}
			switch o := out.px[i]; {
			case o.key == 'm' || o.key == 'e':
				continue // hair and eye are on top
			case o.key == 'b' && k%2 == 0:
				continue // a far leg is behind the body
			}
			out.px[i] = a
		}
	}
	return out
}

// horseCell is what is drawn in one terminal cell.
type horseCell struct {
	glyph string
	st    cell.Style
}

// horseStyler says how a pixel is coloured: the colour, and the attribute for a leg that is bright or dim.
type horseStyler func(a hpx) (cell.Color, cell.Attr)

// horseGlyph packs the pixels t (upper) and b (lower) into one cell. Both empty is a space, one is a half block in its colour,
// the same key twice is a full block, and two different keys are the upper half in the upper colour over the lower colour as
// background. Without colours the glyph carries what colour would: the body is solid (█▀▄), legs and their hooves are shades
// (▒ for the far legs, ▓ for the near ones), mane and tail ░, and the eye is a hole.
func horseGlyph(t, b hpx, mono bool, style horseStyler) horseCell {
	if mono {
		return horseMono(t, b, style)
	}
	te, be := t.empty(), b.empty()
	switch {
	case te && be:
		return horseCell{glyph: " "}
	case be:
		c, at := style(t)
		return horseCell{"▀", cell.Style{FG: c, Attr: at}}
	case te:
		c, at := style(b)
		return horseCell{"▄", cell.Style{FG: c, Attr: at}}
	case t == b:
		c, at := style(t)
		return horseCell{"█", cell.Style{FG: c, Attr: at}}
	}
	tc, ta := style(t)
	bc, _ := style(b)
	return horseCell{"▀", cell.Style{FG: tc, BG: bc, Attr: ta}}
}

func horseMono(t, b hpx, style horseStyler) horseCell {
	// a leg, or the hoof of one, is a shade: ▒ for the far legs (the even ones), ▓ for the near ones, so that neighbours can be told
	// apart; its brightness is its leg's (Bold when busy, Dim when idle)
	owner := func(a hpx) (int8, bool) { return a.leg, (a.key == 'L' || a.key == 'h') && a.leg >= 0 }
	tl, tok := owner(t)
	bl, bok := owner(b)
	switch {
	case tok || bok:
		leg := tl
		if !tok || (bok && bl%2 == 1) {
			leg = bl
		}
		glyph := "▒"
		if leg%2 == 1 {
			glyph = "▓"
		}
		_, at := style(hpx{key: 'L', leg: leg})
		return horseCell{glyph, cell.Style{Attr: at}}
	case t.key == 'L' || b.key == 'L' || t.key == 'h' || b.key == 'h': // a hoof with no leg near it
		return horseCell{"▓", cell.Style{}}
	}
	tb, bb := t.key == 'b', b.key == 'b'
	switch {
	case tb && bb:
		return horseCell{"█", cell.Style{}}
	case tb:
		return horseCell{"▀", cell.Style{}}
	case bb:
		return horseCell{"▄", cell.Style{}}
	case t.key == 'm' || b.key == 'm':
		return horseCell{"░", cell.Style{}}
	}
	return horseCell{glyph: " "}
}

// lines draws the picture inside the box of the set, indented by pad cells.
func (s horseSet) lines(pic hpic, pad int, mono bool, style horseStyler) []cell.Line {
	out := make([]cell.Line, 0, s.rows())
	for y := s.y0; y < s.y1; y += 2 {
		var b showRowBuf
		b.space(pad)
		for x := s.x0; x < s.x1; x++ {
			c := horseGlyph(pic.at(x, y), pic.at(x, y+1), mono, style)
			b.add(c.st, c.glyph)
		}
		out = append(out, horseTrimBlank(b.line()))
	}
	return out
}

// horseTrimBlank cuts the spaces at the right end of a line.
func horseTrimBlank(l cell.Line) cell.Line {
	for len(l) > 0 {
		last := &l[len(l)-1]
		t := last.Text
		for len(t) > 0 && t[len(t)-1] == ' ' {
			t = t[:len(t)-1]
		}
		if t == "" {
			l = l[:len(l)-1]
			continue
		}
		last.Text = t
		break
	}
	return l
}
