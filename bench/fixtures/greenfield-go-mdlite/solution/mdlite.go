// Package mdlite converts a small Markdown subset to HTML. README.md is the specification.
package mdlite

import (
	"html"
	"strings"
)

// Render converts src, written in the Markdown subset described in README.md, to HTML.
func Render(src string) string {
	lines := splitLines(strings.ReplaceAll(src, "\r\n", "\n"))
	var out strings.Builder
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case isBlank(line):
			i++
		case strings.HasPrefix(line, "```"):
			i = fencedCode(&out, lines, i)
		case isHeading(line):
			level, text := headingParts(line)
			out.WriteString(tag("h", level, inline(text)))
			i++
		case isItem(line):
			i = list(&out, lines, i)
		default:
			i = paragraph(&out, lines, i)
		}
	}
	return out.String()
}

// splitLines splits at "\n"; a final "\n" does not start another line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func isBlank(line string) bool {
	return strings.Trim(line, " \t") == ""
}

func tag(name string, level int, body string) string {
	n := string(rune('0' + level))
	return "<" + name + n + ">" + body + "</" + name + n + ">\n"
}

// headingParts returns the level and text of a heading line; ok is false when line is not a heading.
func headingParts(line string) (level int, text string) {
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || (line[level] != ' ' && line[level] != '\t') {
		return 0, ""
	}
	return level, strings.Trim(line[level:], " \t")
}

func isHeading(line string) bool {
	level, _ := headingParts(line)
	return level > 0
}

// item parses a list item line: its kind ("ul" or "ol") and its text.
func item(line string) (kind, text string, ok bool) {
	var rest string
	switch {
	case line == "":
		return "", "", false
	case strings.IndexByte("-*+", line[0]) >= 0:
		kind, rest = "ul", line[1:]
	default:
		n := 0
		for n < len(line) && line[n] >= '0' && line[n] <= '9' {
			n++
		}
		if n == 0 || n == len(line) || line[n] != '.' {
			return "", "", false
		}
		kind, rest = "ol", line[n+1:]
	}
	trimmed := strings.TrimLeft(rest, " \t")
	if len(trimmed) == len(rest) { // at least one space or tab must follow the marker
		return "", "", false
	}
	text = strings.TrimRight(trimmed, " \t")
	return kind, text, text != ""
}

func isItem(line string) bool {
	_, _, ok := item(line)
	return ok
}

// fencedCode renders the fenced block opened at lines[i] and returns the index of the next unused line.
func fencedCode(out *strings.Builder, lines []string, i int) int {
	info := strings.Trim(lines[i][3:], " \t")
	lang := info
	if k := strings.IndexAny(info, " \t"); k >= 0 {
		lang = info[:k]
	}
	out.WriteString("<pre><code")
	if lang != "" {
		out.WriteString(` class="language-` + html.EscapeString(lang) + `"`)
	}
	out.WriteString(">")
	for i++; i < len(lines) && lines[i] != "```"; i++ {
		out.WriteString(html.EscapeString(lines[i]) + "\n")
	}
	if i < len(lines) {
		i++ // the closing fence
	}
	out.WriteString("</code></pre>\n")
	return i
}

// list renders the run of items of one kind that starts at lines[i].
func list(out *strings.Builder, lines []string, i int) int {
	kind, _, _ := item(lines[i])
	out.WriteString("<" + kind + ">\n")
	for ; i < len(lines); i++ {
		k, text, ok := item(lines[i])
		if !ok || k != kind {
			break
		}
		out.WriteString("<li>" + inline(text) + "</li>\n")
	}
	out.WriteString("</" + kind + ">\n")
	return i
}

// paragraph renders the paragraph that starts at lines[i].
func paragraph(out *strings.Builder, lines []string, i int) int {
	start := i
	for i < len(lines) && !isBlank(lines[i]) && (i == start || !opensBlock(lines[i])) {
		i++
	}
	out.WriteString("<p>")
	for j := start; j < i; j++ {
		out.WriteString(inline(strings.Trim(lines[j], " \t")))
		if j < i-1 {
			if strings.HasSuffix(lines[j], "  ") {
				out.WriteString("<br>")
			}
			out.WriteString("\n")
		}
	}
	out.WriteString("</p>\n")
	return i
}

// opensBlock reports whether line starts a fenced block, a heading or a list.
func opensBlock(line string) bool {
	return strings.HasPrefix(line, "```") || isHeading(line) || isItem(line)
}
