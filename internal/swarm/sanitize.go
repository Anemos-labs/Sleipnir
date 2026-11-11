package swarm

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/kv"
)

// Text that one agent (or a file name, or a tool argument) can influence is shown
// to other agents: in mail, in the hot view, in task cards, in alerts. It is data.
// The helpers here make it inert: one printable line (or a bounded block), no
// control or direction-changing characters, and nothing that can pass for a
// harness header or a closing tag. They never expand text, so a cap applied
// before sanitising still holds afterwards.

// neutralise defuses harness-looking markers and tag delimiters. What counts as a
// harness marker ("[mail m1", "[stop hook]", "[harness]", the label of a hook's
// context) is decided in one place, kv.EscapeMarkup, which the layers use too: "[mail
// m1" becomes "(mail m1". On top of that every "<" that could start a tag (one
// followed by a letter, "/", "!" or "?") becomes a single-quote angle mark, so no
// text at all can open or close a <live>, <my-notes>, <system-reminder> or similar
// tag. Other angle brackets are ordinary text and stay as they are: a version range
// ">=1.2.3 <2.0.0", an arrow "->" or "a < b" reaches the reader as written, which a
// worker copies into a file or a command.
func neutralise(s string) string {
	s = kv.EscapeMarkup(s)
	if strings.IndexByte(s, '<') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '<' && i+1 < len(s) && tagStart(s[i+1]) {
			b.WriteString("‹")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// tagStart reports whether c, after a "<", makes the pair the start of a tag, a closing tag, a comment or a processing
// instruction.
func tagStart(c byte) bool {
	return c == '/' || c == '!' || c == '?' || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

// printable maps whitespace to a plain space and drops control, zero-width and
// bidirectional-override characters (which can hide or reorder text).
func printable(r rune) rune {
	switch {
	case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029:
		return ' '
	case r == utf8.RuneError:
		return -1
	case unicode.IsControl(r):
		return -1
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x2064, r == 0xFEFF, r >= 0x2066 && r <= 0x2069:
		return -1
	}
	return r
}

// truncRunes cuts s to at most max runes, ending in an ellipsis when it cut.
func truncRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	if len(s) <= max { // bytes <= max implies runes <= max
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// cleanText renders s as one printable line of at most max runes with harness
// markers and tag delimiters defused.
func cleanText(s string, max int) string {
	s = strings.Map(printable, s)
	s = strings.Join(strings.Fields(s), " ")
	return truncRunes(neutralise(s), max)
}

// oneLine is cleanText: every one-line field shown to other agents goes through it.
func oneLine(s string, max int) string { return cleanText(s, max) }

// cleanBlock is cleanText for multi-line text that is shown in a bounded place (a
// verifier's output, a suggestion): line breaks are kept, at most 40 lines, trailing space
// trimmed, everything else as cleanText.
func cleanBlock(s string, max int) string { return cleanBlockLines(s, max, 40) }

// cleanBlockLines is cleanBlock with at most maxLines lines (0: no line limit, for text
// whose every line matters, such as a task description or a long message).
func cleanBlockLines(s string, max, maxLines int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(neutralise(strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if r == '\n' {
				return r
			}
			return printable(r)
		}, l)), " ")
	}
	return truncRunes(strings.TrimSpace(strings.Join(lines, "\n")), max)
}

// safeToken keeps only [A-Za-z0-9._/-] (anything else becomes '?'), at most max
// characters. It is for identifiers that are not chosen by the harness, such as
// file names.
func safeToken(s string, max int) string {
	var sb strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			sb.WriteString("…")
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '/', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('?')
		}
		n++
	}
	return sb.String()
}

// inlineHead is the opening of s that fits in max runes, cut at the last line break in its second half when there is one,
// and how many runes of s are left out. s is whole (rest 0) when it fits.
func inlineHead(s string, max int) (head string, rest int) {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s, 0
	}
	cut := max
	if i := strings.LastIndex(string(r[:max]), "\n"); i >= 0 {
		if n := len([]rune(string(r[:max])[:i])); n >= max/2 {
			cut = n
		}
	}
	return strings.TrimRight(string(r[:cut]), " \n"), len(r) - cut
}

// inlineHeadBytes is inlineHead with limit in bytes, cut on a rune boundary: the head of s within limit bytes, cut at the last line
// break in its second half when there is one, and how many runes of s are left out. With limit below one rune the head is empty.
func inlineHeadBytes(s string, limit int) (head string, rest int) {
	if len(s) <= limit {
		return s, 0
	}
	cut := max(0, limit)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if i := strings.LastIndexByte(s[:cut], '\n'); i >= cut/2 && i > 0 {
		cut = i
	}
	return strings.TrimRight(s[:cut], " \n"), utf8.RuneCountInString(s[cut:])
}
