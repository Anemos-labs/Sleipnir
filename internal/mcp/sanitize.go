package mcp

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Everything an MCP server sends is data from a third party. Tool descriptions
// and schemas end up in the tools array that every agent of a session sends
// byte for byte, tool results end up in the transcript, and both are shown in
// a terminal. This file is the one place text from a server is made safe for
// those three destinations; the rest of the package calls it at the decode
// boundary so nothing unsanitised is ever stored.
//
// What is removed, and why:
//
//   - C0/C1 control characters and ANSI/OSC escape sequences: terminal
//     injection (window titles, OSC 52 clipboard writes, cursor games).
//   - Unicode tag characters (U+E0000..E007F): a model reads them as ASCII,
//     a human reviewer of the server's source or of the description sees
//     nothing ("rules file backdoor" style prompt injection).
//   - Bidi controls, zero-width characters, word joiners, variation selectors
//     and other invisible format characters: they hide or reorder text.
//   - Invalid UTF-8 becomes U+FFFD so downstream JSON and logs stay valid.
//
// Visible prose injection ("ignore previous instructions") cannot be fixed by
// character filtering; see detectInjection and the length caps for the rest.

// truncMarker ends any text that was cut to fit a limit, so neither the model
// nor a reader mistakes a clipped description for a complete one.
const truncMarker = "…[truncated]"

// invisible reports runes that render as nothing, or change how surrounding
// text is ordered, but still reach the model as tokens.
func invisible(r rune) bool {
	switch {
	case r < 0x20:
		return r != '\n' && r != '\t'
	case r < 0x7f:
		return false
	case r <= 0x9f: // DEL and the C1 controls
		return true
	case r == 0xAD, r == 0x061C, r == 0x3164, r == 0xFFA0:
		// soft hyphen, Arabic letter mark, Hangul fillers
		return true
	case r >= 0x180B && r <= 0x180F: // Mongolian free variation selectors and separator
		return true
	case r >= 0x200B && r <= 0x200F: // zero-width space/joiners, LRM, RLM
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2060 && r <= 0x206F: // word joiner, invisible operators, deprecated controls
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r == 0xFEFF: // BOM / zero width no-break space
		return true
	case r >= 0xFFF9 && r <= 0xFFFC: // interlinear annotation, object replacement
		return true
	case r >= 0x1BCA0 && r <= 0x1BCA3, r >= 0x1D173 && r <= 0x1D17A:
		return true
	case r >= 0xE0000 && r <= 0xE007F: // tag characters
		return true
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
		return true
	}
	return false
}

// privateOrNoncharacter marks code points with no defined meaning; strict
// mode (metadata that becomes part of the cached prefix) drops them.
func privateOrNoncharacter(r rune) bool {
	switch {
	case r >= 0xE000 && r <= 0xF8FF, r >= 0xF0000 && r <= 0xFFFFD, r >= 0x100000 && r <= 0x10FFFD:
		return true
	case r >= 0xFDD0 && r <= 0xFDEF:
		return true
	case r&0xFFFE == 0xFFFE: // U+xFFFE and U+xFFFF in every plane
		return true
	}
	return false
}

// cleanText sanitises text that will be shown to a model or a terminal as
// data: tool results, error messages, resource contents. It is lenient about
// characters that legitimate non-English text needs (ZWJ/ZWNJ between letters,
// one emoji presentation selector after an emoji).
func cleanText(s string) string { return clean(s, false) }

// cleanStrict sanitises text that becomes part of the shared, cached tool
// list: names, descriptions, schema strings. Nothing invisible survives.
func cleanStrict(s string) string { return clean(s, true) }

func clean(s string, strict bool) string {
	if isCleanASCII(s) {
		return s
	}
	s = stripANSI(s)
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	if strings.IndexByte(s, '\r') >= 0 {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	var b strings.Builder
	b.Grow(len(s))
	var prev rune
	selector := false // an emoji presentation selector was already kept after prev
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == 0x85 || r == 0x2028 || r == 0x2029:
			r = '\n' // line separators are newlines, not invisible characters
		case invisible(r):
			if strict {
				continue
			}
			next, _ := utf8.DecodeRuneInString(s[i:])
			switch {
			case (r == 0x200C || r == 0x200D) && joinerBetweenLetters(prev, next):
			case (r == 0xFE0E || r == 0xFE0F) && !selector && prev > 0x7f && !invisible(prev):
				selector = true
			default:
				continue
			}
		case strict && privateOrNoncharacter(r):
			continue
		}
		if r != 0xFE0E && r != 0xFE0F {
			selector = false
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// joinerBetweenLetters keeps ZWJ/ZWNJ where scripts and emoji sequences need
// them (both neighbours are visible non-ASCII), and drops them everywhere
// else, which is where zero-width steganography lives.
func joinerBetweenLetters(prev, next rune) bool {
	return prev > 0x7f && next > 0x7f && !invisible(prev) && !invisible(next) && next != utf8.RuneError
}

// isCleanASCII is the fast path: printable ASCII plus newline and tab needs no
// work at all, and most tool text is exactly that.
func isCleanASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\n' && c != '\t' || c >= 0x7f {
			return false
		}
	}
	return true
}

