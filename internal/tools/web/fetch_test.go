package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/tools"
)

var allowAll = Config{AllowPrivate: true}

func TestFetchHTML(t *testing.T) {
	page := `<!doctype html><html><head><title>Test Page</title><script>var x=1;</script></head>
<body><nav>Menu</nav><h1>Welcome</h1><p>Some <b>bold</b> text and a <a href="/docs">link</a>.</p>
<ul><li>one</li><li>two</li></ul><footer>foot</footer></body></html>`
	srv, _ := serve(t, "text/html; charset=utf-8", page)
	h := newHarness(t, allowAll)
	res := h.get(srv.URL + "/page")
	if res.IsError {
		t.Fatal(res.Text)
	}
	body := "# Welcome\n\nSome bold text and a [link](" + srv.URL + "/docs).\n\n- one\n- two"
	n := utf8.RuneCountInString(body)
	want := fmt.Sprintf("[fetched %s/page | HTTP 200 text/html | characters 0-%d of %d]\nTitle: Test Page\n\n%s", srv.URL, n, n, body)
	if res.Text != want {
		t.Errorf("text mismatch:\n got: %q\nwant: %q", res.Text, want)
	}
	if res.Truncated {
		t.Error("a small page was truncated")
	}
	if res.Meta["status"] != 200 || res.Meta["content_type"] != "text/html" || res.Meta["cached"] != false {
		t.Errorf("meta = %v", res.Meta)
	}
}

func TestFetchTitleIsNotRepeated(t *testing.T) {
	srv, _ := serve(t, "text/html", `<title>Welcome</title><h1>Welcome</h1><p>x</p>`)
	res := newHarness(t, allowAll).get(srv.URL)
	if strings.Contains(res.Text, "Title:") {
		t.Errorf("title repeated although the body leads with it: %q", res.Text)
	}
}

func TestFetchJSON(t *testing.T) {
	bs := string(rune(92)) // a backslash, spelled so the source holds no escape sequence
	compact := `{"b":1,"a":[1,2,{"c":null}],"s":"é` + bs + `u00e9","n":12345678901234567890,"f":1.0e3}`
	tests := []struct {
		name  string
		ctype string
		body  string
		want  string
	}{
		{"application/json", "application/json", compact,
			"{\n  \"b\": 1,\n  \"a\": [\n    1,\n    2,\n    {\n      \"c\": null\n    }\n  ],\n  \"s\": \"é" + bs + "u00e9\",\n  \"n\": 12345678901234567890,\n  \"f\": 1.0e3\n}"},
		{"vendor +json", "application/vnd.api+json; charset=utf-8", `{"a":1}`, "{\n  \"a\": 1\n}"},
		{"text/json", "text/json", `[1,2]`, "[\n  1,\n  2\n]"},
		{"array of objects", "application/json", `[{"a":1},{"b":2}]`, "[\n  {\n    \"a\": 1\n  },\n  {\n    \"b\": 2\n  }\n]"},
		{"scalar", "application/json", `"just a string"`, `"just a string"`},
		{"bom", "application/json", string(rune(0xFEFF)) + `{"a":1}`, "{\n  \"a\": 1\n}"},
		{"already pretty", "application/json", "{\n  \"a\": 1\n}\n", "{\n  \"a\": 1\n}"},
		{"invalid json is returned as text", "application/json", `{"a": 1,}`, `{"a": 1,}`},
		{"truncated json is returned as text", "application/json", `{"a": [1, 2`, `{"a": [1, 2`},
		{"empty json body", "application/json", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, tt.ctype, tt.body)
			res := newHarness(t, allowAll).get(srv.URL)
			if res.IsError {
				t.Fatal(res.Text)
			}
			_, body, trailer := splitPage(t, res.Text)
			if trailer != "" {
				t.Errorf("unexpected trailer %q", trailer)
			}
			want := tt.want
			if want == "" {
				want = "[empty page]"
			}
			if body != want {
				t.Errorf("body = %q, want %q", body, want)
			}
		})
	}
}

func TestFetchText(t *testing.T) {
	tests := []struct {
		name  string
		ctype string
		body  string
		want  string
	}{
		{"plain", "text/plain", "hello\nworld\n", "hello\nworld\n"},
		{"crlf and controls", "text/plain", "a\r\nb\x00\x07c\rd\x7f\te\r\n", "a\nbc\nd\te\n"},
		{"markdown", "text/markdown; charset=utf-8", "# Title\n\n* item\n", "# Title\n\n* item\n"},
		{"csv", "text/csv", "a,b\n1,2\n", "a,b\n1,2\n"},
		{"xml", "application/xml", "<a><b>x</b></a>", "<a><b>x</b></a>"},
		{"svg source is text", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"><text>hi</text></svg>`, `<svg xmlns="http://www.w3.org/2000/svg"><text>hi</text></svg>`},
		{"javascript", "application/javascript", "var a = 1;", "var a = 1;"},
		{"yaml", "application/x-yaml", "a: 1\n", "a: 1\n"},
		{"unicode", "text/plain; charset=utf-8", "日本語 😀 ✓\n", "日本語 😀 ✓\n"},
		{"css", "text/css", "p{color:red}", "p{color:red}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, tt.ctype, tt.body)
			res := newHarness(t, allowAll).get(srv.URL)
			if res.IsError {
				t.Fatal(res.Text)
			}
			_, body, _ := splitPage(t, res.Text)
			if body != tt.want {
				t.Errorf("body = %q, want %q", body, tt.want)
			}
		})
	}
}

func TestUnsupportedContentTypes(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	tests := []struct {
		name, ctype, body, named string
	}{
		{"png", "image/png", png, "image/png"},
		{"jpeg", "image/jpeg", "\xff\xd8\xff\xe0", "image/jpeg"},
		{"pdf", "application/pdf", "%PDF-1.4 ...", "application/pdf"},
		{"zip", "application/zip", "PK\x03\x04", "application/zip"},
		{"video", "video/mp4", "\x00\x00\x00\x18ftypmp42", "video/mp4"},
		{"audio", "audio/mpeg", "ID3\x03", "audio/mpeg"},
		{"font", "font/woff2", "wOF2", "font/woff2"},
		{"octet-stream with png bytes is sniffed", "application/octet-stream", png, "image/png"},
		{"missing type with png bytes is sniffed", "", png, "image/png"},
		{"octet-stream with binary", "application/octet-stream", "\x00\x01\x02\x03binary\xff\xfe", "application/octet-stream"},
		{"gzip", "application/gzip", "\x1f\x8b\x08\x00", "application/gzip"},
		{"wasm", "application/wasm", "\x00asm\x01\x00\x00\x00", "application/wasm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.ctype == "" {
					w.Header()["Content-Type"] = nil // suppress Go's own sniffing
				} else {
					w.Header().Set("Content-Type", tt.ctype)
				}
				io.WriteString(w, tt.body)
			})
			res := newHarness(t, allowAll).get(srv.URL)
			if !res.IsError || !strings.Contains(res.Text, "unsupported content-type") || !strings.Contains(res.Text, tt.named) {
				t.Errorf("result = %v %q", res.IsError, res.Text)
			}
			if len(res.Text) > 200 {
				t.Errorf("error is %d bytes; it should be short", len(res.Text))
			}
		})
	}
}

