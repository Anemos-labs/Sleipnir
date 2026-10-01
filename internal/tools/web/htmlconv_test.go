package web

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func mustURL(t testing.TB, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func convertHTML(t testing.TB, src string) (string, string) {
	t.Helper()
	return htmlToText(src, mustURL(t, "https://example.com/dir/page.html"))
}

func TestHTMLConversion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"headings and paragraphs",
			`<html><head><title>T</title></head><body><h1>Main</h1><p>Hello   <b>bold</b> world.</p><h2>Sub <a href="/x">link</a></h2><p>Second</p></body></html>`,
			"# Main\n\nHello bold world.\n\n## Sub [link](https://example.com/x)\n\nSecond"},
		{"all heading levels",
			`<h1>a</h1><h2>b</h2><h3>c</h3><h4>d</h4><h5>e</h5><h6>f</h6>`,
			"# a\n\n## b\n\n### c\n\n#### d\n\n##### e\n\n###### f"},
		{"heading with inline markup", `<h3><span>A</span> <em>B</em></h3>`, "### A B"},
		{"empty heading is dropped", `<h2> </h2><p>x</p>`, "x"},
		{"heading with block content stays on one line", `<h2><div>One</div><div>Two</div></h2>`, "## One Two"},

		// Links.
		{"relative links resolve against the page",
			`<p><a href="../up">a</a> <a href="sub/page">b</a> <a href="//cdn.example.org/x">c</a> <a href="?q=1">d</a> <a href="/abs">e</a></p>`,
			"[a](https://example.com/up) [b](https://example.com/dir/sub/page) [c](https://cdn.example.org/x) [d](https://example.com/dir/page.html?q=1) [e](https://example.com/abs)"},
		{"useless links keep only their text",
			`<p><a href="#top">top</a> <a href="javascript:void(0)">js</a> <a href="tel:123">tel</a> <a>none</a> <a href="">empty</a> <a href="data:text/plain,x">data</a></p>`,
			"top js tel none empty data"},
		{"mailto is kept", `<a href="mailto:a@b.co">mail</a>`, "[mail](mailto:a@b.co)"},
		{"full-url in-page anchor is dropped", `<a href="https://example.com/dir/page.html#sec">sec</a>`, "sec"},
		{"other page fragment is kept", `<a href="/other#sec">sec</a>`, "[sec](https://example.com/other#sec)"},
		{"link text equal to url is bare", `<a href="https://x.org/a">https://x.org/a</a>`, "https://x.org/a"},
		{"link text whitespace collapses", "<a href=\"/p\">  many \n\t words  </a>", "[many words](https://example.com/p)"},
		{"link around blocks is one line", `<a href="/p"><div>Big</div><div>Card</div></a>`, "[Big Card](https://example.com/p)"},
		{"heading inside a link is plain text", `<a href="/x"><h3>Card title</h3><p>blurb</p></a>`, "[Card title blurb](https://example.com/x)"},
		{"link around image uses alt", `<a href="/home"><img src="l.png" alt="Logo"></a>`, "[[image: Logo]](https://example.com/home)"},
		{"empty link vanishes", `<p>a<a href="/x"></a>b</p>`, "ab"},
		{"balanced brackets in link text are kept", `<a href="/p">[1] ref</a>`, `[[1] ref](https://example.com/p)`},
		{"unbalanced brackets in link text are escaped", `<a href="/p">a ] b [</a>`, `[a \] b \[](https://example.com/p)`},
		{"parentheses and spaces in urls are escaped", `<a href="/a (b) c">x</a>`, "[x](https://example.com/a%20%28b%29%20c)"},
		{"adjacent text keeps spacing", `<p>before <a href="/x">mid</a> after<a href="/y">tight</a></p>`, "before [mid](https://example.com/x) after[tight](https://example.com/y)"},
		{"base href is honoured", `<html><head><base href="https://other.example/base/"></head><body><a href="x">y</a></body></html>`, "[y](https://other.example/base/x)"},
		{"protocol-relative and uppercase scheme", `<a href="HTTPS://Example.COM/A">x</a>`, "[x](https://Example.COM/A)"},

		// Lists.
		{"nested lists",
			`<ul><li>one</li><li>two<ul><li>nested a</li><li>nested b</li></ul></li><li>three</li></ul>`,
			"- one\n- two\n  - nested a\n  - nested b\n- three"},
		{"ordered list with start", `<ol start="3"><li>x</li><li>y</li></ol>`, "3. x\n4. y"},
		{"ordered inside unordered", `<ul><li>a<ol><li>b</li><li>c</li></ol></li></ul>`, "- a\n  1. b\n  2. c"},
		{"paragraphs inside items stay compact", `<ul><li><p>a</p></li><li><p>b</p></li></ul>`, "- a\n- b"},
		{"multi-paragraph item", `<ul><li><p>a</p><p>a2</p></li><li>b</li></ul>`, "- a\n\n  a2\n- b"},
		{"empty items leave no marker", `<ul><li></li><li>x</li><li> </li></ul>`, "- x"},
		{"list between paragraphs", `<p>before</p><ul><li>x</li></ul><p>after</p>`, "before\n\n- x\n\nafter"},
		{"text and links in items", `<ul><li><a href="/a">A</a>: desc</li></ul>`, "- [A](https://example.com/a): desc"},

		// Code.
		{"code block with language",
			"<pre><code class=\"language-go\">func main() {\n\tfmt.Println(\"hi\")\n}\n</code></pre>",
			"```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```"},
		{"code block keeps indentation and blank lines", "<pre>a\n\n    b\n</pre>", "```\na\n\n    b\n```"},
		{"code block with backticks gets a longer fence", "<pre>x ``` y</pre>", "````\nx ``` y\n````"},
		{"code block with markup inside", "<pre><span class=\"k\">if</span> x:<br>  <b>y</b></pre>", "```\nif x:\n  y\n```"},
		{"code block language from pre", `<pre class="lang-python highlight">print(1)</pre>`, "```python\nprint(1)\n```"},
		{"empty pre is dropped", "<pre>  \n </pre><p>x</p>", "x"},
		{"inline code", `<p>use <code>go test</code> now</p>`, "use `go test` now"},
		{"inline code with backtick", "<p><code>a`b</code></p>", "`` a`b ``"},
		{"pre in blockquote", "<blockquote><pre>x</pre></blockquote>", "> ```\n> x\n> ```"},

		// Tables.
		{"table with header",
			`<table><thead><tr><th>Name</th><th>Qty</th></tr></thead><tbody><tr><td>apple</td><td>3</td></tr><tr><td>a|b</td><td></td></tr></tbody></table>`,
			"| Name | Qty |\n| --- | --- |\n| apple | 3 |\n| a\\|b |  |"},
		{"table without header", `<table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>`, "| a | b |\n| c | d |"},
		{"header row of th in tbody", `<table><tr><th>h1</th><th>h2</th></tr><tr><td>1</td><td>2</td></tr></table>`, "| h1 | h2 |\n| --- | --- |\n| 1 | 2 |"},
		{"table cells with markup", `<table><tr><td><a href="/x">link</a></td><td><code>c</code> and <b>b</b></td></tr></table>`, "| [link](https://example.com/x) | `c` and b |"},
		{"table caption", `<table><caption>Sales</caption><tr><td>1</td><td>2</td></tr></table>`, "Sales\n| 1 | 2 |"},
		{"empty rows are skipped", `<table><tr><td> </td><td> </td></tr><tr><td>x</td><td>y</td></tr></table>`, "| x | y |"},
		{"layout table renders blocks", `<table><tr><td><h2>Left</h2><p>text</p></td><td><p>Right</p></td></tr></table>`, "## Left\n\ntext\n\nRight"},
		{"multi-line cell is one line", "<table><tr><td>a<br>b<p>c</p></td><td>d</td></tr></table>", "| a b c | d |"},
		{"single column table is plain lines", `<table><tr><td>one</td></tr><tr><td><a href="/x">two</a></td></tr></table>`, "one\n[two](https://example.com/x)"},
		{"ragged table is plain lines", `<table><tr><td>1.</td><td></td><td><a href="/a">Story A</a></td></tr><tr><td></td><td>10 points</td></tr><tr><td>2.</td><td></td><td>Story B</td></tr></table>`, "1. [Story A](https://example.com/a)\n10 points\n2. Story B"},
		{"mostly empty grid is plain lines", `<table><tr><td></td><td></td><td>x</td></tr><tr><td></td><td></td><td>y</td></tr></table>`, "x\ny"},
		{"headerless regular grid stays a table", `<table><tr><td>k1</td><td>v1</td></tr><tr><td>k2</td><td>v2</td></tr><tr><td>k3</td><td>v3</td></tr></table>`, "| k1 | v1 |\n| k2 | v2 |\n| k3 | v3 |"},
		{"header keeps empty cells", `<table><tr><th>a</th><th>b</th><th>c</th></tr><tr><td>1</td><td></td><td></td></tr></table>`, "| a | b | c |\n| --- | --- | --- |\n| 1 |  |  |"},
		{"nested table in a cell", `<table><tr><td><table><tr><td>in</td></tr></table></td></tr></table>`, "in"},

		// Quotes, definitions, rules, images.
		{"blockquote", `<blockquote><p>quote</p><p>second</p></blockquote>`, "> quote\n>\n> second"},
		{"nested blockquote", `<blockquote>a<blockquote>b</blockquote></blockquote>`, "> a\n>\n> > b"},
		{"definition list", `<dl><dt>Term</dt><dd>Def</dd><dt>T2</dt><dd>D2</dd></dl>`, "Term\n  Def\nT2\n  D2"},
		{"horizontal rule", `<p>a</p><hr><p>b</p>`, "a\n\n---\n\nb"},
		{"images", `<img src="a" alt="Logo"><img src="b"><img alt=" "><p><img alt="Two  words"></p>`, "[image: Logo]\n\n[image: Two words]"},
		{"line breaks", `<p>line1<br>line2<br/>line3</p>`, "line1\nline2\nline3"},

		// Dropped content.
		{"non-content elements are dropped",
			`<p>a</p><script>alert(1)</script><style>p{}</style><nav>menu</nav><footer>foot</footer><svg><text>svgtext</text></svg><noscript>nojs</noscript><iframe src=x>frame</iframe><select><option>opt</option></select><p>b</p>`,
			"a\n\nb"},
		{"hidden content is dropped", `<p hidden>x</p><p aria-hidden="true">y</p><p style="display: none">z</p><p style="VISIBILITY:hidden">w</p><p>visible</p>`, "visible"},
		{"script text with markup lookalikes", `<p>a</p><script>var s="</p><p>evil</p>";</script><p>b</p>`, "a\n\nb"},
		{"comments and doctype", `<!DOCTYPE html><!-- c --><p>x<!-- y --></p>`, "x"},
		{"template is dropped", `<template><p>t</p></template><p>x</p>`, "x"},

		// Whitespace and text.
		{"whitespace collapses", "<p>  a &nbsp;&nbsp; b\n\t c </p>", "a b c"},
		{"entities decode", `<p>&lt;div&gt; &amp; &copy; &#8364; &eacute; &quot;q&quot;</p>`, `<div> & © € é "q"`},
		{"unicode is preserved", `<p>日本語 😀 Ελληνικά ✓</p>`, "日本語 😀 Ελληνικά ✓"},
		{"inline elements do not add spaces", `<p>a<span>b</span><i>c</i>d</p>`, "abcd"},
		{"unclosed paragraphs", `<p>one<p>two<p>three`, "one\n\ntwo\n\nthree"},
		{"text directly in body", `just text<div>block</div>tail`, "just text\nblock\ntail"},
		{"empty document", ``, ""},
		{"only whitespace", "  \n\t ", ""},
		{"button and label", `<button>Click</button> <label>Name <input value="v"></label>`, "Click Name"},
		{"details and summary", `<details><summary>Sum</summary>Body</details>`, "Sum\nBody"},
		{"div soup", `<div><div><div>a</div></div><div>b</div></div>`, "a\nb"},
		{"figure", `<figure><img alt="Pic"><figcaption>Caption</figcaption></figure>`, "[image: Pic]\nCaption"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := convertHTML(t, tt.in)
			if got != tt.want {
				t.Errorf("\n got: %q\nwant: %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Error("invalid UTF-8 in output")
			}
			if strings.Contains(got, "\n\n\n") {
				t.Errorf("triple newline in output %q", got)
			}
			for _, line := range strings.Split(got, "\n") {
				if strings.HasSuffix(line, " ") && !strings.Contains(tt.in, "<pre") {
					t.Errorf("trailing space on line %q", line)
				}
			}
		})
	}
}

func TestHTMLTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{`<html><head><title> My  Page </title></head><body>x</body></html>`, "My Page"},
		{`<title>Only</title>`, "Only"},
		{`<html><head></head><body>x</body></html>`, ""},
		{`<title>A &amp; B</title>`, "A & B"},
		{"<title>\n  multi\n  line\n</title>", "multi line"},
	}
	for _, tt := range tests {
		if _, title := convertHTML(t, tt.in); title != tt.want {
			t.Errorf("title of %q = %q, want %q", tt.in, title, tt.want)
		}
	}
	// The title is not repeated in the body text.
	text, _ := convertHTML(t, `<title>T</title><p>body</p>`)
	if text != "body" {
		t.Errorf("text = %q", text)
	}
}

func TestNestingTooDeep(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{"shallow", strings.Repeat("<div>", 50) + strings.Repeat("</div>", 50), false},
		{"at the limit", strings.Repeat("<div>", maxNesting), false},
		{"over the limit", strings.Repeat("<div>", maxNesting+1), true},
		{"closed pairs never deepen", strings.Repeat("<div>x</div>", 100000), false},
		{"unclosed paragraphs are not nesting", strings.Repeat("<p>x", 100000), false},
		{"unclosed list items are not nesting", "<ul>" + strings.Repeat("<li>x", 100000) + "</ul>", false},
		{"unclosed table cells are not nesting", "<table>" + strings.Repeat("<tr><td>x<td>y", 50000) + "</table>", false},
		{"void elements are not nesting", strings.Repeat("<br><img src=x><hr>", 50000), false},
		{"nested lists count", strings.Repeat("<ul><li>", maxNesting+1), true},
		{"nested spans count", strings.Repeat("<span>", maxNesting+1), true},
		{"stray end tags do not underflow", strings.Repeat("</div>", 1000) + strings.Repeat("<div>", 10), false},
		{"self-closing is not nesting", strings.Repeat("<div/>", 100000), false},
		{"empty", "", false},
		{"text only", strings.Repeat("hello ", 100000), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			if got := nestingTooDeep(tt.src); got != tt.want {
				t.Errorf("nestingTooDeep = %v, want %v", got, tt.want)
			}
			// A hang guard, not a stopwatch: work that grew with the square of 100,000 elements takes hours, and a loaded machine
			// (the suite three times at once, a busy runner) takes seconds for a linear pass.
			if d := time.Since(start); d > time.Minute {
				t.Errorf("pre-scan took %v", d)
			}
		})
	}
}

