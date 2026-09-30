package widget

import (
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// The renderer: blocks in, styled lines out. Each block renders to lines of its own, to the width it is given; a container
// renders its children to a narrower width and puts its prefix (a quote bar, a list marker and the hanging indent) in front of
// the lines it gets back. That is why hanging indents stay right under wrapping: the children never know about the prefix.

type mdRenderer struct {
	th   Theme
	g    *glyphSet
	opts MarkdownOptions
	base cell.Style // the style text starts from: Text, or the block quote's
}

func newMDRenderer(th Theme, o MarkdownOptions) *mdRenderer {
	return &mdRenderer{th: th, g: th.glyphs(), opts: o, base: th.Text}
}

// seq renders sibling blocks one after the other, with a blank line between them when blank is true. A block that renders to
// nothing (an empty heading) leaves no trace, not even a separator.
func (r *mdRenderer) seq(bs []mdBlock, w, listDepth int, blank bool) []cell.Line {
	var out []cell.Line
	for i := range bs {
		ls := r.block(&bs[i], w, listDepth)
		if len(ls) == 0 {
			continue
		}
		if len(out) > 0 && blank {
			out = append(out, nil)
		}
		out = append(out, ls...)
	}
	return out
}

func (r *mdRenderer) block(b *mdBlock, w, listDepth int) []cell.Line {
	switch b.kind {
	case mdkPara:
		return r.flow(b.text, w, r.base)
	case mdkHeading:
		return r.flow(b.text, w, composeStyle(r.base, r.th.HeadingStyle(b.level)))
	case mdkRule:
		return []cell.Line{cell.Styled(r.th.Faint, repeatText(r.g.rule, w))}
	case mdkCode:
		return r.code(b, w)
	case mdkQuote:
		return r.quote(b, w, listDepth)
	case mdkList:
		return r.list(b, w, listDepth)
	case mdkTable:
		return r.table(b.table, w)
	}
	return nil
}

// runStyle is the style of a run: the base of the block it is in, with the inline marks laid over it.
func (r *mdRenderer) runStyle(base cell.Style, f uint8) cell.Style {
	st := base
	if f&mdBold != 0 {
		st = composeStyle(st, r.th.Strong)
	}
	if f&mdItalic != 0 {
		st = composeStyle(st, r.th.Emph)
	}
	if f&mdStrike != 0 {
		st = st.With(cell.Strike)
	}
	if f&mdLinkText != 0 {
		st = composeStyle(st, r.th.Link)
	}
	if f&mdURLNote != 0 {
		st = composeStyle(st, r.th.Dim)
	}
	if f&mdImage != 0 {
		st = composeStyle(st, r.th.Info)
	}
	if f&mdCodeSpan != 0 {
		st = composeStyle(st, r.th.Code)
	}
	return st
}

// inlineLines renders inline source to one line per hard-broken segment (not yet wrapped).
func (r *mdRenderer) inlineLines(src string, base cell.Style) []cell.Line {
	runs := mdInline(src, r.g.ellipsis)
	var out []cell.Line
	var cur cell.Line
	var sb strings.Builder
	var curStyle cell.Style
	open := false
	flush := func() {
		if open {
			cur = append(cur, cell.Span{Text: sb.String(), Style: curStyle})
			sb = strings.Builder{}
			open = false
		}
	}
	for _, run := range runs {
		if run.br {
			flush()
			out = append(out, cur)
			cur = nil
			continue
		}
		text := run.text
		if run.flags&mdCodeSpan != 0 && r.th.Mono {
			text = "`" + text + "`" // without colour, the backticks are what marks code
		}
		st := r.runStyle(base, run.flags)
		if !open || curStyle != st {
			flush()
			curStyle, open = st, true
		}
		sb.WriteString(text)
	}
	flush()
	return append(out, cur)
}

// flow renders inline source as wrapped lines: a paragraph or a heading.
func (r *mdRenderer) flow(src string, w int, base cell.Style) []cell.Line {
	var out []cell.Line
	for _, l := range r.inlineLines(src, base) {
		out = append(out, l.Wrap(w, 0)...)
	}
	for len(out) > 0 && out[len(out)-1].Width() == 0 {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0].Width() == 0 {
		out = out[1:]
	}
	return out
}

// code renders a code block: a dim gutter, the language as a label line, the code on the theme's CodeBlock background
// (a slab the width of the text area), long lines cut at the cell boundary with ↳ marking each continuation. An empty block is
// one empty row of the slab.
func (r *mdRenderer) code(b *mdBlock, w int) []cell.Line {
	if out := r.codeRows(b, w); len(out) > 0 {
		return out
	}
	return []cell.Line{r.codeRow(nil, w)}
}

func (r *mdRenderer) codeRow(l cell.Line, w int) cell.Line {
	slab := r.th.CodeBlock
	bar := cell.Span{Text: r.g.codeBar + " ", Style: composeStyle(slab, r.th.Faint)}
	l = append(cell.Line{bar}, overlayStyle(l, slab)...)
	if paintsBlank(slab) {
		l = l.Pad(w, slab)
	}
	return l
}

// codeRows is the label row, if the block has a language, and the rows of the code, without the fallback row of an empty
// block. Row i of the code depends on line i alone, which is what lets the stream freeze a long block line by line.
func (r *mdRenderer) codeRows(b *mdBlock, w int) []cell.Line {
	th, g := r.th, r.g
	bodyW := max(1, w-2)
	var out []cell.Line
	if b.lang != "" {
		label := cell.Styled(composeStyle(th.CodeBlock, composeStyle(th.Dim, cell.Style{Attr: cell.Italic})), safeOneLine(b.lang))
		out = append(out, r.codeRow(label.Truncate(bodyW, g.ellipsis), w))
	}
	cont := cell.Span{Text: g.cont, Style: composeStyle(th.CodeBlock, th.Faint)}
	for _, l := range r.highlight(b) {
		for _, piece := range cutRows(l, bodyW, cont) {
			out = append(out, r.codeRow(piece, w))
		}
	}
	return out
}

// highlight returns the code lines as styled lines: from the Highlighter hook when there is one that answers with one line per
// line of code, else plain. The hook is caller code: a panic in it, or an answer of the wrong shape, costs the colours and
// nothing else, and whatever it returns is cleaned like any other text.
func (r *mdRenderer) highlight(b *mdBlock) []cell.Line {
	plain := func() []cell.Line {
		ls := make([]cell.Line, len(b.code))
		for i, c := range b.code {
			ls[i] = cell.Text(safeText(c))
		}
		return ls
	}
	if r.opts.Highlighter == nil || b.lang == "" || len(b.code) == 0 {
		return plain()
	}
	got := safeHighlight(r.opts.Highlighter, b.lang, strings.Join(b.code, "\n"))
	if len(got) != len(b.code) {
		return plain()
	}
	for i := range got {
		got[i] = safeLine(got[i])
	}
	return got
}

func safeHighlight(h Highlighter, lang, code string) (out []cell.Line) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	return h(lang, code)
}

