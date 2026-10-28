package research

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The wiki's Markdown is CommonMark's everyday subset (headings, paragraphs, lists, block quotes, fenced code, tables, rules,
// emphasis, code spans and links) with three additions:
//
//   - [S12] or [S12, S4] cites sources by id; [F7] points at a finding on the evidence page;
//   - [[page-id]] or [[page-id|text]] links to another page of the wiki;
//   - {{C3}} on a line of its own shows a code snippet with its project, license and source.
//
// HTML in the text is shown as text, never interpreted: pages are written by models from what they read on the web.

// mdRefs is what rendering a page found in it.
type mdRefs struct {
	// Headings are the page's second- and third-level headings, for its table of contents.
	Headings []mdHeading
	// Cites are the sources cited, in order of first citation; Links the pages linked, in order; Snippets the snippets shown.
	Cites    []string
	Links    []string
	Snippets []string
	Findings []string
	// Unknown are references to nothing: "S99", "[[nowhere]]", "C9".
	Unknown []string
	// Uncited counts paragraphs and list items of prose that cite no source; Prose counts all of them.
	Uncited int
	Prose   int
	// Words counts the words of the text.
	Words int
}

type mdHeading struct {
	Level int
	ID    string
	Text  string
}

// mdEnv resolves what a page refers to. A nil func resolves nothing.
type mdEnv struct {
	// Page reports whether a wiki page exists and returns its title.
	Page func(id string) (title string, ok bool)
	// Source reports whether a source exists.
	Source func(id string) bool
	// Finding reports whether a finding exists.
	Finding func(id string) bool
	// Snippet renders a snippet's block, or reports it unknown.
	Snippet func(id string) (html string, ok bool)
	// Href is the address of a page ("other.html").
	Href func(id string) string
	// SourceHref is the address of a source's entry.
	SourceHref func(id string) string
}

var (
	reHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	reFence    = regexp.MustCompile("^(```+|~~~+)\\s*([A-Za-z0-9_+#.-]*)")
	reRule     = regexp.MustCompile(`^(\*\s*){3,}$|^(-\s*){3,}$|^(_\s*){3,}$`)
	reBullet   = regexp.MustCompile(`^(\s*)([-*+])\s+(.*)$`)
	reOrdered  = regexp.MustCompile(`^(\s*)(\d{1,9})[.)]\s+(.*)$`)
	reSnippet  = regexp.MustCompile(`^\{\{\s*(C\d{1,5})\s*\}\}$`)
	reTableSep = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)
	reCiteIDs  = regexp.MustCompile(`^\s*([SF]\d{1,5})(\s*[,;]\s*[SF]\d{1,5})*\s*$`)
	reCallout  = regexp.MustCompile(`^\[!(NOTE|TIP|WARNING|IMPORTANT|CAUTION)\]\s*`)
)

// mdRenderer renders one page.
type mdRenderer struct {
	env    mdEnv
	refs   mdRefs
	seen   map[string]bool
	ids    map[string]int
	b      strings.Builder
	cited  bool // the current block cited a source
	inLink bool // a link's label is being rendered: a bare address in it is text, an <a> does not nest
}

// renderMarkdown renders a page's Markdown as HTML and reports what it refers to.
func renderMarkdown(src string, env mdEnv) (string, mdRefs) {
	r := &mdRenderer{env: env, seen: map[string]bool{}, ids: map[string]int{}}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\t", "    "), "\n")
	r.blocks(lines)
	return r.b.String(), r.refs
}

// Rendering budgets: a table is padded to its header's width, so a wide header and many short rows would otherwise multiply a
// small page into a huge one; and no page renders to more than maxRenderBytes of HTML.
const (
	maxTableCols   = 64
	maxTableCells  = 1 << 14
	maxRenderBytes = 16 << 20
)

