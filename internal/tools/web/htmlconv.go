package web

import (
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// HTML to compact markdown-ish text.
//
// The output is read by a model, so structure that carries meaning (headings,
// links, lists, code, table rows) is kept and everything that costs tokens
// without carrying meaning (scripts, navigation, footers, styling, runs of
// whitespace, emphasis markers) is dropped.
//
// Pages are hostile input. golang.org/x/net/html builds the tree in time
// quadratic in the nesting depth for common elements: 100,000 nested <div>s
// (a 500 KB page) take over twenty seconds to parse. Two defences apply before
// and during parsing, and both fall back to flatText, a linear tokenizer-only
// extraction that keeps the words but not the structure: a cheap pre-scan that
// estimates the nesting depth, and a time budget enforced by cutting the input
// stream off mid-parse. The tree walker itself is recursion-bounded.

const (
	maxNesting   = 300 // estimated element nesting beyond which structure is abandoned
	maxWalkDepth = 200 // recursion bound of the tree walker
	parseBudget  = 4 * time.Second
)

var errParseBudget = errors.New("html parse budget exceeded")

// skipTags never contribute text. Scripts and styles are noise; navigation and
// footers are page furniture; svg, iframes and media have no readable text; a
// <select> can hold hundreds of options.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "nav": true, "footer": true, "svg": true,
	"template": true, "iframe": true, "object": true, "embed": true, "canvas": true, "audio": true,
	"video": true, "select": true, "datalist": true, "map": true, "dialog": true, "head": true,
	"applet": true, "frame": true, "frameset": true,
}

// uncounted elements do not deepen the nesting estimate: void elements have no
// end tag, and elements with optional end tags are closed implicitly by the next
// sibling, so counting them would misjudge ordinary tag-soup pages.
var uncounted = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true,
	"p": true, "li": true, "dt": true, "dd": true, "tr": true, "td": true, "th": true, "thead": true,
	"tbody": true, "tfoot": true, "colgroup": true, "caption": true, "option": true, "optgroup": true,
	"rb": true, "rt": true, "rtc": true, "rp": true, "html": true, "head": true, "body": true,
}

// htmlToText converts an HTML document to text and returns the page title.
func htmlToText(src string, base *url.URL) (text, title string) {
	return htmlToTextBudget(src, base, parseBudget)
}

func htmlToTextBudget(src string, base *url.URL, budget time.Duration) (string, string) {
	if !utf8.ValidString(src) {
		// convert normally receives decoded text; this keeps the function safe
		// for direct callers, since the HTML parser passes bad bytes through.
		src = strings.ToValidUTF8(src, "\uFFFD")
	}
	if nestingTooDeep(src) {
		return flatText(src)
	}
	doc, err := html.Parse(&budgetReader{r: strings.NewReader(src), deadline: time.Now().Add(budget)})
	if err != nil {
		return flatText(src)
	}
	c := &conv{base: base}
	return c.run(doc)
}

// budgetReader aborts the parse: the tokenizer pulls input in small chunks, so
// once the deadline passes the next read fails and html.Parse returns.
type budgetReader struct {
	r        io.Reader
	deadline time.Time
}

// Read checks the parse deadline before each underlying read; it does not interrupt a read already
// in progress.
func (b *budgetReader) Read(p []byte) (int, error) {
	if time.Now().After(b.deadline) {
		return 0, errParseBudget
	}
	return b.r.Read(p)
}

// nestingTooDeep estimates the element nesting of src with the tokenizer alone
// (linear time, no tree).
func nestingTooDeep(src string) bool {
	z := html.NewTokenizer(strings.NewReader(src))
	depth := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken:
			name, _ := z.TagName()
			if uncounted[string(name)] {
				continue
			}
			if depth++; depth > maxNesting {
				return true
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if uncounted[string(name)] {
				continue
			}
			if depth > 0 {
				depth--
			}
		}
	}
}

// ------------------------------------------------------------------ writer

// mdWriter accumulates output line by line. Whitespace is emitted lazily (a
// pending space is written only when more text follows), so lines never end in
// spaces and blank lines never repeat, without a clean-up pass that would also
// mangle code blocks.
type mdWriter struct {
	sb        strings.Builder
	atLine    bool     // nothing written on the current line yet
	lastBlank bool     // the previous line was blank (or nothing was written)
	pendBlank bool     // a blank line is owed before the next content
	blankPfx  string   // outermost prefix seen while it has been owed
	pendSpace bool     // whitespace seen since the last word
	inline    bool     // sub-writer: everything stays on one line
	wrote     bool     // any word written
	leadSpace bool     // (inline) text began with whitespace
	prefix    []string // continuation prefixes of open blockquotes and list items
	bullet    string   // marker awaiting the first line of a list item
}

