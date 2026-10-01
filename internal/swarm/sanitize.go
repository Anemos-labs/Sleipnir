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
// m1" becomes "(mail m1". On top of that every "<" and ">" becomes a single-quote
// angle mark, so no text at all can open or close a <live>, <my-notes> or similar tag.
func neutralise(s string) string {
	s = kv.EscapeMarkup(s)
	if strings.ContainsAny(s, "<>") {
		s = strings.NewReplacer("<", "‹", ">", "›").Replace(s)
	}
	return s
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

// cleanBlock is cleanText for multi-line text (task descriptions): line breaks are
// kept, at most 40 lines, trailing space trimmed, everything else as cleanText.
func cleanBlock(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 40 {
		lines = lines[:40]
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