func TestContentSniffing(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"html", "<!DOCTYPE html><html><body><h1>Sniffed</h1></body></html>", "# Sniffed"},
		{"json object", `{"a":1}`, "{\n  \"a\": 1\n}"},
		{"json array", ` [1,2] `, "[\n  1,\n  2\n]"},
		{"plain text", "just some words\n", "just some words\n"},
		{"json-looking text stays text", "{not json", "{not json"},
		{"empty body", "", "[empty page]"},
	}
	for _, ctype := range []string{"", "application/octet-stream"} {
		for _, tt := range tests {
			t.Run(tt.name+"/"+ctype, func(t *testing.T) {
				srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
					if ctype == "" {
						w.Header()["Content-Type"] = nil
					} else {
						w.Header().Set("Content-Type", ctype)
					}
					io.WriteString(w, tt.body)
				})
				res := newHarness(t, allowAll).get(srv.URL)
				if res.IsError {
					t.Fatal(res.Text)
				}
				_, body, _ := splitPage(t, res.Text)
				if body != tt.want {
					t.Errorf("body = %q, want %q", body, tt.want)
				}
			})
		}
	}
}

func TestCharsets(t *testing.T) {
	utf16le := func(s string) string {
		var b bytes.Buffer
		b.Write([]byte{0xFF, 0xFE})
		for _, r := range s {
			b.Write([]byte{byte(r), byte(r >> 8)})
		}
		return b.String()
	}
	tests := []struct {
		name, ctype, body, want string
	}{
		{"latin1 header", "text/plain; charset=iso-8859-1", "caf\xe9 na\xefve", "café naïve"},
		{"latin1 in html header", "text/html; charset=ISO-8859-1", "<p>caf\xe9</p>", "café"},
		{"meta charset", "text/html", `<meta charset="windows-1252"><p>` + "\x93quoted\x94 \x80 \x99" + `</p>`, "“quoted” € ™"},
		{"http-equiv charset", "text/html", `<meta http-equiv="Content-Type" content="text/html; charset=iso-8859-1"><p>` + "\xfc\xf1" + `</p>`, "üñ"},
		{"utf-16 with bom", "text/plain", utf16le("hi ✓"), "hi ✓"},
		{"utf-8 bom is stripped", "text/plain", "\xef\xbb\xbfhello", "hello"},
		{"undeclared non-utf8 is windows-1252", "text/plain", "na\xefve \x96 dash", "naïve – dash"},
		{"declared utf-8 with bad bytes (a run becomes one replacement)", "text/plain; charset=utf-8", "ok \xff\xfe end", "ok " + string(rune(0xFFFD)) + " end"},
		{"unknown charset degrades to utf-8", "text/plain; charset=shift_jis", "ascii only", "ascii only"},
		{"windows-1252 label", "text/plain; charset=windows-1252", "\x93hi\x94", "“hi”"},
		{"utf-8 stays utf-8", "text/plain; charset=utf-8", "héllo ✓", "héllo ✓"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, tt.ctype, tt.body)
			res := newHarness(t, allowAll).get(srv.URL)
			if res.IsError {
				t.Fatal(res.Text)
			}
			_, body, _ := splitPage(t, res.Text)
			if body != tt.want {
				t.Errorf("body = %q, want %q", body, tt.want)
			}
			if !utf8.ValidString(res.Text) {
				t.Error("result is not valid UTF-8")
			}
		})
	}
}

func TestPaging(t *testing.T) {
	makeText := func(unit string, n int) string {
		var sb strings.Builder
		for i := 0; sb.Len() < n; i++ {
			fmt.Fprintf(&sb, "%s line %05d\n", unit, i)
		}
		return sb.String()
	}
	tests := []struct {
		name    string
		text    string
		params  map[string]any
		wantMin int // minimum number of pages
	}{
		{"ascii lines", makeText("alpha beta gamma", 60_000), nil, 3},
		{"multibyte lines", makeText("日本語のテキスト ✓ é", 60_000), nil, 3},
		{"no newlines at all", strings.Repeat("x", 50_000), nil, 3},
		{"emoji", strings.Repeat("😀", 30_000), nil, 2},
		{"explicit limit", makeText("limit", 5_000), map[string]any{"limit": 1000}, 5},
		{"string limit", makeText("limit", 5_000), map[string]any{"limit": "1000"}, 5},
		{"tiny limit", makeText("tiny", 300), map[string]any{"limit": 40}, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, hits := serve(t, "text/plain; charset=utf-8", tt.text)
			h := newHarness(t, allowAll)
			runes := []rune(tt.text)
			offset, pages := 0, 0
			for {
				in := map[string]any{"url": srv.URL, "offset": offset}
				for k, v := range tt.params {
					in[k] = v
				}
				res := h.run(h.env("a"), in)
				if res.IsError {
					t.Fatalf("page %d: %s", pages, res.Text)
				}
				if res.Truncated {
					t.Fatalf("page %d was truncated by the output limit (%d bytes)", pages, len(res.Text))
				}
				header, body, trailer := splitPage(t, res.Text)
				pages++
				if body == "" {
					t.Fatalf("page %d is empty", pages)
				}
				total := len(runes)
				start := offset
				// The header's range is authoritative: it says which characters of
				// the document this page stands for.
				var end int
				if _, err := fmt.Sscanf(header[strings.Index(header, "characters "):], "characters %d-%d of %d]", new(int), &end, new(int)); err != nil {
					t.Fatalf("unparseable header %q: %v", header, err)
				}
				if want := fmt.Sprintf("characters %d-%d of %d]", start, end, total); !strings.Contains(header, want) {
					t.Fatalf("header %q lacks %q", header, want)
				}
				// The page shows exactly those characters (minus trailing newlines
				// when a trailer follows).
				want := string(runes[start:end])
				if end < total {
					want = strings.TrimRight(want, "\n")
				}
				if body != want {
					t.Fatalf("page %d shows %q…, want %q…", pages, clipRunes(body, 30), clipRunes(want, 30))
				}
				if end >= total {
					if trailer != "" {
						t.Fatalf("last page has a trailer: %q", trailer)
					}
					break
				}
				wantTrailer := fmt.Sprintf("[showing characters %d-%d of %d; call web_fetch again with offset=%d to continue]", start, end, total, end)
				if trailer != wantTrailer {
					t.Fatalf("trailer = %q, want %q", trailer, wantTrailer)
				}
				if end <= start {
					t.Fatal("a page made no progress")
				}
				offset = end
				if pages > 5000 {
					t.Fatal("paging does not terminate")
				}
			}
			if pages < tt.wantMin {
				t.Errorf("%d pages, want at least %d", pages, tt.wantMin)
			}
			if hits.Load() != 1 {
				t.Errorf("server hit %d times; paging must come from the cache", hits.Load())
			}
		})
	}
}

