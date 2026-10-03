package widget

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

func mdFlat(src string, w int) string { return widgettest.Flatten(Markdown(src, w, MonoTheme())) }

func mdStyled(src string, w int) string {
	return widgettest.FlattenStyled(Markdown(src, w, MonoTheme()))
}

func TestMarkdownBlocks(t *testing.T) {
	rule := func(n int) string { return strings.Repeat("─", n) }
	cases := []struct {
		name string
		src  string
		w    int
		want string
	}{
		{"empty", "", 40, ""},
		{"blank lines only", "\n  \n\n", 40, ""},
		{"a paragraph wraps at spaces", "the quick brown fox jumps over the lazy dog", 20, "the quick brown fox\njumps over the lazy\ndog"},
		{"a soft break is a space", "one\ntwo\nthree", 40, "one two three"},
		{"two trailing spaces break the line", "one  \ntwo", 40, "one\ntwo"},
		{"a backslash breaks the line", "one\\\ntwo", 40, "one\ntwo"},
		{"a break at the very end breaks nothing", "one  \n", 40, "one"},
		{"paragraphs are separated by one blank line", "a\n\n\n\nb", 40, "a\n\nb"},
		{"leading spaces of a line are dropped", "a\n   b", 40, "a b"},
		{"ATX headings", "# One\n## Two\n###### Six", 40, "One\n\nTwo\n\nSix"},
		{"ATX needs a space", "#hashtag\n\n####### seven", 40, "#hashtag\n\n####### seven"},
		{"ATX closing hashes are dropped", "## Two ##\n\n# Hash#", 40, "Two\n\nHash#"},
		{"an empty heading leaves no trace", "a\n\n#\n\nb", 40, "a\n\nb"},
		{"setext headings", "Title\n=====\n\nSub\n---\n\ntext", 40, "Title\n\nSub\n\ntext"},
		{"a setext heading can span lines", "one\ntwo\n===", 40, "one two"},
		{"a rule", "a\n\n---\n\nb", 10, "a\n\n" + rule(10) + "\n\nb"},
		{"rules of every kind", "***\n\n___\n\n- - -", 6, rule(6) + "\n\n" + rule(6) + "\n\n" + rule(6)},
		{"a rule with two characters is text", "--", 10, "--"},
		{"indented code", "    x := 1\n    y := 2", 40, "│ x := 1\n│ y := 2"},
		{"indented code keeps inner blank lines", "    a\n\n    b", 40, "│ a\n│\n│ b"},
		{"fenced code with a language", "```go\nfunc main() {}\n```", 40, "│ go\n│ func main() {}"},
		{"fenced code without a language", "```\nplain\n```", 40, "│ plain"},
		{"a tilde fence", "~~~python\nprint(1)\n~~~", 40, "│ python\n│ print(1)"},
		{"a longer closing fence", "```\na\n`````\nb", 40, "│ a\n\nb"},
		{"a short fence inside a long one is code", "````\n```\ninner\n```\n````", 40, "│ ```\n│ inner\n│ ```"},
		{"an unterminated fence runs to the end", "```js\nlet x\n\nmore", 40, "│ js\n│ let x\n│\n│ more"},
		{"the info string is cut at the first word", "```go title=x\nx\n```", 40, "│ go\n│ x"},
		{"code is not reflowed or interpreted", "```\n**not bold**   spaced\n# not a heading\n```", 40, "│ **not bold**   spaced\n│ # not a heading"},
		{"tabs in code are expanded", "```\n\tif x {\n\t\ty\n```", 40, "│     if x {\n│         y"},
		{"long code lines wrap with a continuation marker", "```\nabcdefghijklmnopqrstuvwxyz\n```", 12, "│ abcdefghij\n│ ↳klmnopqrs\n│ ↳tuvwxyz"},
		{"an empty code block is one row", "```\n```", 10, "│"},
		{"an empty code block with a language", "```sh\n```", 10, "│ sh"},
		{"a block quote", "> one\n> two", 40, "▎ one two"},
		{"a block quote with paragraphs", "> a\n>\n> b", 40, "▎ a\n▎\n▎ b"},
		{"nested quotes", "> a\n> > b", 40, "▎ a\n▎\n▎ ▎ b"},
		{"a lazy continuation line", "> one\ntwo\n\nthree", 40, "▎ one two\n\nthree"},
		{"a quote ends at a blank line", "> a\n\n> b", 40, "▎ a\n\n▎ b"},
		{"a quote wraps inside its bar", "> the quick brown fox", 12, "▎ the quick\n▎ brown fox"},
		{"a quote with a list and code", "> - a\n> - b\n>\n> ```\n> x\n> ```", 40, "▎ • a\n▎ • b\n▎\n▎ │ x"},
		{"bullets", "- a\n- b\n* c", 40, "• a\n• b\n\n• c"},
		{"a plus bullet", "+ a\n+ b", 40, "• a\n• b"},
		{"ordered", "1. a\n2. b\n3. c", 40, "1. a\n2. b\n3. c"},
		{"an ordered list keeps its start", "7. a\n8. b", 40, "7. a\n8. b"},
		{"later numbers are renumbered", "1. a\n1. b\n5. c", 40, "1. a\n2. b\n3. c"},
		{"numbers are right-aligned", "9. a\n10. b", 40, " 9. a\n10. b"},
		{"a paren ordered list", "1) a\n2) b", 40, "1. a\n2. b"},
		{"nested bullets", "- a\n  - b\n    - c\n- d", 40, "• a\n  ◦ b\n    ▪ c\n• d"},
		{"nesting under a long marker by two spaces", "1. First\n  - detail\n2. Second", 40, "1. First\n   ◦ detail\n2. Second"},
		{"nested ordered in bullets", "- a\n  1. x\n  2. y", 40, "• a\n  1. x\n  2. y"},
		{"item text wraps with a hanging indent", "- the quick brown fox jumps", 14, "• the quick\n  brown fox\n  jumps"},
		{"ordered item wraps under its text", "10. the quick brown fox", 14, "10. the quick\n    brown fox"},
		{"a lazy item continuation", "- one\ntwo", 40, "• one two"},
		{"an item's hard-wrapped text", "- one\n  two\n- three", 40, "• one two\n• three"},
		{"a loose list", "- a\n\n- b\n- c", 40, "• a\n\n• b\n\n• c"},
		{"an item with two paragraphs", "- a\n\n  b\n- c", 40, "• a\n\n  b\n\n• c"},
		{"task items", "- [ ] todo\n- [x] done\n- [X] Done", 40, "[ ] todo\n[x] done\n[x] Done"},
		{"a task in an ordered list", "1. [ ] a\n2. [x] b", 40, "1. [ ] a\n2. [x] b"},
		{"not a task", "- [] a\n- [y] b\n- [x]c", 40, "• [] a\n• [y] b\n• [x]c"},
		{"an empty item", "-\n- b", 40, "•\n• b"},
		{"an empty task item keeps its box", "- [ ]\n- [x]", 40, "[ ]\n[x]"},
		{"an empty task item in an ordered list", "1. [ ]\n2. [x]", 40, "1. [ ]\n2. [x]"},
		{"a task box needs a space after it", "- [x]a", 40, "• [x]a"},
		{"an item with code", "- run:\n  ```sh\n  make\n  ```\n- done", 40, "• run:\n  │ sh\n  │ make\n• done"},
		{"a list ends at less indented text after a blank", "- a\n\nafter", 40, "• a\n\nafter"},
		{"a list can interrupt a paragraph", "text\n- a\n- b", 40, "text\n\n• a\n• b"},
		{"but only an ordered list that starts at 1", "text\n2. a", 40, "text 2. a"},
		{"and an ordered list at 1 does", "text\n1. a", 40, "text\n\n1. a"},
		{"a bullet-like line without a space is text", "-a\n*b", 40, "-a *b"},
		{"numbers that are not list markers", "1.5 million\n\n2024. was a year", 40, "1.5 million\n\n2024. was a year"},
		{"a long number is not a marker", "1234567890. x", 40, "1234567890. x"},
		{"four spaces make code, not a nested list, after a blank line", "a\n\n    - b", 40, "a\n\n│ - b"},
		{"a pipe table", "| a | b |\n|---|---|\n| 1 | 2 |", 40, "a │ b\n──┼──\n1 │ 2"},
		{"table alignment", "| l | c | r |\n|:--|:-:|--:|\n| 1 | 2 | 3 |\n| aaaa | bbbb | cccc |", 40,
			"l    │  c   │    r\n─────┼──────┼─────\n1    │  2   │    3\naaaa │ bbbb │ cccc"},
		{"a table without outer pipes", "a | b\n--|--\n1 | 2", 40, "a │ b\n──┼──\n1 │ 2"},
		{"a table right after a paragraph line", "intro\na | b\n--|--\n1 | 2", 40, "intro\n\na │ b\n──┼──\n1 │ 2"},
		{"text after a table", "a | b\n--|--\n1 | 2\nafter", 40, "a │ b\n──┼──\n1 │ 2\n\nafter"},
		{"ragged rows", "| a | b |\n|---|---|\n| 1 |\n| 1 | 2 | 3 |", 40, "a │ b\n──┼──\n1 │\n1 │ 2"},
		{"an escaped pipe", "| a |\n|---|\n| x \\| y |", 40, "a\n─────\nx | y"},
		{"a header with no delimiter row is a paragraph", "| a | b |\n| 1 | 2 |", 40, "| a | b | | 1 | 2 |"},
		{"a delimiter row with the wrong number of cells", "| a | b |\n|---|", 40, "| a | b | |---|"},
		{"a pipe in a code span still splits the cell, as in GFM", "| a |\n|---|\n| `x|y` |", 40, "a\n──\n`x"},
		{"inline markup in a cell", "| a |\n|---|\n| **b** `c` |", 40, "a\n─────\nb `c`"},
		{"a one column table", "|a|\n|-|\n|b|", 40, "a\n─\nb"},
		{"a row indented by four is not a header row", "x\n    a | b\n|-|-|", 40, "x a | b |-|-|"},
		{"a list marker is not a header row", "x\n2. a | b\n|-|-|", 40, "x 2. a | b |-|-|"},
	}
	for _, c := range cases {
		got := mdFlat(c.src, c.w)
		if got != c.want {
			t.Errorf("%s:\n src: %q\n got:\n%s\n want:\n%s", c.name, c.src, got, c.want)
		}
		if lines := Markdown(c.src, c.w, MonoTheme()); true {
			checkLines(t, c.name, lines, c.w)
		}
		// CRLF input is the same document
		if c.src != "" && mdFlat(strings.ReplaceAll(c.src, "\n", "\r\n"), c.w) != got {
			t.Errorf("%s: CRLF changes the output", c.name)
		}
	}
}

