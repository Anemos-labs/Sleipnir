package render

import (
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// What is removed from text, and why, is the package documentation (doc.go). sanitizeLine is where it is done.

const (
	tabWidth  = 8
	maxCSI    = 64
	maxString = 4096
	maxInter  = 16
)

// sanitizeLine returns a cleaned copy of l; l itself is not modified. Empty spans are dropped.
func sanitizeLine(l cell.Line) cell.Line {
	out := make(cell.Line, 0, len(l))
	col := 0
	for _, sp := range l {
		text, n := cleanText(sp.Text, col)
		col = n
		if text != "" {
			out = append(out, cell.Span{Text: text, Style: sp.Style})
		}
	}
	return out
}

// cleanText cleans one span's text; col is the display column at its start, and the column at its end is returned.
func cleanText(s string, col int) (string, int) {
	if clean, w := isClean(s); clean && (col > 0 || s == "" || !startsWithMark(s)) {
		return s, col + w
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i += escapeLen(s[i:])
		case c == '\t':
			n := tabWidth - col%tabWidth
			sb.WriteString(strings.Repeat(" ", n))
			col += n
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < utf8.RuneSelf:
			sb.WriteByte(c)
			col++
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			switch {
			case r == utf8.RuneError && n <= 1:
				sb.WriteString("�")
				col++
			case r >= 0x80 && r < 0xa0, r == 0x2028, r == 0x2029:
			case cell.RuneWidth(r) == 0 && col == 0:
			default:
				sb.WriteString(s[i : i+n])
				col += cell.RuneWidth(r)
			}
			i += n
		}
	}
	return sb.String(), col
}

// isClean is the fast path: printable ASCII and well-formed UTF-8 that holds no control, C1, line separator or ESC. It returns
// the display width of s when it is clean.
func isClean(s string) (bool, int) {
	w := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c < 0x20 || c == 0x7f:
			return false, 0
		case c < utf8.RuneSelf:
			w++
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			if (r == utf8.RuneError && n <= 1) || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
				return false, 0
			}
			w += cell.RuneWidth(r)
			i += n
		}
	}
	return true, w
}

func startsWithMark(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return cell.RuneWidth(r) == 0
}

// escapeLen returns how many bytes of s, which starts with ESC, belong to the escape sequence that ESC begins (at least 1).
func escapeLen(s string) int {
	if len(s) < 2 {
		return 1
	}
	switch b := s[1]; {
	case b == '[': // CSI: parameter and intermediate bytes 0x20-0x3f, then a final byte 0x40-0x7e
		for i := 2; i < len(s) && i < maxCSI; i++ {
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
		for i := 2; i < len(s) && i < maxString; i++ {
			switch {
			case s[i] == 0x07:
				return i + 1
			case s[i] == 0x1b:
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i // another ESC ends the string and is handled as the start of a sequence of its own
			case s[i] == 0xc2 && i+1 < len(s) && s[i+1] == 0x9c: // U+009C as UTF-8
				return i + 2
			}
		}
		return 2
	case b >= 0x20 && b <= 0x2f: // ESC ( B and friends: intermediates, then a final byte
		for i := 2; i < len(s) && i < maxInter; i++ {
			switch c := s[i]; {
			case c >= 0x30 && c <= 0x7e:
				return i + 1
			case c >= 0x20 && c <= 0x2f:
			default:
				return 2
			}
		}
		return 2
	case b >= 0x30 && b <= 0x7e: // ESC 7, ESC M, ESC c, ...
		return 2
	}
	return 1
}
