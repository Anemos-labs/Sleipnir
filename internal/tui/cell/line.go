package cell

import (
	"strings"
	"unicode/utf8"
)

// Span is text in one style. It holds no control characters: a line break is the boundary between two Lines.
type Span struct {
	Text  string
	Style Style
}

// Line is a sequence of styled spans laid out on one row (before wrapping).
type Line []Span

// Text is an unstyled line.
func Text(s string) Line { return Line{{Text: s}} }

// Styled is a line of one style.
func Styled(st Style, s string) Line { return Line{{Text: s, Style: st}} }

// Width is the number of cells the line takes.
func (l Line) Width() int {
	w := 0
	for _, sp := range l {
		w += StringWidth(sp.Text)
	}
	return w
}

// Plain is the text of the line without its styles.
func (l Line) Plain() string {
	var b strings.Builder
	for _, sp := range l {
		b.WriteString(sp.Text)
	}
	return b.String()
}

// Append returns the line with spans added; empty spans are dropped.
func (l Line) Append(spans ...Span) Line {
	out := append(Line(nil), l...)
	for _, sp := range spans {
		if sp.Text != "" {
			out = append(out, sp)
		}
	}
	return out
}

// Join concatenates lines into one.
func Join(ls ...Line) Line {
	var out Line
	for _, l := range ls {
		out = append(out, l...)
	}
	return out
}

// Spaces is n cells of space in a style.
func Spaces(n int, st Style) Line {
	if n <= 0 {
		return nil
	}
	return Line{{Text: strings.Repeat(" ", n), Style: st}}
}

// Pad extends the line with spaces in style st to w cells; a line that is already that wide comes back unchanged.
func (l Line) Pad(w int, st Style) Line {
	if n := w - l.Width(); n > 0 {
		return append(append(Line(nil), l...), Span{Text: strings.Repeat(" ", n), Style: st})
	}
	return l
}

// glyph is one rune with its width and style, the unit Truncate and Wrap work in. A combining mark travels with its base:
// it is folded into the previous glyph's text.
type glyph struct {
	text  string
	w     int
	style Style
}

func glyphs(l Line) []glyph {
	var out []glyph
	for _, sp := range l {
		for i := 0; i < len(sp.Text); {
			r, n := utf8.DecodeRuneInString(sp.Text[i:])
			t := sp.Text[i : i+n]
			i += n
			w := RuneWidth(r)
			if r == utf8.RuneError && n == 1 {
				t, w = "�", 1
			}
			if w == 0 && len(out) > 0 && out[len(out)-1].style == sp.Style {
				out[len(out)-1].text += t // a mark belongs to the rune before it
				continue
			}
			if w == 0 {
				continue // a mark with no base draws nothing
			}
			out = append(out, glyph{text: t, w: w, style: sp.Style})
		}
	}
	return out
}

func fromGlyphs(gs []glyph) Line {
	var out Line
	for _, g := range gs {
		if n := len(out); n > 0 && out[n-1].Style == g.style {
			out[n-1].Text += g.text
		} else {
			out = append(out, Span{Text: g.text, Style: g.style})
		}
	}
	return out
}

// Truncate cuts the line to at most w cells. When it had to cut and ellipsis is not empty, the ellipsis replaces the last
// cells so the reader can tell; a wide rune is never split.
func (l Line) Truncate(w int, ellipsis string) Line {
	if l.Width() <= w {
		return l
	}
	if w <= 0 {
		return nil
	}
	ew := StringWidth(ellipsis)
	if ew >= w {
		ellipsis, ew = "", 0
	}
	var keep []glyph
	used := 0
	var st Style
	for _, g := range glyphs(l) {
		if used+g.w > w-ew {
			break
		}
		keep = append(keep, g)
		used += g.w
		st = g.style
	}
	out := fromGlyphs(keep)
	if ellipsis != "" {
		out = append(out, Span{Text: ellipsis, Style: st})
	}
	return out
}

// Wrap breaks the line into lines of at most w cells: at spaces when it can, in the middle of a word when a word is wider than
// a line. Spaces at a break are dropped; continuation lines are indented by indent cells. Styles survive the breaks. An empty
// line wraps to one empty line. A wide rune on a line narrower than itself is placed alone, overflowing rather than lost.
func (l Line) Wrap(w, indent int) []Line {
	if w < 1 {
		w = 1
	}
	if indent < 0 || indent >= w {
		indent = 0
	}
	gs := glyphs(l)
	if len(gs) == 0 {
		return []Line{nil}
	}
	var out []Line
	var cur []glyph
	curW := 0
	first := true
	flush := func() {
		// trailing spaces are invisible and only make the line look wider
		for len(cur) > 0 && cur[len(cur)-1].text == " " {
			cur = cur[:len(cur)-1]
		}
		ln := fromGlyphs(cur)
		if !first && indent > 0 {
			ln = append(Line{{Text: strings.Repeat(" ", indent)}}, ln...)
		}
		out = append(out, ln)
		cur, curW = nil, 0
		first = false
	}
	limit := func() int {
		if first {
			return w
		}
		return w - indent
	}
	i := 0
	for i < len(gs) {
		// a word is a run of non-space glyphs; spaces are separators
		if gs[i].text == " " {
			if len(cur) > 0 && curW+1 <= limit() {
				cur = append(cur, gs[i])
				curW++
			}
			i++
			continue
		}
		j := i
		ww := 0
		for j < len(gs) && gs[j].text != " " {
			ww += gs[j].w
			j++
		}
		word := gs[i:j]
		if curW+ww > limit() && len(cur) > 0 {
			flush()
		}
		if ww <= limit() {
			cur = append(cur, word...)
			curW += ww
		} else { // longer than a line: break it where the line ends
			for _, g := range word {
				if curW+g.w > limit() && len(cur) > 0 {
					flush()
				}
				cur = append(cur, g)
				curW += g.w
			}
		}
		i = j
	}
	if len(cur) > 0 || len(out) == 0 {
		flush()
	}
	return out
}
