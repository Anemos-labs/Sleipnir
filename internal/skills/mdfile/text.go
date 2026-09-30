package mdfile

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Normalize turns raw file bytes into text: a UTF-8 byte order mark is
// dropped, invalid UTF-8 becomes U+FFFD and every line ending becomes "\n".
// Two files that differ only in these respects normalise to identical text,
// which keeps anything derived from them byte-stable.
func Normalize(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	s := strings.ToValidUTF8(string(raw), "�")
	if strings.IndexByte(s, '\r') >= 0 {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	return s
}

// Hidden counts the characters Sanitize removed, by kind.
type Hidden struct {
	Controls int // C0/C1 control characters and terminal escapes
	Format   int // zero-width and other invisible format characters
	Bidi     int // bidirectional controls, which change how text is displayed
	Tags     int // Unicode tag characters and variation-selector supplements
}

// Total is the number of characters removed.
func (h Hidden) Total() int { return h.Controls + h.Format + h.Bidi + h.Tags }

// Suspicious reports whether what was removed has no innocent explanation in a
// markdown file a person wrote: bidi controls, tag characters and control
// bytes are how "rules file backdoors" hide instructions from a human reviewer
// while the model still reads them, and a long run of zero-width characters is
// a data channel. A stray soft hyphen or byte order mark is not suspicious.
func (h Hidden) Suspicious() bool {
	return h.Bidi > 0 || h.Tags > 0 || h.Controls > 0 || h.Format >= 8
}

// hideClass says what kind of hidden character r is, or keepRune.
type hideClass uint8

const (
	keepRune hideClass = iota
	hideControl
	hideFormat
	hideBidi
	hideTag
)

func classify(r rune) hideClass {
	switch {
	case r < 0x20:
		if r == '\n' || r == '\t' {
			return keepRune
		}
		return hideControl
	case r == 0x7F || (r >= 0x80 && r <= 0x9F):
		return hideControl
	case r < 0xAD:
		return keepRune // the common case: printable ASCII and Latin-1 punctuation
	case r == 0xAD, r == 0x034F, r == 0x115F, r == 0x1160, r == 0x17B4, r == 0x17B5, r == 0x180E,
		r == 0x3164, r == 0xFFA0, r == 0xFEFF:
		return hideFormat
	case r == 0x061C, r == 0x200E, r == 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return hideBidi
	case r >= 0x180B && r <= 0x180D, r == 0x180F:
		return hideFormat // Mongolian free variation selectors
	case r >= 0x200B && r <= 0x200D, r == 0x2060, r >= 0x2061 && r <= 0x2064, r >= 0x206A && r <= 0x206F:
		return hideFormat
	case r >= 0xFFF9 && r <= 0xFFFB:
		return hideFormat // interlinear annotation
	case r >= 0x1BCA0 && r <= 0x1BCA3, r >= 0x1D173 && r <= 0x1D17A:
		return hideFormat
	case r >= 0xE0000 && r <= 0xE007F, r >= 0xE0100 && r <= 0xE01EF:
		return hideTag // tag characters and variation selectors supplement
	case r >= 0xFDD0 && r <= 0xFDEF, r&0xFFFE == 0xFFFE:
		return hideFormat // noncharacters
	}
	return keepRune
}

// Sanitize removes characters that are invisible or that steer rendering, and
// reports how many of each kind it dropped. What reaches a model must be what
// a reviewer of the file can see: the Unicode tag block alone can spell a whole
// instruction that no editor or GitHub page displays.
//
// Newline and tab survive; U+2028 and U+2029 become newlines. Zero-width
// joiners survive only between two non-ASCII characters (emoji sequences,
// Persian and Indic spelling); next to ASCII they carry nothing legitimate.
func Sanitize(s string) (string, Hidden) {
	var h Hidden
	plain := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 0x80 || (c < 0x20 && c != '\n' && c != '\t') || c == 0x7F {
			plain = false
			break
		}
	}
	if plain {
		return s, h
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range rs {
		if r == 0x2028 || r == 0x2029 {
			b.WriteByte('\n')
			continue
		}
		if r == 0x200C || r == 0x200D {
			if joinerIsLegit(rs, i) {
				b.WriteRune(r)
			} else {
				h.Format++
			}
			continue
		}
		switch classify(r) {
		case keepRune:
			b.WriteRune(r)
		case hideControl:
			h.Controls++
		case hideFormat:
			h.Format++
		case hideBidi:
			h.Bidi++
		case hideTag:
			h.Tags++
		}
	}
	return b.String(), h
}

func joinerIsLegit(rs []rune, i int) bool {
	if i == 0 || i+1 >= len(rs) {
		return false
	}
	prev, next := rs[i-1], rs[i+1]
	return prev > 0x7F && next > 0x7F && classify(prev) == keepRune && classify(next) == keepRune &&
		prev != 0x200C && prev != 0x200D && next != 0x200C && next != 0x200D
}

// fenceOpen returns the fence marker ("```" or "~~~", possibly longer) when line
// opens or closes a fenced code block, else "".
func fenceOpen(line string) string {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 { // four spaces of indent is a code block, not a fence
		return ""
	}
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(t) && t[n] == c {
			n++
		}
		if n >= 3 {
			return strings.Repeat(string(c), n)
		}
	}
	return ""
}

