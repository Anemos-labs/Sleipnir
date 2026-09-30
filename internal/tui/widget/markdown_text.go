package widget

import (
	"strings"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// Text hygiene and cell arithmetic shared by every widget (this file is named for the area that needs it most; the diff, the box,
// the dialog and the table use it too). Text that reaches a widget is data: a file the model read, a web page, a command line
// it wants to run. The terminal acts on some bytes instead of showing them, so they are removed here, before layout, and no
// widget ever has to ask whether a span is safe to hand to the renderer.

const (
	maxCSIBytes    = 64   // a CSI longer than this is not a sequence: only its introducer is dropped
	maxStringBytes = 4096 // likewise for OSC, DCS, SOS, PM and APC strings that never end
)

// safeText returns s without anything a terminal would act on instead of displaying: escape sequences (CSI, OSC including
// OSC 52 and OSC 8, DCS, APC, PM, SOS, charset selection, two-byte forms, a stray ESC), the C0 and C1 controls, DEL, runes that
// show nothing or reorder text (bidi overrides, zero-width spaces, tag characters), and invalid UTF-8 (which becomes U+FFFD).
// A sequence that never ends loses its introducer only, so a stray ESC cannot swallow the text after it, and no sequence runs
// past the end of its line: a newline ends the attempt. (That is the one place where this differs from
// tools.SanitizeForTerminal, which lets an OSC string run on over newlines up to its terminator. It makes the cleaning of a
// line independent of the lines after it, which the markdown stream relies on: text that is still arriving can never change
// lines that came before it.) CRLF, a lone CR and the Unicode line and paragraph separators become "\n". Newline and tab
// survive; callers decide what they mean. The function is idempotent, and text that went through
// tools.SanitizeForTerminal first is returned unchanged (this package may not import it; markdown_sanitize_test.go keeps the
// two in step).
func safeText(s string) string {
	if !needsSafeText(s) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i += escapeLen(s[i:])
		case c == '\r':
			i++
			if i < len(s) && s[i] == '\n' {
				continue // CRLF: the LF ends the line
			}
			sb.WriteByte('\n')
		case c == '\n' || c == '\t':
			sb.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < utf8.RuneSelf:
			sb.WriteByte(c)
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			switch {
			case r == utf8.RuneError && n <= 1:
				sb.WriteRune(utf8.RuneError)
			case r == 0x2028 || r == 0x2029:
				sb.WriteByte('\n')
			case r < 0xA0 || invisibleRune(r):
			default:
				sb.WriteString(s[i : i+n])
			}
			i += n
		}
	}
	return sb.String()
}

func needsSafeText(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n' || c == '\t':
			i++
		case c < 0x20 || c == 0x7f:
			return true
		case c < utf8.RuneSelf:
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			if (r == utf8.RuneError && n <= 1) || r < 0xA0 || r == 0x2028 || r == 0x2029 || invisibleRune(r) {
				return true
			}
			i += n
		}
	}
	return false
}

// escapeLen is how many bytes of s (which starts with ESC) belong to the escape sequence it begins; at least 1.
func escapeLen(s string) int {
	if len(s) < 2 {
		return 1
	}
	switch b := s[1]; {
	case b == '[': // CSI: parameter bytes 0x30-0x3F, intermediates 0x20-0x2F, final byte 0x40-0x7E
		for i := 2; i < len(s) && i < maxCSIBytes; i++ {
			switch c := s[i]; {
			case c >= 0x40 && c <= 0x7e:
				return i + 1
			case c >= 0x20 && c <= 0x3f:
			default:
				return 2 // malformed: drop the introducer, keep what follows as text
			}
		}
		return 2
	case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_': // strings, ended by BEL, ST (ESC \) or C1 ST
		for i := 2; i < len(s) && i < maxStringBytes; i++ {
			switch {
			case s[i] == 0x07:
				return i + 1
			case s[i] == '\n':
				return 2 // not terminated on its line: the introducer goes, the rest is text
			case s[i] == 0x1b:
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i // another ESC ends the string; it starts a sequence of its own
			case s[i] == 0xc2 && i+1 < len(s) && s[i+1] == 0x9c: // U+009C as UTF-8
				return i + 2
			}
		}
		return 2
	case b >= 0x20 && b <= 0x2f: // ESC ( B and friends: intermediates, then a final byte
		for i := 2; i < len(s) && i < 16; i++ {
			switch c := s[i]; {
			case c >= 0x30 && c <= 0x7e:
				return i + 1
			case c >= 0x20 && c <= 0x2f:
			default:
				return 2
			}
		}
		return 2
	case b >= 0x30 && b <= 0x7e: // ESC c, ESC 7, ESC M, ...
		return 2
	}
	return 1
}

// invisibleRune reports runes that draw nothing, or only reorder text, and so could make a command look like another one to
// the person approving it. Zero-width joiner and non-joiner and the left/right marks stay: emoji sequences and several scripts
// need them.
func invisibleRune(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x180E, r == 0x200B, r == 0x2060, r == 0xFEFF:
	case r >= 0x2061 && r <= 0x2064:
	case r >= 0x202A && r <= 0x202E:
	case r >= 0x2066 && r <= 0x2069:
	case r >= 0xE0000 && r <= 0xE007F:
	case r >= 0xE0100 && r <= 0xE01EF:
	default:
		return false
	}
	return true
}

