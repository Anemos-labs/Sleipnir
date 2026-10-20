package fs

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// maxListedHeadings bounds how many headings a failed section lookup names, so a
// huge document cannot flood the result.
const maxListedHeadings = 40

// mdHeading is one ATX heading of a Markdown file: its level (1-6), its text with
// the markers and closing hashes removed, and its 1-based line.
type mdHeading struct {
	level int
	text  string
	line  int
}

// mdHeadings lists the ATX headings of body in order. Lines inside fenced code
// blocks (``` or ~~~) are not headings, and neither is a line indented by four or
// more spaces. Setext headings (text underlined with = or -) are not recognised.
func mdHeadings(body []byte) []mdHeading {
	var out []mdHeading
	fence := ""
	line := 0
	for len(body) > 0 {
		line++
		var l []byte
		if i := bytes.IndexByte(body, '\n'); i >= 0 {
			l, body = body[:i], body[i+1:]
		} else {
			l, body = body, nil
		}
		s := strings.TrimRight(string(l), "\r")
		trimmed := strings.TrimLeft(s, " ")
		if len(s)-len(trimmed) >= 4 {
			continue
		}
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.TrimSpace(strings.TrimLeft(trimmed, fence[:1])) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			c := trimmed[:1]
			n := len(trimmed) - len(strings.TrimLeft(trimmed, c))
			fence = strings.Repeat(c, n)
			continue
		}
		level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
		if level < 1 || level > 6 {
			continue
		}
		rest := trimmed[level:]
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
			continue
		}
		text := strings.TrimSpace(rest)
		if t := strings.TrimRight(text, "#"); t != text && (t == "" || strings.HasSuffix(t, " ") || strings.HasSuffix(t, "\t")) {
			text = strings.TrimSpace(t)
		}
		out = append(out, mdHeading{level: level, text: text, line: line})
	}
	return out
}

// inlineLink matches a Markdown link or image, [text](target) or ![alt](target),
// whose rendered text is what an anchor is made from.
var inlineLink = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)

// headingSlug is a heading's anchor as GitHub writes it: links reduced to their
// text, lower case, punctuation other than '-' and '_' removed, spaces turned
// into '-'.
func headingSlug(s string) string {
	s = inlineLink.ReplaceAllString(s, "$1")
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r == ' ' || r == '-':
			b.WriteByte('-')
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normHeading compares heading text without case or surrounding space and with
// runs of white space as one.
func normHeading(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// findSection locates the section named want among hs: the heading whose text
// matches want (ignoring case and spacing), or else whose anchor slug does; a
// leading '#' on want is ignored. It returns the section's first and last line;
// the section runs to the line before the next heading of the same or a higher
// level, or to the end of the file (total lines). The first match wins when two
// headings share a name. ok is false when nothing matches.
func findSection(hs []mdHeading, want string, total int) (first, last int, ok bool) {
	want = strings.TrimLeft(strings.TrimSpace(want), "#")
	idx := -1
	for i, h := range hs {
		if normHeading(h.text) == normHeading(want) {
			idx = i
			break
		}
	}
	if idx < 0 {
		// GitHub suffixes repeated anchors in document order: setup, setup-1, setup-2.
		slug := headingSlug(want)
		seen := map[string]int{}
		for i, h := range hs {
			a := headingSlug(h.text)
			if n := seen[a]; n > 0 {
				seen[a] = n + 1
				a = fmt.Sprintf("%s-%d", a, n)
			} else {
				seen[a] = 1
			}
			if slug != "" && a == slug {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return 0, 0, false
	}
	first, last = hs[idx].line, total
	for _, h := range hs[idx+1:] {
		if h.level <= hs[idx].level {
			last = h.line - 1
			break
		}
	}
	return first, last, true
}

// headingList names up to maxListedHeadings headings, indented by level, for a
// result that tells the model which sections a file has.
func headingList(hs []mdHeading) string {
	var b strings.Builder
	for i, h := range hs {
		if i == maxListedHeadings {
			fmt.Fprintf(&b, "\n  … and %d more", len(hs)-i)
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  " + strings.Repeat("  ", h.level-1) + strings.Repeat("#", h.level) + " " + displayLine([]byte(h.text)))
	}
	return b.String()
}