func TestPagingBounds(t *testing.T) {
	text := strings.Repeat("0123456789\n", 500) // 5500 chars
	srv, _ := serve(t, "text/plain", text)
	h := newHarness(t, allowAll)
	tests := []struct {
		name    string
		params  map[string]any
		wantErr string
		check   func(t *testing.T, res *tools.Result)
	}{
		{"offset at end", map[string]any{"offset": 5500}, "beyond the end of the document (5500 characters)", nil},
		{"offset past end", map[string]any{"offset": 99999}, "beyond the end", nil},
		{"offset last char", map[string]any{"offset": 5499}, "", func(t *testing.T, res *tools.Result) {
			_, body, trailer := splitPage(t, res.Text)
			if body != "\n" || trailer != "" {
				t.Errorf("body=%q trailer=%q", body, trailer)
			}
		}},
		{"negative offset", map[string]any{"offset": -1}, `"offset" must not be negative`, nil},
		{"string offset", map[string]any{"offset": "5490"}, "", func(t *testing.T, res *tools.Result) {
			if _, body, _ := splitPage(t, res.Text); body != text[5490:] {
				t.Errorf("body = %q", body)
			}
		}},
		{"float offset truncates", map[string]any{"offset": 5490.9}, "", func(t *testing.T, res *tools.Result) {
			if _, body, _ := splitPage(t, res.Text); body != text[5490:] {
				t.Errorf("body = %q", body)
			}
		}},
		{"bad offset", map[string]any{"offset": "abc"}, `field "offset" must be an integer`, nil},
		{"bad limit", map[string]any{"limit": []int{1}}, `field "limit" must be an integer`, nil},
		{"zero limit means default", map[string]any{"limit": 0}, "", func(t *testing.T, res *tools.Result) {
			if _, body, trailer := splitPage(t, res.Text); body != text || trailer != "" {
				t.Errorf("default limit should cover the whole 5500 chars")
			}
		}},
		{"negative limit means default", map[string]any{"limit": -5}, "", func(t *testing.T, res *tools.Result) {
			if _, body, _ := splitPage(t, res.Text); body != text {
				t.Errorf("body length %d", len(body))
			}
		}},
		{"limit one", map[string]any{"limit": 1}, "", func(t *testing.T, res *tools.Result) {
			if _, body, _ := splitPage(t, res.Text); body != "0" {
				t.Errorf("body = %q", body)
			}
		}},
		{"huge limit is clamped", map[string]any{"limit": 1e18}, "", func(t *testing.T, res *tools.Result) {
			if res.IsError || res.Truncated {
				t.Errorf("result = %v %v", res.IsError, res.Truncated)
			}
		}},
		{"limit cuts at a line boundary", map[string]any{"limit": 5000}, "", func(t *testing.T, res *tools.Result) {
			header, body, _ := splitPage(t, res.Text)
			// The page covers 4994 characters: 454 whole lines. The final newline is
			// not shown because the trailer supplies its own separation.
			if !strings.Contains(header, "characters 0-4994 of 5500]") || !strings.HasSuffix(body, "0123456789") || len(body) != 4993 {
				t.Errorf("header = %q, page is %d chars, want to end at the last full line", header, len(body))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := h.get(srv.URL, tt.params)
			if tt.wantErr != "" {
				if !res.IsError || !strings.Contains(res.Text, tt.wantErr) {
					t.Fatalf("result = %v %q, want error containing %q", res.IsError, res.Text, tt.wantErr)
				}
				return
			}
			if res.IsError {
				t.Fatal(res.Text)
			}
			tt.check(t, res)
		})
	}
}

// Pages are sized so that Env.Finish never truncates them, whatever the
// configured output limit: a truncated middle would silently lose text that the
// trailer claims was delivered.
func TestPagesFitTheOutputLimit(t *testing.T) {
	text := strings.Repeat("héllo wörld ✓ 日本語\n", 5000)
	srv, _ := serve(t, "text/plain; charset=utf-8", text)
	for _, limit := range []int{800, 2000, 8000, 24000, 100000} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			h := newHarness(t, allowAll)
			env := h.env("a")
			env.Limits = tools.DefaultLimits()
			env.Limits.MaxOutputChars = limit
			runes := []rune(text)
			offset := 0
			for pages := 0; offset < len(runes); pages++ {
				res := h.run(env, map[string]any{"url": srv.URL, "offset": offset, "limit": 100000})
				if res.IsError || res.Truncated {
					t.Fatalf("offset %d: error=%v truncated=%v", offset, res.IsError, res.Truncated)
				}
				if limit >= 2000 && len(res.Text) > limit {
					t.Fatalf("page is %d bytes, over the %d limit", len(res.Text), limit)
				}
				header, body, _ := splitPage(t, res.Text)
				var end int
				if _, err := fmt.Sscanf(header[strings.Index(header, "characters "):], "characters %d-%d of %d]", new(int), &end, new(int)); err != nil || end <= offset {
					t.Fatalf("header %q: end=%d err=%v", header, end, err)
				}
				want := string(runes[offset:end])
				if end < len(runes) {
					want = strings.TrimRight(want, "\n")
				}
				if body != want {
					t.Fatalf("offset %d: page does not show its own range", offset)
				}
				offset = end
				if pages > 10000 {
					t.Fatal("no progress")
				}
			}
		})
	}
}

func TestFetchNoOutputLimit(t *testing.T) {
	// MaxOutputChars 0 with the rest of Limits set: no size clamp beyond limit.
	srv, _ := serve(t, "text/plain", strings.Repeat("x", 50000))
	h := newHarness(t, allowAll)
	env := h.env("a")
	env.Limits = tools.Limits{DefaultTimeout: time.Minute, MaxTimeout: time.Minute}
	res := h.run(env, map[string]any{"url": srv.URL, "limit": 40000})
	_, body, _ := splitPage(t, res.Text)
	if len(body) != 40000 {
		t.Errorf("body = %d chars, want 40000", len(body))
	}
}