func TestMarkdownInline(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"emphasis and strong", "*a* **b** _c_ __d__", "{i}a{/} {b}b{/} {i}c{/} {b}d{/}"},
		{"strong emphasis", "***a*** and ___b___", "{bi}a{/} and {bi}b{/}"},
		{"emphasis inside strong", "**a *b* c**", "{b}a {/}{bi}b{/}{b} c{/}"},
		{"strong inside emphasis", "*a **b** c*", "{i}a {/}{bi}b{/}{i} c{/}"},
		{"strikethrough", "~~gone~~ and ~one~", "{s}gone{/} and ~one~"},
		{"emphasis across a soft break", "*a\nb*", "{i}a b{/}"},
		{"an underscore inside a word is text", "snake_case_name", "snake_case_name"},
		{"an asterisk inside a word is emphasis", "2*3*4", "2{i}3{/}4"},
		{"spaced asterisks are text", "2 * 3 * 4", "2 * 3 * 4"},
		{"an unterminated strong is text", "**bold", "**bold"},
		{"an unterminated emphasis is text", "a *b c", "a *b c"},
		{"lone delimiters", "a * _ ~~ b", "a * _ ~~ b"},
		{"three and one", "***a** b*", "{bi}a{/}{i} b{/}"},
		{"mismatched closers", "*a**", "{i}a{/}*"},
		{"mixed delimiters do not match", "*a_", "*a_"},
		{"code span", "use `go test` now", "use `go test` now"},
		{"code span styles in colour", "x `y` z", "x `y` z"},
		{"a code span with backticks", "`` a`b ``", "`a`b`"},
		{"an unterminated code span", "a ` b", "a ` b"},
		{"a code span beats emphasis", "*a `b*` c*", "{i}a `b*` c{/}"},
		{"a code span does not contain markup", "`**x**`", "`**x**`"},
		{"escapes", "\\*a\\* \\_b\\_ \\# \\\\ \\x", "*a* _b_ # \\ \\x"},
		{"entities", "&amp; &lt;b&gt; &copy; &#35; &#x41; &bogus; &", "& <b> © # A &bogus; &"},
		{"an entity that is a control character vanishes", "a&#27;b&#0;c", "ab\ufffdc"},
		{"html tags are text", "<b>bold</b> <br> <div class=\"x\"> a < b > c", "<b>bold</b> <br> <div class=\"x\"> a < b > c"},
		{"an html comment is text", "<!-- note -->", "<!-- note -->"},
		{"a link whose text is its address", "[https://a.b](https://a.b)", "{u}https://a.b{/}"},
		{"a link", "[docs](https://a.b/c)", "{u}docs{/}{d} (https://a.b/c){/}"},
		{"a link with a title", "[docs](https://a.b \"The Docs\")", "{u}docs{/}{d} (https://a.b){/}"},
		{"a link with angle brackets", "[d](<a b>)", "{u}d{/}{d} (a b){/}"},
		{"a link with balanced parentheses", "[d](a(b)c)", "{u}d{/}{d} (a(b)c){/}"},
		{"a link with no text", "[](https://a.b)", "{u}https://a.b{/}"},
		{"a link with an empty address", "[text]()", "{u}text{/}"},
		{"emphasis inside a link", "[*a* b](u)", "{iu}a{/}{u} b{/}{d} (u){/}"},
		{"a link inside emphasis", "*a [b](u) c*", "{i}a {/}{iu}b{/}{di} (u){/}{i} c{/}"},
		{"brackets without an address", "[text] and [a][b] and ]", "[text] and [a][b] and ]"},
		{"an unterminated destination", "[text](u", "[text](u"},
		{"an autolink", "see <https://a.b/c> and <me@a.b>", "see {u}https://a.b/c{/} and {u}me@a.b{/}"},
		{"a bare url", "go to https://a.b/c?d=e. Then", "go to {u}https://a.b/c?d=e{/}. Then"},
		{"a bare url in parentheses", "(see https://a.b/c)", "(see {u}https://a.b/c{/})"},
		{"a bare url keeps balanced parentheses", "https://a.b/c_(d)", "{u}https://a.b/c_(d){/}"},
		{"http inside a word is text", "xhttps://a.b", "xhttps://a.b"},
		{"a scheme alone is text", "see https:// now", "see https:// now"},
		{"a link in a link is not nested", "[a [b](c)](d)", "[a {u}b{/}{d} (c){/}](d)"},
		{"a bare url as link text", "[see https://a.b](https://a.b/x)", "{u}see https://a.b{/}{d} (https://a.b/x){/}"},
	}
	for _, c := range cases {
		got := mdStyled(c.src, 60)
		if got != c.want {
			t.Errorf("%s:\n src: %q\n got:  %s\n want: %s", c.name, c.src, got, c.want)
		}
	}
	if got := mdFlat("![a cat](cat.png) and ![](b.png)", 60); got != "[a cat] and [image] (b.png)" {
		t.Errorf("images: %q", got)
	}
	if got := mdFlat("**![a *cat*](c.png)**", 60); got != "[a cat]" {
		t.Errorf("image alt text is plain: %q", got)
	}
}

