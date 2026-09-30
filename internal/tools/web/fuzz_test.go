package web

import (
	"context"
	"encoding/json"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The seed corpora run as ordinary tests; `go test -fuzz FuzzName` explores
// further. The invariants are the ones the rest of the harness relies on: no
// panic on hostile input, valid UTF-8 out, bounded work.

func FuzzHTMLToText(f *testing.F) {
	for _, s := range []string{
		"", "<p>hello</p>", "<h1>t</h1><ul><li>a<li>b</ul>", "<table><tr><td>x<td>y</table>",
		"<a href='/x'>y</a>", "<pre><code class=language-go>x := 1\n</code></pre>", "<blockquote><p>q</p></blockquote>",
		"<div><div><div>", "<script>alert(1)</script><style>p{}</style>", "<svg><g><text>t</text></g></svg>",
		"<table><caption>c</caption><thead><tr><th>h</th></tr></thead></table>", "<dl><dt>t<dd>d</dl>",
		"<!--", "<", "<a", "<img alt=\"x\">", "\xff\xfe<p>\x00</p>", "<base href='http://x/'><a href=y>z</a>",
		"<ol start=-5><li>a</ol>", "<ol start=99999999999999999999><li>a</ol>", "<h7>x</h7>", "<h0>x</h0>",
	} {
		f.Add(s)
	}
	base, _ := url.Parse("https://example.com/dir/page")
	f.Fuzz(func(t *testing.T, src string) {
		text, title := htmlToTextBudget(src, base, 2*time.Second)
		if !utf8.ValidString(text) || !utf8.ValidString(title) {
			t.Fatalf("invalid UTF-8 for input %q", src)
		}
		if strings.Contains(title, "\n") {
			t.Fatalf("title has a newline: %q", title)
		}
		if strings.TrimSpace(text) != text {
			t.Fatalf("output not trimmed: %q", text)
		}
	})
}

func FuzzFlatText(f *testing.F) {
	for _, s := range []string{"", "<p>a</p>", "<nav>x", "<script>", "<title>t", "<li><li>", "<td>a<td>b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		src = strings.ToValidUTF8(src, "�")
		text, title := flatText(src)
		if !utf8.ValidString(text) || !utf8.ValidString(title) {
			t.Fatalf("invalid UTF-8 for input %q", src)
		}
	})
}

func FuzzNestingTooDeep(f *testing.F) {
	for _, s := range []string{"", "<div>", "</div>", "<div/>", "<p><div>", "<ul><li>", "<a><b></a>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		nestingTooDeep(src)
	})
}

func FuzzHostHandling(f *testing.F) {
	for _, s := range []string{
		"", "localhost", "127.0.0.1", "::1", "[::1]", "2130706433", "0x7f.1", "0177.0.0.1", "1.2.3.4.5",
		"example.com", "fe80::1%eth0", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "0x", "0x.0x", "..",
		"999999999999999999999999", "1..2", ".1", "1.", "-1", "+1", "1_0", "0b11", "0o17", "０x7f",
	} {
		f.Add(s)
	}
	g := newGuard(false, []string{"*.ok.test", "ok.test:8080", "[::1]:9000"})
	g.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	f.Fuzz(func(t *testing.T, host string) {
		if a, isIP, err := literalIP(host); isIP {
			if err != nil || !a.IsValid() {
				t.Fatalf("literalIP(%q) = %v,%v,%v", host, a, isIP, err)
			}
			classify(a)
		}
		folded := asciiFold(host)
		if asciiFold(folded) != folded {
			t.Fatalf("asciiFold not idempotent for %q", host)
		}
		addrs, err := g.resolve(context.Background(), host, "80")
		if err == nil {
			// Whatever the guard lets through must be allowed by its own policy
			// unless the host is on the allow list.
			for _, a := range addrs {
				if why := classify(a); why != "" && !g.allowed(normalizeHost(host), "80") {
					t.Fatalf("resolve(%q) returned blocked address %s (%s)", host, a, why)
				}
			}
		}
		g.vetOnly(context.Background(), host, "80")
	})
}

func FuzzParseFetchURL(f *testing.F) {
	for _, s := range []string{
		"", "https://example.com", "example.com/x", "//x.org", "http://[::1]:80/", "http://user:pw@h/", "file:///x",
		"javascript:alert(1)", "localhost:8080", "a:1", "http://", "http:///x", "https://ex ample.com", "%zz", "http://\x00",
		"HTTPS://X", "https://[", "https://h:port/", "https://h:99999999/", "https://h/%zz",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := parseFetchURL(raw)
		if err != nil {
			return
		}
		if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
			t.Fatalf("scheme %q accepted for %q", u.Scheme, raw)
		}
		if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			t.Fatalf("bad URL accepted for %q: %v", raw, u)
		}
		_ = cacheKey(u)
		_ = portOf(u)
	})
}