// newMD initializes a Markdown writer at a blank line with the requested inline mode.
func newMD(inline bool) *mdWriter { return &mdWriter{atLine: true, lastBlank: true, inline: inline} }

// String returns generated Markdown with surrounding whitespace removed.
func (w *mdWriter) String() string { return strings.TrimSpace(w.sb.String()) }

// pushPrefix adds a nested line prefix for subsequent Markdown output.
func (w *mdWriter) pushPrefix(p string) { w.prefix = append(w.prefix, p) }

// popPrefix removes the innermost Markdown line prefix, leaving an empty stack unchanged.
func (w *mdWriter) popPrefix() {
	if len(w.prefix) > 0 {
		w.prefix = w.prefix[:len(w.prefix)-1]
	}
}

// breakLine ends the current line.
func (w *mdWriter) breakLine() {
	if w.inline {
		w.pendSpace = w.wrote
		return
	}
	if w.atLine {
		return
	}
	w.sb.WriteByte('\n')
	w.atLine = true
	w.pendSpace = false
}

// blankLine ends the current line and owes one blank line before whatever
// comes next. The blank line is written lazily, when that next content
// arrives: by then the enclosing blockquote or list item has closed (or not),
// so the blank line carries the right prefix and never trails the output.
func (w *mdWriter) blankLine() {
	w.breakLine()
	if w.inline {
		return
	}
	// The blank line belongs at the outermost nesting level it was requested
	// at: the gap between a paragraph and the blockquote that follows it is not
	// itself quoted.
	if cur := strings.Join(w.prefix, ""); !w.pendBlank || len(cur) < len(w.blankPfx) {
		w.blankPfx = cur
	}
	w.pendBlank = true
}

func (w *mdWriter) flushBlank() {
	if !w.pendBlank {
		return
	}
	w.pendBlank = false
	if w.lastBlank || w.bullet != "" {
		return // already separated, or first thing in a list item
	}
	w.sb.WriteString(strings.TrimRight(w.blankPfx, " "))
	w.sb.WriteByte('\n')
	w.lastBlank = true
}

func (w *mdWriter) writePrefix() {
	if w.bullet != "" && len(w.prefix) > 0 {
		for _, p := range w.prefix[:len(w.prefix)-1] {
			w.sb.WriteString(p)
		}
		w.sb.WriteString(w.bullet)
		w.bullet = ""
		return
	}
	for _, p := range w.prefix {
		w.sb.WriteString(p)
	}
}

// word writes one unbreakable token.
func (w *mdWriter) word(s string) {
	if s == "" {
		return
	}
	switch {
	case w.inline:
		if w.pendSpace && w.wrote {
			w.sb.WriteByte(' ')
		}
	case w.atLine:
		w.flushBlank()
		w.writePrefix()
	case w.pendSpace:
		w.sb.WriteByte(' ')
	}
	w.atLine, w.pendSpace, w.lastBlank, w.wrote = false, false, false, true
	w.sb.WriteString(s)
}

// marker writes a leading marker ("##", "-") that is followed by a space.
func (w *mdWriter) marker(s string) {
	w.word(s)
	w.pendSpace = true
}

// text writes running text, collapsing whitespace.
func (w *mdWriter) text(s string) {
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				w.word(s[start:i])
				start = -1
			}
			if !w.wrote && w.inline {
				w.leadSpace = true
			}
			w.pendSpace = true
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		w.word(s[start:])
	}
}

// rawLine writes a whole line verbatim (code, table rows).
func (w *mdWriter) rawLine(s string) {
	if w.inline {
		w.word(s)
		return
	}
	w.breakLine()
	w.flushBlank()
	if s == "" {
		w.sb.WriteString(strings.TrimRight(strings.Join(w.prefix, ""), " "))
	} else {
		w.writePrefix()
		w.sb.WriteString(s)
	}
	w.sb.WriteByte('\n')
	w.atLine, w.pendSpace, w.lastBlank, w.wrote = true, false, s == "", true
}

// ------------------------------------------------------------------ converter