func TestRedirects(t *testing.T) {
	newRedirector := func(t *testing.T) (*httptest.Server, *atomic.Int32) {
		return newServer(t, func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case strings.HasPrefix(path, "/hop/"):
				n, _ := strconv.Atoi(strings.TrimPrefix(path, "/hop/"))
				if n <= 0 {
					w.Header().Set("Content-Type", "text/plain")
					io.WriteString(w, "arrived")
					return
				}
				http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
			case path == "/loop":
				http.Redirect(w, r, "/loop", http.StatusFound)
			case path == "/dir/relative":
				w.Header().Set("Location", "../final#frag")
				w.WriteHeader(http.StatusMovedPermanently)
			case path == "/final":
				w.Header().Set("Content-Type", "text/plain")
				io.WriteString(w, "final page")
			case path == "/nolocation":
				w.WriteHeader(http.StatusFound)
			case path == "/ftp":
				w.Header().Set("Location", "ftp://example.com/file")
				w.WriteHeader(http.StatusFound)
			case path == "/js":
				w.Header().Set("Location", "javascript:alert(1)")
				w.WriteHeader(http.StatusFound)
			case path == "/creds":
				w.Header().Set("Location", "http://user:pw@example.com/")
				w.WriteHeader(http.StatusFound)
			case path == "/badloc":
				w.Header().Set("Location", "http://[::1")
				w.WriteHeader(http.StatusFound)
			case path == "/nohost":
				w.Header().Set("Location", "http:///x")
				w.WriteHeader(http.StatusFound)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
	}
	srv, _ := newRedirector(t)
	h := newHarness(t, allowAll)

	t.Run("chain of hops", func(t *testing.T) {
		res := h.get(srv.URL + "/hop/3")
		if res.IsError {
			t.Fatal(res.Text)
		}
		header, body, _ := splitPage(t, res.Text)
		if body != "arrived" {
			t.Errorf("body = %q", body)
		}
		if !strings.Contains(header, "[fetched "+srv.URL+"/hop/0 ") || !strings.Contains(header, "redirected from "+srv.URL+"/hop/3") {
			t.Errorf("header = %q", header)
		}
		if strings.Contains(header, "different host") {
			t.Errorf("same-host redirect reported as cross-host: %q", header)
		}
		if res.Meta["redirected"] != true || res.Meta["cross_host"] != false {
			t.Errorf("meta = %v", res.Meta)
		}
	})
	t.Run("five hops are allowed", func(t *testing.T) {
		if res := h.get(srv.URL + "/hop/5"); res.IsError {
			t.Errorf("5 redirects rejected: %s", res.Text)
		}
	})
	t.Run("six hops are not", func(t *testing.T) {
		res := h.get(srv.URL + "/hop/6")
		if !res.IsError || !strings.Contains(res.Text, "too many redirects (more than 5)") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("loop", func(t *testing.T) {
		res := h.get(srv.URL + "/loop")
		if !res.IsError || !strings.Contains(res.Text, "too many redirects") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("relative location and fragment", func(t *testing.T) {
		res := h.get(srv.URL + "/dir/relative")
		header, body, _ := splitPage(t, res.Text)
		if body != "final page" || !strings.Contains(header, "[fetched "+srv.URL+"/final |") {
			t.Errorf("header=%q body=%q", header, body)
		}
	})
	for code, name := range map[int]string{301: "301", 302: "302", 303: "303", 307: "307", 308: "308"} {
		t.Run("status "+name, func(t *testing.T) {
			s, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/to" {
					w.Header().Set("Content-Type", "text/plain")
					io.WriteString(w, "ok "+r.Method)
					return
				}
				w.Header().Set("Location", "/to")
				w.WriteHeader(code)
			})
			res := h.get(s.URL + "/from")
			if _, body, _ := splitPage(t, res.Text); body != "ok GET" {
				t.Errorf("result = %q", res.Text)
			}
		})
	}
	for path, want := range map[string]string{
		"/nolocation": "without a Location header",
		"/ftp":        `unsupported URL scheme "ftp"`,
		"/js":         `unsupported URL scheme "javascript"`,
		"/creds":      "embedded credentials",
		"/badloc":     "Location header",
		"/nohost":     "has no host",
	} {
		t.Run(path, func(t *testing.T) {
			res := h.get(srv.URL + path)
			if !res.IsError || !strings.Contains(res.Text, want) {
				t.Errorf("result = %v %q, want error containing %q", res.IsError, res.Text, want)
			}
		})
	}
}

func TestCrossHostRedirectIsReported(t *testing.T) {
	target, _ := serve(t, "text/plain", "target content")
	origin, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://b.test:"+tcpPort(target)+"/landing", http.StatusFound)
	})
	h := newHarness(t, Config{AllowHosts: []string{"*.test"}})
	h.f.guard.lookup = staticLookup(map[string]string{"a.test": "127.0.0.1", "b.test": "127.0.0.1"})
	res := h.get("http://a.test:" + tcpPort(origin) + "/start")
	if res.IsError {
		t.Fatal(res.Text)
	}
	header, body, _ := splitPage(t, res.Text)
	if body != "target content" {
		t.Errorf("body = %q", body)
	}
	if !strings.Contains(header, "redirected from http://a.test:"+tcpPort(origin)+"/start (different host)") {
		t.Errorf("cross-host redirect not reported: %q", header)
	}
	if res.Meta["cross_host"] != true {
		t.Errorf("meta = %v", res.Meta)
	}
	// Host comparison ignores case and port.
	if !strings.EqualFold("A.TEST", "a.test") {
		t.Fatal("sanity")
	}
}

