package widget

import (
	"strconv"
	"strings"
)

// The block parser: lines in, a tree of blocks out. It is a CommonMark subset written for what models emit, and it never fails:
// every line belongs to some block, and every construct that does not parse is text. Containers (block quotes, list items) are
// parsed by stripping their prefix and recursing on the remaining lines, so the work is linear in the text times the nesting
// depth, which is capped.
//
// What it does not do, on purpose: reference-style links and footnotes (their text stays literal), and HTML blocks (tags are
// text). The rules that differ from CommonMark, all to be kind to model output: a list item nested by two or more spaces
// counts as nested even under a long marker ("1. x" with "  - y" under it), and a table may directly follow a paragraph line.
//
// The parse of a block depends only on its own lines. The stream relies on this: a block that is followed by another block
// can never change again.

type mdKind uint8

const (
	mdkPara mdKind = iota + 1
	mdkHeading
	mdkRule
	mdkCode
	mdkQuote
	mdkList
	mdkTable
)

// mdMaxDepth caps the nesting of containers. Deeper lines are parsed as paragraph text, which keeps hostile input from making
// the layout narrower than it is tall.
const mdMaxDepth = 10

type mdBlock struct {
	kind        mdKind
	lo, hi      int  // the lines of the parent's slice this block came from
	blankBefore bool // a blank line came between it and the block before
	level       int  // heading level 1..6
	text        string
	lang        string
	fenced      bool
	closed      bool // a fenced block whose closing fence was seen
	code        []string
	kids        []mdBlock // block quote
	items       []mdItem  // list
	ordered     bool
	start       int
	loose       bool
	table       *mdTable
}

type mdItem struct {
	task int // 0 not a task, 1 open "[ ]", 2 done "[x]"
	kids []mdBlock
}

// mdIndent counts leading ASCII spaces in a Markdown line.
func mdIndent(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// mdBlank reports whether a Markdown line consists entirely of recognized indentation.
func mdBlank(s string) bool { return mdIndent(s) == len(s) }

// mdParse parses lines (tabs already expanded) into blocks.
func mdParse(lines []string, depth int) []mdBlock {
	var out []mdBlock
	blank := false
	for i := 0; i < len(lines); {
		if mdBlank(lines[i]) {
			blank = true
			i++
			continue
		}
		start := i
		var b mdBlock
		if mdIndent(lines[i]) >= 4 {
			b, i = mdIndentedCode(lines, i)
		} else {
			b, i = mdBlockAt(lines, i, depth)
		}
		if i <= start { // cannot happen: every constructor consumes a line. Guard against an endless loop all the same.
			i = start + 1
		}
		b.lo, b.hi, b.blankBefore = start, i, blank && len(out) > 0
		blank = false
		out = append(out, b)
	}
	return out
}

func mdBlockAt(lines []string, i, depth int) (mdBlock, int) {
	ln := lines[i]
	if f, ok := mdFenceOpen(ln); ok {
		return mdFenced(lines, i, f)
	}
	if lvl, txt, ok := mdATX(ln); ok {
		return mdBlock{kind: mdkHeading, level: lvl, text: txt}, i + 1
	}
	if mdIsRule(ln) {
		return mdBlock{kind: mdkRule}, i + 1
	}
	if depth < mdMaxDepth {
		if _, ok := mdQuoteLine(ln); ok {
			return mdQuote(lines, i, depth)
		}
		if m, ok := mdListMarker(ln); ok {
			return mdListAt(lines, i, depth, m)
		}
	}
	if t, next, ok := mdTableAt(lines, i); ok {
		return mdBlock{kind: mdkTable, table: t}, next
	}
	return mdParagraph(lines, i, depth)
}

// ---- line classifiers ----

func mdATX(line string) (level int, text string, ok bool) {
	ind := mdIndent(line)
	if ind > 3 {
		return 0, "", false
	}
	s := line[ind:]
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n < 1 || n > 6 || (n < len(s) && s[n] != ' ') {
		return 0, "", false
	}
	s = strings.TrimSpace(s[n:])
	if t := strings.TrimRight(s, "#"); t == "" {
		s = ""
	} else if t != s && t[len(t)-1] == ' ' {
		s = strings.TrimRight(t, " ")
	}
	return n, s, true
}

func mdIsRule(line string) bool {
	ind := mdIndent(line)
	if ind > 3 {
		return false
	}
	s := strings.TrimRight(line[ind:], " ")
	if len(s) < 3 || (s[0] != '-' && s[0] != '*' && s[0] != '_') {
		return false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case s[0]:
			n++
		case ' ':
		default:
			return false
		}
	}
	return n >= 3
}

