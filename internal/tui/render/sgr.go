package render

import (
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// wireStyle is a style as it will be written: down-sampled colours and the attributes. The zero value is the terminal's
// default rendition.
type wireStyle struct {
	fg, bg wireColor
	attr   cell.Attr
}

func (m *colorMap) wire(s cell.Style) wireStyle {
	return wireStyle{fg: m.resolve(s.FG), bg: m.resolve(s.BG), attr: s.Attr}
}

// attrCodes are the SGR codes that switch an attribute on and off, in the order they are written.
var attrCodes = [...]struct {
	a       cell.Attr
	on, off int
}{
	{cell.Bold, 1, 22}, {cell.Dim, 2, 22}, {cell.Italic, 3, 23}, {cell.Underline, 4, 24}, {cell.Reverse, 7, 27}, {cell.Strike, 9, 29},
}

// colorCodes appends the SGR parameters that set a colour; bg selects the background set.
func colorCodes(dst []int, c wireColor, bg bool) []int {
	base, ext, def := 30, 38, 39
	if bg {
		base, ext, def = 40, 48, 49
	}
	switch c.kind {
	case wireANSI:
		if c.n < 8 {
			return append(dst, base+int(c.n))
		}
		return append(dst, base+60+int(c.n)-8)
	case wireIndexed:
		return append(dst, ext, 5, int(c.n))
	case wireRGB:
		return append(dst, ext, 2, int(c.r), int(c.g), int(c.b))
	}
	return append(dst, def)
}

func csiM(codes []int) string {
	var b strings.Builder
	b.WriteString("\x1b[")
	for i, c := range codes {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(strconv.Itoa(c))
	}
	b.WriteByte('m')
	return b.String()
}

// sgr returns the escape sequence that takes a terminal from rendition from to rendition to, or "" when they are the same. It
// writes only what changed, and when starting over from a reset is shorter than undoing the difference (several attributes
// to clear, for instance) it resets and sets everything it needs. The empty rendition is reached with one reset.
func sgr(from, to wireStyle) string {
	if from == to {
		return ""
	}
	if to == (wireStyle{}) {
		return "\x1b[0m"
	}
	var inc []int
	// attributes that have to go: 22 clears bold and dim together, so one that stays is set again after it
	var dropped cell.Attr
	for _, ac := range attrCodes {
		if from.attr&ac.a != 0 && to.attr&ac.a == 0 {
			dropped |= ac.a
		}
	}
	cleared22 := false
	for _, ac := range attrCodes {
		if dropped&ac.a == 0 {
			continue
		}
		if ac.off == 22 {
			if cleared22 {
				continue
			}
			cleared22 = true
		}
		inc = append(inc, ac.off)
	}
	for _, ac := range attrCodes {
		had := from.attr&ac.a != 0
		if cleared22 && ac.off == 22 { // 22 turned both off
			had = false
		}
		if to.attr&ac.a != 0 && !had {
			inc = append(inc, ac.on)
		}
	}
	if to.fg != from.fg {
		inc = colorCodes(inc, to.fg, false)
	}
	if to.bg != from.bg {
		inc = colorCodes(inc, to.bg, true)
	}
	// the other way: reset, then set everything the target has
	full := []int{0}
	for _, ac := range attrCodes {
		if to.attr&ac.a != 0 {
			full = append(full, ac.on)
		}
	}
	if to.fg.kind != wireDefault {
		full = colorCodes(full, to.fg, false)
	}
	if to.bg.kind != wireDefault {
		full = colorCodes(full, to.bg, true)
	}
	a, b := csiM(inc), csiM(full)
	if len(b) < len(a) {
		return b
	}
	return a
}

// invisibleSpace reports whether a space in the style shows nothing: no background, and no attribute that marks the cell itself
// (underline, reverse, strike-through). Colour, bold, dim and italic do not show on a space.
func invisibleSpace(s wireStyle) bool {
	return s.bg.kind == wireDefault && s.attr&(cell.Underline|cell.Reverse|cell.Strike) == 0
}

// span is text in one wire style: the unit an encoded row is made of.
type span struct {
	text  string
	style wireStyle
}

// encode turns a line into the bytes that draw it: text, with the SGR sequences between spans that change the rendition, and a
// reset at the end if the line leaves one set, so that every row starts from the default and the next row never inherits
// anything. It also returns the display width of what is written. Spaces at the end of the line that show nothing (see
// invisibleSpace) are left out, because the rest of the row is erased anyway; that is also why two lines that differ only there
// encode alike, and why a row never wraps because of a space nobody can see. Text must already be sanitized.
func (m *colorMap) encode(l cell.Line) (string, int) {
	var spans []span
	for _, sp := range l {
		if sp.Text == "" {
			continue
		}
		ws := m.wire(sp.Style)
		if n := len(spans); n > 0 && spans[n-1].style == ws {
			spans[n-1].text += sp.Text
		} else {
			spans = append(spans, span{text: sp.Text, style: ws})
		}
	}
	for n := len(spans); n > 0 && invisibleSpace(spans[n-1].style); n-- {
		spans[n-1].text = strings.TrimRight(spans[n-1].text, " ")
		if spans[n-1].text != "" {
			break
		}
		spans = spans[:n-1]
	}
	var b strings.Builder
	var cur wireStyle
	w := 0
	for _, sp := range spans {
		b.WriteString(sgr(cur, sp.style))
		cur = sp.style
		b.WriteString(sp.text)
		w += cell.StringWidth(sp.text)
	}
	if cur != (wireStyle{}) {
		b.WriteString("\x1b[0m")
	}
	return b.String(), w
}
