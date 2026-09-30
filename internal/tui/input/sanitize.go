package input

import (
	"strings"
	"unicode/utf8"
)

// Everything that enters the editor, typed, pasted, recalled from history or completed, is untrusted text: a terminal acts on
// escape sequences instead of showing them, so nothing with a control character in it may reach the buffer, and therefore
// nothing drawn from the buffer can reach the terminal as a command. cleanText is that one gate.

const (
	maxEscCSI    = 64   // a CSI sequence longer than this loses only its introducer, so a bare ESC cannot swallow the text after it
	maxEscString = 4096 // the same for OSC, DCS, SOS, PM and APC strings
)

// cleanText returns s as it may enter the buffer:
//   - invalid UTF-8 becomes U+FFFD (one per bad byte);
//   - escape sequences (CSI, OSC, DCS, SOS, PM, APC, charset selection, two-byte forms) are removed whole, not just their
//     first byte, so "ESC[31mred" becomes "red" and not "[31mred";
//   - C0 controls, DEL, the C1 controls and NUL are removed, except newline and tab;
//   - CRLF and a lone CR become one newline, and so do U+2028 and U+2029;
//   - invisible characters that show nothing or reorder text (bidi overrides and isolates, zero-width space, word joiner,
//     byte order mark, Unicode tag characters) are removed: a paste may not carry words the user cannot see. The zero-width
//     joiner and non-joiner stay, emoji and several scripts need them;
//   - the private-use range the editor keeps for paste chips is removed, so text cannot forge a chip.
//
// It is idempotent and returns s itself when there is nothing to change.
func cleanText(s string) string {
	if !needsCleaning(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
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
			b.WriteByte('\n')
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			switch {
			case r == utf8.RuneError && n <= 1:
				b.WriteRune(utf8.RuneError)
			case r == 0x2028 || r == 0x2029:
				b.WriteByte('\n')
			case r < 0xa0 || invisible(r) || isChip(r):
			default:
				b.WriteString(s[i : i+n])
			}
			i += n
		}
	}
	return b.String()
}

func needsCleaning(s string) bool {
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
			if (r == utf8.RuneError && n <= 1) || r < 0xa0 || r == 0x2028 || r == 0x2029 || invisible(r) || isChip(r) {
				return true
			}
			i += n
		}
	}
	return false
}

// invisible reports runes that display as nothing, or only reorder the text around them. It is the same set as
// tools.Invisible (kept here so that this package depends on nothing else in the repository).
func invisible(r rune) bool {
	switch {
	case r == 0x00ad, r == 0x180e, r == 0x200b, r == 0x2060, r == 0xfeff:
	case r >= 0x2061 && r <= 0x2064:
	case r >= 0x202a && r <= 0x202e:
	case r >= 0x2066 && r <= 0x2069:
	case r >= 0xe0000 && r <= 0xe007f:
	case r >= 0xe0100 && r <= 0xe01ef:
	default:
		return false
	}
	return true
}

// escapeLen is how many bytes of s, which starts with ESC, belong to the escape sequence it begins (at least 1).
func escapeLen(s string) int {
	if len(s) < 2 {
		return 1
	}
	switch b := s[1]; {
	case b == '[': // CSI
		for i := 2; i < len(s) && i < maxEscCSI; i++ {
			switch c := s[i]; {
			case c >= 0x40 && c <= 0x7e:
				return i + 1
			case c >= 0x20 && c <= 0x3f:
			default:
				return 2 // damaged: drop the introducer, keep what follows as text
			}
		}
		return 2
	case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_': // strings, ended by BEL or ST
		for i := 2; i < len(s) && i < maxEscString; i++ {
			switch {
			case s[i] == 0x07:
				return i + 1
			case s[i] == 0x1b:
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i // another ESC ends the string and starts a sequence of its own
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
	case b >= 0x30 && b <= 0x7e: // ESC c, ESC 7, ESC M
		return 2
	}
	return 1
}