func TestMarkdownLongURLsAreShortenedNotDropped(t *testing.T) {
	u := "https://example.com/" + strings.Repeat("a", 400)
	got := mdFlat("[t]("+u+")", 400)
	if !strings.HasPrefix(got, "t (https://example.com/aaa") || !strings.HasSuffix(got, "…)") || cell.StringWidth(got) > 170 {
		t.Errorf("a long destination is cut to a readable length: %d cells: %q", cell.StringWidth(got), got)
	}
}

func TestMarkdownStylesInColour(t *testing.T) {
	th := DefaultTheme()
	lines := Markdown("# H1\n\n## H2\n\n### H3\n\n#### H4\n\n##### H5\n\n###### H6", 40, th)
	var styles []cell.Style
	for _, l := range lines {
		if l.Width() > 0 {
			styles = append(styles, l[0].Style)
		}
	}
	if len(styles) != 6 {
		t.Fatalf("%d heading lines", len(styles))
	}
	for i, st := range styles {
		if st != th.Heading[i] {
			t.Errorf("H%d has style %+v, want %+v", i+1, st, th.Heading[i])
		}
	}

	got := Markdown("a **b** *c* ~~d~~ `e` [f](g) > h", 60, th)
	byText := map[string]cell.Style{}
	for _, sp := range got[0] {
		byText[strings.TrimSpace(sp.Text)] = sp.Style
	}
	if !byText["b"].Has(cell.Bold) || !byText["c"].Has(cell.Italic) || !byText["d"].Has(cell.Strike) {
		t.Errorf("inline styles: %+v", byText)
	}
	if byText["e"].FG != th.Code.FG {
		t.Errorf("inline code is in the code colour: %+v", byText["e"])
	}
	if byText["f"].FG != th.Link.FG || !byText["f"].Has(cell.Underline) {
		t.Errorf("link text: %+v", byText["f"])
	}
	if byText["(g)"].FG != th.Dim.FG {
		t.Errorf("the address is dim: %+v", byText["(g)"])
	}

	// emphasis inside a heading keeps the heading's style and adds its own
	h := Markdown("## a *b* `c`", 40, th)[0]
	if h[0].Style != th.Heading[1] {
		t.Errorf("heading text: %+v", h[0].Style)
	}
	if st := h[1].Style; st.FG != th.Heading[1].FG || !st.Has(cell.Italic) || !st.Has(cell.Bold) {
		t.Errorf("emphasis in a heading: %+v", st)
	}
	if st := h[len(h)-1].Style; st.FG != th.Code.FG || !st.Has(cell.Bold) {
		t.Errorf("code in a heading: %+v", st)
	}
}

