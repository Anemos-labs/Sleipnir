package widget

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Highlighter colours the code of a fenced code block. It gets the language (the first word of the fence's info string, never
// empty) and the code (the lines joined by "\n", no trailing newline) and answers with one styled line per line of code,
// without the line's newline and with tabs already expanded. An answer with another number of lines, or a panic, is ignored and
// the block is drawn plain. What it returns is cleaned of control characters like any other text. It must be pure: the stream
// re-renders the same block and expects the same answer, and (when a highlighter is set) freezes a code block into the
// scrollback only when the block is complete.
type Highlighter func(lang, code string) []cell.Line

// MarkdownOptions are the knobs of the markdown renderer. The zero value is what Markdown uses.
type MarkdownOptions struct {
	// Highlighter, when set, colours fenced code blocks that name a language. There is no syntax colouring without it.
	Highlighter Highlighter
}

// Markdown renders a CommonMark subset for a terminal, as lines at most width cells wide: headings (ATX and setext, one style
// per level), paragraphs (soft-wrapped by display width, hard line breaks kept), emphasis, strong, strikethrough, inline code,
// fenced and indented code blocks (a dim gutter, the language as a label, long lines wrapped with ↳ marking each
// continuation), block quotes, bullet and numbered lists (nested, with hanging indents that survive wrapping; task lists),
// pipe tables (columns by display width, alignment, cells cut with an ellipsis when the table is too wide), horizontal rules,
// links (the text, then the address dim in parentheses when it differs; never a hyperlink escape), images (the alt text in
// brackets) and bare URLs. HTML tags are shown as the text they are.
//
// It takes whatever a model can emit: the output is always valid, because nothing is an error. Unterminated fences, emphasis and
// tables are drawn as what they are so far; enormous lines are wrapped; container nesting deeper than 10 levels becomes text;
// control characters, escape sequences (OSC 52 among them), bidi overrides and invalid UTF-8 are removed before anything is
// parsed, so the output holds no control character at all; wide runes and combining marks are measured and never split. Blocks
// are separated by one blank line, with none at the start or the end. Empty input, and a width below 1, give no lines.
//
// The work is linear in the size of the input (times the nesting depth, which is capped); the output is deterministic.
func Markdown(src string, width int, th Theme) []cell.Line {
	return MarkdownWith(src, width, th, MarkdownOptions{})
}

// MarkdownWith is Markdown with options.
func MarkdownWith(src string, width int, th Theme, o MarkdownOptions) []cell.Line {
	if width < 1 {
		return nil
	}
	_, lines, _, _ := mdSplit(src)
	blocks := mdParse(lines, 0)
	r := newMDRenderer(th, o)
	parts := make([][]cell.Line, len(blocks))
	for k := range blocks {
		parts[k] = r.top(&blocks[k], width)
	}
	return mdJoin(parts)
}

// top renders one top-level block, cut to the width as a last resort.
func (r *mdRenderer) top(b *mdBlock, w int) []cell.Line {
	return clipLines(r.block(b, w, 0), w)
}

// mdJoin concatenates the parts with one blank line between them; a part with no lines leaves no trace.
func mdJoin(parts [][]cell.Line) []cell.Line {
	var out []cell.Line
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, nil)
		}
		out = append(out, p...)
	}
	return out
}

// mdSplit cleans src (see safeText) and cuts it into lines with their tabs expanded to 4-column stops. body is the cleaned
// text without its final newline, and starts[i] the offset in body where line i begins (with len(lines) entries plus one past
// the end), so the source of a range of lines is a substring of body. nl says that the text ended with a newline, which is
// whether the last line is complete. Text with no characters gives no lines.
func mdSplit(src string) (body string, lines []string, starts []int, nl bool) {
	body = safeText(src)
	if body == "" {
		return "", nil, nil, false
	}
	nl = strings.HasSuffix(body, "\n")
	body = strings.TrimSuffix(body, "\n")
	lines = strings.Split(body, "\n")
	starts = make([]int, len(lines)+1)
	off := 0
	for i, l := range lines {
		starts[i] = off
		off += len(l) + 1
		if strings.IndexByte(l, '\t') >= 0 {
			lines[i] = expandTabStops(l, 4)
		}
	}
	starts[len(lines)] = off
	return body, lines, starts, nl
}