func TestOversizeBodies(t *testing.T) {
	t.Run("declared content-length over the limit is refused before reading", func(t *testing.T) {
		var sent atomic.Int64
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", strconv.Itoa(50<<20))
			w.WriteHeader(200)
			chunk := bytes.Repeat([]byte("x"), 64<<10)
			for i := 0; i < 800; i++ {
				n, err := w.Write(chunk)
				sent.Add(int64(n))
				if err != nil {
					return
				}
			}
		})
		res := newHarness(t, allowAll).get(srv.URL)
		if !res.IsError || !strings.Contains(res.Text, "larger than 10 MiB") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("streamed body over the limit stops at the limit", func(t *testing.T) {
		var sent atomic.Int64
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			chunk := bytes.Repeat([]byte("x"), 64<<10)
			for i := 0; i < 2000; i++ { // 125 MiB if nobody stops us
				n, err := w.Write(chunk)
				sent.Add(int64(n))
				if err != nil {
					return
				}
			}
		})
		res := newHarness(t, allowAll).get(srv.URL)
		if !res.IsError || !strings.Contains(res.Text, "larger than 10 MiB") {
			t.Fatalf("result = %v %q", res.IsError, res.Text)
		}
		time.Sleep(100 * time.Millisecond)
		if s := sent.Load(); s > 40<<20 {
			t.Errorf("server managed to send %d MiB: the client did not stop reading", s>>20)
		}
	})
	t.Run("gzip bomb is bounded after decompression", func(t *testing.T) {
		var zbuf bytes.Buffer
		zw := gzip.NewWriter(&zbuf)
		zero := make([]byte, 1<<20)
		for i := 0; i < 24; i++ { // 24 MiB of zeros compress to a few dozen KB: over the 10 MiB limit
			zw.Write(zero)
		}
		zw.Close()
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(zbuf.Bytes())
		})
		res := newHarness(t, allowAll).get(srv.URL)
		if !res.IsError || !strings.Contains(res.Text, "larger than 10 MiB") {
			t.Errorf("result = %v %.100q", res.IsError, res.Text)
		}
	})
	t.Run("gzip content is decoded", func(t *testing.T) {
		var zbuf bytes.Buffer
		zw := gzip.NewWriter(&zbuf)
		zw.Write([]byte("compressed hello"))
		zw.Close()
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(zbuf.Bytes())
		})
		res := newHarness(t, allowAll).get(srv.URL)
		if _, body, _ := splitPage(t, res.Text); body != "compressed hello" {
			t.Errorf("body = %q", body)
		}
	})
	t.Run("a body exactly at the limit is accepted", func(t *testing.T) {
		srv, _ := serve(t, "text/plain", strings.Repeat("y", 1000))
		h := newHarness(t, Config{AllowPrivate: true, MaxBody: 1000})
		if res := h.get(srv.URL); res.IsError {
			t.Errorf("1000 bytes with a 1000 byte limit: %s", res.Text)
		}
		srv2, _ := serve(t, "text/plain", strings.Repeat("y", 1001))
		if res := h.get(srv2.URL); !res.IsError || !strings.Contains(res.Text, "larger than 1000 bytes") {
			t.Errorf("1001 bytes: %v %q", res.IsError, res.Text)
		}
	})
}

func TestHTTPErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		ctype  string
		body   string
		want   []string
	}{
		{"404 html", 404, "text/html", "<html><body><h1>Not   Found</h1><p>No such page</p></body></html>", []string{"HTTP 404 Not Found for ", "Not Found", "No such page"}},
		{"500 json", 500, "application/json", `{"error":{"code":"boom","message":"database down"}}`, []string{"HTTP 500 Internal Server Error", "database down"}},
		{"429", 429, "text/plain", "slow down", []string{"HTTP 429 Too Many Requests", "slow down"}},
		{"403 empty", 403, "", "", []string{"HTTP 403 Forbidden"}},
		{"401 binary body", 401, "application/octet-stream", "\x00\x01\x02", []string{"HTTP 401 Unauthorized"}},
		{"503 huge body is clipped", 503, "text/plain", strings.Repeat("word ", 5000), []string{"HTTP 503 Service Unavailable"}},
		{"304", 304, "", "", []string{"HTTP 304 Not Modified"}},
		{"418", 418, "text/plain", "teapot", []string{"HTTP 418 I'm a teapot", "teapot"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.ctype != "" {
					w.Header().Set("Content-Type", tt.ctype)
				}
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})
			res := newHarness(t, allowAll).get(srv.URL + "/x")
			if !res.IsError {
				t.Fatalf("status %d was not an error: %q", tt.status, res.Text)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("error %q lacks %q", res.Text, w)
				}
			}
			if len(res.Text) > 900 {
				t.Errorf("error is %d bytes; the body snippet must be clipped", len(res.Text))
			}
		})
	}
	t.Run("204 is an empty success", func(t *testing.T) {
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		res := newHarness(t, allowAll).get(srv.URL)
		if res.IsError || !strings.HasSuffix(res.Text, "[empty page]") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("errors are not cached", func(t *testing.T) {
		var n atomic.Int32
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			if n.Add(1) == 1 {
				w.WriteHeader(500)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "recovered")
		})
		h := newHarness(t, allowAll)
		if res := h.get(srv.URL); !res.IsError {
			t.Fatal("first call should fail")
		}
		if res := h.get(srv.URL); res.IsError || !strings.Contains(res.Text, "recovered") {
			t.Errorf("second call = %v %q", res.IsError, res.Text)
		}
	})
}

func TestRequestShape(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	var methods []string
	srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Clone())
		methods = append(methods, r.Method)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	})
	h := newHarness(t, allowAll)
	h.get(srv.URL + "/path?q=1&r=2#fragment-not-sent")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || methods[0] != "GET" {
		t.Fatalf("requests = %v", methods)
	}
	if ua := got[0].Get("User-Agent"); ua != "Sleipnir/1.0" {
		t.Errorf("User-Agent = %q", ua)
	}
	if got[0].Get("Authorization") != "" || got[0].Get("Cookie") != "" {
		t.Errorf("credentials sent: %v", got[0])
	}
}

func TestFetchTimeout(t *testing.T) {
	tests := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request, release <-chan struct{})
	}{
		{"no response", func(w http.ResponseWriter, r *http.Request, release <-chan struct{}) { <-release }},
		{"stalled body", func(w http.ResponseWriter, r *http.Request, release <-chan struct{}) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "1000")
			io.WriteString(w, "partial")
			w.(http.Flusher).Flush()
			<-release
		}},
		{"slow drip", func(w http.ResponseWriter, r *http.Request, release <-chan struct{}) {
			w.Header().Set("Content-Type", "text/plain")
			for i := 0; i < 100; i++ {
				select {
				case <-release:
					return
				case <-time.After(50 * time.Millisecond):
				}
				io.WriteString(w, "x")
				w.(http.Flusher).Flush()
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := make(chan struct{})
			srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { tt.handler(w, r, release) })
			// Cleanups run last-in first-out: release the handlers before the
			// server waits for them in Close.
			t.Cleanup(func() { close(release) })
			h := newHarness(t, Config{AllowPrivate: true, Timeout: 300 * time.Millisecond})
			start := time.Now()
			res := h.get(srv.URL)
			if !res.IsError || !strings.Contains(res.Text, "timed out after 0.3s") {
				t.Errorf("result = %v %q", res.IsError, res.Text)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("took %v", d)
			}
		})
	}
}

func TestCancelledContext(t *testing.T) {
	release := make(chan struct{})
	srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) }) // runs before the server's Close
	h := newHarness(t, allowAll)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	res, err := h.tool.Run(ctx, &tools.Call{Input: json.RawMessage(`{"url":"` + srv.URL + `"}`), Env: h.env("a")})
	if err != nil || !res.IsError || !strings.Contains(res.Text, "cancelled") {
		t.Errorf("result = %+v, %v", res, err)
	}
}