func TestMarkdownBlockStylesInColour(t *testing.T) {
	th := DefaultTheme()
	code := Markdown("```go\nx := 1\n```", 30, th)
	if len(code) != 2 {
		t.Fatalf("%d rows", len(code))
	}
	for i, l := range code {
		if l.Width() != 30 {
			t.Errorf("row %d: the code slab is as wide as the text area: %d", i, l.Width())
		}
		for _, sp := range l {
			if sp.Style.BG != th.CodeBlock.BG {
				t.Errorf("row %d: span %q is not on the slab: %+v", i, sp.Text, sp.Style)
			}
		}
		if l[0].Text != "│ " || l[0].Style.FG != th.Faint.FG {
			t.Errorf("row %d: the gutter is dim: %+v", i, l[0])
		}
	}
	if !code[0][1].Style.Has(cell.Italic) || code[0][1].Style.FG != th.Dim.FG {
		t.Errorf("the language label is dim and italic: %+v", code[0][1])
	}

	q := Markdown("> quoted\n\nplain", 30, th)
	if q[0][0].Text != "▎ " || q[0][0].Style != th.Quote {
		t.Errorf("quote bar: %+v", q[0][0])
	}
	if !q[0][1].Style.Has(cell.Italic) {
		t.Errorf("quoted text is in the quote style: %+v", q[0][1])
	}
	if q[2][0].Style != th.Text {
		t.Errorf("text after a quote is plain: %+v", q[2][0])
	}

	l := Markdown("- a\n- [x] b\n- [ ] c", 30, th)
	if l[0][0].Text != "• " || l[0][0].Style != th.Dim {
		t.Errorf("bullet: %+v", l[0][0])
	}
	if l[1][0].Text != "[x] " || l[1][0].Style.FG != th.Good.FG || !l[1][0].Style.Has(cell.Bold) {
		t.Errorf("a done task is green: %+v", l[1][0])
	}
	if l[2][0].Text != "[ ] " || l[2][0].Style != th.Dim {
		t.Errorf("an open task is dim: %+v", l[2][0])
	}
	r := Markdown("---", 10, th)
	if r[0][0].Style != th.Faint {
		t.Errorf("rule: %+v", r[0][0])
	}
}

