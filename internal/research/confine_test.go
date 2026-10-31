package research

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestALedgerWithAnUnsafePageIDIsRefused(t *testing.T) {
	_, _, r, root := newTestTool(t, "quick", ModeReport)
	for _, id := range []string{"../../escape", "javascript:alert(1)", "sources"} {
		bad := *r
		bad.Pages = []PageMeta{{ID: id, Title: "x"}}
		b, _ := json.Marshal(&bad)
		if err := os.WriteFile(filepath.Join(RunDir(root, r.Slug), "run.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, r.Slug); err == nil {
			t.Errorf("a ledger with page id %q was loaded", id)
		}
	}
}

func TestPageFilesStayInTheRunsPagesDirectory(t *testing.T) {
	_, _, r, root := newTestTool(t, "quick", ModeReport)
	outside := t.TempDir()
	host := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(host, []byte("host secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages := filepath.Join(RunDir(root, r.Slug), "pages")
	if err := os.Symlink(host, filepath.Join(pages, "leak.md")); err != nil {
		t.Fatal(err)
	}
	if got := ReadPage(root, r.Slug, "leak"); got != "" {
		t.Fatalf("read through a page symlink: %q", got)
	}
	if err := writePageFile(root, r.Slug, "leak", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(host); string(b) != "host secret" {
		t.Fatalf("a page write went through the link: %q", b)
	}
	// A pages directory that is itself a link out is not used at all.
	if err := os.RemoveAll(pages); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, pages); err != nil {
		t.Fatal(err)
	}
	if got := ReadPage(root, r.Slug, "secret"); got != "" {
		t.Fatalf("read through a linked pages directory: %q", got)
	}
	if err := writePageFile(root, r.Slug, "secret", []byte("x")); err == nil {
		t.Fatal("wrote through a linked pages directory")
	}
}

func TestAWideTableIsBounded(t *testing.T) {
	head := "|" + strings.Repeat(" h |", 5000) + "\n|" + strings.Repeat("--|", 5000) + "\n"
	src := head + strings.Repeat("|x|\n", 20000)
	html, _ := renderMarkdown(src, mdEnv{})
	if len(html) > 4<<20 {
		t.Fatalf("a %d-byte table rendered to %d bytes", len(src), len(html))
	}
	if !strings.Contains(html, "more rows not shown") {
		t.Error("the cut is not said")
	}
}

// A bare address is a link labelled with itself: its label is not read as Markdown again, which would find the address again.
func TestBareAddressIsALinkOfItself(t *testing.T) {
	out, _ := renderMarkdown("see https://example.com/a_b_c, then [x](https://example.com)", mdEnv{})
	if !strings.Contains(out, `<a href="https://example.com/a_b_c" class="ext" rel="noopener noreferrer" target="_blank">https://example.com/a_b_c</a>`) {
		t.Errorf("bare address: %s", out)
	}
}

// An address in a link's label is the label's text: a link does not nest inside another.
func TestAddressInALinkLabelIsText(t *testing.T) {
	out, _ := renderMarkdown("[see https://a.example/x](https://b.example/y)", mdEnv{})
	if strings.Count(out, "<a ") != 1 || !strings.Contains(out, `>see https://a.example/x</a>`) {
		t.Errorf("label address: %s", out)
	}
	out, _ = renderMarkdown("[[index|https://a.example/_draft_]]", mdEnv{})
	if strings.Count(out, "<a ") != 1 || !strings.Contains(out, `>https://a.example/_draft_</a>`) {
		t.Errorf("wiki label address: %s", out)
	}
}

// Openers without closers render in time linear in the text: each looks for its closer once, not once per opener.
func TestUnclosedOpenersRenderQuickly(t *testing.T) {
	for _, open := range []string{"[", "[[", "`", "[x](", "*a"} {
		src := strings.Repeat(open, 200000/len(open))
		start := time.Now()
		renderMarkdown(src, mdEnv{})
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%q x %d took %v", open, len(src)/len(open), d)
		}
	}
}

// A backtick run with no closing run of its length is literal text, and a long one renders in one pass.
func TestAnUnmatchedBacktickRunIsLiteral(t *testing.T) {
	if out, _ := renderMarkdown("``x and `y`", mdEnv{}); !strings.Contains(out, "``x") || !strings.Contains(out, "<code>y</code>") {
		t.Errorf("rendered %s", out)
	}
	start := time.Now()
	renderMarkdown(strings.Repeat("`", 200000)+"x", mdEnv{})
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a long backtick run took %v", d)
	}
}