func FuzzDecodeText(f *testing.F) {
	f.Add([]byte("hello"), "", true)
	f.Add([]byte{0xFF, 0xFE, 'h', 0}, "", false)
	f.Add([]byte{0xFE, 0xFF, 0, 'h'}, "utf-16", false)
	f.Add([]byte{0xEF, 0xBB, 0xBF, 'x'}, "", false)
	f.Add([]byte("caf\xe9"), "iso-8859-1", false)
	f.Add([]byte(`<meta charset="windows-1252">`+"\x93"), "", true)
	f.Add([]byte{0xE2, 0x82}, "utf-8", false)
	f.Fuzz(func(t *testing.T, body []byte, label string, isHTML bool) {
		if s := decodeText(body, label, isHTML); !utf8.ValidString(s) {
			t.Fatalf("decodeText produced invalid UTF-8 for %q %q", body, label)
		}
	})
}

func FuzzCleanTextAndJSON(f *testing.F) {
	for _, s := range []string{"", "a\r\nb", "\x00\x01", "{\"a\":1}", "[1,", string(rune(0xFEFF)) + "{}", "\r", "a\rb"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		s = strings.ToValidUTF8(s, "�")
		c := cleanText(s)
		if !utf8.ValidString(c) || strings.ContainsRune(c, '\r') {
			t.Fatalf("cleanText(%q) = %q", s, c)
		}
		if p := prettyJSON(s); !utf8.ValidString(p) {
			t.Fatalf("prettyJSON(%q) = %q", s, p)
		}
		if json.Valid([]byte(s)) && looksJSON([]byte(s)) {
			var buf strings.Builder
			buf.WriteString(prettyJSON(s))
			if !json.Valid([]byte(buf.String())) && strings.TrimSpace(s) != "" {
				t.Fatalf("pretty-printing broke valid JSON %q -> %q", s, buf.String())
			}
		}
	})
}

func FuzzPaging(f *testing.F) {
	f.Add("hello\nworld\n", 0, int64(5), 100)
	f.Add("日本語のテキスト", 2, int64(3), 7)
	f.Add("😀😀😀", 1, int64(1), 1)
	f.Add("", 0, int64(10), 10)
	f.Add(strings.Repeat("x", 100), 50, int64(1000), 20)
	f.Fuzz(func(t *testing.T, s string, offset int, limit int64, maxBytes int) {
		s = strings.ToValidUTF8(s, "�")
		total := utf8.RuneCountInString(s)
		if offset < 0 || offset > total {
			return
		}
		if maxBytes < 1 {
			maxBytes = 1
		}
		start := byteOffset(s, total, int64(offset))
		if start > len(s) || (start < len(s) && !utf8.RuneStart(s[start])) {
			t.Fatalf("byteOffset(%q, %d) = %d", s, offset, start)
		}
		if got := utf8.RuneCountInString(s[:start]); got != offset {
			t.Fatalf("byteOffset(%q, %d) lands after %d runes", s, offset, got)
		}
		end := pageEnd(s, start, limit, maxBytes)
		if end < start || end > len(s) {
			t.Fatalf("pageEnd(%q, %d, %d, %d) = %d", s, start, limit, maxBytes, end)
		}
		if end < len(s) && !utf8.RuneStart(s[end]) {
			t.Fatalf("page ends inside a character: %q end=%d", s, end)
		}
		if limit >= 1 && start < len(s) && end == start && maxBytes >= 4 {
			t.Fatalf("no progress: %q start=%d limit=%d max=%d", s, start, limit, maxBytes)
		}
	})
}

func FuzzArgs(f *testing.F) {
	for _, s := range []string{"", "null", "{}", `{"url":"x"}`, `{"offset":"5"}`, `{"offset":1e999}`, `{"url":`, `[`, `{"a":{"b":[1,2]}}`, `{"limit":-1e18}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		a, err := parseArgs(json.RawMessage(raw))
		if err != nil {
			return
		}
		a.str("url")
		a.integer("offset")
		a.integer("limit")
		_ = a.missing("url", "url", "offset")
	})
}

func FuzzSearchClean(f *testing.F) {
	for _, s := range []string{"", "<b>x</b>", "&amp;&lt;", "<", "&#xD800;", "\xff", "<script>", "a\nb", strings.Repeat("é", 500)} {
		f.Add(s, s, "https://example.com/"+s)
	}
	f.Fuzz(func(t *testing.T, title, snippet, u string) {
		rs := cleanResults([]SearchResult{{Title: title, URL: u, Snippet: snippet}}, 5)
		out := formatResults(title, rs)
		if !utf8.ValidString(out) {
			t.Fatalf("formatResults produced invalid UTF-8: %q", out)
		}
		for _, r := range rs {
			if strings.ContainsAny(r.Title+r.Snippet, "\n\r") {
				t.Fatalf("newline survived cleaning: %+v", r)
			}
		}
	})
}