// mdPrefixed puts a prefix in front of every line: first on the first line, rest on the others (a blank line gets rest without
// its trailing spaces, so quote bars run through blank lines and nothing else leaves trailing space).
func mdPrefixed(ls []cell.Line, first, rest cell.Span) []cell.Line {
	out := make([]cell.Line, len(ls))
	for i, l := range ls {
		p := rest
		if i == 0 {
			p = first
		}
		if l.Width() == 0 {
			p.Text = strings.TrimRight(p.Text, " ")
			if p.Text == "" {
				out[i] = nil
				continue
			}
			out[i] = cell.Line{p}
			continue
		}
		out[i] = append(cell.Line{p}, l...)
	}
	return out
}

// mdTrimRight returns l without the spaces that end it (a copy: l is not modified).
func mdTrimRight(l cell.Line) cell.Line {
	out := append(cell.Line(nil), l...)
	for len(out) > 0 {
		last := &out[len(out)-1]
		last.Text = strings.TrimRight(last.Text, " ")
		if last.Text != "" {
			break
		}
		out = out[:len(out)-1]
	}
	return out
}

func (r *mdRenderer) quote(b *mdBlock, w, listDepth int) []cell.Line {
	bar := cell.Span{Text: r.g.quote + " ", Style: r.th.Quote}
	inner := w - 2
	if inner < 6 { // no room for a bar: flatten rather than squeeze the text to nothing
		return r.seq(b.kids, w, listDepth, true)
	}
	kid := *r
	kid.base = composeStyle(r.base, r.th.Quote)
	return mdPrefixed(kid.seq(b.kids, inner, listDepth, true), bar, bar)
}

func (r *mdRenderer) list(b *mdBlock, w, depth int) []cell.Line {
	markers := make([]string, len(b.items))
	markW := 0
	for i := range b.items {
		if b.ordered {
			markers[i] = strconv.Itoa(b.start+i) + "."
		} else {
			markers[i] = r.g.bullets[depth%len(r.g.bullets)]
		}
		markW = max(markW, cell.StringWidth(markers[i]))
	}
	var out []cell.Line
	for i, it := range b.items {
		marker := cell.Span{Text: repeatText(" ", markW-cell.StringWidth(markers[i])) + markers[i] + " ", Style: r.th.Dim}
		var box cell.Span
		switch it.task {
		case 1:
			box = cell.Span{Text: "[ ] ", Style: r.th.Dim}
		case 2:
			box = cell.Span{Text: "[x] ", Style: composeStyle(r.th.Good, cell.Style{Attr: cell.Bold})}
		}
		head := cell.Line{marker}
		if box.Text != "" {
			if !b.ordered { // the checkbox takes the bullet's place
				head = cell.Line{box}
			} else {
				head = append(head, box)
			}
		}
		hw := head.Width()
		kw := w - hw
		if kw < 6 { // too narrow for a hanging indent: the item's text gets the whole width
			kw, head, hw = w, nil, 0
		}
		ls := r.seq(it.kids, kw, depth+1, b.loose)
		if len(ls) == 0 {
			ls = []cell.Line{nil} // an empty item is its marker
		}
		if b.loose && len(out) > 0 {
			out = append(out, nil)
		}
		if hw == 0 {
			out = append(out, ls...)
			continue
		}
		pad := cell.Span{Text: repeatText(" ", hw)}
		for j, l := range ls {
			switch {
			case j == 0 && l.Width() == 0:
				out = append(out, mdTrimRight(head))
			case j == 0:
				out = append(out, append(append(cell.Line(nil), head...), l...))
			case l.Width() == 0:
				out = append(out, nil)
			default:
				out = append(out, append(cell.Line{pad}, l...))
			}
		}
	}
	return out
}