type conv struct {
	base *url.URL
}

func (c *conv) run(doc *html.Node) (string, string) {
	title := findTitle(doc)
	if b := findFirst(doc, "base"); b != nil && c.base != nil {
		if href := attr(b, "href"); href != "" {
			if u, err := c.base.Parse(href); err == nil {
				c.base = u
			}
		}
	}
	root := findFirst(doc, "body")
	if root == nil {
		root = doc
	}
	w := newMD(false)
	c.children(root, w, 0)
	return w.String(), title
}

func firstLines(s string, n int) string {
	idx := 0
	for i := 0; i < n; i++ {
		j := strings.IndexByte(s[idx:], '\n')
		if j < 0 {
			return s
		}
		idx += j + 1
	}
	return s[:idx]
}

// children converts a node's children in document order at the supplied traversal depth.
func (c *conv) children(n *html.Node, w *mdWriter, depth int) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch, w, depth)
	}
}

// inlineOf renders n's content on one line.
func (c *conv) inlineOf(n *html.Node, depth int) *mdWriter {
	sub := newMD(true)
	c.children(n, sub, depth+1)
	return sub
}

func (c *conv) walk(n *html.Node, w *mdWriter, depth int) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.DocumentNode:
		c.children(n, w, depth)
		return
	case html.ElementNode:
	default:
		return
	}
	name := n.Data
	if skipTags[name] || n.Namespace == "svg" || isHidden(n) {
		return
	}
	if depth > maxWalkDepth {
		w.text(flatten(n))
		return
	}
	switch name {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		sub := c.inlineOf(n, depth)
		if s := sub.String(); s != "" {
			w.blankLine()
			if !w.inline { // a heading inside a link or a table cell is just its text
				w.marker(strings.Repeat("#", int(name[1]-'0')))
			}
			w.text(s)
			w.blankLine()
		}
	case "p", "ul", "ol", "dl", "blockquote", "figure", "pre", "table", "address", "fieldset", "form", "details":
		inItem := n.Parent != nil && n.Parent.Data == "li"
		listInItem := inItem && (name == "ul" || name == "ol")
		lastPara := inItem && name == "p" && n.NextSibling == nil
		if listInItem {
			w.breakLine() // a list inside an item hugs its parent item
			w.pendBlank = false
		} else {
			w.blankLine()
		}
		switch name {
		case "ul", "ol":
			c.list(n, w, depth, name == "ol")
		case "blockquote":
			w.pushPrefix("> ")
			c.children(n, w, depth+1)
			w.popPrefix()
		case "pre":
			c.pre(n, w)
		case "table":
			c.table(n, w, depth)
		default:
			c.children(n, w, depth+1)
		}
		if listInItem || lastPara {
			w.breakLine() // the last paragraph of an item does not loosen the list
		} else {
			w.blankLine()
		}
	case "li":
		// A stray <li> outside a list.
		c.item(n, w, depth, "- ")
	case "dt":
		w.breakLine()
		c.children(n, w, depth+1)
		w.breakLine()
	case "dd":
		w.breakLine()
		w.pushPrefix("  ")
		c.children(n, w, depth+1)
		w.popPrefix()
		w.breakLine()
	case "br":
		w.breakLine()
	case "hr":
		w.blankLine()
		w.word("---")
		w.blankLine()
	case "a":
		c.link(n, w, depth)
	case "img":
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			w.word("[image: " + oneLine(alt) + "]")
		}
	case "code", "kbd", "samp", "tt":
		if s := c.inlineOf(n, depth).String(); s != "" {
			w.word(codeSpan(s))
		}
	case "div", "section", "article", "main", "header", "aside", "figcaption", "summary", "legend",
		"caption", "center", "hgroup", "tr", "body", "html", "menu", "dir", "search":
		w.breakLine()
		c.children(n, w, depth+1)
		w.breakLine()
	default:
		c.children(n, w, depth+1)
	}
}

func (c *conv) list(n *html.Node, w *mdWriter, depth int, ordered bool) {
	idx := 1
	if ordered {
		if s := attr(n, "start"); s != "" {
			if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				idx = v
			}
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.Data == "li" && !isHidden(ch) {
			marker := "- "
			if ordered {
				marker = strconv.Itoa(idx) + ". "
				idx++
			}
			c.item(ch, w, depth, marker)
		} else {
			c.walk(ch, w, depth+1)
		}
	}
}

