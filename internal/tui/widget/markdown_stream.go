package widget

import (
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// MDStream renders markdown that arrives in pieces, the way a model streams it, so that a terminal can print what is finished
// into its scrollback (where it stays, and is never redrawn) and redraw only what is still growing.
//
// Append the chunks as they arrive, in any size (a chunk may end inside a word, a UTF-8 sequence or an escape sequence); call
// Render whenever the screen is drawn. Render returns the lines of the document so far in two parts:
//
//   - stable: lines that will never change again, whatever text follows: the blocks that a later block has certainly begun
//     after, and, for a fenced code block that is still open, its label and the lines of it that are complete. With the same width
//     and theme, every call's stable starts with the stable of the call before: the caller keeps a count of the lines it has
//     printed and prints stable[count:].
//   - tail: the rest, which can still grow or change meaning (a paragraph becomes a heading when its underline arrives, a row
//     becomes a table). Draw it in the live region and redraw it on every call. When stable is not empty and the tail is not,
//     the tail starts with the blank line that separates them, so the blank line is not printed until the block after it is sure.
//
// Always, stable followed by tail is exactly Markdown(everything appended so far, width, theme), for any way the text was cut
// into chunks; the tests check both properties, at every step, with random chunkings. A block counts as finished when the next
// block has begun on a line that is complete (ended by a newline) and cannot be taken back by a line that is not (a table
// header that follows a paragraph needs the row under it), so stable lags the text by about a line: that is what makes it safe. The cost of a call is
// one pass over the text so far plus the rendering of what is not finished; a block is rendered once, when it finishes. A
// change of width or theme starts over (the lines of the old width are not stable at the new one).
//
// The zero value is an empty stream. Set Options before the first Append and leave them alone. An MDStream is not safe for
// concurrent use (the goroutine that draws owns it). The lines returned share memory with the stream's memory of finished
// blocks: do not modify them.
type MDStream struct {
	Options MarkdownOptions

	src []byte

	width int
	th    Theme
	valid bool
	done  []mdDone // rendered finished blocks, by index, for the width and theme above
}

type mdDone struct {
	src   string
	kind  mdKind
	lines []cell.Line
}

// Append adds a chunk of the text. Any string is fine, including an empty one.
func (s *MDStream) Append(chunk string) { s.src = append(s.src, chunk...) }

// Len is the number of bytes appended so far.
func (s *MDStream) Len() int { return len(s.src) }

// Reset forgets the text and everything remembered about it, for reuse by the next message.
func (s *MDStream) Reset() {
	s.src, s.done, s.valid = s.src[:0], nil, false
}

// Render returns the document so far as stable and tail lines (see MDStream). A width below 1 gives nothing.
func (s *MDStream) Render(width int, th Theme) (stable, tail []cell.Line) {
	if width < 1 {
		return nil, nil
	}
	if !s.valid || s.width != width || s.th != th {
		s.width, s.th, s.valid, s.done = width, th, true, nil
	}
	body, lines, starts, nl := mdSplit(string(s.src))
	blocks := mdParse(lines, 0)
	r := newMDRenderer(th, s.Options)
	n := len(blocks)
	complete := len(lines) // the lines that end in a newline: only they are final
	if !nl {
		complete--
	}
	finished := 0
	for finished < n-1 && mdFollowedSurely(&blocks[finished], &blocks[finished+1], complete) {
		finished++
	}
	parts := make([][]cell.Line, n)
	for k := range blocks {
		if k < finished {
			parts[k] = s.finishedPart(k, &blocks[k], body[starts[blocks[k].lo]:starts[blocks[k].hi]-1], r, width)
		} else {
			parts[k] = r.top(&blocks[k], width)
		}
	}
	if len(s.done) > finished {
		s.done = s.done[:finished]
	}
	all := mdJoin(parts)
	cut := joinedLen(parts[:finished])
	if finished < n && s.Options.Highlighter == nil {
		if f := mdFrozenRows(&blocks[finished], complete, r, width); f > 0 && f <= len(parts[finished]) {
			if cut > 0 {
				cut++ // the blank line before the block is certain too
			}
			cut += f
		}
	}
	if cut > 0 {
		stable = all[:cut:cut] // the cap makes a caller's append copy instead of overwriting the tail
	}
	if cut < len(all) {
		tail = all[cut:]
	}
	return stable, tail
}

// mdFollowedSurely reports whether prev, the block before next, is final: next begins on a complete line, and (when prev is a
// paragraph and next is a table header that follows it directly) so is the line after it. Until the row under it is complete,
// such a header row is a line of the paragraph, and the paragraph loses it when the row turns out to be a delimiter row. Any
// other block ends before the header row whatever the row turns out to be, so it is final as soon as next has begun (asking
// for more would un-finish a block that was final a moment ago: "# x" and "a|" make the heading final, and "a|" over "|-"
// must not take that back). A block that is separated from the one before by a blank line needs no more either: nothing that
// is typed on later lines can reach back over the blank line.
func mdFollowedSurely(prev, next *mdBlock, complete int) bool {
	if next.lo >= complete {
		return false
	}
	return prev.kind != mdkPara || next.kind != mdkTable || next.blankBefore || next.lo+1 < complete
}

// joinedLen is the number of lines mdJoin makes of parts.
func joinedLen(parts [][]cell.Line) int {
	n, nonEmpty := 0, 0
	for _, p := range parts {
		if len(p) > 0 {
			n += len(p)
			nonEmpty++
		}
	}
	if nonEmpty > 1 {
		n += nonEmpty - 1
	}
	return n
}

// finishedPart renders block k, which is final, or takes it from the memory when the same source was rendered before.
func (s *MDStream) finishedPart(k int, b *mdBlock, src string, r *mdRenderer, w int) []cell.Line {
	if k < len(s.done) && s.done[k].kind == b.kind && s.done[k].src == src {
		return s.done[k].lines
	}
	ls := r.top(b, w)
	s.done = append(s.done[:min(k, len(s.done))], mdDone{src: strings.Clone(src), kind: b.kind, lines: ls})
	return ls
}

// mdFrozenRows is how many rows of a block that is not finished will never change: for a fenced code block, its label and the
// rows of the code lines that are complete (ended by a newline; the line being typed may still turn into a closing fence or
// anything else, and so may the language on a fence line that is not complete). Other blocks freeze when they finish.
func mdFrozenRows(b *mdBlock, complete int, r *mdRenderer, w int) int {
	if b.kind != mdkCode || !b.fenced || b.lo >= complete {
		return 0
	}
	m := max(0, min(complete-(b.lo+1), len(b.code)))
	part := *b
	part.code = b.code[:m]
	return len(r.codeRows(&part, w))
}