// closesFence reports whether line closes a fence opened with marker.
func closesFence(line, marker string) bool {
	t := strings.TrimSpace(line)
	return len(t) >= len(marker) && strings.Trim(t, marker[:1]) == ""
}

// StripComments removes <!-- ... --> comments, including multi-line ones, but
// leaves fenced code blocks and inline code spans alone: documentation that
// shows what an HTML comment looks like must survive. A line that held nothing
// but a comment disappears instead of leaving a blank line.
//
// Comments are notes to human editors. GitHub renders them invisibly, so a
// model that read them would be taking instructions its reviewer cannot see.
func StripComments(text string) string {
	if !strings.Contains(text, "<!--") {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inFence, inComment := false, false
	marker := ""
	for _, line := range lines {
		if !inComment {
			if f := fenceOpen(line); f != "" {
				switch {
				case !inFence:
					inFence, marker = true, f
				case closesFence(line, marker):
					inFence = false
				}
				out = append(out, line)
				continue
			}
			if inFence {
				out = append(out, line)
				continue
			}
		}
		res, still, touched := stripLine(line, inComment)
		inComment = still
		if touched {
			res = strings.TrimRight(res, " \t")
			if strings.TrimSpace(res) == "" {
				continue
			}
		}
		out = append(out, res)
	}
	return strings.Join(out, "\n")
}

// stripLine removes comment text from one line. inComment says the line starts
// inside a comment left open by an earlier line; still reports whether it ends
// inside one; touched reports whether anything was removed.
func stripLine(line string, inComment bool) (out string, still, touched bool) {
	var b strings.Builder
	inCode := false
	for i := 0; i < len(line); {
		if inComment {
			j := strings.Index(line[i:], "-->")
			if j < 0 {
				return b.String(), true, true
			}
			i += j + len("-->")
			inComment, touched = false, true
			continue
		}
		switch {
		case line[i] == '`':
			for i < len(line) && line[i] == '`' {
				b.WriteByte('`')
				i++
			}
			inCode = !inCode
		case !inCode && strings.HasPrefix(line[i:], "<!--"):
			inComment, touched = true, true
			i += len("<!--")
		default:
			b.WriteByte(line[i])
			i++
		}
	}
	return b.String(), inComment, touched
}

// OneLine collapses all white space (newlines included) to single spaces and,
// when maxRunes > 0, truncates to that many runes with a trailing "…". A word
// boundary near the cut is preferred. The result never contains a line break,
// so it cannot start a new line in a prompt listing.
func OneLine(s string, maxRunes int) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	rs := []rune(s)
	cut := max(maxRunes-1, 0)
	for j := cut; j > cut-cut/5 && j > 0; j-- {
		if rs[j] == ' ' {
			cut = j
			break
		}
	}
	return strings.TrimRight(string(rs[:cut]), " ,;:.-") + "…"
}

