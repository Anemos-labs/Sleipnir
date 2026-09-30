package vt

import (
	"strconv"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// arg is parameter i, or def when it was left out.
func (t *Term) arg(i, def int) int {
	if i < len(t.p.params) && t.p.params[i].v >= 0 {
		return t.p.params[i].v
	}
	return def
}

// count is a repeat count: left out or 0 means 1.
func (t *Term) count() int { return max(t.arg(0, 1), 1) }

// csi acts on a complete CSI sequence. Anything it does not model is ignored.
func (t *Term) csi(final byte) {
	p := &t.p
	if len(p.inter) > 0 {
		return // CSI with intermediates (DECSTR, DECSCUSR, DECRQM, ...): not modelled
	}
	if p.private != 0 {
		if p.private == '?' && (final == 'h' || final == 'l') {
			t.privateModes(final == 'h')
		}
		return
	}
	switch final {
	case 'A':
		t.moveTo(t.x, t.y-t.count())
	case 'B':
		t.moveTo(t.x, t.y+t.count())
	case 'C':
		t.moveTo(t.x+t.count(), t.y)
	case 'D':
		t.moveTo(t.x-t.count(), t.y)
	case 'E':
		t.moveTo(0, t.y+t.count())
	case 'F':
		t.moveTo(0, t.y-t.count())
	case 'G':
		t.moveTo(t.arg(0, 1)-1, t.y)
	case 'H', 'f':
		t.moveTo(t.arg(1, 1)-1, t.arg(0, 1)-1)
	case 'd':
		t.moveTo(t.x, t.arg(0, 1)-1)
	case 'J':
		t.eraseDisplay(t.arg(0, 0))
	case 'K':
		t.eraseLine(t.arg(0, 0))
	case 'L':
		t.insertLines(t.count())
	case 'M':
		t.deleteLines(t.count())
	case 'S':
		t.scrollUp(t.count())
	case 'T':
		t.scrollDown(t.count())
	case 'X':
		t.clearRange(t.y, t.x, t.x+t.count())
	case 'm':
		t.sgr()
	case 'n':
		switch t.arg(0, 0) {
		case 5:
			t.reply = append(t.reply, "\x1b[0n"...)
		case 6:
			t.reply = append(t.reply, "\x1b["+strconv.Itoa(t.y+1)+";"+strconv.Itoa(t.x+1)+"R"...)
		}
	case 's':
		if len(p.params) == 0 { // with parameters it is DECSLRM, which is not modelled
			t.saveCursor()
		}
	case 'u':
		if len(p.params) == 0 && t.saved.ok {
			t.restoreCursor()
		}
	}
}

// privateModes sets or resets each mode named in a CSI ? ... h/l.
func (t *Term) privateModes(set bool) {
	for _, pr := range t.p.params {
		switch pr.v {
		case 7:
			t.autoWrap = set
			if !set {
				t.pending = false
			}
		case 25:
			t.curVis = set
		case 47, 1047:
			t.setAlt(set, false)
		case 1049:
			t.setAlt(set, true)
		case 2004:
			t.paste = set
		case 2026:
			if set {
				t.syncDepth++
				t.syncBegins++
			} else {
				t.syncDepth = max(t.syncDepth-1, 0)
				t.syncEnds++
			}
		}
	}
}

// sgr applies Select Graphic Rendition: 0, 1, 2, 3, 4 (and 4:n), 7, 9, 22-24, 27, 29, 30-37, 38, 39, 40-47, 48, 49, 90-97 and
// 100-107. Blink, conceal, overline and underline colours are consumed without effect.
func (t *Term) sgr() {
	ps := t.p.params
	if len(ps) == 0 {
		t.sty = cell.Style{}
		return
	}
	for i := 0; i < len(ps); i++ {
		v := max(ps[i].v, 0)
		switch {
		case v == 0:
			t.sty = cell.Style{}
		case v == 1:
			t.sty.Attr |= cell.Bold
		case v == 2:
			t.sty.Attr |= cell.Dim
		case v == 3:
			t.sty.Attr |= cell.Italic
		case v == 4:
			on := true
			if i+1 < len(ps) && ps[i+1].sub { // 4:0 is off, 4:1 to 4:5 are kinds of underline
				on = ps[i+1].v != 0
				for i+1 < len(ps) && ps[i+1].sub {
					i++
				}
			}
			if on {
				t.sty.Attr |= cell.Underline
			} else {
				t.sty.Attr &^= cell.Underline
			}
		case v == 7:
			t.sty.Attr |= cell.Reverse
		case v == 9:
			t.sty.Attr |= cell.Strike
		case v == 22:
			t.sty.Attr &^= cell.Bold | cell.Dim
		case v == 23:
			t.sty.Attr &^= cell.Italic
		case v == 24:
			t.sty.Attr &^= cell.Underline
		case v == 27:
			t.sty.Attr &^= cell.Reverse
		case v == 29:
			t.sty.Attr &^= cell.Strike
		case v >= 30 && v <= 37:
			t.sty.FG = cell.ANSI(v - 30)
		case v == 39:
			t.sty.FG = cell.Default()
		case v >= 40 && v <= 47:
			t.sty.BG = cell.ANSI(v - 40)
		case v == 49:
			t.sty.BG = cell.Default()
		case v >= 90 && v <= 97:
			t.sty.FG = cell.ANSI(v - 90 + 8)
		case v >= 100 && v <= 107:
			t.sty.BG = cell.ANSI(v - 100 + 8)
		case v == 38 || v == 48 || v == 58:
			var c cell.Color
			var ok bool
			c, ok, i = extendedColor(ps, i)
			switch {
			case !ok:
			case v == 38:
				t.sty.FG = c
			case v == 48:
				t.sty.BG = c
			}
		}
	}
}

// extendedColor reads the colour that follows a 38, 48 or 58 at ps[i]: 5;n or 2;r;g;b, or their colon forms 5:n, 2:r:g:b and
// 2::r:g:b (with a colour space id). It returns the colour, whether it was well formed, and the index of the last parameter
// it used.
func extendedColor(ps []param, i int) (c cell.Color, ok bool, last int) {
	byte8 := func(v int) int { return min(max(v, 0), 255) }
	if i+1 < len(ps) && ps[i+1].sub { // colon form: the whole run of sub-parameters belongs to this colour
		j := i + 1
		for j+1 < len(ps) && ps[j+1].sub {
			j++
		}
		run := ps[i+1 : j+1]
		switch {
		case run[0].v == 5 && len(run) >= 2:
			return cell.Indexed(byte8(run[1].v)), true, j
		case run[0].v == 2 && len(run) >= 4: // the last three are r, g, b; a fourth before them is the colour space id
			r, g, b := run[len(run)-3], run[len(run)-2], run[len(run)-1]
			return cell.RGB(uint8(byte8(r.v)), uint8(byte8(g.v)), uint8(byte8(b.v))), true, j
		}
		return cell.Color{}, false, j
	}
	if i+1 >= len(ps) {
		return cell.Color{}, false, i
	}
	switch ps[i+1].v {
	case 5:
		if i+2 < len(ps) {
			return cell.Indexed(byte8(ps[i+2].v)), true, i + 2
		}
	case 2:
		if i+4 < len(ps) {
			return cell.RGB(uint8(byte8(ps[i+2].v)), uint8(byte8(ps[i+3].v)), uint8(byte8(ps[i+4].v))), true, i + 4
		}
	}
	return cell.Color{}, false, len(ps) - 1 // not enough parameters: the rest of the sequence is consumed
}