// safeOneLine cleans s and turns every newline and tab into one space: text for a single row (a title, a label, a path).
func safeOneLine(s string) string {
	s = safeText(s)
	if strings.ContainsAny(s, "\n\t") {
		s = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' {
				return ' '
			}
			return r
		}, s)
	}
	return s
}

// expandTabStops replaces each tab of s by the spaces that reach the next multiple of tw columns (columns are display cells).
func expandTabStops(s string, tw int) string {
	if tw < 1 {
		tw = 1
	}
	if !strings.Contains(s, "\t") {
		return s
	}
	var sb strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tw - col%tw
			sb.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		sb.WriteRune(r)
		col += cell.RuneWidth(r)
	}
	return sb.String()
}

// safeLine returns l with every span made safe: control characters removed, newlines turned into spaces and tabs expanded
// (to 4-column stops counted from the start of the line). A line that is already clean is returned as it is. The widgets
// that take caller-built lines (Box, Dialog, Table) pass them through here: a Span is documented to hold no control
// characters, and a widget does not trust that.
func safeLine(l cell.Line) cell.Line {
	dirty := false
	for _, sp := range l {
		if needsSafeText(sp.Text) || strings.ContainsAny(sp.Text, "\n\t") {
			dirty = true
			break
		}
	}
	if !dirty {
		return l
	}
	out := make(cell.Line, 0, len(l))
	col := 0
	for _, sp := range l {
		t := safeText(sp.Text)
		if strings.ContainsAny(t, "\n\t") {
			var sb strings.Builder
			for _, r := range t {
				switch r {
				case '\t':
					n := 4 - col%4
					sb.WriteString(strings.Repeat(" ", n))
					col += n
				case '\n':
					sb.WriteByte(' ')
					col++
				default:
					sb.WriteRune(r)
					col += cell.RuneWidth(r)
				}
			}
			t = sb.String()
		} else {
			col += cell.StringWidth(t)
		}
		if t != "" {
			out = append(out, cell.Span{Text: t, Style: sp.Style})
		}
	}
	return out
}

// clipLine returns l cut to at most w cells without an ellipsis (a wide rune is never split); w < 1 gives nil. Every widget ends
// with it, which is what makes "no line is wider than the width" hold whatever the layout arithmetic did.
func clipLine(l cell.Line, w int) cell.Line {
	if w < 1 {
		return nil
	}
	if l.Width() <= w {
		return l
	}
	return l.Truncate(w, "")
}

// clipLines clips every line and returns the slice it was given.
func clipLines(ls []cell.Line, w int) []cell.Line {
	for i, l := range ls {
		ls[i] = clipLine(l, w)
	}
	return ls
}

// cutRows breaks l into rows of at most w cells exactly at the cell boundary, keeping every space (code and commands must not
// be reflowed): a wide rune is not split, a combining mark stays with its base, and a rune wider than a row is taken whole so
// that the loop always ends (clipLine then cuts what cannot fit). Rows after the first start with marker (when it is not empty) and
// hold w minus the marker's width. An empty line is one empty row. One pass over the text: linear however many rows come out.
func cutRows(l cell.Line, w int, marker cell.Span) []cell.Line {
	if w < 1 {
		w = 1
	}
	mw := cell.StringWidth(marker.Text)
	if mw >= w {
		marker, mw = cell.Span{}, 0
	}
	var out []cell.Line
	var row cell.Line
	room, used := w, 0 // cells a row holds, cells this one has
	finish := func() {
		if len(out) > 0 && mw > 0 {
			row = append(cell.Line{marker}, row...)
		}
		out = append(out, row)
		row, used, room = nil, 0, w-mw
	}
	for _, sp := range l {
		t, from := sp.Text, 0
		for i := 0; i < len(t); {
			r, n := utf8.DecodeRuneInString(t[i:])
			rw := cell.RuneWidth(r)
			if r == utf8.RuneError && n == 1 {
				rw = 1
			}
			if rw > 0 && used > 0 && used+rw > room {
				if i > from {
					row = append(row, cell.Span{Text: t[from:i], Style: sp.Style})
				}
				from = i
				finish()
			}
			used += rw
			i += n
		}
		if from < len(t) {
			row = append(row, cell.Span{Text: t[from:], Style: sp.Style})
		}
	}
	finish()
	return out
}

// ellipsizeLeft shortens s to at most w cells by cutting its beginning and putting ell there: the end of a path is the part that
// tells files apart.
func ellipsizeLeft(s string, w int, ell string) string {
	if cell.StringWidth(s) <= w {
		return s
	}
	ew := cell.StringWidth(ell)
	if w <= ew {
		ell, ew = "", 0
	}
	if w < 1 {
		return ""
	}
	rs := []rune(s)
	used, i := 0, len(rs)
	for i > 0 {
		rw := cell.RuneWidth(rs[i-1])
		if used+rw > w-ew {
			break
		}
		used += rw
		i--
	}
	for i < len(rs) && cell.RuneWidth(rs[i]) == 0 { // a mark whose base was cut off draws nothing
		i++
	}
	return ell + string(rs[i:])
}

// repeatText is strings.Repeat that tolerates a negative count.
func repeatText(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}