// EscapeTags neutralises text that could pass for the markup a harness uses to
// frame sections of a prompt: a "<" followed by a letter, "/", "!" or "?" becomes
// U+2039 "‹". It is deterministic, so text that goes into a cached prompt layer
// stays byte-stable.
func EscapeTags(s string) string {
	if strings.IndexByte(s, '<') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '<' && i+1 < len(s) {
			n := s[i+1]
			if n == '/' || n == '!' || n == '?' || (n|0x20 >= 'a' && n|0x20 <= 'z') {
				b.WriteString("‹")
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// CutLines shortens s to at most maxBytes bytes, at a line boundary when one is
// close and never inside a UTF-8 character. cut reports whether anything was
// removed.
func CutLines(s string, maxBytes int) (out string, cut bool) {
	if maxBytes < 0 || len(s) <= maxBytes {
		return s, false
	}
	s = s[:maxBytes]
	if i := strings.LastIndexByte(s, '\n'); i >= 0 && i >= len(s)-1024 {
		return s[:i], true
	}
	for range utf8.UTFMax {
		if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size <= 1 && len(s) > 0 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s, true
}

// NameRules describes the accepted spelling of a definition name.
type NameRules struct {
	Max        int  // maximum length in bytes
	Upper      bool // allow A-Z
	Dot        bool // allow "." after the first character
	Underscore bool // allow "_" after the first character
}

// CheckName validates a definition name. Names end up in prompt listings, tool
// arguments and file paths, so the accepted set is a small ASCII one: no white
// space, no punctuation that could forge structure, no path separators.
func CheckName(name string, r NameRules) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if r.Max > 0 && len(name) > r.Max {
		return fmt.Errorf("name %q is %d characters long; the limit is %d", clip(name, 40), len(name), r.Max)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || r.Upper && c >= 'A' && c <= 'Z'
		if i > 0 {
			ok = ok || c == '-' || r.Dot && c == '.' || r.Underscore && c == '_'
		}
		if !ok {
			return fmt.Errorf("name %q has an invalid character %q; use %s", clip(name, 40), string(rune(c)), nameHint(r))
		}
	}
	return nil
}

func nameHint(r NameRules) string {
	s := "lower-case letters, digits and hyphens"
	if r.Upper {
		s = "letters, digits and hyphens"
	}
	if r.Dot {
		s += ", dots"
	}
	if r.Underscore {
		s += ", underscores"
	}
	return s + " (starting with a letter or digit)"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// SplitList splits a list written as one string, such as "Read, Grep, Bash(git
// diff:*)" or "Read Grep", on commas and white space outside parentheses (a
// tool pattern may contain both). Surrounding quotes are removed from items and
// repeated items dropped, keeping the first.
func SplitList(s string) []string {
	var out []string
	depth := 0
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		item := strings.TrimSpace(s[start:end])
		if len(item) >= 2 && (item[0] == '"' || item[0] == '\'') && item[len(item)-1] == item[0] {
			item = strings.TrimSpace(item[1 : len(item)-1])
		}
		if item != "" {
			out = append(out, item)
		}
		start = -1
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '(':
			depth++
			if start < 0 {
				start = i
			}
		case c == ')':
			if depth > 0 {
				depth--
			}
			if start < 0 {
				start = i
			}
		case depth == 0 && (c == ',' || c == ' ' || c == '\t' || c == '\n'):
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(s))
	return dedupe(out)
}

func dedupe(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// ValidTool reports whether s is a well-formed tool rule as the permission
// engine spells them: a tool name (letters, digits, "_", "-", ".", ":" and "*",
// so MCP names and wildcards work), optionally followed by a parenthesised
// pattern with balanced parentheses and no control characters. It checks shape
// only; whether the rule means anything is the permission engine's business.
func ValidTool(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	name, pat, hasPat := strings.Cut(s, "(")
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':' || c == '*') {
			return false
		}
	}
	if !hasPat {
		return true
	}
	if !strings.HasSuffix(pat, ")") {
		return false
	}
	depth := 1
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; {
		case c < 0x20 || c == 0x7F:
			return false
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 && i != len(pat)-1 {
				return false
			}
		}
	}
	return depth == 0
}

// FilterTools keeps the well-formed entries of list and reports the rest.
func FilterTools(list []string) (ok, bad []string) {
	for _, t := range list {
		if ValidTool(t) {
			ok = append(ok, t)
		} else {
			bad = append(bad, t)
		}
	}
	return ok, bad
}