func mdSetext(line string) (level int, ok bool) {
	ind := mdIndent(line)
	if ind > 3 {
		return 0, false
	}
	s := strings.TrimRight(line[ind:], " ")
	if s == "" || (s[0] != '=' && s[0] != '-') {
		return 0, false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return 0, false
		}
	}
	if s[0] == '=' {
		return 1, true
	}
	return 2, true
}

type mdFence struct {
	indent int
	ch     byte
	n      int
	info   string
}

func mdFenceOpen(line string) (mdFence, bool) {
	ind := mdIndent(line)
	if ind > 3 || len(line)-ind < 3 {
		return mdFence{}, false
	}
	s := line[ind:]
	c := s[0]
	if c != '`' && c != '~' {
		return mdFence{}, false
	}
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	if n < 3 {
		return mdFence{}, false
	}
	info := strings.TrimSpace(s[n:])
	if c == '`' && strings.Contains(info, "`") {
		return mdFence{}, false
	}
	return mdFence{indent: ind, ch: c, n: n, info: info}, true
}

// closes reports whether line is a closing fence for f: the same character, at least as many, nothing else.
func (f mdFence) closes(line string) bool {
	ind := mdIndent(line)
	if ind > 3 {
		return false
	}
	s := strings.TrimRight(line[ind:], " ")
	if len(s) < f.n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != f.ch {
			return false
		}
	}
	return true
}

func mdQuoteLine(line string) (inner string, ok bool) {
	ind := mdIndent(line)
	if ind > 3 || ind >= len(line) || line[ind] != '>' {
		return "", false
	}
	s := line[ind+1:]
	if strings.HasPrefix(s, " ") {
		s = s[1:]
	}
	return s, true
}

type mdMarker struct {
	indent  int
	ordered bool
	bullet  byte // '-', '*', '+' or, for an ordered list, the delimiter '.' or ')'
	num     int
	content int    // the column where the item's content starts
	rest    string // the first line's content
	empty   bool
}

func mdListMarker(line string) (m mdMarker, ok bool) {
	ind := mdIndent(line)
	if ind > 3 || ind >= len(line) {
		return m, false
	}
	s := line[ind:]
	width := 0
	switch c := s[0]; {
	case c == '-' || c == '+' || c == '*':
		m.bullet, width = c, 1
	case c >= '0' && c <= '9':
		d := 0
		for d < len(s) && s[d] >= '0' && s[d] <= '9' {
			d++
		}
		if d > 9 || d >= len(s) || (s[d] != '.' && s[d] != ')') {
			return m, false
		}
		m.ordered, m.bullet = true, s[d]
		m.num, _ = strconv.Atoi(s[:d])
		width = d + 1
	default:
		return m, false
	}
	after := s[width:]
	if after != "" && after[0] != ' ' {
		return m, false
	}
	sp := mdIndent(after)
	m.indent = ind
	switch {
	case sp == len(after): // nothing after the marker
		m.empty, m.content = true, ind+width+1
	case sp > 4: // five or more spaces: the content is indented code, the marker keeps one
		m.content, m.rest = ind+width+1, after[1:]
	default:
		m.content, m.rest = ind+width+sp, after[sp:]
	}
	return m, true
}

