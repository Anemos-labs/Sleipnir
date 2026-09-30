package memory

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// clean normalises a raw file into prompt text: BOM dropped, invalid UTF-8
// replaced, line endings unified to "\n", hidden and control characters removed
// (see stripHidden), HTML comments removed (they are notes to human editors, not
// instructions, and would otherwise cost tokens on every request), and
// leading/trailing blank lines trimmed. Two files that differ only in these
// respects clean to identical text, which is what keeps the rendered prompt layer
// byte-stable.
func clean(raw []byte) string {
	s, _ := cleanCounting(raw)
	return s
}

// cleanCounting is clean that also reports what stripHidden removed.
func cleanCounting(raw []byte) (string, stripped) {
	s := string(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})) // UTF-8 byte order mark
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// Before comments and imports are looked for: a zero-width character inside
	// "<!--" or after "@" must not change what those constructs mean.
	s, st := stripHidden(s)
	s = stripComments(s)
	return strings.Trim(s, "\n"), st
}

// The kinds of code point stripHidden removes.
const (
	hiddenNone      = iota
	hiddenTag       // U+E0000-E0FFF: tag characters, variation selectors supplement
	hiddenBidi      // bidirectional controls, which reorder how text is displayed
	hiddenZeroWidth // zero-width, variation selectors and other format characters
	hiddenControl   // C0 and C1 controls other than tab and newline, and DEL
)

// hiddenKind classifies r. Everything but hiddenNone renders as nothing (or
// rearranges what is rendered) in an editor, a diff or a code-review page while a
// language model reads it like any other character: the "rules file backdoor",
// where instructions are smuggled in as invisible tag characters or hidden
// between visible ones.
func hiddenKind(r rune) int {
	switch {
	case r == '\n' || r == '\t':
		return hiddenNone
	case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		return hiddenControl
	case r >= 0xE0000 && r <= 0xE0FFF:
		return hiddenTag
	case r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069):
		return hiddenBidi
	case r == 0x034F || r == 0x115F || r == 0x1160 || r == 0x17B4 || r == 0x17B5 || r == 0x3164 || r == 0xFFA0:
		return hiddenZeroWidth // combining grapheme joiner, Hangul and Khmer fillers
	case r >= 0x180B && r <= 0x180F, r >= 0xFE00 && r <= 0xFE0F:
		return hiddenZeroWidth // Mongolian and general variation selectors
	case unicode.Is(unicode.Cf, r):
		return hiddenZeroWidth // ZWSP/ZWNJ/ZWJ, word joiner, soft hyphen, BOM inside text, ...
	}
	return hiddenNone
}

// stripped counts what stripHidden removed, by kind.
type stripped struct{ tags, bidi, zeroWidth, control int }

func (s stripped) total() int { return s.tags + s.bidi + s.zeroWidth + s.control }

func (s stripped) String() string {
	var parts []string
	for _, p := range []struct {
		n    int
		name string
	}{{s.tags, "tag characters"}, {s.bidi, "bidi controls"}, {s.zeroWidth, "zero-width or format characters"}, {s.control, "control characters"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
		}
	}
	return strings.Join(parts, ", ")
}

// stripHidden removes code points that hiddenKind classifies, and turns the
// Unicode line separators (NEL, U+2028, U+2029) into "\n" so that the line
// structure the rest of the loader sees is the one a reader sees.
func stripHidden(s string) (string, stripped) {
	var st stripped
	clean := true
	for _, r := range s {
		if hiddenKind(r) != hiddenNone || r == 0x85 || r == 0x2028 || r == 0x2029 {
			clean = false
			break
		}
	}
	if clean {
		return s, st
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case 0x85, 0x2028, 0x2029:
			b.WriteByte('\n')
			continue
		}
		switch hiddenKind(r) {
		case hiddenNone:
			b.WriteRune(r)
		case hiddenTag:
			st.tags++
		case hiddenBidi:
			st.bidi++
		case hiddenZeroWidth:
			st.zeroWidth++
		case hiddenControl:
			st.control++
		}
	}
	return b.String(), st
}

// fenceOpen returns the fence marker ("```" or "~~~", possibly longer) when
// line opens or closes a fenced code block, else "".
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

// stripComments removes <!-- ... --> comments, including multi-line ones, but
// leaves fenced code blocks and inline code spans alone: documentation that
// shows what an HTML comment looks like must survive. A line that held nothing
// but a comment disappears entirely instead of leaving a blank line.
func stripComments(text string) string {
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
// inside one. touched reports whether anything was removed.
func stripLine(line string, inComment bool) (out string, still, touched bool) {
	var b strings.Builder
	inCode := false
	for i := 0; i < len(line); {
		if inComment {
			j := strings.Index(line[i:], "-->")
			if j < 0 {
				return b.String(), true, true // the rest of the line is comment
			}
			i += j + len("-->")
			inComment, touched = false, true
			continue
		}
		switch {
		case line[i] == '`':
			// A run of backticks opens or closes an inline code span.
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

// findImports returns the "@path" specs of import lines in text, in order. A
// line is an import when, trimmed, it is "@" followed by a path with no
// whitespace. Lines inside fenced code blocks, and indented code blocks, are
// examples, not imports.
func findImports(text string) []string {
	var specs []string
	inFence := false
	marker := ""
	for _, line := range strings.Split(text, "\n") {
		if f := fenceOpen(line); f != "" {
			switch {
			case !inFence:
				inFence, marker = true, f
			case closesFence(line, marker):
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		// Four spaces or a tab of indentation is an indented code block, where an
		// "@path" line is an example, not an import.
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ") {
			continue
		}
		t := strings.TrimSpace(line)
		if len(t) < 2 || t[0] != '@' || strings.ContainsAny(t, " \t") {
			continue
		}
		specs = append(specs, t[1:])
	}
	return specs
}
