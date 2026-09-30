# mdlite: a Markdown subset to HTML

`mdlite` converts a small, precisely defined subset of Markdown to HTML. It is a Go library (package `mdlite`) and a
small command line tool. This document is the complete specification. Where it speaks, it is exact; where it is silent
the behaviour is not specified and not tested. Only the Go standard library is available.

## What to build

The module is `example.com/mdlite` (see `go.mod`, Go 1.24). Create:

- the package `mdlite` in the module root (any file names, for example `mdlite.go` and `inline.go`) with

  ```go
  // Render converts src, written in the Markdown subset described in README.md, to HTML.
  func Render(src string) string
  ```

- `cmd/mdlite/main.go`: a program that reads all of standard input, writes `mdlite.Render(input)` to standard output
  exactly (not one byte more or less), and exits with status 0. It takes no arguments (ignore any). If reading standard
  input fails it prints `mdlite: ` followed by the error to standard error and exits with status 1.

Check your work with `go test ./...`. `mdlite_test.go` holds a few examples; the complete acceptance tests run the
library on every example below and on other inputs that follow from the rules, and run the tool with
`go run ./cmd/mdlite` from the module root, feeding it input on standard input and comparing standard output.

## Output conventions

- The output is the HTML of the document's blocks, in order, concatenated. The HTML of every block ends with exactly
  one `\n`, and nothing else separates blocks. A document without blocks (an empty input, for example) renders as the
  empty string `""`.