func TestMarkdownInMonoKeepsBackticksAndAttributes(t *testing.T) {
	th := MonoTheme()
	got := Markdown("a `b` **c**", 40, th)[0]
	if got.Plain() != "a `b` c" {
		t.Errorf("mono keeps the backticks of a code span: %q", got.Plain())
	}
	for _, sp := range got {
		if sp.Style.FG.Kind != cell.KindDefault || sp.Style.BG.Kind != cell.KindDefault {
			t.Errorf("colour in the mono theme: %+v", sp)
		}
	}
	col := Markdown("a `b`", 40, DefaultTheme())[0]
	if col.Plain() != "a b" {
		t.Errorf("with colours the code is styled, not quoted: %q", col.Plain())
	}
	asc := Markdown("- a\n> b\n```\nc\n```\n---", 20, DefaultTheme().WithASCII(true))
	if s := widgettest.Flatten(asc); strings.ContainsFunc(s, func(r rune) bool { return r > 127 }) {
		t.Errorf("ASCII output holds non-ASCII:\n%s", s)
	}
}

func TestMarkdownHeadingLevelsDifferInPlainAttributes(t *testing.T) {
	got := mdStyled("# a\n\n## b\n\n### c\n\n#### d\n\n##### e\n\n###### f", 20)
	seen := map[string]bool{}
	for _, ln := range strings.Split(got, "\n") {
		if ln != "" {
			if seen[ln] {
				t.Errorf("two heading levels look the same in mono: %q\n%s", ln, got)
			}
			seen[ln] = true
		}
	}
	if len(seen) != 6 {
		t.Errorf("%d distinct headings:\n%s", len(seen), got)
	}
}