// mdInterrupts reports whether a line starts a block that may interrupt a paragraph (and so ends it, and is not a lazy
// continuation line). An empty list item, and an ordered item that does not start at 1, may not.
func mdInterrupts(line string, depth int) bool {
	if mdIndent(line) >= 4 {
		return false
	}
	if _, ok := mdFenceOpen(line); ok {
		return true
	}
	if _, _, ok := mdATX(line); ok {
		return true
	}
	if mdIsRule(line) {
		return true
	}
	if depth < mdMaxDepth {
		if _, ok := mdQuoteLine(line); ok {
			return true
		}
		if m, ok := mdListMarker(line); ok && !m.empty && (!m.ordered || m.num == 1) {
			return true
		}
	}
	return false
}

// mdStartsBlock reports whether a line that does not continue an enclosing container (a quote, a list item) starts a block of
// the level outside it, and so ends the container instead of being a lazy continuation of its paragraph. Unlike mdInterrupts
// it has no exceptions for list items: "2. x" under a nested item is the next item of the outer list, not more text.
func mdStartsBlock(line string, depth int) bool {
	if mdIndent(line) >= 4 {
		return false
	}
	if _, ok := mdFenceOpen(line); ok {
		return true
	}
	if _, _, ok := mdATX(line); ok || mdIsRule(line) {
		return true
	}
	if depth < mdMaxDepth {
		if _, ok := mdQuoteLine(line); ok {
			return true
		}
		return mdIsMarker(line)
	}
	return false
}

// mdLazy follows the lines of a container to know whether the last one is paragraph text, which a line with less indentation
// may continue ("lazy continuation"), and whether it is inside a fenced block, which nothing may continue.
type mdLazy struct {
	fence *mdFence
	para  bool
}

func (z *mdLazy) feed(line string) {
	switch {
	case z.fence != nil:
		if z.fence.closes(line) {
			z.fence = nil
		}
		z.para = false
	case mdBlank(line):
		z.para = false
	default:
		if f, ok := mdFenceOpen(line); ok {
			z.fence, z.para = &f, false
			return
		}
		if _, _, ok := mdATX(line); ok || mdIsRule(line) {
			z.para = false
		} else if !z.para {
			z.para = mdIndent(line) < 4
		}
	}
}

// ---- blocks ----

// mdFirstWord extracts the language-like prefix of a code-fence info string before whitespace or
// an attribute brace.
func mdFirstWord(info string) string {
	if i := strings.IndexAny(info, " \t{"); i >= 0 {
		info = info[:i]
	}
	return info
}

// mdStripIndent removes up to n leading indentation bytes; n must be nonnegative.
func mdStripIndent(line string, n int) string {
	k := min(mdIndent(line), n)
	return line[k:]
}

func mdFenced(lines []string, i int, f mdFence) (mdBlock, int) {
	b := mdBlock{kind: mdkCode, fenced: true, lang: mdFirstWord(f.info)}
	for i++; i < len(lines); i++ {
		if f.closes(lines[i]) {
			b.closed = true
			i++
			break
		}
		b.code = append(b.code, mdStripIndent(lines[i], f.indent))
	}
	return b, i
}

func mdIndentedCode(lines []string, i int) (mdBlock, int) {
	b := mdBlock{kind: mdkCode}
	end := i
	var pending []string
	for ; i < len(lines); i++ {
		ln := lines[i]
		if mdBlank(ln) {
			pending = append(pending, "")
			continue
		}
		if mdIndent(ln) < 4 {
			break
		}
		b.code = append(b.code, pending...)
		pending = nil
		b.code = append(b.code, ln[4:])
		end = i + 1
	}
	return b, end
}