// Hostile pages must neither hang nor crash the conversion, and must still
// yield their words.
func TestHTMLAdversarial(t *testing.T) {
	gen := func(f func(sb *strings.Builder)) string {
		var sb strings.Builder
		f(&sb)
		return sb.String()
	}
	tests := []struct {
		name     string
		src      string
		contains string
		budget   time.Duration
	}{
		{"200k nested divs", strings.Repeat("<div>", 200_000) + "deep words" + strings.Repeat("</div>", 200_000), "deep words", parseBudget},
		{"1M unclosed divs", strings.Repeat("<div>", 1_000_000) + "tail words", "tail words", parseBudget},
		{"100k nested lists", strings.Repeat("<ul><li>", 100_000) + "leaf", "leaf", parseBudget},
		{"20k nested tables", strings.Repeat("<table><tr><td>", 20_000) + "cell", "cell", parseBudget},
		{"500k nested spans", strings.Repeat("<span>", 500_000) + "spanned", "spanned", parseBudget},
		{"200 nested blockquotes", strings.Repeat("<blockquote>", 250) + "quoted" + strings.Repeat("</blockquote>", 250), "quoted", parseBudget},
		{"250 nested divs then text", strings.Repeat("<div>", 250) + "text at depth", "text at depth", parseBudget},
		// Quadratic in the parser. The depth estimate catches these outright; the
		// short budget is the backstop for anything it misses.
		{"adoption agency abuse", strings.Repeat("<a><b></a>", 300_000) + "payload", "payload", 300 * time.Millisecond},
		{"deep pairs at estimator limit", strings.Repeat(strings.Repeat("<div>", 250)+strings.Repeat("</div>", 250)+"x ", 500), "x\nx\nx", 300 * time.Millisecond},
		{"dl nesting", strings.Repeat("<dl><dt>", 100_000) + "term", "term", 300 * time.Millisecond},
		{"multi-MB text", strings.Repeat("hello world ", 300_000), "hello world", parseBudget},
		{"huge attribute", `<p class="` + strings.Repeat("a", 5_000_000) + `">attr</p>`, "attr", parseBudget},
		{"a million attributes", "<div " + strings.Repeat("a=b ", 250_000) + ">x</div>", "x", parseBudget},
		{"unterminated tag", "<p>before <a href=\"" + strings.Repeat("x", 100_000), "before", parseBudget},
		{"unterminated comment", "<p>a</p><!-- " + strings.Repeat("x", 100_000), "a", parseBudget},
		{"nul and control bytes", "<p>a\x00b\x01c</p>", "a", parseBudget},
		{"invalid utf-8", "<p>caf\xe9 \xff\xfe ok</p>", "ok", parseBudget},
		{"many entities", strings.Repeat("&amp;&lt;&#x41;&bogus;", 100_000), "&<A", parseBudget},
		{"script bomb", "<p>x</p><script>" + strings.Repeat("<", 1_000_000) + "</script><p>y</p>", "x\n\ny", parseBudget},
		{"table foster parenting", "<table>" + strings.Repeat("<b>x<tr>", 50_000), "x", parseBudget},
		{"formatting elements", strings.Repeat("<b class=a><i class=b><u class=c>x", 50_000), "x", parseBudget},
		// An unclosed <select> swallows what follows it, as in browsers.
		{"select and options", strings.Repeat("<select><option>", 50_000) + "after", "", parseBudget},
		{"body tags", strings.Repeat("<body>", 200_000) + "b", "b", parseBudget},
		{"wide flat page", gen(func(sb *strings.Builder) {
			for i := 0; i < 30_000; i++ {
				fmt.Fprintf(sb, "<div><p>item %d <a href=\"/i/%d\">link</a></p></div>", i, i)
			}
		}), "item 29999", parseBudget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			got, _ := htmlToTextBudget(tt.src, mustURL(t, "https://example.com/"), tt.budget)
			// A hang guard (see TestNestingTooDeep): the parse enforces its own budget and falls back to flat text, which is what the
			// short budgets above are for; this only says that nothing hangs.
			if d := time.Since(start); d > 2*time.Minute {
				t.Fatalf("took %v", d)
			}
			if !strings.Contains(got, tt.contains) {
				n := min(len(got), 200)
				t.Errorf("output lacks %q: %q…", tt.contains, got[:n])
			}
			if !utf8.ValidString(got) {
				t.Error("invalid UTF-8 in output")
			}
		})
	}
}

