package memory

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// clean normalises a raw file into prompt text: BOM dropped, invalid UTF-8
// replaced, line endings unified to "\n", HTML comments removed (they are notes
// to human editors, not instructions, and would otherwise cost tokens on every
// request), and leading/trailing blank lines trimmed. Two files that differ only
// in these respects clean to identical text, which is what keeps the rendered
// prompt layer byte-stable.
func clean(raw []byte) string {
	s := string(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})) // UTF-8 byte order mark
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = stripComments(s)
	return strings.Trim(s, "\n")
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