func TestConnectionFailures(t *testing.T) {
	srv, _ := serve(t, "text/plain", "x")
	url := srv.URL
	srv.Close() // now nothing listens there
	h := newHarness(t, allowAll)
	res := h.get(url)
	if !res.IsError || !strings.Contains(res.Text, "web_fetch: ") || len(res.Text) > 400 {
		t.Errorf("result = %v %q", res.IsError, res.Text)
	}
	if strings.Contains(res.Text, "Get \"") {
		t.Errorf("url.Error wrapper leaked into the message: %q", res.Text)
	}
	h2 := newHarness(t, allowAll)
	h2.f.guard.lookup = staticLookup(nil)
	res = h2.get("http://no-such-host.test/")
	if !res.IsError || !strings.Contains(res.Text, `could not resolve host "no-such-host.test"`) {
		t.Errorf("result = %v %q", res.IsError, res.Text)
	}
}

func TestHTTPSVerifiesCertificates(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "secret")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the client's handshake abort is expected
	srv.StartTLS()
	t.Cleanup(srv.Close)
	res := newHarness(t, allowAll).get(srv.URL)
	if !res.IsError || !strings.Contains(res.Text, "TLS certificate error") {
		t.Errorf("result = %v %q; an untrusted certificate must not be accepted", res.IsError, res.Text)
	}
}

// ------------------------------------------------------------------ cache

func TestCache(t *testing.T) {
	var n atomic.Int32
	srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		v := n.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "version %d of %s", v, r.URL.RequestURI())
	})
	h := newHarness(t, allowAll)
	agentA, agentB := h.env("a"), h.env("b")
	get := func(env *tools.Env, path string) (string, bool) {
		res := h.run(env, map[string]any{"url": srv.URL + path})
		if res.IsError {
			t.Fatal(res.Text)
		}
		_, body, _ := splitPage(t, res.Text)
		return body, res.Meta["cached"] == true
	}

	if body, cached := get(agentA, "/p"); body != "version 1 of /p" || cached {
		t.Fatalf("first fetch = %q cached=%v", body, cached)
	}
	// The cache is shared by every agent and keyed by URL alone.
	if body, cached := get(agentB, "/p"); body != "version 1 of /p" || !cached {
		t.Errorf("second agent = %q cached=%v", body, cached)
	}
	if n.Load() != 1 {
		t.Errorf("server hit %d times, want 1", n.Load())
	}
	// A different path, a different query and a different host spelling are different keys.
	get(agentA, "/q")
	get(agentA, "/p?x=1")
	if n.Load() != 3 {
		t.Errorf("server hits = %d, want 3", n.Load())
	}
	// A fragment is not part of the key.
	if _, cached := get(agentA, "/p#section"); !cached {
		t.Error("fragment changed the cache key")
	}
	// Host case does not matter.
	upper := strings.Replace(srv.URL, "127.0.0.1", "127.0.0.1", 1)
	if res := h.run(agentA, map[string]any{"url": upper + "/p"}); res.Meta["cached"] != true {
		t.Error("identical URL missed the cache")
	}

	// Just inside the TTL it is still served from the cache; at the TTL it is refetched.
	h.advance(15*time.Minute - time.Second)
	if _, cached := get(agentA, "/p"); !cached {
		t.Error("entry expired early")
	}
	h.advance(time.Second)
	if body, cached := get(agentA, "/p"); cached || body != "version 4 of /p" {
		t.Errorf("after the TTL: %q cached=%v", body, cached)
	}
	// Reading an entry does not extend its life.
	h.advance(14 * time.Minute)
	if _, cached := get(agentA, "/p"); !cached {
		t.Error("expected a hit")
	}
	h.advance(2 * time.Minute)
	if _, cached := get(agentA, "/p"); cached {
		t.Error("expired entry served after being read once more")
	}
}

func TestCacheStillChecksPermission(t *testing.T) {
	srv, hits := serve(t, "text/plain", "content")
	h := newHarness(t, allowAll)
	if res := h.get(srv.URL); res.IsError {
		t.Fatal(res.Text)
	}
	denier := &recordingPerm{allow: false, why: "network access is off for reviewers"}
	env := h.env("reviewer")
	env.Role = "reviewer"
	env.Perm = denier
	res := h.run(env, map[string]any{"url": srv.URL})
	if !res.IsError || !strings.Contains(res.Text, "permission denied: network access is off for reviewers") {
		t.Errorf("cached content served without permission: %+v", res)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d", hits.Load())
	}
}

func TestCacheEviction(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "body of "+r.URL.Path)
	})
	h := newHarness(t, Config{AllowPrivate: true, CacheEntries: 2})
	get := func(p string) { h.get(srv.URL + p) }
	get("/a")
	get("/b")
	get("/a") // refreshes a: b is now the least recently used
	get("/c") // evicts b
	get("/a") // still cached
	get("/b") // refetched
	mu.Lock()
	defer mu.Unlock()
	if counts["/a"] != 1 || counts["/b"] != 2 || counts["/c"] != 1 {
		t.Errorf("fetch counts = %v, want a:1 b:2 c:1", counts)
	}
	if h.f.cache.len() != 2 {
		t.Errorf("cache holds %d entries, want 2", h.f.cache.len())
	}
}

func TestCacheUnit(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newCache(3, 1000, time.Minute)
	mk := func(size int) *document { return &document{text: strings.Repeat("x", size)} }
	sz := func(size int) int64 { return mk(size).size() }
	if sz(10) < 256 {
		t.Fatal("size accounting should include overhead")
	}
	c.put("a", mk(10), now)
	c.put("b", mk(10), now)
	if _, ok := c.get("a", now); !ok {
		t.Fatal("a missing")
	}
	// A document larger than the whole budget is not cached.
	c.put("huge", mk(5000), now)
	if _, ok := c.get("huge", now); ok {
		t.Error("oversized document cached")
	}
	// Byte budget: three ~300 byte entries fit in 1000, a fourth evicts the LRU one.
	c2 := newCache(100, 1000, time.Minute)
	for _, k := range []string{"1", "2", "3"} {
		c2.put(k, mk(20), now)
	}
	c2.get("1", now) // 2 is now the oldest
	c2.put("4", mk(20), now)
	if _, ok := c2.get("2", now); ok {
		t.Error("byte budget not enforced")
	}
	if _, ok := c2.get("1", now); !ok {
		t.Error("recently used entry evicted")
	}
	// Replacing a key does not double count.
	c3 := newCache(10, 1000, time.Minute)
	for i := 0; i < 20; i++ {
		c3.put("k", mk(20), now)
	}
	if c3.len() != 1 || c3.bytes != sz(20) {
		t.Errorf("len=%d bytes=%d after replacing one key repeatedly", c3.len(), c3.bytes)
	}
	// Expiry drops the entry.
	if _, ok := c3.get("k", now.Add(time.Minute)); ok {
		t.Error("expired entry returned")
	}
	if c3.len() != 0 || c3.bytes != 0 {
		t.Errorf("expired entry not removed: len=%d bytes=%d", c3.len(), c3.bytes)
	}
}

