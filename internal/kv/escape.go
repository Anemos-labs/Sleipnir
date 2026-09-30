package kv

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text that a model, a tool, a peer or a file wrote must never be able to pass for the
// prompt's own structure. Pins, notes and spine are user-role context in which the
// only structure the model has is a handful of tags ("<my-notes>", "<live>", ...), a
// "## key" header per notes section and the "[mail ...]" header of a delivered
// message; anything that can write text into those places can forge them.
//
// The helpers here are the one place that decides what "defused" means. They are
// deterministic and depend on nothing but their argument: the layer text they
// produce is part of the cached prefix, so the same input must give the same bytes on
// every run and machine. They are idempotent (escaping escaped text changes
// nothing), so the same text can be escaped where it is written and again where it
// is rendered without drifting, and they leave ordinary text alone: text with no
// structural tag, no forged marker and no hidden character comes back byte for byte.
//
// Escaping is one way and lossy at the display: "<my-notes>" becomes "‹my-notes>".
// Nothing that recall needs is ever escaped: the archive stores turns as they were,
// and the layers point at them ("recall t7") where they show a cut or an excerpt.

// structuralTags are the tag names the prompt protocol gives a meaning to. "skills"
// is deliberately not one of them: the harness writes a <skills> block into the shared
// layer itself and it is not a frame anything else is delimited by.
var structuralTags = []string{"my-notes", "history", "shared-context", "role-context", "live", "compactor-task", "peer-mail"}

var (
	// tagRe matches the start of any spelling of a structural tag: "<live", "</live",
	// "< / LIVE", "<\nmy-notes". \b makes "<lively>" and "<history_of_x>" ordinary text.
	tagRe = regexp.MustCompile(`(?i)<(\s*/?\s*)(` + strings.Join(structuralTags, "|") + `)\b`)
	// markerRe matches the start of a bracketed marker the harness writes itself
	// ("[mail m1 from be-2]", "[stop hook] ...", "[hook] ...", the "[untrusted ...]"
	// frame, the "[context from your hooks]" label of what a hook added): text that
	// starts with one could pass for a message from the harness or from another agent.
	markerRe = regexp.MustCompile(`(?i)\[(\s*/?\s*)(mail|end|system|harness|untrusted|user|assistant|tool|live|hook|stop|context\s+from\s+your\s+hooks)\b`)
	// headerRe matches a markdown header at the start of a line. A notes layer renders
	// "## <key>" at column 0, so a line of note text that looks the same forges a section.
	headerRe = regexp.MustCompile(`(?m)^(#{1,6})([ \t]|$)`)
)

// hidden reports whether r is a character that renders as nothing (or reorders text)
// and so can carry a message a human reviewer never sees: control characters, format
// characters (zero width, bidi, tag characters, soft hyphen, invisible operators),
// variation selectors, script fillers and the private plane-14 range.
func hidden(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		return true
	case unicode.Is(unicode.Cf, r):
		return true
	}
	switch {
	case r == 0x034F, r == 0x115F, r == 0x1160, r == 0x17B4, r == 0x17B5, r == 0x3164, r == 0xFFA0:
		return true // combining grapheme joiner and the Hangul/Khmer fillers
	case r >= 0x180B && r <= 0x180D, r >= 0xFE00 && r <= 0xFE0F:
		return true // variation selectors
	case r >= 0xE0000 && r <= 0xE0FFF:
		return true // tag characters and the variation selectors supplement
	case r == 0xFFFE || r == 0xFFFF:
		return true
	}
	return false
}

// lineBreak maps the characters that end a line in some renderer to "\n".
func lineBreak(r rune) bool { return r == '\r' || r == 0x85 || r == 0x2028 || r == 0x2029 }

// needsEscape is the cheap check that lets ordinary text skip the regular expressions:
// only these bytes can start anything the slow path would change.
func needsEscape(s string, headers bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '<' || c == '[' || c >= 0x80 || c == 0x7f:
			return true
		case c < 0x20 && c != '\n' && c != '\t':
			return true
		case headers && c == '#' && (i == 0 || s[i-1] == '\n'):
			return true
		}
	}
	return false
}

