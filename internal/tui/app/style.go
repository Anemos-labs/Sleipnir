package app

import (
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// styles are the looks the views of this package draw their own text with, made from the palette the widgets use, so that a
// screen that mixes the two is of one piece. Where the palette has no colour (a monochrome terminal) attributes stand in for it.
type styles struct {
	dim, faint, good, bad, warn, info, accent, bold cell.Style
}

func stylesOf(p widget.Palette) styles {
	fg := func(c cell.Color, fallback cell.Attr) cell.Style {
		if c == (cell.Color{}) {
			return cell.Style{Attr: fallback}
		}
		return cell.Style{FG: c}
	}
	return styles{
		dim:    fg(p.Dim, cell.Dim),
		faint:  fg(p.Faint, cell.Dim),
		good:   cell.Style{FG: p.Good},
		bad:    cell.Style{FG: p.Bad, Attr: cell.Bold},
		warn:   cell.Style{FG: p.Warn},
		info:   cell.Style{FG: p.Info},
		accent: cell.Style{FG: p.Accent, Attr: cell.Bold},
		bold:   cell.Style{Attr: cell.Bold},
	}
}

// row builds one line of styled text and knows its width.
type row struct {
	l cell.Line
	w int
}

func (r *row) add(st cell.Style, s string) *row {
	if s == "" {
		return r
	}
	r.l = append(r.l, cell.Span{Text: s, Style: st})
	r.w += cell.StringWidth(s)
	return r
}

func (r *row) addLine(l cell.Line) *row {
	for _, sp := range l {
		r.add(sp.Style, sp.Text)
	}
	return r
}

func (r *row) space(n int) *row {
	if n > 0 {
		r.add(cell.Style{}, strings.Repeat(" ", n))
	}
	return r
}

func (r *row) line() cell.Line { return r.l }

// fit cuts a line to w cells, with an ellipsis when something was cut.
func fit(l cell.Line, w int) cell.Line {
	if w <= 0 {
		return nil
	}
	if l.Width() <= w {
		return l
	}
	var out cell.Line
	used := 0
	for _, sp := range l {
		sw := cell.StringWidth(sp.Text)
		if used+sw <= w-1 {
			out = append(out, sp)
			used += sw
			continue
		}
		var b strings.Builder
		for _, r := range sp.Text {
			rw := cell.RuneWidth(r)
			if used+rw > w-1 {
				break
			}
			b.WriteRune(r)
			used += rw
		}
		if b.Len() > 0 {
			out = append(out, cell.Span{Text: b.String(), Style: sp.Style})
		}
		break
	}
	return append(out, cell.Span{Text: "…", Style: lastStyle(out)})
}

func lastStyle(l cell.Line) cell.Style {
	if len(l) == 0 {
		return cell.Style{}
	}
	return l[len(l)-1].Style
}

// clean makes text that came from the session one line without control characters, so that whatever it says, printing it cannot
// do more than print it. The state package already does this for what it keeps; the views do it again for what they put together.
func clean(s string) string {
	var b strings.Builder
	for _, r := range tools.SanitizeForTerminal(s) { // escape sequences whole, controls, invisible and reordering characters, bad UTF-8
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// padRight pads a line with spaces up to w cells (and cuts one that is wider).
func padRight(l cell.Line, w int) cell.Line {
	l = fit(l, w)
	return l.Pad(w, cell.Style{})
}

// alignRight puts right at the right edge of a line w cells wide, after left; when they do not both fit the left one is cut.
func alignRight(left, right cell.Line, w int) cell.Line {
	rw := right.Width()
	if rw >= w {
		return fit(right, w)
	}
	gap := 2
	l := fit(left, w-rw-gap)
	var r row
	r.addLine(l).space(w - l.Width() - rw).addLine(right)
	return r.line()
}

// paragraph lays text out in lines of at most w cells, breaking between words (a word longer than a line is cut).
func paragraph(st cell.Style, text string, w int) []cell.Line {
	if w <= 0 {
		return nil
	}
	var out []cell.Line
	line := ""
	flush := func() {
		if line != "" {
			out = append(out, cell.Styled(st, line))
			line = ""
		}
	}
	for _, word := range strings.Fields(clean(text)) {
		for cell.StringWidth(word) > w { // a word that does not fit a line is cut at the line's width
			flush()
			used, n := 0, 0
			for n < len(word) {
				r, size := utf8.DecodeRuneInString(word[n:])
				if used+cell.RuneWidth(r) > w {
					break
				}
				used += cell.RuneWidth(r)
				n += size
			}
			if n == 0 { // a character wider than the line: nothing can show it
				_, size := utf8.DecodeRuneInString(word)
				word = word[size:]
				continue
			}
			out = append(out, cell.Styled(st, word[:n]))
			word = word[n:]
		}
		if word == "" {
			continue
		}
		switch {
		case line == "":
			line = word
		case cell.StringWidth(line)+1+cell.StringWidth(word) <= w:
			line += " " + word
		default:
			flush()
			line = word
		}
	}
	flush()
	return out
}
