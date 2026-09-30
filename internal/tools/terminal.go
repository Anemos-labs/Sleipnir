package tools

import (
	"strings"
	"unicode/utf8"
)

// Tool output is attacker-influenced text: a file the model reads, a page it
// fetches, a name in a directory listing, a command's stderr. The model receives
// it as data, but a UI that prints it to a terminal hands every byte of it to
// the terminal, which acts on some of them instead of showing them: cursor
// movement and screen clears overwrite what the reviewer thinks they saw, OSC 52
// writes the clipboard, OSC 8 forges hyperlinks, title sequences and DCS/APC
// strings reach the terminal's own features, and a bare carriage return or
// backspace redraws a line ("rm -rf /\rharmless"). The shell tool cleans command
// output as it reads it; file contents, web text and listings reach the model
// verbatim (an edit must match the file's real bytes), so whatever displays them
// cleans them for the terminal with SanitizeForTerminal, at the point of display.

// SanitizeForTerminal returns s with everything removed that a terminal would act
// on instead of display:
//
//   - escape sequences: CSI (colours, cursor movement, erase), OSC (titles, clipboard,
//     hyperlinks), DCS, APC, PM and SOS strings, charset selection and the two-byte
//     forms, and a stray ESC. A sequence that never ends (within 64 bytes for CSI, 4096
//     for strings) loses only its introducer, so a bare ESC cannot swallow the
//     rest of the text;
//   - the C1 controls (U+0080-U+009F, single-character CSI/OSC/DCS), NUL, BEL,
//     backspace, vertical tab, form feed, shift-in/out and the other C0 controls, DEL;
//   - invisible and reordering characters (bidi overrides and isolates, zero-width
//     spaces, word joiners, tag characters: see Invisible), which show nothing, or
//     show text in a different order than it is stored, in a transcript;
//   - invalid UTF-8, which becomes U+FFFD.
//
// Newline and tab stay. CRLF becomes LF, and so do a lone CR and the Unicode line and
// paragraph separators: a terminal would draw the text after a CR over the line so
// far. Ordinary text, including every printable Unicode character, is returned
// unchanged (and without copying). The function is idempotent.
func SanitizeForTerminal(s string) string {
	if !needsTerminalCleaning(s) {
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
			case r < 0xA0 || Invisible(r):
			default:
				sb.WriteString(s[i : i+n])
			}
			i += n
		}
	}
	return sb.String()
}

// needsTerminalCleaning is the fast path: does s hold anything SanitizeForTerminal
// would change?
func needsTerminalCleaning(s string) bool {
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
			if (r == utf8.RuneError && n <= 1) || r < 0xA0 || r == 0x2028 || r == 0x2029 || Invisible(r) {
				return true
			}
			i += n
		}
	}
	return false
}

const (
	maxCSIBytes    = 64
	maxStringBytes = 4096
)

// escapeLen returns how many bytes of s (which starts with ESC) belong to the
// escape sequence it begins; at least 1.
func escapeLen(s string) int {
	if len(s) < 2 {
		return 1
	}
	switch b := s[1]; {
	case b == '[': // CSI: parameter bytes 0x30-0x3F, intermediates 0x20-0x2F, final 0x40-0x7E
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

// Invisible reports runes that display as nothing (or only reorder text) and so
// can smuggle content past a human reader while reaching a model intact: soft
// hyphen, Mongolian vowel separator, zero-width space, word joiner and the
// invisible operators, byte order mark, bidi embeddings, overrides and isolates,
// Unicode tag characters and the variation selectors supplement. Zero-width joiner
// and non-joiner and the left/right marks are deliberately not listed: emoji
// sequences and several scripts need them.
func Invisible(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x180E, r == 0x200B, r == 0x2060, r == 0xFEFF:
	case r >= 0x2061 && r <= 0x2064: // invisible operators
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
	case r >= 0xE0000 && r <= 0xE007F: // tag characters
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
	default:
		return false
	}
	return true
}
