package app

import (
	"strings"

	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/tui/cell"
)

// Everything the chat prints that somebody else wrote (a model, a tool, a file, a web page, another agent, the person's own
// paste) passes here or through a widget, which does the same, before it becomes a cell.Line. The renderer cleans again when it
// takes a line (render.Inline), so this is the second gate and not the only one: what it adds is that a line break is honoured
// as a break between lines (the renderer drops one inside a span) and that tabs have a width.

// textLines is text as the lines it shows: sanitised for a terminal (tools.SanitizeForTerminal: escape sequences of every family,
// control characters, bidi overrides, invalid UTF-8), split at newlines, with tabs expanded to stops of four cells and trailing
// white space cut. A text with nothing in it is no lines.
func textLines(s string) []string {
	s = tools.SanitizeForTerminal(s)
	if s == "" {
		return nil
	}
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.IndexByte(l, '\t') >= 0 {
			l = expandTabs(l, 4)
		}
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// expandTabs replaces each tab of a line with the spaces that reach the next tab stop.
func expandTabs(s string, tw int) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tw - col%tw
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += cell.RuneWidth(r)
	}
	return b.String()
}

// firstLine is the first line of s with a mark that there is more, cut to n cells.
func firstLine(s string, n int, ellipsis string) string {
	lines := textLines(s)
	if len(lines) == 0 {
		return ""
	}
	out := strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		out += " " + ellipsis
	}
	return cutCells(out, n, ellipsis)
}

// cutCells cuts s to at most n cells, ending in the ellipsis when it had to cut.
func cutCells(s string, n int, ellipsis string) string {
	if n <= 0 {
		return ""
	}
	if cell.StringWidth(s) <= n {
		return s
	}
	return cell.Text(s).Truncate(n, ellipsis).Plain()
}

// indentLines puts pad in front of every line.
func indentLines(lines []cell.Line, pad cell.Line) []cell.Line {
	out := make([]cell.Line, len(lines))
	for i, l := range lines {
		if len(l) == 0 {
			out[i] = nil
			continue
		}
		out[i] = cell.Join(pad, l)
	}
	return out
}
