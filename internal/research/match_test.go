package research

import (
	"strings"
	"testing"
)

func TestCanonical(t *testing.T) {
	cases := map[string]string{
		"http://www.Example.com/a/b/?utm_source=x&b=2&a=1#frag": "https://example.com/a/b?a=1&b=2",
		"https://example.com/":                        "https://example.com",
		"https://docs.rs/serde/latest/serde/?ref=foo": "https://docs.rs/serde/latest/serde",
	}
	for in, want := range cases {
		got, err := Canonical(in)
		if err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"ftp://x.org/a", "/relative", "mailto:a@b.c"} {
		if _, err := Canonical(bad); err == nil {
			t.Errorf("Canonical(%q) accepted", bad)
		}
	}
}

func TestMatchQuote(t *testing.T) {
	page := "## Overview\n\nSTORM **models** the pre-writing stage: it discovers *diverse perspectives*, simulates conversations &amp; " +
		"curates the information into an outline. The final article cites its sources."
	ok, _ := matchQuote(page, "STORM models the pre-writing stage: it discovers diverse perspectives, simulates conversations & curates")
	if !ok {
		t.Fatal("a quote copied from the rendered page did not match its Markdown")
	}
	if ok, _ := matchQuote(page, "STORM models the pre-writing stage … the final article cites its sources"); !ok {
		t.Fatal("elided parts in order did not match")
	}
	if ok, _ := matchQuote(page, "the final article cites its sources … STORM models the pre-writing stage"); ok {
		t.Fatal("elided parts out of order matched")
	}
	ok, near := matchQuote(page, "STORM models the pre-writing stage by inventing whole articles from nothing")
	if ok {
		t.Fatal("an invented quote matched")
	}
	if near == "" {
		t.Fatal("a miss gave no nearby passage to copy from")
	}
	for _, q := range []string{strings.Repeat("-", 25), "the ---------------------", "STORM … sources"} {
		if ok, _ := matchQuote(page, q); ok {
			t.Errorf("%q matched: too few letters and digits to be evidence", q)
		}
	}
}

func TestMatchCode(t *testing.T) {
	page := "```go\nfunc Add(a, b int) int {\n\treturn a + b\n}\n```"
	if !matchCode(page, "func Add(a, b int) int { return a + b }") {
		t.Fatal("code reflowed onto one line did not match")
	}
	if matchCode(page, "func Add(a, b int) int { return a - b }") {
		t.Fatal("changed code matched")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"How do the best harnesses do deep research?": "best-harnesses-deep-research",
		"   ":                         "research",
		"Rust TUI frameworks in 2026": "rust-tui-frameworks-2026",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