// blocks renders a sequence of lines as blocks.
func (r *mdRenderer) blocks(lines []string) {
	for i := 0; i < len(lines); {
		if r.b.Len() > maxRenderBytes {
			r.b.WriteString("<p>[The rest of this page is too large to render.]</p>\n")
			return
		}
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			i++
		case reFence.MatchString(trim):
			i = r.fence(lines, i)
		case reHeading.MatchString(trim):
			m := reHeading.FindStringSubmatch(trim)
			r.heading(len(m[1]), m[2])
			i++
		case reRule.MatchString(trim):
			r.b.WriteString("<hr>\n")
			i++
		case reSnippet.MatchString(trim):
			id := reSnippet.FindStringSubmatch(trim)[1]
			r.snippet(id)
			i++
		case strings.HasPrefix(trim, ">"):
			i = r.quote(lines, i)
		case strings.Contains(trim, "|") && i+1 < len(lines) && reTableSep.MatchString(strings.TrimSpace(lines[i+1])):
			i = r.table(lines, i)
		case reBullet.MatchString(line) || reOrdered.MatchString(line):
			i = r.list(lines, i)
		default:
			i = r.paragraph(lines, i)
		}
	}
}

// fence renders a fenced code block starting at lines[i]; it returns the next line.
func (r *mdRenderer) fence(lines []string, i int) int {
	m := reFence.FindStringSubmatch(strings.TrimSpace(lines[i]))
	mark, lang := m[1], m[2]
	var body []string
	j := i + 1
	for ; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), mark[:3]) && strings.Trim(strings.TrimSpace(lines[j]), mark[:1]) == "" {
			j++
			break
		}
		body = append(body, lines[j])
	}
	r.code(lang, strings.Join(body, "\n"))
	return j
}

// code writes a code block.
func (r *mdRenderer) code(lang, text string) {
	cls := ""
	if lang != "" {
		cls = ` class="lang-` + html.EscapeString(strings.ToLower(lang)) + `"`
		r.b.WriteString(`<figure class="code"><figcaption>` + html.EscapeString(lang) + `</figcaption>`)
	} else {
		r.b.WriteString(`<figure class="code">`)
	}
	r.b.WriteString("<pre><code" + cls + ">" + html.EscapeString(text) + "</code></pre></figure>\n")
}

// heading writes a heading with an id, and keeps second- and third-level ones for the table of contents.
func (r *mdRenderer) heading(level int, text string) {
	if level == 1 {
		level = 2 // the page title is the only h1
	}
	id := r.anchor(text)
	inner := r.inline(text)
	plain := stripTags(inner)
	if level <= 3 {
		r.refs.Headings = append(r.refs.Headings, mdHeading{Level: level, ID: id, Text: plain})
	}
	fmt.Fprintf(&r.b, "<h%d id=\"%s\">%s<a class=\"hl\" href=\"#%s\" aria-label=\"link to this section\">#</a></h%d>\n", level, id, inner, id, level)
}

// anchor makes a unique id out of a heading.
func (r *mdRenderer) anchor(text string) string {
	base := strings.ReplaceAll(foldText(stripInline(text)), " ", "-")
	if base == "" {
		base = "section"
	}
	if len(base) > 60 {
		base = strings.TrimRight(base[:60], "-")
	}
	r.ids[base]++
	if n := r.ids[base]; n > 1 {
		return base + "-" + strconv.Itoa(n)
	}
	return base
}

// snippet writes a snippet block.
func (r *mdRenderer) snippet(id string) {
	if r.env.Snippet != nil {
		if h, ok := r.env.Snippet(id); ok {
			r.refs.Snippets = addUnique(r.refs.Snippets, id)
			r.b.WriteString(h)
			return
		}
	}
	r.refs.Unknown = addUnique(r.refs.Unknown, id)
	r.b.WriteString(`<p class="missing">unknown snippet ` + html.EscapeString(id) + "</p>\n")
}

// quote renders a block quote (or a callout: "> [!NOTE]") starting at lines[i].
func (r *mdRenderer) quote(lines []string, i int) int {
	var inner []string
	j := i
	for ; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if !strings.HasPrefix(t, ">") {
			break
		}
		t = strings.TrimPrefix(t, ">")
		inner = append(inner, strings.TrimPrefix(t, " "))
	}
	kind := ""
	if len(inner) > 0 {
		if m := reCallout.FindStringSubmatch(inner[0]); m != nil {
			kind = strings.ToLower(m[1])
			inner[0] = reCallout.ReplaceAllString(inner[0], "")
		}
	}
	if kind != "" {
		r.b.WriteString(`<aside class="callout ` + kind + `"><span class="callout-kind">` + kind + "</span>\n")
		r.blocks(inner)
		r.b.WriteString("</aside>\n")
	} else {
		r.b.WriteString("<blockquote>\n")
		r.blocks(inner)
		r.b.WriteString("</blockquote>\n")
	}
	return j
}

