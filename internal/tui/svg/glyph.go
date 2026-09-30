package svg

import (
	"fmt"
	"strings"
)

// Block elements, shades and box-drawing characters are drawn as shapes, not as glyphs: a font's blocks and lines stop short
// of the cell's top and bottom (the row pitch is larger than the glyph), and a picture made of them shows hairline gaps
// between rows. Shapes fill the cell exactly. Braille patterns (the spinners) and the two parallelograms of the progress bars
// are shapes as well: which font draws them, and how wide, differs from one viewer to the next.

type drawer interface {
	draw(b *strings.Builder, x, y, w, h float64, fg string, dim bool)
}

// glyph returns the shape that draws r, if it has one.
func glyph(r rune) (drawer, bool) {
	if r >= 0x2800 && r <= 0x28ff {
		return brailleShape(r - 0x2800), true
	}
	if d, ok := blocks[r]; ok {
		return d, true
	}
	if d, ok := boxes[r]; ok {
		return d, true
	}
	return nil, false
}

// mergeable says that a run of the same glyph in neighbouring cells is drawn exactly by one shape as wide as the run: a block that
// fills the cell from edge to edge, and a plain horizontal line. Anything else (a half block, a corner, a dot) is drawn cell by cell.
func mergeable(d drawer) bool {
	switch s := d.(type) {
	case blockShape:
		for _, f := range s {
			if f.x != 0 || f.w != 1 {
				return false
			}
		}
		return true
	case lineShape:
		return s.u == 0 && s.d == 0 && s.l > 0 && s.l == s.r && !s.round
	}
	return false
}

// A frac is a rectangle in fractions of the cell, with an opacity (0 means opaque).
type frac struct{ x, y, w, h, a float64 }

type blockShape []frac

