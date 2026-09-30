// Package cell is the vocabulary of the terminal UI: colours, styles, styled text and what it takes to lay text out in a
// terminal, which is measured in cells, not in bytes or runes. It holds no escape codes; turning a Line into bytes is the
// renderer's job (internal/tui/render), which knows what the terminal supports.
package cell

// Kind says how a Color is specified.
type Kind uint8

const (
	KindDefault Kind = iota // the terminal's own colour
	KindANSI                // one of the 16 standard colours (0-7 normal, 8-15 bright)
	KindIndexed             // the 256-colour palette
	KindRGB                 // 24-bit colour
)

// Color is a foreground or background colour. The zero value is the terminal's default.
type Color struct {
	Kind    Kind
	R, G, B uint8 // KindRGB
	N       uint8 // KindANSI, KindIndexed
}

// Default is the terminal's own colour.
func Default() Color { return Color{} }

// ANSI is one of the 16 standard colours.
func ANSI(n int) Color { return Color{Kind: KindANSI, N: uint8(n & 15)} }

// Indexed is a colour of the 256-colour palette.
func Indexed(n int) Color { return Color{Kind: KindIndexed, N: uint8(n)} }

// RGB is a 24-bit colour; a terminal without truecolor gets the nearest palette colour.
func RGB(r, g, b uint8) Color { return Color{Kind: KindRGB, R: r, G: g, B: b} }

// Hex parses "#rrggbb" (or "rrggbb"); anything else is the default colour.
func Hex(s string) Color {
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	if len(s) != 6 {
		return Color{}
	}
	var v [3]uint8
	for i := 0; i < 3; i++ {
		hi, ok1 := hexVal(s[2*i])
		lo, ok2 := hexVal(s[2*i+1])
		if !ok1 || !ok2 {
			return Color{}
		}
		v[i] = hi<<4 | lo
	}
	return RGB(v[0], v[1], v[2])
}

func hexVal(c byte) (uint8, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// Attr is a set of text attributes.
type Attr uint8

const (
	Bold Attr = 1 << iota
	Dim
	Italic
	Underline
	Reverse
	Strike
)

// Style is how a run of text looks.
type Style struct {
	FG, BG Color
	Attr   Attr
}

// Has reports whether every attribute in a is set.
func (s Style) Has(a Attr) bool { return s.Attr&a == a }

// Fg returns the style with a foreground colour.
func (s Style) Fg(c Color) Style { s.FG = c; return s }

// Bg returns the style with a background colour.
func (s Style) Bg(c Color) Style { s.BG = c; return s }

// With returns the style with the attributes added.
func (s Style) With(a Attr) Style { s.Attr |= a; return s }

// Without returns the style with the attributes removed.
func (s Style) Without(a Attr) Style { s.Attr &^= a; return s }