// table renders a pipe table starting at lines[i] (the header, then the separator).
func (r *mdRenderer) table(lines []string, i int) int {
	cells := func(s string) []string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(strings.TrimSuffix(s, "|"), "|")
		var out []string
		for _, c := range splitCells(s) {
			out = append(out, strings.TrimSpace(c))
		}
		return out
	}
	head := cells(lines[i])
	if len(head) > maxTableCols {
		head = head[:maxTableCols]
	}
	var align []string
	for _, c := range cells(lines[i+1]) {
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
			align = append(align, "center")
		case strings.HasSuffix(c, ":"):
			align = append(align, "right")
		default:
			align = append(align, "")
		}
	}
	style := func(k int) string {
		if k < len(align) && align[k] != "" {
			return ` style="text-align:` + align[k] + `"`
		}
		return ""
	}
	r.b.WriteString("<div class=\"table\"><table>\n<thead><tr>")
	for k, c := range head {
		r.b.WriteString("<th" + style(k) + ">" + r.inline(c) + "</th>")
	}
	r.b.WriteString("</tr></thead>\n<tbody>\n")
	j := i + 2
	shown := len(head)
	for ; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if t == "" || !strings.Contains(t, "|") {
			break
		}
		if shown += len(head); shown > maxTableCells {
			continue // past the budget the rest of the table is consumed, not rendered
		}
		r.cited = false
		r.b.WriteString("<tr>")
		row := cells(t)
		for k := range head {
			c := ""
			if k < len(row) {
				c = row[k]
			}
			r.b.WriteString("<td" + style(k) + ">" + r.inline(c) + "</td>")
		}
		r.b.WriteString("</tr>\n")
	}
	if shown > maxTableCells {
		fmt.Fprintf(&r.b, "<tr><td colspan=\"%d\">[%d more rows not shown]</td></tr>\n", len(head), (shown-maxTableCells+len(head)-1)/len(head))
	}
	r.b.WriteString("</tbody></table></div>\n")
	return j
}