func (c *conv) item(li *html.Node, w *mdWriter, depth int, marker string) {
	if w.inline {
		w.marker(strings.TrimSpace(marker))
		c.children(li, w, depth+1)
		return
	}
	w.breakLine()
	w.flushBlank() // the separation the list is owed goes before its first item
	w.pushPrefix(strings.Repeat(" ", len(marker)))
	w.bullet = marker
	c.children(li, w, depth+1)
	w.bullet = "" // an empty item leaves no dangling marker
	w.popPrefix()
	w.breakLine()
	w.pendBlank = false // items are compact whatever they end with
}

func (c *conv) link(n *html.Node, w *mdWriter, depth int) {
	sub := c.inlineOf(n, depth)
	text := sub.String()
	if text == "" {
		return
	}
	if sub.leadSpace {
		w.pendSpace = true
	}
	href := c.resolveLink(attr(n, "href"))
	switch {
	case href == "":
		w.text(text)
	case text == href:
		w.word(href)
	default:
		w.word("[" + escapeLinkText(text) + "](" + href + ")")
	}
	if sub.pendSpace {
		w.pendSpace = true
	}
}

var linkURLEscaper = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "\n", "", "\r", "", "\t", "")

// resolveLink returns an absolute http(s)/mailto URL for href, or "" when the
// link goes nowhere useful (empty, script, in-page anchor).
func (c *conv) resolveLink(href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if c.base != nil {
		u = c.base.ResolveReference(u)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
	default:
		return ""
	}
	if c.base != nil && u.Fragment != "" {
		a, b := *u, *c.base
		a.Fragment, b.Fragment = "", ""
		if a.String() == b.String() {
			return "" // in-page anchor spelled as a full URL
		}
	}
	return linkURLEscaper.Replace(u.String())
}

// escapeLinkText escapes square brackets in link text unless they balance, in
// which case Markdown accepts them as they are.
func escapeLinkText(s string) string {
	if !strings.ContainsAny(s, "[]") {
		return s
	}
	depth := 0
	balanced := true
	for i := 0; i < len(s) && balanced; i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			if depth--; depth < 0 {
				balanced = false
			}
		}
	}
	if balanced && depth == 0 {
		return s
	}
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}

// oneLine collapses whitespace into single spaces and trims both ends.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// codeSpan wraps s in backticks, using a longer fence when s contains some.
func codeSpan(s string) string {
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	return "`` " + s + " ``"
}

var langRe = regexp.MustCompile(`(?:^|\s)(?:language|lang)-([A-Za-z0-9_+#.-]+)`)

func (c *conv) pre(n *html.Node, w *mdWriter) {
	code := strings.Trim(strings.ReplaceAll(preText(n), "\r\n", "\n"), "\n")
	if strings.TrimSpace(code) == "" {
		return
	}
	lang := ""
	classes := attr(n, "class")
	if ch := findFirstChild(n, "code"); ch != nil {
		classes += " " + attr(ch, "class")
	}
	if m := langRe.FindStringSubmatch(classes); m != nil {
		lang = m[1]
	}
	fence := "```"
	for strings.Contains(code, fence) {
		fence += "`"
	}
	if w.inline {
		w.word(codeSpan(oneLine(code)))
		return
	}
	w.rawLine(fence + lang)
	for _, line := range strings.Split(code, "\n") {
		w.rawLine(strings.TrimRight(line, " \t\r"))
	}
	w.rawLine(fence)
}

// preText collects the text under n with whitespace intact, iteratively.
func preText(n *html.Node) string {
	var sb strings.Builder
	stack := []*html.Node{n}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch cur.Type {
		case html.TextNode:
			sb.WriteString(cur.Data)
			continue
		case html.ElementNode:
			if skipTags[cur.Data] {
				continue
			}
			if cur.Data == "br" {
				sb.WriteByte('\n')
				continue
			}
		}
		var kids []*html.Node
		for ch := cur.FirstChild; ch != nil; ch = ch.NextSibling {
			kids = append(kids, ch)
		}
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return sb.String()
}

// ------------------------------------------------------------------ tables

// blockish marks content that makes a table a layout table. Paragraphs, divs
// and lists in a cell are common in data tables (word-processor and wiki
// exports) and flatten fine into a pipe row; nested tables, headings, forms and
// preformatted blocks are what page-layout tables hold.
var blockish = map[string]bool{
	"table": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"form": true, "pre": true, "blockquote": true, "hr": true,
}