func TestConcurrentFetches(t *testing.T) {
	srv, hits := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "page %s", r.URL.Path)
	})
	h := newHarness(t, allowAll)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("/shared")
			if i%2 == 1 {
				path = fmt.Sprintf("/own/%d", i)
			}
			for k := 0; k < 5; k++ {
				res, err := h.tryRun(h.env(fmt.Sprintf("agent-%d", i)), map[string]any{"url": srv.URL + path})
				if err != nil || res.IsError {
					t.Errorf("agent %d: %v %+v", i, err, res)
					return
				}
				if _, body, _ := splitPage(t, res.Text); body != "page "+path {
					t.Errorf("agent %d: body = %q", i, body)
					return
				}
			}
		}()
	}
	wg.Wait()
	// 16 distinct own pages, plus the shared one at most once per racing agent.
	if n := hits.Load(); n < 17 || n > 32 {
		t.Errorf("server hits = %d, want between 17 and 32", n)
	}
}

// ------------------------------------------------------------------ input and permission

func TestFetchInputDecoding(t *testing.T) {
	srv, _ := serve(t, "text/plain", "ok")
	h := newHarness(t, allowAll)
	tests := []struct {
		name  string
		input string
		want  string // error substring; empty means success
	}{
		{"missing url", `{}`, `missing required field "url"`},
		{"empty input", ``, `missing required field "url"`},
		{"typo names unknown fields", `{"uri":"http://x"}`, `unknown field(s) "uri"`},
		{"url wrong type", `{"url": 5}`, `field "url" must be a string`},
		{"url null", `{"url": null}`, `missing required field "url"`},
		{"blank url", `{"url": "  "}`, `missing required field "url"`},
		{"array input", `[]`, "must be a JSON object"},
		{"string input", `"http://x"`, "must be a JSON object"},
		{"truncated", `{"url":"http://x`, "not valid JSON"},
		{"offset wrong type", `{"url":"` + srv.URL + `","offset":{}}`, `field "offset" must be an integer`},
		{"extra fields ignored", `{"url":"` + srv.URL + `","method":"POST","headers":{"x":"y"}}`, ""},
		{"file scheme", `{"url":"file:///etc/passwd"}`, `unsupported URL scheme "file"`},
		{"ftp scheme", `{"url":"ftp://example.com/x"}`, `unsupported URL scheme "ftp"`},
		{"javascript scheme", `{"url":"javascript:alert(1)"}`, `unsupported URL scheme "javascript"`},
		{"data scheme", `{"url":"data:text/html,<h1>x</h1>"}`, `unsupported URL scheme "data"`},
		{"gopher scheme", `{"url":"gopher://example.com/"}`, `unsupported URL scheme "gopher"`},
		{"credentials", `{"url":"http://user:pass@example.com/"}`, "embedded credentials"},
		{"no host", `{"url":"http:///path"}`, "no host"},
		{"control characters", `{"url":"http://exa\nmple.com/"}`, "control characters"},
		{"very long url", `{"url":"http://example.com/` + strings.Repeat("a", 9000) + `"}`, "limit is 8192"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := h.run(h.env("a"), tt.input)
			if tt.want == "" {
				if res.IsError {
					t.Fatalf("unexpected error: %s", res.Text)
				}
				return
			}
			if !res.IsError || !strings.Contains(res.Text, tt.want) || !strings.HasPrefix(res.Text, "web_fetch: ") {
				t.Errorf("result = %v %q, want error containing %q", res.IsError, res.Text, tt.want)
			}
		})
	}
}

func TestParseFetchURL(t *testing.T) {
	tests := []struct {
		in   string
		want string // normalised URL, or "" for an error
	}{
		{"https://example.com/a?b=c", "https://example.com/a?b=c"},
		{"http://example.com", "http://example.com"},
		{"  https://example.com/x  ", "https://example.com/x"},
		{"example.com/page", "https://example.com/page"},
		{"example.com", "https://example.com"},
		{"//example.com/x", "https://example.com/x"},
		{"localhost:8080/x", "https://localhost:8080/x"},
		{"example.com:443", "https://example.com:443"},
		{"https://example.com/a#frag", "https://example.com/a"},
		{"HTTPS://EXAMPLE.com/A", "https://EXAMPLE.com/A"},
		{"http://[2606:4700::1111]:8080/x", "http://[2606:4700::1111]:8080/x"},
		{"https://example.com/päth?q=é", "https://example.com/p%C3%A4th?q=é"},
		{"mailto:a@b.co", ""},
		{"tel:+15551234", ""},
		{"javascript:alert(1)", ""},
		{"data:text/plain,x", ""},
		{"file:///etc/passwd", ""},
		{"ftp://x/y", ""},
		{"http://", ""},
		{"https://user@example.com", ""},
		{"", ""},
		{"http://exa mple.com", ""},
		{"http://%zz/", ""},
	}
	for _, tt := range tests {
		u, err := parseFetchURL(tt.in)
		switch {
		case tt.want == "" && err == nil:
			t.Errorf("parseFetchURL(%q) = %v, want an error", tt.in, u)
		case tt.want != "" && err != nil:
			t.Errorf("parseFetchURL(%q) error %v, want %s", tt.in, err, tt.want)
		case tt.want != "" && u.String() != tt.want:
			t.Errorf("parseFetchURL(%q) = %s, want %s", tt.in, u, tt.want)
		}
	}
}

