package mdlite_test

import (
	"testing"

	"example.com/mdlite"
)

// Acceptance tests for mdlite. Every expectation follows from README.md: the first test replays its examples, the
// others apply its rules one by one and in combination.

type specCase struct {
	name string
	in   string
	want string
}

func runSpecCases(t *testing.T, cases []specCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mdlite.Render(c.in); got != c.want {
				t.Errorf("Render(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

func TestSpecReadmeExamples(t *testing.T) {
	runSpecCases(t, []specCase{
		{
			"1 headings",
			"# One\n## Two\n###### Six\n####### Seven\n#NoSpace\n",
			"<h1>One</h1>\n<h2>Two</h2>\n<h6>Six</h6>\n<p>####### Seven\n#NoSpace</p>\n",
		},
		{
			"2 soft and hard breaks",
			"first\nsecond\n\n\nroses are red  \nviolets are blue\n",
			"<p>first\nsecond</p>\n<p>roses are red<br>\nviolets are blue</p>\n",
		},
		{
			"3 emphasis strong code nesting",
			"Mix of *a*, _b_, **c**, `d` and **e *f* g**, and *h **i** j*.\n",
			"<p>Mix of <em>a</em>, <em>b</em>, <strong>c</strong>, <code>d</code> and <strong>e <em>f</em> g</strong>, and <em>h <strong>i</strong> j</em>.</p>\n",
		},
		{
			"4 delimiters that stay literal",
			"2 * 3 = 6, snake_case_name, **oops and *x *y and `tick and _z\n",
			"<p>2 * 3 = 6, snake_case_name, **oops and *x *y and `tick and _z</p>\n",
		},
		{
			"5 underscore rules",
			"_a_ (_b_) _c_b 2*3*4 a**b**c\n",
			"<p><em>a</em> (<em>b</em>) _c_b 2<em>3</em>4 a<strong>b</strong>c</p>\n",
		},
		{
			"6 links",
			"See [the *docs*](https://example.com/a?x=1&y=2) and [empty]() or [a b](c d) or ![img](u.png)\n",
			"<p>See <a href=\"https://example.com/a?x=1&amp;y=2\">the <em>docs</em></a> and [empty]() or [a b](c d) or !<a href=\"u.png\">img</a></p>\n",
		},
		{
			"7 escaping",
			"5 < 6 & 7 > 3, \"quoted\" and 'single' &amp; <b>\n",
			"<p>5 &lt; 6 &amp; 7 &gt; 3, &#34;quoted&#34; and &#39;single&#39; &amp;amp; &lt;b&gt;</p>\n",
		},
		{
			"8 code spans",
			"Use `<b>*x*</b> & more`, or *see `*` here*.\n",
			"<p>Use <code>&lt;b&gt;*x*&lt;/b&gt; &amp; more</code>, or <em>see <code>*</code> here</em>.</p>\n",
		},
		{
			"9 fenced code with a language",
			"```go\nfunc main() {\n    fmt.Println(\"<hi>\") // *not* emphasis\n}\n\n// after a blank line\n```\n",
			"<pre><code class=\"language-go\">func main() {\n    fmt.Println(&#34;&lt;hi&gt;&#34;) // *not* emphasis\n}\n\n// after a blank line\n</code></pre>\n",
		},
		{
			"10 fences: no language, a line that does not close, unclosed",
			"```\n# not a heading\n- not a list\n```js\n```\nafter\n```\nnever closed\n",
			"<pre><code># not a heading\n- not a list\n```js\n</code></pre>\n<p>after</p>\n<pre><code>never closed\n</code></pre>\n",
		},
		{
			"11 bullet list interrupts a paragraph",
			"Shopping list:\n- *fresh* eggs\n* **two** litres of milk\n+ [bread](/b)\n",
			"<p>Shopping list:</p>\n<ul>\n<li><em>fresh</em> eggs</li>\n<li><strong>two</strong> litres of milk</li>\n<li><a href=\"/b\">bread</a></li>\n</ul>\n",
		},
		{
			"12 numbered lists and kinds",
			"3. three\n1. one\n- dash\n10. ten\n",
			"<ol>\n<li>three</li>\n<li>one</li>\n</ol>\n<ul>\n<li>dash</li>\n</ul>\n<ol>\n<li>ten</li>\n</ol>\n",
		},
		{
			"13 what ends a list",
			"- a\n\n- b\n- c\nnot an item\n  - nested is not supported\n",
			"<ul>\n<li>a</li>\n</ul>\n<ul>\n<li>b</li>\n<li>c</li>\n</ul>\n<p>not an item\n- nested is not supported</p>\n",
		},
		{
			"14 column 0 only",
			"  # not a heading\n  - not an item\n",
			"<p># not a heading\n- not an item</p>\n",
		},
		{
			"15 not supported",
			"> quote\n<div>raw</div>\n---\n",
			"<p>&gt; quote\n&lt;div&gt;raw&lt;/div&gt;\n---</p>\n",
		},
		{
			"16 heading text",
			"##   A *bold* `move` ##  \n",
			"<h2>A <em>bold</em> <code>move</code> ##</h2>\n",
		},
		{
			"17 line endings",
			"a\r\nb  \r\nc\r\n",
			"<p>a\nb<br>\nc</p>\n",
		},
		{"18 empty", "", ""},
		{"18 newline only", "\n", ""},
		{"18 blank lines only", "  \n\t\n\n", ""},
		{
			"19 whole document",
			"# Title\n\nIntro with *emphasis* and a [link](http://x.y).\nSecond line  \nafter a hard break.\n\n## Steps\n\n1. Install `mdlite`\n2. Run **it**\n\n- fast\n- small\n\n```sh\necho \"hi\" && ls\n```\n\nThe end.\n",
			"<h1>Title</h1>\n<p>Intro with <em>emphasis</em> and a <a href=\"http://x.y\">link</a>.\nSecond line<br>\nafter a hard break.</p>\n<h2>Steps</h2>\n<ol>\n<li>Install <code>mdlite</code></li>\n<li>Run <strong>it</strong></li>\n</ol>\n<ul>\n<li>fast</li>\n<li>small</li>\n</ul>\n<pre><code class=\"language-sh\">echo &#34;hi&#34; &amp;&amp; ls\n</code></pre>\n<p>The end.</p>\n",
		},
	})
}

func TestSpecHeadings(t *testing.T) {
	runSpecCases(t, []specCase{
		{
			"all levels",
			"# h1\n## h2\n### h3\n#### h4\n##### h5\n###### h6\n",
			"<h1>h1</h1>\n<h2>h2</h2>\n<h3>h3</h3>\n<h4>h4</h4>\n<h5>h5</h5>\n<h6>h6</h6>\n",
		},
		{"tab after the hashes", "#\tTab title\n", "<h1>Tab title</h1>\n"},
		{"whitespace around the text is removed", "##   spaced out   \n", "<h2>spaced out</h2>\n"},
		{"closing hashes are text", "# Title #\n", "<h1>Title #</h1>\n"},
		{"no whitespace after the hashes", "#hash\n", "<p>#hash</p>\n"},
		{"seven hashes", "####### seven\n", "<p>####### seven</p>\n"},
		{"escaping", "# A & B <c> \"d\" 'e'\n", "<h1>A &amp; B &lt;c&gt; &#34;d&#34; &#39;e&#39;</h1>\n"},
		{
			"inline syntax",
			"## A *b* and `c` and [d](e) and _f_ and **g**\n",
			"<h2>A <em>b</em> and <code>c</code> and <a href=\"e\">d</a> and <em>f</em> and <strong>g</strong></h2>\n",
		},
		{"between paragraphs", "text\n# Head\nmore\n", "<p>text</p>\n<h1>Head</h1>\n<p>more</p>\n"},
		{"indented hash is text", " # no\n", "<p># no</p>\n"},
		{"heading after heading", "# a\n# b\n", "<h1>a</h1>\n<h1>b</h1>\n"},
		{"heading ends a list", "- a\n# H\n", "<ul>\n<li>a</li>\n</ul>\n<h1>H</h1>\n"},
		{"list after a heading", "# H\n- a\n", "<h1>H</h1>\n<ul>\n<li>a</li>\n</ul>\n"},
	})
}

func TestSpecParagraphs(t *testing.T) {
	runSpecCases(t, []specCase{
		{"blank lines separate paragraphs", "one\n\n\n\ntwo\n", "<p>one</p>\n<p>two</p>\n"},
		{"soft breaks", "a\nb\nc\n", "<p>a\nb\nc</p>\n"},
		{"leading whitespace is removed", "  a\n\t b\n", "<p>a\nb</p>\n"},
		{"hard breaks need two spaces", "a  \nb   \nc \nd  \n", "<p>a<br>\nb<br>\nc\nd</p>\n"},
		{"hard break then emphasis", "*a*  \n*b*\n", "<p><em>a</em><br>\n<em>b</em></p>\n"},
		{"trailing spaces on the last line", "a  ", "<p>a</p>\n"},
		{"no final newline", "a", "<p>a</p>\n"},
		{"emphasis does not span lines", "*a\nb*\n", "<p>*a\nb*</p>\n"},
		{"code span does not span lines", "`a\nb`\n", "<p>`a\nb`</p>\n"},
		{"link does not span lines", "[a\n](b)\n", "<p>[a\n](b)</p>\n"},
		{"whitespace-only lines are blank", "a\n   \t \nb\n", "<p>a</p>\n<p>b</p>\n"},
		{"tab-only line is blank", "a\n\t\nb\n", "<p>a</p>\n<p>b</p>\n"},
		{
			"a fence interrupts a paragraph",
			"text\n```\ncode\n```\nafter\n",
			"<p>text</p>\n<pre><code>code\n</code></pre>\n<p>after</p>\n",
		},
		{"a bullet list interrupts a paragraph", "intro\n- a\n- b\n", "<p>intro</p>\n<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n"},
		{"a numbered list interrupts a paragraph", "intro\n1. a\n", "<p>intro</p>\n<ol>\n<li>a</li>\n</ol>\n"},
		{
			"crlf",
			"# T\r\n\r\npara\r\nline2  \r\nline3\r\n",
			"<h1>T</h1>\n<p>para\nline2<br>\nline3</p>\n",
		},
		{"text after a blank line continues nothing", "a\n\nb\nc\n", "<p>a</p>\n<p>b\nc</p>\n"},
	})
}

func TestSpecEmphasis(t *testing.T) {
	runSpecCases(t, []specCase{
		{"the three forms", "*a* _b_ **c**\n", "<p><em>a</em> <em>b</em> <strong>c</strong></p>\n"},
		{"several words", "*a b c* and **d e f**\n", "<p><em>a b c</em> and <strong>d e f</strong></p>\n"},
		{"several on a line", "**a** and *b* and _c_ and **d**\n", "<p><strong>a</strong> and <em>b</em> and <em>c</em> and <strong>d</strong></p>\n"},
		{"opener followed by a space, closer preceded by a space", "x * a* and *b *\n", "<p>x * a* and *b *</p>\n"},
		{"closer preceded by a space", "a *b * c\n", "<p>a *b * c</p>\n"},
		{"strong closer preceded by a space", "**a **b\n", "<p>**a **b</p>\n"},
		{"strong inside emphasis (underscore)", "_a **b** c_\n", "<p><em>a <strong>b</strong> c</em></p>\n"},
		{"emphasis inside strong (underscore)", "**a _b_ c**\n", "<p><strong>a <em>b</em> c</strong></p>\n"},
		{"strong inside emphasis (asterisk)", "*a **b** c*\n", "<p><em>a <strong>b</strong> c</em></p>\n"},
		{"emphasis inside strong (asterisk)", "**a *b* c**\n", "<p><strong>a <em>b</em> c</strong></p>\n"},
		{
			"underscores inside words",
			"snake_case_name, _em_, (_em_), _em_.\n",
			"<p>snake_case_name, <em>em</em>, (<em>em</em>), <em>em</em>.</p>\n",
		},
		{"underscore closer followed by a word character", "_a_b\n", "<p>_a_b</p>\n"},
		{"underscore opener preceded by a word character", "c_d_ e\n", "<p>c_d_ e</p>\n"},
		{"asterisks inside words", "2*3*4 and a**b**c\n", "<p>2<em>3</em>4 and a<strong>b</strong>c</p>\n"},
		{
			"unmatched delimiters",
			"a * b, **c, `d, [e, (f) and _g\n",
			"<p>a * b, **c, `d, [e, (f) and _g</p>\n",
		},
		{
			"emphasis around code",
			"*`code`* and **`x`**\n",
			"<p><em><code>code</code></em> and <strong><code>x</code></strong></p>\n",
		},
		{"a code span hides a single asterisk", "*a `*` b*\n", "<p><em>a <code>*</code> b</em></p>\n"},
		{"a code span hides a double asterisk", "**a `**` b**\n", "<p><strong>a <code>**</code> b</strong></p>\n"},
		{"a code span hides an underscore", "_a `_` b_\n", "<p><em>a <code>_</code> b</em></p>\n"},
		{
			"punctuation around and inside",
			"(*a*) [*b*] \"_c_\" *d, e!*\n",
			"<p>(<em>a</em>) [<em>b</em>] &#34;<em>c</em>&#34; <em>d, e!</em></p>\n",
		},
		{
			"emphasis around links",
			"*[a](b)* **[c](d)**\n",
			"<p><em><a href=\"b\">a</a></em> <strong><a href=\"d\">c</a></strong></p>\n",
		},
		{"escaping inside emphasis", "*a & b* **<c>**\n", "<p><em>a &amp; b</em> <strong>&lt;c&gt;</strong></p>\n"},
		{"non-ASCII text", "_café_ and *naïve*\n", "<p><em>café</em> and <em>naïve</em></p>\n"},
	})
}

func TestSpecCodeSpans(t *testing.T) {
	runSpecCases(t, []specCase{
		{"basic", "`a` and `b c` and ` d `\n", "<p><code>a</code> and <code>b c</code> and <code> d </code></p>\n"},
		{
			"escaping and no inline syntax",
			"`*not em*` `<b>` `a&b` `\"q\"` `'s'`\n",
			"<p><code>*not em*</code> <code>&lt;b&gt;</code> <code>a&amp;b</code> <code>&#34;q&#34;</code> <code>&#39;s&#39;</code></p>\n",
		},
		{"unmatched backtick", "one ` tick\n", "<p>one ` tick</p>\n"},
		{"brackets inside", "`[a](b)`\n", "<p><code>[a](b)</code></p>\n"},
		{"inside link text", "[`x`](y)\n", "<p><a href=\"y\"><code>x</code></a></p>\n"},
		{"in a list item", "- `a` and `b`\n", "<ul>\n<li><code>a</code> and <code>b</code></li>\n</ul>\n"},
	})
}

func TestSpecLinks(t *testing.T) {
	runSpecCases(t, []specCase{
		{"basic", "[a](b)\n", "<p><a href=\"b\">a</a></p>\n"},
		{"url is escaped", "[q](/a?b=1&c=2)\n", "<p><a href=\"/a?b=1&amp;c=2\">q</a></p>\n"},
		{"quotes in the url", "[q](/a\"b'c<d>)\n", "<p><a href=\"/a&#34;b&#39;c&lt;d&gt;\">q</a></p>\n"},
		{
			"inline syntax in the text",
			"[*a* and **b** `c`](d)\n",
			"<p><a href=\"d\"><em>a</em> and <strong>b</strong> <code>c</code></a></p>\n",
		},
		{
			"things that are not links",
			"[](x) [y]() [z](a b) [w] (v) [u](t\n",
			"<p>[](x) [y]() [z](a b) [w] (v) [u](t</p>\n",
		},
		{"images are not special", "![alt](src.png)\n", "<p>!<a href=\"src.png\">alt</a></p>\n"},
		{
			"links and emphasis nest",
			"*see [docs](/d)* and _[x](y)_\n",
			"<p><em>see <a href=\"/d\">docs</a></em> and <em><a href=\"y\">x</a></em></p>\n",
		},
		{
			"adjacent links",
			"[a](b)[c](d) [e](f)\n",
			"<p><a href=\"b\">a</a><a href=\"d\">c</a> <a href=\"f\">e</a></p>\n",
		},
		{"the first parenthesis closes the url", "[a](b)c)\n", "<p><a href=\"b\">a</a>c)</p>\n"},
		{"reference links are text", "[a][b]\n\n[b]: /x\n", "<p>[a][b]</p>\n<p>[b]: /x</p>\n"},
		{"in a heading", "# See [x](/y)\n", "<h1>See <a href=\"/y\">x</a></h1>\n"},
		{"in a list item", "- [x](/y) and [z](/w)\n", "<ul>\n<li><a href=\"/y\">x</a> and <a href=\"/w\">z</a></li>\n</ul>\n"},
	})
}

func TestSpecEscaping(t *testing.T) {
	runSpecCases(t, []specCase{
		{"entities are not special", "&amp; &lt;b&gt; &#34;\n", "<p>&amp;amp; &amp;lt;b&amp;gt; &amp;#34;</p>\n"},
		{
			"non-ASCII text is copied",
			"Café ☕ *naïve* `ü` [日本](/x)\n",
			"<p>Café ☕ <em>naïve</em> <code>ü</code> <a href=\"/x\">日本</a></p>\n",
		},
		{"raw html is text", "<script>alert(1)</script>\n", "<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>\n"},
		{"list items", "- a < b & c\n", "<ul>\n<li>a &lt; b &amp; c</li>\n</ul>\n"},
		{"quotes", "say \"hi\" it's\n", "<p>say &#34;hi&#34; it&#39;s</p>\n"},
		{
			"the language of a fence",
			"```a\"b\nx\n```\n",
			"<pre><code class=\"language-a&#34;b\">x\n</code></pre>\n",
		},
	})
}

func TestSpecFencedCode(t *testing.T) {
	runSpecCases(t, []specCase{
		{
			"language",
			"```python\nprint('<hi>')\n```\n",
			"<pre><code class=\"language-python\">print(&#39;&lt;hi&gt;&#39;)\n</code></pre>\n",
		},
		{
			"only the first word of the info string is the language",
			"```  go  extra words \nx\n```\n",
			"<pre><code class=\"language-go\">x\n</code></pre>\n",
		},
		{"space between the fence and the language", "``` go\nx\n```\n", "<pre><code class=\"language-go\">x\n</code></pre>\n"},
		{
			"content is verbatim",
			"```\n# not heading\n- not list\n  indented\n\n*x* `y`\n   \n```\n",
			"<pre><code># not heading\n- not list\n  indented\n\n*x* `y`\n   \n</code></pre>\n",
		},
		{"unclosed", "```js\nlet a = 1;\n", "<pre><code class=\"language-js\">let a = 1;\n</code></pre>\n"},
		{"empty", "```\n```\n", "<pre><code></code></pre>\n"},
		{"unclosed and empty", "```", "<pre><code></code></pre>\n"},
		{
			"only three backticks close",
			"```\na\n```js\n````\n```\nafter\n",
			"<pre><code>a\n```js\n````\n</code></pre>\n<p>after</p>\n",
		},
		{
			"consecutive blocks",
			"```\na\n```\n```\nb\n```\n",
			"<pre><code>a\n</code></pre>\n<pre><code>b\n</code></pre>\n",
		},
		{
			"between paragraphs",
			"a\n\n```\nb\n```\n\nc\n",
			"<p>a</p>\n<pre><code>b\n</code></pre>\n<p>c</p>\n",
		},
		{"crlf", "```\r\na\r\n```\r\n", "<pre><code>a\n</code></pre>\n"},
		{
			"after a list",
			"- a\n```\nx\n```\n",
			"<ul>\n<li>a</li>\n</ul>\n<pre><code>x\n</code></pre>\n",
		},
		{
			"escaping",
			"```html\n<a href=\"x\">&amp;</a>\n```\n",
			"<pre><code class=\"language-html\">&lt;a href=&#34;x&#34;&gt;&amp;amp;&lt;/a&gt;\n</code></pre>\n",
		},
		{
			"a blank line inside is kept",
			"```\na\n\n\nb\n```\n",
			"<pre><code>a\n\n\nb\n</code></pre>\n",
		},
	})
}

func TestSpecLists(t *testing.T) {
	runSpecCases(t, []specCase{
		{"the three bullets", "- a\n* b\n+ c\n", "<ul>\n<li>a</li>\n<li>b</li>\n<li>c</li>\n</ul>\n"},
		{"numbers are ignored", "1. a\n1. b\n7. c\n", "<ol>\n<li>a</li>\n<li>b</li>\n<li>c</li>\n</ol>\n"},
		{"a number above nine", "10. ten\n", "<ol>\n<li>ten</li>\n</ol>\n"},
		{"zero is a number", "0. zero\n", "<ol>\n<li>zero</li>\n</ol>\n"},
		{
			"switching kind starts a new list",
			"- a\n1. b\n- c\n",
			"<ul>\n<li>a</li>\n</ul>\n<ol>\n<li>b</li>\n</ol>\n<ul>\n<li>c</li>\n</ul>\n",
		},
		{"a blank line splits a list", "- a\n\n- b\n", "<ul>\n<li>a</li>\n</ul>\n<ul>\n<li>b</li>\n</ul>\n"},
		{"a paragraph follows a list", "- a\ntext\n", "<ul>\n<li>a</li>\n</ul>\n<p>text</p>\n"},
		{
			"heading and fence around lists",
			"- a\n# H\n- b\n```\nx\n```\n",
			"<ul>\n<li>a</li>\n</ul>\n<h1>H</h1>\n<ul>\n<li>b</li>\n</ul>\n<pre><code>x\n</code></pre>\n",
		},
		{
			"inline syntax in items",
			"- **a** `b` [c](d) _e_\n",
			"<ul>\n<li><strong>a</strong> <code>b</code> <a href=\"d\">c</a> <em>e</em></li>\n</ul>\n",
		},
		{"whitespace after the marker and at the end", "-   spaced   \n-\ttab\n", "<ul>\n<li>spaced</li>\n<li>tab</li>\n</ul>\n"},
		{"trailing spaces are not a line break", "- a  \n", "<ul>\n<li>a</li>\n</ul>\n"},
		{"no whitespace after the marker", "-a\n*b*\n1.x\n", "<p>-a\n<em>b</em>\n1.x</p>\n"},
		{
			"an emphasis line, then a bullet",
			"*a* b\n* c\n",
			"<p><em>a</em> b</p>\n<ul>\n<li>c</li>\n</ul>\n",
		},
		{"a strong line is not an item", "**a** b\n", "<p><strong>a</strong> b</p>\n"},
		{"an indented marker is not an item", "- a\n  - b\n", "<ul>\n<li>a</li>\n</ul>\n<p>- b</p>\n"},
		{"a number that looks like a year", "1986. A great year\n", "<ol>\n<li>A great year</li>\n</ol>\n"},
		{"a marker without text", "-\n", "<p>-</p>\n"},
		{"a marker and a space without text", "- \n", "<p>-</p>\n"},
		{"star items", "* a\n* b\n", "<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n"},
		{"counts do not matter", "2. a\n3. b\n", "<ol>\n<li>a</li>\n<li>b</li>\n</ol>\n"},
	})
}

func TestSpecNotSupported(t *testing.T) {
	runSpecCases(t, []specCase{
		{"horizontal rule", "---\n", "<p>---</p>\n"},
		{"setext heading", "Title\n===\n", "<p>Title\n===</p>\n"},
		{"indented code", "    code\n", "<p>code</p>\n"},
		{
			"table",
			"| a | b |\n|---|---|\n| 1 | 2 |\n",
			"<p>| a | b |\n|---|---|\n| 1 | 2 |</p>\n",
		},
		{"parenthesis numbers", "1) a\n", "<p>1) a</p>\n"},
		{"autolink", "<http://x.y>\n", "<p>&lt;http://x.y&gt;</p>\n"},
		{"strikethrough", "~~a~~\n", "<p>~~a~~</p>\n"},
		{"block quote", "> a\n> b\n", "<p>&gt; a\n&gt; b</p>\n"},
	})
}

func TestSpecDocuments(t *testing.T) {
	runSpecCases(t, []specCase{
		{
			"headings, lists, a fence and a hard break",
			"# Doc\nintro *a*\n## Sub\n- x **y**\n- z\n\n1. one\n2. two\n```txt\n<raw> & \"q\"\n```\nlast  \nline\n",
			"<h1>Doc</h1>\n<p>intro <em>a</em></p>\n<h2>Sub</h2>\n<ul>\n<li>x <strong>y</strong></li>\n<li>z</li>\n</ul>\n<ol>\n<li>one</li>\n<li>two</li>\n</ol>\n<pre><code class=\"language-txt\">&lt;raw&gt; &amp; &#34;q&#34;\n</code></pre>\n<p>last<br>\nline</p>\n",
		},
		{
			"no final newline and windows line endings",
			"# A\r\n\r\n- x\r\n- y\r\n\r\ntext",
			"<h1>A</h1>\n<ul>\n<li>x</li>\n<li>y</li>\n</ul>\n<p>text</p>\n",
		},
	})
}