// splitCells splits a table row at pipes outside code spans; "\|" is a literal pipe.
func splitCells(s string) []string {
	var out []string
	var cur strings.Builder
	code := false
	for k := 0; k < len(s); k++ {
		c := s[k]
		switch {
		case c == '\\' && k+1 < len(s) && s[k+1] == '|':
			cur.WriteByte('|')
			k++
		case c == '`':
			code = !code
			cur.WriteByte(c)
		case c == '|' && !code:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

// listItem is one parsed item of a list.
type listItem struct {
	lines []string
}

// list renders a list starting at lines[i], nested lists inside its items; it returns the next line.
func (r *mdRenderer) list(lines []string, i int) int {
	indent, ordered, start := listMarker(lines[i])
	var items []listItem
	j := i
	for j < len(lines) {
		line := lines[j]
		if strings.TrimSpace(line) == "" {
			// a blank line ends the list unless the next line goes on with it
			if j+1 < len(lines) && (leading(lines[j+1]) > indent || sameList(lines[j+1], indent, ordered)) {
				if len(items) > 0 {
					items[len(items)-1].lines = append(items[len(items)-1].lines, "")
				}
				j++
				continue
			}
			break
		}
		if sameList(line, indent, ordered) {
			_, _, _, text := splitMarker(line)
			items = append(items, listItem{lines: []string{text}})
			j++
			continue
		}
		if leading(line) > indent && len(items) > 0 {
			items[len(items)-1].lines = append(items[len(items)-1].lines, line)
			j++
			continue
		}
		if len(items) > 0 && !reBullet.MatchString(line) && !reOrdered.MatchString(line) && leading(line) <= indent &&
			!strings.HasPrefix(strings.TrimSpace(line), "#") && !strings.HasPrefix(strings.TrimSpace(line), ">") {
			// a lazy continuation of the last item's text
			items[len(items)-1].lines = append(items[len(items)-1].lines, strings.TrimSpace(line))
			j++
			continue
		}
		break
	}
	tag := "ul"
	attr := ""
	if ordered {
		tag = "ol"
		if start != 1 {
			attr = ` start="` + strconv.Itoa(start) + `"`
		}
	}
	r.b.WriteString("<" + tag + attr + ">\n")
	for _, it := range items {
		r.b.WriteString("<li>")
		r.item(it.lines, indent)
		r.b.WriteString("</li>\n")
	}
	r.b.WriteString("</" + tag + ">\n")
	return j
}

// item renders one list item: its first paragraph inline, then any nested blocks.
func (r *mdRenderer) item(lines []string, indent int) {
	var text []string
	k := 0
	for ; k < len(lines); k++ {
		t := lines[k]
		if k > 0 && (strings.TrimSpace(t) == "" || reBullet.MatchString(t) || reOrdered.MatchString(t) || reFence.MatchString(strings.TrimSpace(t))) {
			break
		}
		text = append(text, strings.TrimSpace(t))
	}
	r.cited = false
	r.b.WriteString(r.inline(strings.Join(text, " ")))
	r.countProse(strings.Join(text, " "))
	if k < len(lines) {
		var rest []string
		cut := indent + 2
		for _, l := range lines[k:] {
			if leading(l) >= cut {
				l = l[cut:]
			} else {
				l = strings.TrimLeft(l, " ")
			}
			rest = append(rest, l)
		}
		r.b.WriteString("\n")
		r.blocks(rest)
	}
}

// countProse counts a paragraph or list item of prose, and whether it cited a source. Short lines (labels, link lists) are not prose.
func (r *mdRenderer) countProse(text string) {
	words := len(strings.Fields(stripInline(text)))
	r.refs.Words += words
	if words < 25 {
		return
	}
	r.refs.Prose++
	if !r.cited {
		r.refs.Uncited++
	}
}

// listMarker reads a list line: its indent, whether it is ordered and its number.
func listMarker(line string) (indent int, ordered bool, start int) {
	indent, ordered, start, _ = splitMarker(line)
	return
}

// splitMarker splits a list line into its indent, kind, number and text.
func splitMarker(line string) (indent int, ordered bool, start int, text string) {
	if m := reOrdered.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[2])
		return len(m[1]), true, n, m[3]
	}
	if m := reBullet.FindStringSubmatch(line); m != nil {
		return len(m[1]), false, 1, m[3]
	}
	return leading(line), false, 1, strings.TrimSpace(line)
}

// sameList reports whether line is an item of the list at indent of that kind.
func sameList(line string, indent int, ordered bool) bool {
	if ordered {
		m := reOrdered.FindStringSubmatch(line)
		return m != nil && len(m[1]) == indent
	}
	m := reBullet.FindStringSubmatch(line)
	return m != nil && len(m[1]) == indent && !reRule.MatchString(strings.TrimSpace(line))
}

// leading counts a line's leading spaces.
func leading(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// paragraph renders consecutive lines of text.
func (r *mdRenderer) paragraph(lines []string, i int) int {
	var text []string
	j := i
	for ; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if t == "" || (j > i && (reHeading.MatchString(t) || reFence.MatchString(t) || strings.HasPrefix(t, ">") ||
			reBullet.MatchString(lines[j]) || reOrdered.MatchString(lines[j]) || reSnippet.MatchString(t) || reRule.MatchString(t))) {
			break
		}
		text = append(text, t)
	}
	joined := strings.Join(text, " ")
	r.cited = false
	r.b.WriteString("<p>" + r.inline(joined) + "</p>\n")
	r.countProse(joined)
	return j
}

// inline renders a run of text: code spans, emphasis, links, wiki links and citations; everything else is escaped.
func (r *mdRenderer) inline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_[]{}()#+-.!|>", s[i+1]) >= 0:
			b.WriteString(html.EscapeString(s[i+1 : i+2]))
			i += 2
			continue
		case c == '`':
			n := run(s, i, '`')
			if end := strings.Index(s[i+n:], strings.Repeat("`", n)); end >= 0 {
				b.WriteString("<code>" + html.EscapeString(strings.TrimSpace(s[i+n:i+n+end])) + "</code>")
				i += n + end + n
				continue
			}
		case c == '[' && strings.HasPrefix(s[i:], "[["):
			if end := strings.Index(s[i+2:], "]]"); end >= 0 {
				b.WriteString(r.wikiLink(s[i+2 : i+2+end]))
				i += 2 + end + 2
				continue
			}
		case c == '[':
			if end := strings.IndexByte(s[i+1:], ']'); end >= 0 {
				label := s[i+1 : i+1+end]
				after := i + 1 + end + 1
				if reCiteIDs.MatchString(label) && (after >= len(s) || s[after] != '(') {
					b.WriteString(r.cite(label))
					i = after
					continue
				}
				if after < len(s) && s[after] == '(' {
					if close := strings.IndexByte(s[after:], ')'); close >= 0 {
						href := strings.TrimSpace(s[after+1 : after+close])
						if sp := strings.IndexAny(href, " \t"); sp >= 0 {
							href = href[:sp] // a link title is dropped
						}
						b.WriteString(r.link(label, href))
						i = after + close + 1
						continue
					}
				}
			}
		case (c == '*' || c == '_') && i+1 < len(s):
			n := min(run(s, i, c), 3)
			if c == '_' && i > 0 && isWord(s[i-1]) {
				break // snake_case is not emphasis
			}
			closer := strings.Repeat(string(c), n)
			if end := strings.Index(s[i+n:], closer); end > 0 && s[i+n] != ' ' {
				inner := r.inline(s[i+n : i+n+end])
				switch n {
				case 1:
					b.WriteString("<em>" + inner + "</em>")
				case 2:
					b.WriteString("<strong>" + inner + "</strong>")
				default:
					b.WriteString("<strong><em>" + inner + "</em></strong>")
				}
				i += n + end + n
				continue
			}
		case c == 'h' && !r.inLink && (strings.HasPrefix(s[i:], "https://") || strings.HasPrefix(s[i:], "http://")) && (i == 0 || !isWord(s[i-1])):
			end := i
			for end < len(s) && !strings.ContainsRune(" \t<>\"')]", rune(s[end])) {
				end++
			}
			raw := strings.TrimRight(s[i:end], ".,;:!?")
			b.WriteString(r.linkHTML(raw, func() string { return html.EscapeString(raw) }, raw))
			i += len(raw)
			continue
		}
		b.WriteString(html.EscapeString(s[i : i+1]))
		i++
	}
	return b.String()
}