// stripANSI removes terminal escape sequences (CSI, OSC and the other string
// sequences, two-byte escapes). Removing only the ESC byte would leave the
// parameters behind as visible junk, and would leave an unterminated OSC
// swallowing nothing while still being emitted.
func stripANSI(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		i++ // ESC
		if i >= len(s) {
			break
		}
		switch n := s[i]; {
		case n == '[': // CSI: parameters 0x30-0x3F, intermediates 0x20-0x2F, final 0x40-0x7E
			i++
			for j := 0; i < len(s) && j < 64; j++ {
				c := s[i]
				if c >= 0x40 && c <= 0x7e {
					i++
					break
				}
				if c < 0x20 || c > 0x3f {
					break // malformed: resume normal processing at this byte
				}
				i++
			}
		case n == ']' || n == 'P' || n == 'X' || n == '^' || n == '_':
			// OSC, DCS, SOS, PM, APC: a string that ends at BEL or ST (ESC \).
			// Bounded so an unterminated one cannot eat the rest of the output.
			i++
			for j := 0; i < len(s) && j < 4096; j++ {
				if s[i] == 0x07 {
					i++
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		case n >= 0x20 && n <= 0x2f: // ESC, intermediates, final byte (charset selection)
			for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
				i++
			}
			if i < len(s) {
				i++
			}
		default: // two-byte sequence such as ESC c or ESC 7
			i++
		}
	}
	return b.String()
}

// tidy normalises whitespace in multi-line metadata so equivalent descriptions
// produce equal bytes and blank-line padding costs no tokens: trailing spaces
// are dropped, runs of blank lines collapse to one, the ends are trimmed.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// cleanMeta is the sanitiser for descriptions and other prose that becomes
// part of the frozen tool list: strict characters, tidy whitespace, at most
// max runes (marker included).
func cleanMeta(s string, max int) string {
	s = tidy(cleanStrict(s))
	s, _ = truncateRunes(s, max)
	return s
}

// oneLine collapses s to a single trimmed line of at most max runes, for
// summaries in permission prompts and log lines.
func oneLine(s string, max int) string {
	s = cleanText(s)
	s = strings.Join(strings.Fields(s), " ")
	s, _ = truncateRunes(s, max)
	return s
}

// truncateRunes cuts s to at most max runes including truncMarker, on a rune
// boundary. It reports whether it cut.
func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max { // len(s) >= rune count: cheap exit for the common case
		return s, false
	}
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	keep := max - utf8.RuneCountInString(truncMarker)
	if keep < 1 {
		// No room for a marker: a hard cut is still better than an oversized field.
		return cutRunes(s, max), true
	}
	return strings.TrimRight(cutRunes(s, keep), " \t\n") + truncMarker, true
}

// cutRunes returns a prefix ending at a rune boundary after at most n runes; n should be
// nonnegative.
func cutRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// minSecretLen is the shortest value redacted. Shorter values ("1", "true",
// "dev") would mangle ordinary text for no security gain.
const minSecretLen = 8

// redactor replaces known secret values (the server's configured env and
// header values) wherever they show up in text that leaves this package:
// errors, logs, the stderr excerpt of a crashed server, even tool results,
// because a server that echoes its credentials back is a leak to the model.
// The zero value and nil redact nothing.
type redactor struct{ rep *strings.Replacer }

func newRedactor(secrets []string) *redactor {
	seen := map[string]bool{}
	var list []string
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if len(s) < minSecretLen || seen[s] {
			continue
		}
		seen[s] = true
		list = append(list, s)
	}
	if len(list) == 0 {
		return nil
	}
	// Longest first: strings.Replacer tries old strings in argument order, so a
	// secret that contains another is masked whole instead of in pieces.
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	pairs := make([]string, 0, 2*len(list))
	for _, s := range list {
		pairs = append(pairs, s, "***")
	}
	return &redactor{rep: strings.NewReplacer(pairs...)}
}

// apply replaces configured secrets, leaving text unchanged for a nil or uninitialized redactor.
func (r *redactor) apply(s string) string {
	if r == nil || r.rep == nil {
		return s
	}
	return r.rep.Replace(s)
}

// globMatch reports whether name matches pattern, where '*' matches any run
// of characters (including '/', tool names are not paths) and '?' exactly one
// character. Matching is iterative with a single backtrack point: linear in
// practice, O(len(pattern)*len(name)) at worst, and inputs are length-capped
// by the callers.
func globMatch(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	pi, ni := 0, 0
	star, mark := -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]) && p[pi] != '*':
			pi++
			ni++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ni = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
