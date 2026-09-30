package provider

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxErrorText is the longest error message, in bytes, that an adapter passes on.
// Error text comes from the endpoint, and an endpoint can be a third-party
// gateway or anything a base-URL override points at. It is written to the event
// log, printed on a terminal and, when a task fails, shown to other agents.
const MaxErrorText = 2048

// SanitizeText makes text that came from a server inert: one printable line of
// at most max bytes (MaxErrorText when max <= 0).
//
//   - ANSI escape sequences (CSI, OSC such as the OSC 52 clipboard write, DCS, SOS,
//     PM, APC, character-set selection and the 8-bit C1 forms of those) are removed
//     whole, payload included, not just their ESC byte.
//   - Control characters and characters that hide or reorder text are removed:
//     bidirectional controls, zero-width and other format characters, the Unicode
//     tag block, variation selectors, fillers, noncharacters and private-use code
//     points. Invalid UTF-8 becomes U+FFFD.
//   - Every run of white space (line breaks included) becomes one space, so a
//     message cannot pose as several log lines.
//   - Text longer than max is cut at a character boundary and ends in an ellipsis.
//
// The work is bounded by max, not by the input size: a megabyte of hostile text
// costs about as much as max bytes of it.
func SanitizeText(s string, max int) string {
	if max <= 0 {
		max = MaxErrorText
	}
	// Escape sequences shrink text; four times the budget plus a margin is more than
	// enough input to fill it, and it caps the work on absurd input.
	if limit := max*4 + 256; len(s) > limit {
		s = s[:limit]
	}
	s = strings.ToValidUTF8(s, string(utf8.RuneError))

	var b strings.Builder
	b.Grow(min(len(s), max+8))
	space := false
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch r {
		case 0x1b: // ESC: the sequence it introduces goes with it
			i += escapeSkip(s[i:])
			continue
		case 0x9b: // CSI
			i += csiSkip(s[i:])
			continue
		case 0x9d: // OSC
			i += stringSkip(s[i:], true)
			continue
		case 0x90, 0x98, 0x9e, 0x9f: // DCS, SOS, PM, APC
			i += stringSkip(s[i:], false)
			continue
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if hiddenRune(r) {
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
		if b.Len() > max+utf8.UTFMax {
			break // over budget already; the cut below settles the rest
		}
	}
	out := b.String()
	if len(out) <= max {
		return out
	}
	const ellipsis = "…"
	cut := max
	if max > len(ellipsis)+1 {
		cut = max - len(ellipsis)
	}
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	out = strings.TrimRight(out[:cut], " ") // a cut right after a word gap leaves no dangling space
	if max > len(ellipsis)+1 {
		out += ellipsis
	}
	return out
}

// hiddenRune reports whether r is a character that is dropped from server text:
// it is invisible, changes how neighbouring text is displayed or ordered, or is
// a control.
func hiddenRune(r rune) bool {
	switch {
	case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
		return true // includes ZWSP/ZWJ, bidi controls, soft hyphen, BOM and the tag characters
	case r >= 0xE0000 && r <= 0xE0FFF: // plane 14: tag characters, variation selectors supplement
		return true
	case r >= 0xFE00 && r <= 0xFE0F, r >= 0x180B && r <= 0x180F: // variation selectors
		return true
	case r == 0x034F, r == 0x115F, r == 0x1160, r == 0x17B4, r == 0x17B5, r == 0x3164, r == 0xFFA0: // joiner and fillers
		return true
	case r >= 0xFDD0 && r <= 0xFDEF, r&0xFFFE == 0xFFFE: // noncharacters
		return true
	}
	return false
}

// escapeSkip returns how many bytes after an ESC belong to its sequence.
func escapeSkip(s string) int {
	if s == "" {
		return 0
	}
	switch c := s[0]; {
	case c == '[':
		return 1 + csiSkip(s[1:])
	case c == ']':
		return 1 + stringSkip(s[1:], true)
	case c == 'P' || c == 'X' || c == '^' || c == '_':
		return 1 + stringSkip(s[1:], false)
	case c >= 0x20 && c <= 0x2f: // intermediate bytes, then a final byte: "ESC ( B"
		n := 1
		for n < len(s) && s[n] >= 0x20 && s[n] <= 0x2f {
			n++
		}
		if n < len(s) && s[n] >= 0x30 && s[n] <= 0x7e {
			n++
		}
		return n
	case c >= 0x30 && c <= 0x7e: // "ESC c", "ESC 7", "ESC =", ...
		return 1
	}
	return 0
}

// csiSkip consumes the parameter, intermediate and final bytes of a CSI sequence.
func csiSkip(s string) int {
	n := 0
	for n < len(s) && s[n] >= 0x30 && s[n] <= 0x3f {
		n++
	}
	for n < len(s) && s[n] >= 0x20 && s[n] <= 0x2f {
		n++
	}
	if n < len(s) && s[n] >= 0x40 && s[n] <= 0x7e {
		n++
	}
	return n
}

// stringSkip consumes a string-type sequence (OSC, DCS, ...) up to and including
// its terminator: ST (ESC \ or U+009C) or, for OSC, BEL. An ESC that does not
// begin ST ends the string without being consumed, as terminals treat it. A
// string that never ends swallows the rest of the text, as it would in a terminal.
func stringSkip(s string, bel bool) int {
	for n := 0; n < len(s); n++ {
		switch c := s[n]; {
		case c == 0x07 && bel:
			return n + 1
		case c == 0x1b:
			if n+1 < len(s) && s[n+1] == '\\' {
				return n + 2
			}
			return n
		case c == 0xc2 && n+1 < len(s) && s[n+1] == 0x9c:
			return n + 2
		}
	}
	return len(s)
}

// MaxRawBytes bounds Error.Raw, the endpoint's error body kept for diagnosis.
const MaxRawBytes = 64 << 10

// CapRaw copies b for Error.Raw, cut to MaxRawBytes. What is kept is the start of
// the body, verbatim; nothing that reads Raw may assume it is complete JSON.
func CapRaw(b []byte) json.RawMessage {
	if len(b) > MaxRawBytes {
		b = b[:MaxRawBytes]
	}
	return append(json.RawMessage(nil), b...)
}