func TestFetchPermissionRequest(t *testing.T) {
	srv, hits := serve(t, "text/plain", "content")
	h := newHarness(t, allowAll)

	t.Run("denied", func(t *testing.T) {
		p := &recordingPerm{allow: false, why: "networking is disabled"}
		env := h.env("planner")
		env.Role = "planner"
		env.Perm = p
		res := h.run(env, map[string]any{"url": srv.URL + "/secret"})
		if !res.IsError || res.Text != "permission denied: networking is disabled" {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
		if hits.Load() != 0 {
			t.Error("a denied fetch reached the network")
		}
		reqs := p.seen()
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		r := reqs[0]
		if r.Tool != "web_fetch" || !r.Network || r.Writes || r.Command != "" || r.Paths != nil || r.Agent != "planner" || r.Role != "planner" {
			t.Errorf("request = %+v", r)
		}
		if want := "fetch " + srv.URL + "/secret"; r.Summary != want {
			t.Errorf("Summary = %q, want %q", r.Summary, want)
		}
		if !json.Valid(r.Input) {
			t.Errorf("Input = %q", r.Input)
		}
	})
	t.Run("denied without reason", func(t *testing.T) {
		env := h.env("a")
		env.Perm = &recordingPerm{}
		if res := h.run(env, map[string]any{"url": srv.URL}); res.Text != "permission denied" {
			t.Errorf("text = %q", res.Text)
		}
	})
	t.Run("allowed", func(t *testing.T) {
		env := h.env("a")
		env.Perm = &recordingPerm{allow: true}
		if res := h.run(env, map[string]any{"url": srv.URL + "/ok"}); res.IsError {
			t.Errorf("result = %s", res.Text)
		}
	})
	t.Run("summary is bounded", func(t *testing.T) {
		p := &recordingPerm{allow: false}
		env := h.env("a")
		env.Perm = p
		h.run(env, map[string]any{"url": srv.URL + "/" + strings.Repeat("a", 3000)})
		if s := p.seen()[0].Summary; len(s) > 320 {
			t.Errorf("Summary is %d bytes", len(s))
		}
	})
	t.Run("input is validated before asking", func(t *testing.T) {
		p := &recordingPerm{allow: true}
		env := h.env("a")
		env.Perm = p
		h.run(env, map[string]any{"url": "file:///etc/passwd"})
		if len(p.seen()) != 0 {
			t.Error("asked permission for an unsupported URL")
		}
	})
}

func TestNilEnv(t *testing.T) {
	srv, _ := serve(t, "text/plain", "hi")
	h := newHarness(t, allowAll)
	res, err := h.tool.Run(context.Background(), &tools.Call{Input: json.RawMessage(`{"url":"` + srv.URL + `"}`)})
	if err != nil || res.IsError {
		t.Errorf("nil Env: %+v %v", res, err)
	}
}

func TestSpecs(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg, Config{})
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "web_fetch" {
		t.Fatalf("Config{} registered %v; web_search must need a backend", names(specs))
	}
	reg2 := tools.NewRegistry()
	Register(reg2, Config{Backend: &fakeBackend{}})
	specs, err = reg2.Specs()
	if err != nil {
		t.Fatal(err)
	}
	if n := names(specs); strings.Join(n, ",") != "web_fetch,web_search" {
		t.Fatalf("registered %v", n)
	}
	for _, s := range specs {
		if w := len(strings.Fields(s.Description)); w == 0 || w > 120 {
			t.Errorf("%s: %d words in the description", s.Name, w)
		}
		var schema struct {
			Type       string                    `json:"type"`
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		}
		if err := json.Unmarshal(s.InputSchema, &schema); err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		if schema.Type != "object" || len(schema.Required) != 1 {
			t.Errorf("%s: schema = %s", s.Name, s.InputSchema)
		}
		if _, ok := schema.Properties[schema.Required[0]]; !ok {
			t.Errorf("%s: required property missing", s.Name)
		}
		if _, err := core.Canonical(s.InputSchema); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
		if !s.ReadOnly {
			t.Errorf("%s should be marked read-only", s.Name)
		}
		if len(s.InputSchema) > 700 {
			t.Errorf("%s: schema is %d bytes", s.Name, len(s.InputSchema))
		}
	}
	// The contract names the parameters models are prompted with.
	for _, want := range []string{`"url"`, `"offset"`, `"limit"`} {
		if !strings.Contains(string(specs[0].InputSchema), want) {
			t.Errorf("web_fetch schema lacks %s", want)
		}
	}
	for _, want := range []string{`"query"`, `"max_results"`} {
		if !strings.Contains(string(specs[1].InputSchema), want) {
			t.Errorf("web_search schema lacks %s", want)
		}
	}
}

func names(specs []core.ToolSpec) []string {
	var out []string
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// ensure the url import is used in all build configurations
var _ = url.Parse

// Hidden characters are the carrier for instructions a human reviewing the
// transcript never sees; they are removed at the source for every content type.
func TestFetchStripsInvisibleCharacters(t *testing.T) {
	tagged := "\U000E0049\U000E0067\U000E006E\U000E006F\U000E0072\U000E0065" // "Ignore" in tag characters
	hidden := tagged + "\u202e\u2066\u200b\u2060\ufeff\u00ad"
	zwj := "\U0001F468\u200d\U0001F469"
	tests := []struct {
		name, ctype, body, want string
	}{
		{"html text", "text/html", "<p>vis" + hidden + "ible " + zwj + "</p>", "visible " + zwj},
		{"html link text and url", "text/html", `<a href="/x">li` + hidden + `nk</a>`, "[link](%URL%/x)"},
		{"html title", "text/html", "<title>Ti" + hidden + "tle</title><p>body</p>", "body"},
		{"plain text", "text/plain", "pla" + hidden + "in\n", "plain\n"},
		{"json string", "application/json", `{"k":"v` + hidden + `al"}`, "{\n  \"k\": \"val\"\n}"},
		{"alt text", "text/html", `<img alt="a` + hidden + `lt">`, "[image: alt]"},
		{"pre block", "text/html", "<pre>co" + hidden + "de</pre>", "```\ncode\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, tt.ctype, tt.body)
			res := newHarness(t, allowAll).get(srv.URL)
			if res.IsError {
				t.Fatal(res.Text)
			}
			for _, r := range res.Text {
				if invisible(r) {
					t.Fatalf("U+%04X survived in %q", r, res.Text)
				}
			}
			_, body, _ := splitPage(t, res.Text)
			if want := strings.ReplaceAll(tt.want, "%URL%", srv.URL); body != want {
				t.Errorf("body = %q, want %q", body, want)
			}
		})
	}
	// The page title line is cleaned too.
	srv, _ := serve(t, "text/html", "<title>Ti"+hidden+"tle</title><p>body</p>")
	if res := newHarness(t, allowAll).get(srv.URL); !strings.Contains(res.Text, "\nTitle: Title\n") {
		t.Errorf("text = %q", res.Text)
	}
}

func TestStripInvisible(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"plain ascii", "plain ascii"},
		{"caf\u00e9 \u65e5\u672c\u8a9e \U0001F600", "caf\u00e9 \u65e5\u672c\u8a9e \U0001F600"},
		{"a\U000E0041b", "ab"},
		{"a\u202eb", "ab"},
		{"a\u200bb\u2060c\ufeffd", "abcd"},
		{"soft\u00adhyphen", "softhyphen"},
		{"\u200d\u200c\u200e\u200f", "\u200d\u200c\u200e\u200f"},
		{"\u2764\ufe0f", "\u2764\ufe0f"},
	}
	for _, tt := range tests {
		if got := stripInvisible(tt.in); got != tt.want {
			t.Errorf("stripInvisible(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
