package mdlite_test

import (
	"testing"

	"example.com/mdlite"
)

// A few of the examples of README.md. The acceptance tests that verify your work cover all of them and more.
func TestExamples(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "headings",
			in:   "# One\n## Two\n###### Six\n####### Seven\n#NoSpace\n",
			want: "<h1>One</h1>\n<h2>Two</h2>\n<h6>Six</h6>\n<p>####### Seven\n#NoSpace</p>\n",
		},
		{
			name: "hard line break",
			in:   "first\nsecond\n\n\nroses are red  \nviolets are blue\n",
			want: "<p>first\nsecond</p>\n<p>roses are red<br>\nviolets are blue</p>\n",
		},
		{
			name: "emphasis",
			in:   "Mix of *a*, _b_, **c**, `d` and **e *f* g**, and *h **i** j*.\n",
			want: "<p>Mix of <em>a</em>, <em>b</em>, <strong>c</strong>, <code>d</code> and <strong>e <em>f</em> g</strong>, and <em>h <strong>i</strong> j</em>.</p>\n",
		},
		{
			name: "escaping",
			in:   "5 < 6 & 7 > 3, \"quoted\" and 'single' &amp; <b>\n",
			want: "<p>5 &lt; 6 &amp; 7 &gt; 3, &#34;quoted&#34; and &#39;single&#39; &amp;amp; &lt;b&gt;</p>\n",
		},
		{
			name: "fenced code",
			in:   "```go\nx := a < b\n\nreturn\n```\n",
			want: "<pre><code class=\"language-go\">x := a &lt; b\n\nreturn\n</code></pre>\n",
		},
		{
			name: "list interrupts a paragraph",
			in:   "Shopping list:\n- *fresh* eggs\n* **two** litres of milk\n+ [bread](/b)\n",
			want: "<p>Shopping list:</p>\n<ul>\n<li><em>fresh</em> eggs</li>\n<li><strong>two</strong> litres of milk</li>\n<li><a href=\"/b\">bread</a></li>\n</ul>\n",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mdlite.Render(c.in); got != c.want {
				t.Errorf("Render(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}