- Wherever text from the input is copied into the output (paragraph, heading and list item text, code spans, fenced
  code, link URLs, and the language of a fenced code block) exactly these five characters are replaced:

  | character | replaced by |
  |-----------|-------------|
  | `&` | `&amp;` |
  | `<` | `&lt;` |
  | `>` | `&gt;` |
  | `"` | `&#34;` |
  | `'` | `&#39;` |

  (This is what Go's `html.EscapeString` does.) Every `&` is replaced, even one that already looks like an entity.
  Every other character, non-ASCII ones included, is copied unchanged. There is no raw HTML: a `<div>` in the input is
  text and comes out as `&lt;div&gt;`.

## Lines

Before anything else, every `\r\n` in the input is replaced by `\n`. The input is then split into lines at `\n`; a
final `\n` does not start another, empty, line (so `""` has no lines, and `"a\n\n"` has two lines: `a` and an empty one).

A *blank line* is a line that is empty or contains only spaces and tabs. "Whitespace" below always means space or tab.

## Blocks

Block syntax is recognised only at the very start of a line, in column 0: a line that begins with a space or a tab is
never a fence, a heading or a list item.

The document is read from the top. At each step the first line that has not been used yet is looked at, and the first of
these rules that matches it is applied:

1. **blank line**: skipped, nothing is output.
2. **fenced code block**: the line starts with three backticks.
3. **heading**.
4. **list**: the line is a list item.
5. **paragraph**: anything else.

### Fenced code block

A line that starts with three backticks opens a fence. The rest of the line is the *info string*; remove the whitespace
around it. The *language* is the first word of the info string (up to its first space or tab), or none if the info
string is empty.

The *content* is the following lines up to the next line that is exactly three backticks (nothing else on it). That line
closes the block and is not part of the content. If there is no such line, the content runs to the end of the input.

Output: `<pre><code>` (or, when there is a language, `<pre><code class="language-LANG">` with LANG escaped), then every
content line escaped and followed by `\n`, then `</code></pre>` and `\n`.

The content is copied verbatim: no trimming, no inline syntax; blank lines and lines that look like headings, lists or
fences (for example three backticks followed by text) are just content.

### Heading

One to six `#` characters, then at least one space or tab, then the heading text. The output is `<hN>`, the inline
rendering (see below) of the text, `</hN>` and `\n`, where N is the number of `#`. The text is the rest of the line with
the whitespace around it removed. Nothing else is a heading: `#tag` (no whitespace after the hashes) and lines that
start with seven or more `#` are paragraph text. There are no closing hashes: a trailing `#` is part of the text.

### List

A *list item line* is a line that starts (in column 0) with a bullet, `-` or `*` or `+`, or with a number, one or more
ASCII digits followed by `.`; then one or more spaces or tabs; then item text that has at least one non-whitespace
character. The item text is that rest of the line without trailing whitespace. A line that starts with `*` or `-` but
has no whitespace after it (`*em*`, `-x`) is not a list item, nor is `1.x`.

A list is a run of consecutive list item lines of the same kind, bullet or number, taken as long as possible. The list
ends at a line that is not a list item of that kind: a blank line, an item of the other kind (which starts a new list),
or any other line (which is then processed by the rules above as usual). It does not matter which bullet character or
which number is used.

Output for bullets: `<ul>\n`, then for every item `<li>` + inline rendering of the item text + `</li>\n`, then `</ul>\n`.
For numbers the same with `ol` (always a plain `<ol>`, there is no `start` attribute).

An item is exactly one line. There are no nested lists and no continuation lines: `  - x` (with leading spaces) is not
an item, and a line that follows an item and is not an item ends the list.

### Paragraph

A paragraph is the line that matched no other rule, plus the following lines, up to (not including) the next blank line
or the next line that opens a fence, a heading or a list item (which then starts the next block).

Every line of the paragraph is rendered on its own: the whitespace at its start and end is removed, then the inline
rendering is applied. The output is `<p>`, the lines joined, `</p>` and `\n`. Lines are joined with `\n`, except that a
line that is not the last one and that ended with two or more spaces (before the whitespace was removed) is followed by
`<br>` and then `\n`: a *hard line break*. Trailing spaces on the last line of a paragraph never make a break.

## Inline rendering

Inline syntax applies to the text of a paragraph line, of a heading and of a list item, one line at a time: a construct
cannot start on one line and end on another.

The text is scanned from left to right. At each position the first of the rules below that applies is used and the scan
goes on after what the rule consumed. If no rule applies, the character is *literal*: it is copied (escaped as described
above) and the scan moves on by one character. A *word character* is an ASCII letter or digit.

**Code span.** A backtick starts a code span if there is another backtick later on the line and the *next* one is not
directly after it (there is at least one character between the two); that next backtick closes the span. Output:
`<code>`, the characters between the backticks escaped and otherwise unchanged (no trimming, no inline syntax),
`</code>`. Any other backtick is literal.

**Link.** `[text](url)`: a `[`, then the text up to the next `]` (at least one character), then a `(` right after that
`]`, then the URL up to the next `)` (at least one character, and no whitespace in it). Output: `<a href="`, the URL
escaped, `">`, the inline rendering of the text, `</a>`. If any of this does not hold, the `[` is literal. There are no
images: in `![alt](src)` the `!` is literal and `[alt](src)` is a link.

**Strong** `**text**` and **emphasis** `*text*` and `_text_`. The *delimiter* is the `**`, `*` or `_` at the current
position (two adjacent asterisks are a `**`; strong is tried before emphasis). It forms strong or emphasis only if all of
this holds:

1. The delimiter is followed by a character that is not whitespace. An `_` must also not be directly preceded by a word
   character (so `snake_case_name` contains no emphasis).
2. The *closer* is found by scanning to the right, starting right after the delimiter: a code span met while scanning is
   skipped as a whole, and when the delimiter is a single `*`, two adjacent asterisks are skipped as a unit. The closer
   is the first occurrence of the delimiter found this way (`**` for strong, one `*` or one `_` for emphasis). There
   must be one.
3. There is at least one character between the delimiter and the closer, and the last of them is not whitespace. An `_`
   closer must also not be directly followed by a word character.

Then the output is `<strong>` or `<em>`, the inline rendering of the text between the delimiter and the closer (as if
it were a line of its own), and `</strong>` or `</em>`, and the scan goes on after the closer. If any condition fails,
nothing is formed at this position and the character here is literal: when `**` fails, the first `*` is literal and the
scan goes on with the second `*`, which can be the start of an emphasis.

Not specified and not tested: runs of three or more `*`, `__`, backslash escapes, and anything else not described here.

## Not supported

These are not part of the subset and are not recognised; they are ordinary text and come out as the rules above say:
block quotes (`> x`), horizontal rules (`---`), tables, setext headings (`===`), indented code blocks, nested lists,
raw HTML, images, autolinks, reference links, footnotes, strikethrough, and `1)` style numbers.

## Examples

Examples are given as the input, then the exact output. Inputs end with a newline unless stated. Where trailing spaces
matter the input is also given as a Go string.

**1. Headings**

````text
# One
## Two
###### Six
####### Seven
#NoSpace
````

````html
<h1>One</h1>
<h2>Two</h2>
<h6>Six</h6>
<p>####### Seven
#NoSpace</p>
````

**2. Paragraphs, soft and hard line breaks.** Input `"first\nsecond\n\n\nroses are red  \nviolets are blue\n"`:

````html
<p>first
second</p>
<p>roses are red<br>
violets are blue</p>
````

**3. Emphasis, strong, code, nesting**

````text
Mix of *a*, _b_, **c**, `d` and **e *f* g**, and *h **i** j*.
````

````html
<p>Mix of <em>a</em>, <em>b</em>, <strong>c</strong>, <code>d</code> and <strong>e <em>f</em> g</strong>, and <em>h <strong>i</strong> j</em>.</p>
````

**4. Delimiters that do not form emphasis.** Everything stays literal:

````text
2 * 3 = 6, snake_case_name, **oops and *x *y and `tick and _z
````

````html
<p>2 * 3 = 6, snake_case_name, **oops and *x *y and `tick and _z</p>
````

**5. Underscore rules versus asterisks**

````text
_a_ (_b_) _c_b 2*3*4 a**b**c
````

````html
<p><em>a</em> (<em>b</em>) _c_b 2<em>3</em>4 a<strong>b</strong>c</p>
````

**6. Links**

````text
See [the *docs*](https://example.com/a?x=1&y=2) and [empty]() or [a b](c d) or ![img](u.png)
````

````html
<p>See <a href="https://example.com/a?x=1&amp;y=2">the <em>docs</em></a> and [empty]() or [a b](c d) or !<a href="u.png">img</a></p>
````

**7. Escaping**

````text
5 < 6 & 7 > 3, "quoted" and 'single' &amp; <b>
````

````html
<p>5 &lt; 6 &amp; 7 &gt; 3, &#34;quoted&#34; and &#39;single&#39; &amp;amp; &lt;b&gt;</p>
````

**8. Code spans.** What is inside is not interpreted; a code span also hides delimiters from the search for a closer:

````text
Use `<b>*x*</b> & more`, or *see `*` here*.
````

````html
<p>Use <code>&lt;b&gt;*x*&lt;/b&gt; &amp; more</code>, or <em>see <code>*</code> here</em>.</p>
````

**9. Fenced code with a language.** Content is verbatim, blank lines included:

`````text
```go
func main() {
    fmt.Println("<hi>") // *not* emphasis
}

// after a blank line
```
`````

````html
<pre><code class="language-go">func main() {
    fmt.Println(&#34;&lt;hi&gt;&#34;) // *not* emphasis
}

// after a blank line
</code></pre>
````

**10. Fenced code without a language; a line that does not close; an unclosed fence**

`````text
```
# not a heading
- not a list
```js
```
after
```
never closed
`````

````html
<pre><code># not a heading
- not a list
```js
</code></pre>
<p>after</p>
<pre><code>never closed
</code></pre>
````

The line ` ```js ` does not close the first block: only a line of exactly three backticks does. The last fence is never
closed, so it runs to the end of the input.

**11. A bullet list that interrupts a paragraph**

````text
Shopping list:
- *fresh* eggs
* **two** litres of milk
+ [bread](/b)
````

````html
<p>Shopping list:</p>
<ul>
<li><em>fresh</em> eggs</li>
<li><strong>two</strong> litres of milk</li>
<li><a href="/b">bread</a></li>
</ul>
````

**12. Numbered lists; a list of the other kind starts a new list**

````text
3. three
1. one
- dash
10. ten
````

````html
<ol>
<li>three</li>
<li>one</li>
</ol>
<ul>
<li>dash</li>
</ul>
<ol>
<li>ten</li>
</ol>
````

**13. What ends a list**

````text
- a

- b
- c
not an item
  - nested is not supported
````

````html
<ul>
<li>a</li>
</ul>
<ul>
<li>b</li>
<li>c</li>
</ul>
<p>not an item
- nested is not supported</p>
````

**14. Column 0 only.** The lines begin with a space, so they are paragraph text:

````text
  # not a heading
  - not an item
````

````html
<p># not a heading
- not an item</p>
````

**15. Not supported, so text**

````text
> quote
<div>raw</div>
---
````

````html
<p>&gt; quote
&lt;div&gt;raw&lt;/div&gt;
---</p>
````

**16. Heading text**

````text
##   A *bold* `move` ##  
````

````html
<h2>A <em>bold</em> <code>move</code> ##</h2>
````

**17. Line endings.** Input `"a\r\nb  \r\nc\r\n"` (the `\r\n` are replaced by `\n` first, so `b  ` is a hard line break):

````html
<p>a
b<br>
c</p>
````

**18. No blocks, no output.** The inputs `""`, `"\n"` and `"  \n\t\n\n"` all render as `""`.

**19. A whole document.** (In the input, the line `Second line` ends with two spaces.)

`````text
# Title

Intro with *emphasis* and a [link](http://x.y).
Second line  
after a hard break.

## Steps

1. Install `mdlite`
2. Run **it**

- fast
- small

```sh
echo "hi" && ls
```

The end.
`````

`````html
<h1>Title</h1>
<p>Intro with <em>emphasis</em> and a <a href="http://x.y">link</a>.
Second line<br>
after a hard break.</p>
<h2>Steps</h2>
<ol>
<li>Install <code>mdlite</code></li>
<li>Run <strong>it</strong></li>
</ol>
<ul>
<li>fast</li>
<li>small</li>
</ul>
<pre><code class="language-sh">echo &#34;hi&#34; &amp;&amp; ls
</code></pre>
<p>The end.</p>
`````
