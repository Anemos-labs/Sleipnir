package shell

import (
	"strings"
	"unicode/utf8"
)

// Command output is arbitrary bytes, but everything downstream (provider JSON,
// the event log, a terminal UI) needs clean UTF-8, and every byte that reaches
// the model costs tokens. The sanitizer therefore runs on the raw stream as it
// is read, once, and everything after it (ring buffers, live streaming, the
// blob store) only ever sees the cleaned text:
//
//   - invalid UTF-8 becomes U+FFFD, and a multi-byte character split across two
//     reads is carried over instead of being mangled;
//   - ANSI escape sequences (colours, cursor movement, OSC titles) are removed
//     even when a read boundary falls in the middle of one;
//   - NUL and other unprintable control bytes are dropped (and counted, so a
//     stream that is mostly control bytes can be recognised as binary);
//   - invisible format characters (Unicode tag characters, bidirectional
//     overrides, zero-width spaces and joiners-that-join-nothing) are dropped:
//     they render as nothing for a human reviewing a transcript yet reach the
//     model intact, which makes them the standard carrier for hidden
//     instructions ("ASCII smuggling"). Command output is attacker-influenced
//     text like any other (`cat README.md`);
//   - \t, \n and \r survive. \r is resolved later by collapseCR because a
//     progress bar redraws one line thousands of times.

type scState uint8

const (
	scText   scState = iota
	scEsc            // after ESC
	scEscInt         // ESC followed by intermediate bytes (e.g. charset selection)
	scCSI            // ESC [ ... final
	scStr            // OSC / DCS / APC / PM / SOS payload
	scStrEsc         // ESC seen inside a string payload (expecting the ST backslash)
)

const (
	maxCSILen = 64
	maxStrLen = 4096
)

// plainByte marks bytes copied through untouched on the fast path.
var plainByte = func() (t [256]bool) {
	for b := 0x20; b < 0x7f; b++ {
		t[b] = true
	}
	t['\n'], t['\t'], t['\r'] = true, true, true
	return
}()

// sanitizer is stateful per stream (stdout and stderr each own one) and is not
// safe for concurrent use.
type sanitizer struct {
	st    scState
	n     int // bytes swallowed by the escape sequence in progress
	pend  [utf8.UTFMax]byte
	npend int
	ctrl  int64 // dropped control bytes: the "looks binary" signal
}

// feed appends the cleaned form of src to dst.
func (s *sanitizer) feed(dst, src []byte) []byte {
	if s.npend > 0 {
		joined := make([]byte, 0, s.npend+len(src))
		joined = append(joined, s.pend[:s.npend]...)
		joined = append(joined, src...)
		s.npend = 0
		src = joined
	}
	for i := 0; i < len(src); {
		switch s.st {
		case scText:
			j := i
			for j < len(src) && plainByte[src[j]] {
				j++
			}
			dst = append(dst, src[i:j]...)
			i = j
			if i == len(src) {
				break
			}
			b := src[i]
			switch {
			case b == 0x1b:
				s.st, s.n = scEsc, 0
				i++
			case b >= utf8.RuneSelf:
				r, size := utf8.DecodeRune(src[i:])
				if r == utf8.RuneError && size <= 1 {
					if !utf8.FullRune(src[i:]) {
						// A character split by the read boundary: wait for the rest.
						s.npend = copy(s.pend[:], src[i:])
						return dst
					}
					dst = append(dst, "�"...)
					i++
					continue
				}
				switch {
				case r < 0xA0: // C1 controls
					s.ctrl++
				case invisible(r):
				default:
					dst = append(dst, src[i:i+size]...)
				}
				i += size
			default:
				switch b {
				case 0x07, 0x08, 0x0b, 0x0c: // BEL, BS, VT, FF: dropped, but common in text
				default:
					s.ctrl++
				}
				i++
			}
		case scEsc:
			switch b := src[i]; {
			case b == '[':
				s.st, s.n = scCSI, 0
				i++
			case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
				s.st, s.n = scStr, 0
				i++
			case b >= 0x20 && b <= 0x2f:
				s.st = scEscInt
				i++
			case b >= 0x30 && b <= 0x7e:
				s.st = scText // two-byte sequence such as ESC c or ESC 7
				i++
			default:
				s.st = scText // stray ESC: reprocess this byte as text
			}
		case scEscInt:
			switch b := src[i]; {
			case b >= 0x20 && b <= 0x2f:
				i++
			case b >= 0x30 && b <= 0x7e:
				s.st = scText
				i++
			default:
				s.st = scText
			}
		case scCSI:
			b := src[i]
			s.n++
			switch {
			case b >= 0x40 && b <= 0x7e:
				s.st = scText
				i++
			case b >= 0x20 && b <= 0x3f && s.n < maxCSILen:
				i++
			default:
				s.st = scText // malformed: give up and reprocess this byte
			}
		case scStr:
			b := src[i]
			s.n++
			switch {
			case b == 0x07:
				s.st = scText
				i++
			case b == 0x1b:
				s.st = scStrEsc
				i++
			case s.n > maxStrLen:
				s.st = scText // unterminated: do not swallow the rest of the output
			default:
				i++
			}
		case scStrEsc:
			if src[i] == '\\' {
				s.st = scText
				i++
			} else {
				s.st = scEsc // the ESC started a new sequence; reprocess this byte
			}
		}
	}
	return dst
}

// invisible reports runes that display as nothing (or only reorder text) and so
// can smuggle content past a human reader. Zero-width joiner and non-joiner and
// the left/right marks are deliberately kept: emoji sequences and several
// scripts need them.
func invisible(r rune) bool {
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

// flush emits whatever a dangling partial character turns into at end of stream.
func (s *sanitizer) flush(dst []byte) []byte {
	if s.npend > 0 {
		dst = append(dst, "�"...)
		s.npend = 0
	}
	s.st = scText
	return dst
}

// collapseCR resolves carriage returns the way a terminal would show the final
// screen: CRLF becomes LF, and text after a lone CR replaces the line so far.
// Without this a single progress bar (curl, pip, npm, git clone) floods the
// context with thousands of near-identical fragments.
func collapseCR(s string) string {
	if strings.IndexByte(s, '\r') < 0 {
		return s
	}
	// A byte slice rather than a Builder: overwriting a line is a truncation
	// back to the line start, which must be O(1) or a progress bar with a
	// million redraws becomes quadratic.
	buf := make([]byte, 0, len(s))
	lineStart := 0
	reset := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				continue // CRLF: the LF that follows ends the line
			}
			reset = true // a later character on this line overwrites what we have
		case '\n':
			reset = false
			buf = append(buf, '\n')
			lineStart = len(buf)
		default:
			if reset {
				buf = buf[:lineStart]
				reset = false
			}
			buf = append(buf, c)
		}
	}
	return string(buf)
}

// tidyOutput is the final pass over captured text: repair any character cut in
// half by ring-buffer elision and resolve carriage returns.
func tidyOutput(s string) string {
	return collapseCR(strings.ToValidUTF8(s, "�"))
}