// run counts the repeats of c from i.
func run(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// isWord reports whether c is a letter, digit or underscore.
func isWord(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// cite renders a citation of sources and findings ("S12, S4").
func (r *mdRenderer) cite(label string) string {
	var parts []string
	for _, id := range regexp.MustCompile(`[SF]\d{1,5}`).FindAllString(label, -1) {
		if id[0] == 'F' {
			if r.env.Finding == nil || !r.env.Finding(id) {
				r.refs.Unknown = addUnique(r.refs.Unknown, id)
				parts = append(parts, `<span class="missing">`+id+`</span>`)
				continue
			}
			r.refs.Findings = addUnique(r.refs.Findings, id)
			parts = append(parts, `<a class="fref" href="evidence.html#`+id+`" data-finding="`+id+`">`+id+`</a>`)
			continue
		}
		if r.env.Source == nil || !r.env.Source(id) {
			r.refs.Unknown = addUnique(r.refs.Unknown, id)
			parts = append(parts, `<span class="missing">`+id+`</span>`)
			continue
		}
		if !r.seen[id] {
			r.seen[id] = true
			r.refs.Cites = append(r.refs.Cites, id)
		}
		r.cited = true
		n := 0
		for k, c := range r.refs.Cites {
			if c == id {
				n = k + 1
			}
		}
		href := "#ref-" + id
		parts = append(parts, fmt.Sprintf(`<a href="%s" data-src="%s">%d</a>`, href, id, n))
	}
	return `<sup class="cite">[` + strings.Join(parts, ", ") + `]</sup>`
}

// wikiLink renders [[id]] or [[id|text]].
func (r *mdRenderer) wikiLink(body string) string {
	id, text, _ := strings.Cut(body, "|")
	id, text = strings.TrimSpace(id), strings.TrimSpace(text)
	page, frag, _ := strings.Cut(id, "#")
	page = strings.ToLower(page)
	title, ok := "", false
	if special[page] != "" {
		title, ok = special[page], true
	} else if r.env.Page != nil {
		title, ok = r.env.Page(page)
	}
	if text == "" {
		text = title
		if text == "" {
			text = page
		}
	}
	if !ok {
		r.refs.Unknown = addUnique(r.refs.Unknown, "[["+page+"]]")
		return `<span class="missing" title="no such page">` + html.EscapeString(text) + `</span>`
	}
	if special[page] == "" {
		r.refs.Links = addUnique(r.refs.Links, page)
	}
	href := page + ".html"
	if r.env.Href != nil && special[page] == "" {
		href = r.env.Href(page)
	}
	if frag != "" {
		href += "#" + url.PathEscape(frag)
	}
	return `<a class="wl" href="` + html.EscapeString(href) + `">` + r.inline(text) + `</a>`
}

// special are the pages the builder makes itself, which any page may link to.
var special = map[string]string{
	"sources": "Sources", "evidence": "Evidence", "method": "Method", "snippets": "Code snippets", "gaps": "Open questions", "index": "Overview",
}

// link renders an ordinary link. Only web, mail and relative addresses are links; anything else (javascript:) is text.
func (r *mdRenderer) link(label, href string) string {
	return r.linkHTML(label, func() string {
		outer := r.inLink
		r.inLink = true
		defer func() { r.inLink = outer }()
		return r.inline(label)
	}, href)
}

// linkHTML is link with the label's markup made by labelHTML: a bare address is its own label, shown as it is (parsed as Markdown
// it would be a bare address again, without end).
func (r *mdRenderer) linkHTML(label string, labelHTML func() string, href string) string {
	u, err := url.Parse(href)
	if err != nil || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
		return html.EscapeString(label)
	}
	ext := u.Scheme == "http" || u.Scheme == "https"
	attrs := ""
	if ext {
		attrs = ` class="ext" rel="noopener noreferrer" target="_blank"`
	}
	return `<a href="` + html.EscapeString(href) + `"` + attrs + `>` + labelHTML() + `</a>`
}

var reTags = regexp.MustCompile(`<[^>]*>`)

// stripTags removes markup from rendered HTML and decodes its entities.
func stripTags(s string) string { return html.UnescapeString(reTags.ReplaceAllString(s, "")) }

var reInlineMarks = regexp.MustCompile("\\[\\[([^\\]|]*)(?:\\|([^\\]]*))?\\]\\]|\\[[SF]\\d{1,5}(?:\\s*[,;]\\s*[SF]\\d{1,5})*\\]|[*_`]+")

// stripInline removes inline Markdown marks and citations from text, keeping a wiki link's words.
func stripInline(s string) string {
	return reInlineMarks.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "[[") {
			sub := reInlineMarks.FindStringSubmatch(m)
			if sub[2] != "" {
				return sub[2]
			}
			return sub[1]
		}
		if strings.HasPrefix(m, "[") {
			return ""
		}
		return ""
	})
}

// reSpacedCite is a citation with the space before it.
var reSpacedCite = regexp.MustCompile(`\s*\[[SF]\d{1,5}(?:\s*[,;]\s*[SF]\d{1,5})*\]`)

// plainText reduces a page's Markdown to its words, for the search index.
func plainText(src string) string {
	var out []string
	inFence := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if reFence.MatchString(t) {
			inFence = !inFence
			continue
		}
		if inFence || reSnippet.MatchString(t) || reTableSep.MatchString(t) {
			continue
		}
		heading := strings.HasPrefix(t, "#")
		t = strings.TrimLeft(t, "#>-*+0123456789.) |")
		t = reSpacedCite.ReplaceAllString(t, "")
		t = stripInline(strings.ReplaceAll(t, "|", " "))
		if heading && t != "" {
			t += "."
		}
		out = append(out, t)
	}
	return strings.Join(strings.Fields(strings.Join(out, " ")), " ")
}