func (s blockShape) draw(b *strings.Builder, x, y, w, h float64, fg string, dim bool) {
	for _, f := range s {
		op := f.a
		if dim {
			if op == 0 {
				op = 1
			}
			op *= 0.62
		}
		o := ""
		if op > 0 && op < 1 {
			o = fmt.Sprintf(` fill-opacity="%s"`, num(op))
		}
		// one extra tenth of a unit on the far edges closes the seam that antialiasing leaves between neighbours
		fmt.Fprintf(b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"%s/>`, num(x+f.x*w), num(y+f.y*h), num(f.w*w+0.25), num(f.h*h+0.25), fg, o)
	}
}

var blocks = func() map[rune]drawer {
	m := map[rune]drawer{}
	m['▀'] = blockShape{{0, 0, 1, 0.5, 0}}       // ▀
	m['█'] = blockShape{{0, 0, 1, 1, 0}}         // █
	m['▐'] = blockShape{{0.5, 0, 0.5, 1, 0}}     // ▐
	m['░'] = blockShape{{0, 0, 1, 1, 0.25}}      // ░
	m['▒'] = blockShape{{0, 0, 1, 1, 0.5}}       // ▒
	m['▓'] = blockShape{{0, 0, 1, 1, 0.75}}      // ▓
	m['▔'] = blockShape{{0, 0, 1, 0.125, 0}}     // ▔
	m['▕'] = blockShape{{0.875, 0, 0.125, 1, 0}} // ▕
	for i := 1; i <= 7; i++ {                    // ▁ … ▇: the lower i eighths
		f := float64(i) / 8
		m[rune(0x2580+i)] = blockShape{{0, 1 - f, 1, f, 0}}
	}
	for i := 1; i <= 7; i++ { // ▏ … ▉: the left i eighths (U+258F is one eighth, U+2589 seven)
		f := float64(i) / 8
		m[rune(0x2590-i)] = blockShape{{0, 0, f, 1, 0}}
	}
	ul, ur, ll, lr := frac{0, 0, 0.5, 0.5, 0}, frac{0.5, 0, 0.5, 0.5, 0}, frac{0, 0.5, 0.5, 0.5, 0}, frac{0.5, 0.5, 0.5, 0.5, 0}
	m['▖'] = blockShape{ll}
	m['▗'] = blockShape{lr}
	m['▘'] = blockShape{ul}
	m['▙'] = blockShape{ul, ll, lr}
	m['▚'] = blockShape{ul, lr}
	m['▛'] = blockShape{ul, ur, ll}
	m['▜'] = blockShape{ul, ur, lr}
	m['▝'] = blockShape{ur}
	m['▞'] = blockShape{ur, ll}
	m['▟'] = blockShape{ur, ll, lr}
	// ▰ and ▱, the filled and the outlined parallelogram of a progress bar; the gap between neighbours is the font's too
	para := [4][2]float64{{0.22, 0.3}, {1, 0.3}, {0.78, 0.72}, {0, 0.72}}
	m['▰'] = polyShape{para, true}
	m['▱'] = polyShape{para, false}
	return m
}()

// A brailleShape is a braille pattern: two columns of four dots, bit n of the pattern being dot n+1 (dots 1-3 and 7 are the
// left column, top to bottom, 4-6 and 8 the right one).
type brailleShape uint8

func (s brailleShape) draw(b *strings.Builder, x, y, w, h float64, fg string, dim bool) {
	op := ""
	if dim {
		op = ` fill-opacity="0.62"`
	}
	rad := min(w*0.16, h*0.075)
	for bit := 0; bit < 8; bit++ {
		if s&(1<<bit) == 0 {
			continue
		}
		col, row := bit/3, bit%3 // dots 1-3 are the left column and 4-6 the right, top to bottom
		if bit == 6 {
			col, row = 0, 3
		} else if bit == 7 {
			col, row = 1, 3
		}
		fmt.Fprintf(b, `<circle cx="%s" cy="%s" r="%s" fill="%s"%s/>`, num(x+w*(0.3+0.4*float64(col))), num(y+h*(0.2+0.2*float64(row))), num(rad), fg, op)
	}
}

// A polyShape is a polygon in fractions of the cell, filled or only outlined.
type polyShape struct {
	pts  [4][2]float64
	fill bool
}

func (s polyShape) draw(b *strings.Builder, x, y, w, h float64, fg string, dim bool) {
	var d strings.Builder
	for i, p := range s.pts {
		op := "L"
		if i == 0 {
			op = "M"
		}
		fmt.Fprintf(&d, "%s%s %s", op, num(x+p[0]*w), num(y+p[1]*h))
	}
	d.WriteString("Z")
	op := ""
	if dim {
		op = ` opacity="0.62"`
	}
	if s.fill {
		fmt.Fprintf(b, `<path d="%s" fill="%s"%s/>`, d.String(), fg, op)
		return
	}
	fmt.Fprintf(b, `<path d="%s" fill="none" stroke="%s" stroke-width="1" stroke-linejoin="round"%s/>`, d.String(), fg, op)
}

// A line character is four arms from the cell's centre, each absent, light or heavy; round says the corner is rounded and
// dash that the line is dashed.
type lineShape struct {
	l, r, u, d uint8 // 0 none, 1 light, 2 heavy
	round      bool
	dash       bool
}

func (s lineShape) draw(b *strings.Builder, x, y, w, h float64, fg string, dim bool) {
	cx, cy := x+w/2, y+h/2
	op := ""
	if dim {
		op = ` opacity="0.62"`
	}
	stroke := func(weight uint8) string {
		sw := 1.15
		if weight == 2 {
			sw = 2.3
		}
		return num(sw)
	}
	dash := ""
	if s.dash {
		dash = ` stroke-dasharray="3.2 3.2"`
	}
	path := func(d string, weight uint8) {
		fmt.Fprintf(b, `<path d="%s" fill="none" stroke="%s" stroke-width="%s"%s%s/>`, d, fg, stroke(weight), dash, op)
	}
	half := func(weight uint8) float64 {
		if weight == 2 {
			return 1.15
		}
		return 0.6
	}
	if s.round && s.count() == 2 && s.l+s.r+s.u+s.d > 0 {
		// a rounded corner: from the middle of one arm's edge to the middle of the other's, bending at the centre
		rad := min(w, h) / 2
		var hx, vy float64 // the horizontal arm's far end and the vertical arm's
		var hdir, vdir float64
		switch {
		case s.r > 0:
			hx, hdir = x+w, 1
		default:
			hx, hdir = x, -1
		}
		switch {
		case s.d > 0:
			vy, vdir = y+h, 1
		default:
			vy, vdir = y, -1
		}
		weight := max(s.l, s.r, s.u, s.d)
		path(fmt.Sprintf("M%s %s L%s %s Q%s %s %s %s L%s %s",
			num(hx), num(cy), num(cx+hdir*rad), num(cy), num(cx), num(cy), num(cx), num(cy+vdir*rad), num(cx), num(vy)), weight)
		return
	}
	if s.dash { // a dashed straight line spans the whole cell in one piece
		if s.l+s.r > 0 && s.u+s.d == 0 {
			path(fmt.Sprintf("M%s %s L%s %s", num(x), num(cy), num(x+w), num(cy)), max(s.l, s.r))
			return
		}
		if s.u+s.d > 0 && s.l+s.r == 0 {
			path(fmt.Sprintf("M%s %s L%s %s", num(cx), num(y), num(cx), num(y+h)), max(s.u, s.d))
			return
		}
	}
	// straight arms: a horizontal line through the centre, a vertical one, each as long as it has arms for, so that a
	// light line crossing a heavy one is drawn with both weights
	if s.l > 0 || s.r > 0 {
		x0, x1 := cx, cx
		if s.l > 0 {
			x0 = x
		} else {
			x0 = cx - half(s.r)
		}
		if s.r > 0 {
			x1 = x + w
		} else {
			x1 = cx + half(s.l)
		}
		if s.l == s.r || s.l == 0 || s.r == 0 {
			path(fmt.Sprintf("M%s %s L%s %s", num(x0), num(cy), num(x1), num(cy)), max(s.l, s.r))
		} else {
			path(fmt.Sprintf("M%s %s L%s %s", num(x0), num(cy), num(cx), num(cy)), s.l)
			path(fmt.Sprintf("M%s %s L%s %s", num(cx), num(cy), num(x1), num(cy)), s.r)
		}
	}
	if s.u > 0 || s.d > 0 {
		y0, y1 := cy, cy
		if s.u > 0 {
			y0 = y
		} else {
			y0 = cy - half(s.d)
		}
		if s.d > 0 {
			y1 = y + h
		} else {
			y1 = cy + half(s.u)
		}
		if s.u == s.d || s.u == 0 || s.d == 0 {
			path(fmt.Sprintf("M%s %s L%s %s", num(cx), num(y0), num(cx), num(y1)), max(s.u, s.d))
		} else {
			path(fmt.Sprintf("M%s %s L%s %s", num(cx), num(y0), num(cx), num(cy)), s.u)
			path(fmt.Sprintf("M%s %s L%s %s", num(cx), num(cy), num(cx), num(y1)), s.d)
		}
	}
}

func (s lineShape) count() int {
	n := 0
	for _, a := range []uint8{s.l, s.r, s.u, s.d} {
		if a > 0 {
			n++
		}
	}
	return n
}

var boxes = func() map[rune]drawer {
	m := map[rune]drawer{}
	set := func(r rune, l, rr, u, d uint8) { m[r] = lineShape{l: l, r: rr, u: u, d: d} }
	// light and heavy straight lines, corners, tees and the cross
	set('─', 1, 1, 0, 0)
	set('━', 2, 2, 0, 0)
	set('│', 0, 0, 1, 1)
	set('┃', 0, 0, 2, 2)
	set('┌', 0, 1, 0, 1)
	set('┐', 1, 0, 0, 1)
	set('└', 0, 1, 1, 0)
	set('┘', 1, 0, 1, 0)
	set('├', 0, 1, 1, 1)
	set('┤', 1, 0, 1, 1)
	set('┬', 1, 1, 0, 1)
	set('┴', 1, 1, 1, 0)
	set('┼', 1, 1, 1, 1)
	set('┏', 0, 2, 0, 2)
	set('┓', 2, 0, 0, 2)
	set('┗', 0, 2, 2, 0)
	set('┛', 2, 0, 2, 0)
	set('┣', 0, 2, 2, 2)
	set('┫', 2, 0, 2, 2)
	set('┳', 2, 2, 0, 2)
	set('┻', 2, 2, 2, 0)
	set('╋', 2, 2, 2, 2)
	// rounded corners
	for r, s := range map[rune]lineShape{
		'╭': {r: 1, d: 1, round: true}, '╮': {l: 1, d: 1, round: true}, '╯': {l: 1, u: 1, round: true}, '╰': {r: 1, u: 1, round: true},
	} {
		m[r] = s
	}
	// dashed lines (all the dash variants are drawn with the same dash)
	for r, s := range map[rune]lineShape{
		'┄': {l: 1, r: 1, dash: true}, '┅': {l: 2, r: 2, dash: true}, '┆': {u: 1, d: 1, dash: true}, '┇': {u: 2, d: 2, dash: true},
		'┈': {l: 1, r: 1, dash: true}, '┉': {l: 2, r: 2, dash: true}, '┊': {u: 1, d: 1, dash: true}, '┋': {u: 2, d: 2, dash: true},
		'╌': {l: 1, r: 1, dash: true}, '╍': {l: 2, r: 2, dash: true}, '╎': {u: 1, d: 1, dash: true}, '╏': {u: 2, d: 2, dash: true},
	} {
		m[r] = s
	}
	// double lines are drawn heavy: a double stroke at this size is a blur
	set('═', 2, 2, 0, 0)
	set('║', 0, 0, 2, 2)
	set('╔', 0, 2, 0, 2)
	set('╗', 2, 0, 0, 2)
	set('╚', 0, 2, 2, 0)
	set('╝', 2, 0, 2, 0)
	set('╠', 0, 2, 2, 2)
	set('╣', 2, 0, 2, 2)
	set('╦', 2, 2, 0, 2)
	set('╩', 2, 2, 2, 0)
	set('╬', 2, 2, 2, 2)
	return m
}()