func TestMarkdownWideAndCombiningText(t *testing.T) {
	src := "日本語のテキストです。長い文章は折り返されます。 e\u0301 a\u0308 🐎 👨\u200d👩\u200d👧 x"
	for w := 10; w <= 40; w++ {
		lines := Markdown(src, w, MonoTheme())
		checkLines(t, "wide", lines, w)
		joined := strings.ReplaceAll(widgettest.Flatten(lines), "\n", "")
		joined = strings.ReplaceAll(joined, " ", "")
		if want := strings.ReplaceAll(src, " ", ""); joined != want {
			t.Fatalf("w=%d: text changed: %q != %q", w, joined, want)
		}
	}
}

func TestMarkdownRemovesControlCharactersAndEscapeSequences(t *testing.T) {
	cases := []struct{ src, want string }{
		{"a\x1b[31mb\x1b]52;c;QQ==\x07c", "abc"},
		{"# \x1b[2Jtitle", "title"},
		{"```\n\x1b]0;x\x07code\n```", "│ code"},
		{"- \x1b[1mitem", "• item"},
		{"[t](\x1b]8;;evil\x07u)", "t (u)"},
		{"a\u0085b\u009bc", "abc"},
		{"rm -rf /\rharmless", "rm -rf / harmless"},
		{"\u202Ereversed\u202C", "reversed"},
	}
	for _, c := range cases {
		if got := mdFlat(c.src, 40); got != c.want {
			t.Errorf("Markdown(%q) = %q, want %q", c.src, got, c.want)
		}
	}
	for _, h := range widgettest.Hostile() {
		for _, src := range []string{h, "# " + h, "- " + h, "> " + h, "```\n" + h + "\n```", "[" + h + "](" + h + ")", "| " + h + " |\n|---|\n| " + h + " |", "`" + h + "`", "*" + h + "*"} {
			checkLines(t, "hostile markdown", Markdown(src, 30, DefaultTheme()), 30)
		}
	}
}

func TestMarkdownLimits(t *testing.T) {
	if Markdown("x", 0, MonoTheme()) != nil || Markdown("x", -3, MonoTheme()) != nil {
		t.Error("a width below 1 gives nil")
	}
	// very narrow widths degrade: nothing is wider than the width
	for w := 1; w < 10; w++ {
		for _, src := range []string{"a long paragraph of text", "- a\n  - b\n    - c", "> > > x", "```\ncode code code\n```", "| a | b |\n|--|--|\n| 1 | 2 |", "日本語"} {
			checkLines(t, "narrow", Markdown(src, w, DefaultTheme()), w)
		}
	}
}

func TestMarkdownDeepNestingIsCapped(t *testing.T) {
	quotes := strings.Repeat("> ", 200) + "deep"
	lists := ""
	for i := 0; i < 200; i++ {
		lists += strings.Repeat("  ", i) + "- item\n"
	}
	for _, src := range []string{quotes, lists, strings.Repeat("- ", 300) + "x", strings.Repeat("1. ", 300) + "x", strings.Repeat("> - ", 100) + "x"} {
		for _, w := range []int{10, 40, 120} {
			got := Markdown(src, w, DefaultTheme())
			checkLines(t, "deep", got, w)
			if len(got) > 2*len(src) {
				t.Fatalf("output of %d lines for %d bytes", len(got), len(src))
			}
		}
	}
	// the cap means the innermost text is still there
	if got := mdFlat(quotes, 200); !strings.Contains(got, "deep") {
		t.Errorf("deep text lost: %q", got)
	}
}