func (c *conv) table(n *html.Node, w *mdWriter, depth int) {
	type row struct {
		tr   *html.Node
		head bool // inside <thead>
	}
	var rows []row
	var caption *html.Node
	var collect func(parent *html.Node, head bool)
	collect = func(parent *html.Node, head bool) {
		for ch := parent.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type != html.ElementNode {
				continue
			}
			switch ch.Data {
			case "tr":
				rows = append(rows, row{ch, head})
			case "thead":
				collect(ch, true)
			case "tbody", "tfoot":
				collect(ch, false)
			case "caption":
				caption = ch
			}
		}
	}
	collect(n, false)
	if caption != nil {
		if s := c.inlineOf(caption, depth).String(); s != "" {
			w.text(s)
			w.breakLine()
		}
	}

	type cell struct {
		n  *html.Node
		th bool
	}
	type gridRow struct {
		cells []cell
		texts []string
		head  bool // in <thead>, or made of header cells
	}
	grid := make([]gridRow, 0, len(rows))
	layout := false
	for _, r := range rows {
		gr := gridRow{head: r.head}
		for ch := r.tr.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode && (ch.Data == "td" || ch.Data == "th") && !isHidden(ch) {
				gr.cells = append(gr.cells, cell{ch, ch.Data == "th"})
				gr.head = gr.head || ch.Data == "th"
				if !layout && hasBlockDescendant(ch) {
					layout = true
				}
			}
		}
		if len(gr.cells) > 0 {
			grid = append(grid, gr)
		}
	}
	if len(grid) == 0 {
		return
	}
	if layout {
		// A table used for page layout: its cells hold blocks, so render them as
		// blocks rather than cramming them into pipe rows.
		for _, gr := range grid {
			for _, ce := range gr.cells {
				w.breakLine()
				c.children(ce.n, w, depth+1)
				w.breakLine()
			}
		}
		return
	}

	// Render every cell on one line, then decide whether this is data or
	// scaffolding. Header cells, or a regular grid of mostly filled cells, mean
	// data: pipe rows keep the column structure. Anything else (single columns,
	// ragged rows, spacer cells: think of a link list laid out with a table) reads
	// better as plain lines.
	hasHeader, uniform, empties, total := false, true, 0, 0
	for i := range grid {
		gr := &grid[i]
		gr.texts = make([]string, len(gr.cells))
		hasHeader = hasHeader || gr.head
		uniform = uniform && len(gr.cells) == len(grid[0].cells)
		rowEmpties := 0
		for k, ce := range gr.cells {
			gr.texts[k] = strings.ReplaceAll(c.inlineOf(ce.n, depth).String(), "\n", " ")
			if gr.texts[k] == "" {
				rowEmpties++
			}
		}
		if rowEmpties < len(gr.cells) { // wholly blank rows are skipped, not counted against the grid
			empties += rowEmpties
			total += len(gr.cells)
		}
	}
	tabular := hasHeader || (uniform && len(grid[0].cells) >= 2 && empties*4 <= total)

	first := true
	for _, gr := range grid {
		var filled []string
		for _, s := range gr.texts {
			if s != "" {
				filled = append(filled, s)
			}
		}
		if len(filled) == 0 {
			continue
		}
		if !tabular {
			w.rawLine(strings.Join(filled, " "))
			continue
		}
		cells := make([]string, len(gr.texts))
		for k, s := range gr.texts {
			cells[k] = strings.ReplaceAll(s, "|", `\|`)
		}
		w.rawLine("| " + strings.Join(cells, " | ") + " |")
		if first && gr.head {
			seps := make([]string, len(cells))
			for k := range seps {
				seps[k] = "---"
			}
			w.rawLine("| " + strings.Join(seps, " | ") + " |")
		}
		first = false
	}
}

// hasBlockDescendant reports whether n contains block-level markup, scanning
// iteratively and stopping at the first hit.
func hasBlockDescendant(n *html.Node) bool {
	stack := []*html.Node{}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		stack = append(stack, ch)
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.Type != html.ElementNode {
			continue
		}
		if blockish[cur.Data] {
			return true
		}
		for ch := cur.FirstChild; ch != nil; ch = ch.NextSibling {
			stack = append(stack, ch)
		}
	}
	return false
}

// ------------------------------------------------------------------ helpers