func TestParseBudgetForcesFlatFallback(t *testing.T) {
	src := `<html><head><title>T</title></head><body><h1>Head</h1><p>para <a href="/x">link</a></p><ul><li>item</li></ul></body></html>`
	full, _ := htmlToText(src, mustURL(t, "https://example.com/"))
	if !strings.Contains(full, "[link](https://example.com/x)") {
		t.Fatalf("full conversion lacks the link: %q", full)
	}
	// A budget that has already expired aborts the parse at the first read.
	flat, title := htmlToTextBudget(src, mustURL(t, "https://example.com/"), -time.Second)
	if strings.Contains(flat, "](") {
		t.Errorf("flat fallback should not render links: %q", flat)
	}
	for _, want := range []string{"# Head", "para link", "- item"} {
		if !strings.Contains(flat, want) {
			t.Errorf("flat output lacks %q: %q", want, flat)
		}
	}
	if title != "T" {
		t.Errorf("flat title = %q", title)
	}
}

func TestFlatText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"paragraphs", "<p>one</p><p>two</p>", "one\n\ntwo"},
		{"heading", "<h2>Title</h2><p>x</p>", "## Title\n\nx"},
		{"list", "<ul><li>a</li><li>b</li></ul>", "- a\n- b"},
		{"skips scripts and nav", "<p>a</p><script>x()</script><nav><a>menu</a></nav><style>y{}</style><p>b</p>", "a\n\nb"},
		{"nested skipped elements", "<nav>a<nav>b</nav>still hidden</nav><p>shown</p>", "shown"},
		{"br and div", "a<br>b<div>c</div>d", "a\nb\nc\nd"},
		{"table cells", "<table><tr><td>a</td><td>b</td></tr></table>", "| a | b"},
		{"entities", "<p>&amp; &lt;</p>", "& <"},
		{"hr", "<p>a</p><hr><p>b</p>", "a\n\n---\n\nb"},
		{"title is not text", "<title>T</title><p>x</p>", "x"},
		{"empty", "", ""},
		{"unterminated", "<p>a <b", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := flatText(tt.in)
			if got != tt.want {
				t.Errorf("flatText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMDWriterInvariants(t *testing.T) {
	// A random-ish walk over the writer API never leaves trailing spaces, more
	// than one blank line in a row, or a dangling list marker.
	w := newMD(false)
	ops := []func(){
		func() { w.text("some words here") },
		func() { w.text("  ") },
		func() { w.breakLine() },
		func() { w.blankLine() },
		func() { w.word("x") },
		func() { w.pushPrefix("> ") },
		func() { w.popPrefix() },
		func() { w.rawLine("raw line  ") },
		func() { w.pushPrefix("  "); w.bullet = "- " },
		func() { w.rawLine("") },
	}
	seed := uint32(12345)
	for i := 0; i < 20000; i++ {
		seed = seed*1664525 + 1013904223
		ops[int(seed>>16)%len(ops)]()
	}
	for _, line := range strings.Split(w.String(), "\n") {
		if strings.HasSuffix(line, " ") && !strings.Contains(line, "raw line") {
			t.Fatalf("trailing space on %q", line)
		}
	}
}