// escape is the shared implementation: normalise, drop hidden characters, defuse tags
// and markers and, for multi-line layer bodies, section-header lookalikes.
func escape(s string, headers bool) string {
	if !needsEscape(s, headers) {
		return s
	}
	s = strings.ToValidUTF8(s, "\U0000FFFD")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case lineBreak(r):
			return '\n'
		case r == '\v' || r == '\f':
			return ' '
		case hidden(r):
			return -1
		}
		return r
	}, s)
	// The order matters: hidden characters go first, so a zero-width character between
	// "<" and the tag name cannot hide a tag from the expressions below.
	if strings.IndexByte(s, '<') >= 0 {
		s = tagRe.ReplaceAllString(s, "‹$1$2")
	}
	if strings.IndexByte(s, '[') >= 0 {
		s = markerRe.ReplaceAllString(s, "($1$2")
	}
	if headers && strings.IndexByte(s, '#') >= 0 {
		s = headerRe.ReplaceAllString(s, `\$1$2`)
	}
	return s
}

// EscapeUntrusted makes multi-line text that someone else wrote safe to place inside
// a layer, a brief or a hot view: structural tags are defused ("</my-notes>" becomes
// "‹/my-notes>"), so are harness markers ("[mail" becomes "(mail") and section
// headers at the start of a line ("## instructions" becomes "\## instructions"),
// line breaks are normalised and hidden characters are removed. Text with none of
// those comes back unchanged.
func EscapeUntrusted(s string) string { return escape(s, true) }

// EscapeMarkup is EscapeUntrusted without the header rule and without touching line
// layout: for text placed inside a line or a block that is not a layer body (a mail, a
// hot view line, a task card), where "#" at the start of a line is ordinary text.
func EscapeMarkup(s string) string { return escape(s, false) }

// escapeProtocol is EscapeUntrusted without the header rule, for layers whose own text
// is markdown (the shared pin holds "### AGENTS.md (project, unverified)" headers the
// harness wrote and that must stay).
func escapeProtocol(s string) string { return escape(s, false) }

// EscapeLine is EscapeUntrusted for a value that must stay on one line (a spine digest,
// a fragment of the compactor's brief, a status): runs of white space collapse to a
// single space and the result is cut to at most max runes, ending in an ellipsis when
// it was cut (max <= 0 means no cut).
func EscapeLine(s string, max int) string {
	s = strings.Join(strings.Fields(escape(s, false)), " ")
	if cut := cutRunes(s, max); cut != s {
		// A cut can end a word where it did not end before ("<liveness" becomes
		// "<live…"), so what is left is defused again.
		s = escape(cut, false)
	}
	return s
}

// cutRunes shortens s to at most max runes, ending in an ellipsis when it cut.
func cutRunes(s string, max int) string {
	if max <= 0 || len(s) <= max { // bytes <= max implies runes <= max
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// SectionKey validates the name of a notes section: lower case letters, digits and
// hyphens, at most 32 characters, starting with a letter or digit. Keys become the
// "## key" header of a section, so anything else (a newline, a tag, a look-alike
// Unicode letter) would let a name pose as another section.
func SectionKey(key string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" || len(k) > 32 {
		return "", false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return "", false
		}
	}
	return k, true
}

// GuardFrame defuses the structural tags inside a harness-written frame such as the
// hot view ("<live board="v12">...</live>") while keeping the frame's own opening and
// closing tags. The interior of such a frame is assembled from board state that
// other agents wrote; whatever sanitising the assembler did, the frame the model
// receives has exactly one opening and one closing tag. Text that is not a single
// well-formed frame is escaped whole. Text without a forged tag comes back unchanged.
func GuardFrame(text, tag string) string {
	trimmed := strings.TrimSpace(text)
	open, closeTag := "<"+tag, "</"+tag+">"
	if !strings.HasPrefix(trimmed, open) || !strings.HasSuffix(trimmed, closeTag) {
		return escapeProtocol(text)
	}
	// The opening tag ends at the first '>' and must start with the tag name itself.
	rest := trimmed[len(open):]
	if rest == "" || (rest[0] != '>' && rest[0] != ' ') {
		return escapeProtocol(text)
	}
	end := strings.IndexByte(trimmed, '>')
	if end < 0 || end+1 > len(trimmed)-len(closeTag) {
		return escapeProtocol(text)
	}
	head, body, tail := trimmed[:end+1], trimmed[end+1:len(trimmed)-len(closeTag)], closeTag
	if strings.ContainsAny(head, "\n\r") || len(head) > 200 {
		return escapeProtocol(text)
	}
	guarded := escapeProtocol(body)
	if guarded == body {
		return text
	}
	lead := text[:strings.Index(text, trimmed)]
	return lead + head + guarded + tail + text[len(lead)+len(trimmed):]
}