// Huge inputs still render, and are still well formed.
func TestMarkdownEnormousInputs(t *testing.T) {
	n := 20_000
	if widgetUnderRace() {
		n = 4_000
	}
	big := map[string]string{
		"one line":        strings.Repeat("word ", n),
		"one long word":   strings.Repeat("x", 10*n),
		"many lines":      strings.Repeat("a line of text\n", n),
		"many paragraphs": strings.Repeat("p\n\n", n),
		"many items":      strings.Repeat("- item\n", n),
		"many openers":    strings.Repeat("*a ", n),
		"many brackets":   strings.Repeat("[a](b ", n),
		"many backticks":  strings.Repeat("`a ``b ```c ", n),
		"many quotes":     strings.Repeat("> q\n", n),
		"many rows":       "| a | b |\n|---|---|\n" + strings.Repeat("| 1 | 2 |\n", n),
		"mixed closers":   strings.Repeat("*a_b**c__d", n),
		"many urls":       strings.Repeat("https://a.b/c ", n),
		"many entities":   strings.Repeat("&amp;&#35;&bogus;", n),
	}
	for name, src := range big {
		lines := Markdown(src, 80, DefaultTheme())
		checkLines(t, name, lines, 80)
		if len(lines) > len(src) {
			t.Errorf("%s: %d lines for %d bytes", name, len(lines), len(src))
		}
	}
}

// The work is linear in the size of the input. Each unit is a shape
// that makes a markdown parser quadratic somewhere if it is written carelessly (repeated string concatenation, a scan for a
// closer from every opener, a prefix re-parsed at every nesting level).
func TestMarkdownScalesLinearly(t *testing.T) {
	units := []string{
		"x", "a line of text\n", "p\n\n", "a\n---\n", "- item\n", "> q\n", "> ", "- [ ] t\n  - n\n", "*a **b ", "*a_b**c__d", "[a](b ", "a] ",
		"`a ``b ```c ", "https://a.b/c ", "&amp;&#35;&bogus; ", "```\ncode\n", "| a | b |\n|---|---|\n| 1 | 2 |\n", "a | b\n",
	}
	for _, unit := range units {
		n := 12_000 / len(unit) // about 12 KB of text; the helper compares it with eight times as much
		requireLinear(t, "Markdown("+strconv.Quote(unit)+"*n)", n, func(n int) {
			Markdown(strings.Repeat(unit, n), 80, DefaultTheme())
		})
	}
}

func TestMarkdownIsDeterministic(t *testing.T) {
	g := widgettest.NewRand(5)
	for i := 0; i < 50; i++ {
		src := g.Markdown(40)
		a, b := Markdown(src, 37, DefaultTheme()), Markdown(src, 37, DefaultTheme())
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("not deterministic for %q", src)
		}
	}
}

// Text with no markup in it comes out whole: nothing is lost or invented, whatever the width.
func TestMarkdownPlainTextIsNeverLost(t *testing.T) {
	g := widgettest.NewRand(6)
	words := []string{"alpha", "beta", "gamma", "日本語", "中文字", "naïve", "e\u0301a", "🐎", "x", "supercalifragilisticexpialidocious"}
	for i := 0; i < 200; i++ {
		var parts []string
		for j := 0; j < 1+g.Intn(30); j++ {
			parts = append(parts, words[g.Intn(len(words))])
		}
		src := strings.Join(parts, " ")
		for _, w := range []int{10, 17, 33, 80} {
			lines := Markdown(src, w, MonoTheme())
			checkLines(t, "plain", lines, w)
			got := strings.Join(strings.Fields(widgettest.Flatten(lines)), "")
			if want := strings.Join(strings.Fields(src), ""); got != want {
				t.Fatalf("w=%d: %q != %q", w, got, want)
			}
		}
	}
}