func mdQuote(lines []string, i, depth int) (mdBlock, int) {
	var inner []string
	var z mdLazy
	for i < len(lines) {
		ln := lines[i]
		if s, ok := mdQuoteLine(ln); ok {
			inner = append(inner, s)
			z.feed(s)
			i++
			continue
		}
		if mdBlank(ln) || !z.para || z.fence != nil || mdStartsBlock(ln, depth) {
			break
		}
		inner = append(inner, ln) // lazy continuation of the paragraph inside the quote
		i++
	}
	return mdBlock{kind: mdkQuote, kids: mdParse(inner, depth+1)}, i
}

func mdListAt(lines []string, i, depth int, first mdMarker) (mdBlock, int) {
	b := mdBlock{kind: mdkList, ordered: first.ordered, start: first.num}
	cur := first
	for {
		item, next := mdItemAt(lines, i, depth, cur)
		for k := 1; k < len(item.kids); k++ {
			if item.kids[k].blankBefore {
				b.loose = true
			}
		}
		b.items = append(b.items, item)
		i = next
		j, blanks := i, 0
		for j < len(lines) && mdBlank(lines[j]) {
			j++
			blanks++
		}
		if j >= len(lines) || mdIsRule(lines[j]) {
			break
		}
		m, ok := mdListMarker(lines[j])
		if !ok || m.ordered != first.ordered || m.bullet != first.bullet {
			break
		}
		if blanks > 0 {
			b.loose = true
		}
		i, cur = j, m
	}
	return b, i
}

// mdItemAt gathers the lines of the item whose marker is on lines[i], strips their indentation and parses them as blocks. It
// returns the index after the item's last line (blank lines after it are left for the caller).
func mdItemAt(lines []string, i, depth int, m mdMarker) (mdItem, int) {
	content := []string{m.rest}
	var z mdLazy
	z.feed(m.rest)
	end := i + 1
	pending := 0
	for j := i + 1; j < len(lines); j++ {
		ln := lines[j]
		if mdBlank(ln) {
			pending++
			continue
		}
		n := mdIndent(ln)
		var text string
		inside := true
		switch {
		case n >= m.content:
			text = ln[m.content:]
		case n >= m.indent+2 && (pending > 0 || mdIsMarker(ln)):
			text = ln[n:] // nested by two spaces or more: under a long marker this is what the writer meant
		case pending == 0 && z.para && z.fence == nil && !mdStartsBlock(ln, depth):
			text = ln[n:] // lazy continuation of the item's paragraph
		default:
			inside = false
		}
		if !inside {
			break
		}
		for ; pending > 0; pending-- {
			content = append(content, "")
			z.feed("")
		}
		content = append(content, text)
		z.feed(text)
		end = j + 1
	}
	it := mdItem{}
	switch c := content[0]; {
	case strings.HasPrefix(c, "[ ]") && (len(c) == 3 || c[3] == ' '):
		it.task, content[0] = 1, strings.TrimLeft(c[3:], " ")
	case (strings.HasPrefix(c, "[x]") || strings.HasPrefix(c, "[X]")) && (len(c) == 3 || c[3] == ' '):
		it.task, content[0] = 2, strings.TrimLeft(c[3:], " ")
	}
	it.kids = mdParse(content, depth+1)
	return it, end
}

// mdIsMarker reports whether a line begins with a recognized Markdown list marker.
func mdIsMarker(line string) bool { _, ok := mdListMarker(line); return ok }

func mdParagraph(lines []string, i, depth int) (mdBlock, int) {
	start := i
	var text []string
	for ; i < len(lines); i++ {
		ln := lines[i]
		if i > start {
			if mdBlank(ln) {
				break
			}
			if lvl, ok := mdSetext(ln); ok {
				return mdBlock{kind: mdkHeading, level: lvl, text: strings.Join(text, "\n")}, i + 1
			}
			if mdInterrupts(ln, depth) || mdTableInterrupts(lines, i, depth) {
				break
			}
		}
		text = append(text, strings.TrimLeft(ln, " "))
	}
	return mdBlock{kind: mdkPara, text: strings.TrimRight(strings.Join(text, "\n"), " ")}, i
}