// attr returns the first matching HTML attribute value or empty when absent.
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// hasAttr checks HTML attribute presence independently of its value.
func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func isHidden(n *html.Node) bool {
	if hasAttr(n, "hidden") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(attr(n, "aria-hidden")), "true") {
		return true
	}
	if style := attr(n, "style"); style != "" {
		s := strings.ToLower(strings.ReplaceAll(style, " ", ""))
		if strings.Contains(s, "display:none") || strings.Contains(s, "visibility:hidden") {
			return true
		}
	}
	return false
}

// findFirst returns the first element called name in document order, iteratively.
func findFirst(root *html.Node, name string) *html.Node {
	stack := []*html.Node{root}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.Type == html.ElementNode && cur.Data == name {
			return cur
		}
		var kids []*html.Node
		for ch := cur.FirstChild; ch != nil; ch = ch.NextSibling {
			kids = append(kids, ch)
		}
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return nil
}

// findFirstChild returns the first immediate element child with the requested name, without
// recursive search.
func findFirstChild(n *html.Node, name string) *html.Node {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.Data == name {
			return ch
		}
	}
	return nil
}

func findTitle(doc *html.Node) string {
	t := findFirst(doc, "title")
	if t == nil {
		return ""
	}
	var sb strings.Builder
	for ch := t.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.TextNode {
			sb.WriteString(ch.Data)
		}
	}
	return oneLine(sb.String())
}

// flatten returns the text under n, skipping non-content elements, without
// recursion. It is what remains of a subtree nested deeper than maxWalkDepth.
func flatten(n *html.Node) string {
	var sb strings.Builder
	stack := []*html.Node{n}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch cur.Type {
		case html.TextNode:
			sb.WriteString(cur.Data)
			sb.WriteByte(' ')
			continue
		case html.ElementNode:
			if skipTags[cur.Data] || cur.Namespace == "svg" {
				continue
			}
		}
		var kids []*html.Node
		for ch := cur.FirstChild; ch != nil; ch = ch.NextSibling {
			kids = append(kids, ch)
		}
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return sb.String()
}

// ------------------------------------------------------------------ fallback

// flatText extracts readable text from src with the tokenizer only: linear
// time and constant nesting, whatever the input looks like. Structure is
// reduced to line breaks, headings, list dashes and rules.
func flatText(src string) (string, string) {
	z := html.NewTokenizer(strings.NewReader(src))
	w := newMD(false)
	var title strings.Builder
	skip, skipDepth := "", 0
	inTitle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return w.String(), oneLine(title.String())
		case html.TextToken:
			switch {
			case skip != "":
			case inTitle:
				title.Write(z.Text())
			default:
				w.text(string(z.Text()))
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			nameB, _ := z.TagName()
			name := string(nameB)
			if skip != "" {
				if name == skip && tt == html.StartTagToken {
					skipDepth++
				}
				continue
			}
			if skipTags[name] && name != "head" {
				if tt == html.StartTagToken {
					skip, skipDepth = name, 1
				}
				continue
			}
			switch name {
			case "title":
				inTitle = tt == html.StartTagToken
			case "p", "ul", "ol", "dl", "table", "pre", "blockquote", "form", "figure":
				w.blankLine()
			case "h1", "h2", "h3", "h4", "h5", "h6":
				w.blankLine()
				w.marker(strings.Repeat("#", int(name[1]-'0')))
			case "li":
				w.breakLine()
				w.marker("-")
			case "br", "div", "section", "article", "main", "header", "aside", "tr", "dt", "dd", "caption", "details", "summary":
				w.breakLine()
			case "hr":
				w.blankLine()
				w.word("---")
				w.blankLine()
			case "td", "th":
				w.pendSpace = true
				w.marker("|")
			}
		case html.EndTagToken:
			nameB, _ := z.TagName()
			name := string(nameB)
			if skip != "" {
				if name == skip {
					if skipDepth--; skipDepth <= 0 {
						skip, skipDepth = "", 0
					}
				}
				continue
			}
			switch name {
			case "title":
				inTitle = false
			case "p", "ul", "ol", "dl", "table", "pre", "blockquote", "form", "figure",
				"h1", "h2", "h3", "h4", "h5", "h6":
				w.blankLine()
			case "div", "section", "article", "main", "header", "aside", "tr", "li", "dt", "dd":
				w.breakLine()
			}
		}
	}
}