func TestMarkdownRandomInputAtEveryWidth(t *testing.T) {
	g := widgettest.NewRand(7)
	for i := 0; i < 150; i++ {
		src := g.Markdown(1 + g.Intn(60))
		for _, nt := range allTestThemes() {
			for w := 10; w <= 120; w += 13 + g.Intn(20) {
				lines := Markdown(src, w, nt.th)
				checkLines(t, "random markdown/"+nt.name, lines, w)
				if len(lines) > 2*len(src)+4 {
					t.Fatalf("%d lines for %q", len(lines), src)
				}
			}
		}
	}
}

func TestMarkdownHighlighterHook(t *testing.T) {
	calls := 0
	hl := func(lang, code string) []cell.Line {
		calls++
		var out []cell.Line
		for _, ln := range strings.Split(code, "\n") {
			out = append(out, cell.Line{{Text: lang + ":", Style: cell.Style{Attr: cell.Bold}}, {Text: ln + "\x1b[31m"}})
		}
		return out
	}
	th := DefaultTheme()
	got := MarkdownWith("```go\na\nb\n```\n\n```\nnolang\n```", 30, th, MarkdownOptions{Highlighter: hl})
	if calls != 1 {
		t.Errorf("the hook is asked about a block with a language only: %d calls", calls)
	}
	flat := widgettest.Flatten(got)
	if !strings.Contains(flat, "│ go:a") || !strings.Contains(flat, "│ go:b") || !strings.Contains(flat, "│ nolang") {
		t.Errorf("the highlighted lines replace the plain ones:\n%s", flat)
	}
	checkLines(t, "highlighter", got, 30)
	var bold cell.Span
	for _, sp := range got[1] {
		if strings.HasPrefix(sp.Text, "go:") {
			bold = sp
		}
	}
	if !bold.Style.Has(cell.Bold) || bold.Style.BG != th.CodeBlock.BG {
		t.Errorf("the hook's styles are kept and put on the slab: %+v", bold.Style)
	}
	// a wrong answer or a panic means a plain block
	bad := []Highlighter{
		func(lang, code string) []cell.Line { return []cell.Line{cell.Text("only one")} },
		func(lang, code string) []cell.Line { panic("boom") },
		func(lang, code string) []cell.Line { return nil },
	}
	for i, h := range bad {
		got := widgettest.Flatten(MarkdownWith("```go\na\nb\n```", 30, MonoTheme(), MarkdownOptions{Highlighter: h}))
		if got != "│ go\n│ a\n│ b" {
			t.Errorf("bad highlighter %d: %q", i, got)
		}
	}
}

func TestMarkdownGolden(t *testing.T) {
	doc := mdKitchenSink
	for _, nt := range []themeCase{{"default", DefaultTheme()}, {"mono", MonoTheme()}} {
		for _, w := range []int{72, 36} {
			checkGoldenLines(t, "markdown_"+nt.name+"_"+strconv.Itoa(w), nt.th, Markdown(doc, w, nt.th))
		}
	}
	checkGoldenLines(t, "markdown_ascii_44", DefaultTheme().WithASCII(true), Markdown(mdKitchenSink, 44, DefaultTheme().WithASCII(true)))
}

const mdKitchenSink = "# Fixing the pagination bug\n\n" +
	"The test in `./orders` fails because **`List`** computes the offset from a *zero-based* page, but pages are " +
	"one-based: page 1 skips the first `size` rows. See [the handler](https://example.com/orders/list.go) or <https://example.com/docs>.\n\n" +
	"## What changed\n\n" +
	"- `offset := page * size` became `(page - 1) * size`\n" +
	"- a guard for `page < 1`:\n" +
	"  1. return an error\n" +
	"  2. or clamp to 1\n" +
	"- [x] test added\n- [ ] changelog entry\n\n" +
	"> Note: pages stay one-based in the API.\n> Only the SQL offset changes.\n\n" +
	"```go\nfunc (s *Store) List(ctx context.Context, page, size int) ([]Order, error) {\n\toffset := (page - 1) * size\n\treturn s.query(ctx, offset, size)\n}\n```\n\n" +
	"| page | offset | rows |\n|:-----|-------:|:----:|\n| 1 | 0 | 50 |\n| 2 | 50 | 50 |\n| 3 | 100 | 12 |\n\n" +
	"Setext heading\n--------------\n\n" +
	"~~old text~~ ![diagram](d.png) 日本語のテキストは折り返されます。 and a hard  \nbreak.\n\n---\n\nDone.\n"
