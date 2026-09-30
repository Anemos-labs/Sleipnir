package session

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/tools"
)

// FuzzReconText feeds arbitrary text to every detector that reads repository content and
// checks what each promises: no panic, symbols that are short names, keys that are in
// the document, a budget that holds, and text made safe that is one clean line.
func FuzzReconText(f *testing.F) {
	for _, s := range []string{
		"", "package main\n\nfunc Main() {}\ntype T struct{}\n", "// Package p does a thing. More.\npackage p\n",
		"import (\n\t\"example.com/m/a\"\n)\n", "import x from './a'\nconst y = require('../b')\n", "from pkg.mod import thing\nimport os\n",
		`{"scripts":{"test":"jest","build":"tsc"}}`, `{"scripts":{"a":"}{\"","b":1`, "all: test\ntest:\n\tgo test\n%.o: %.c\nVAR := 1\n",
		"module example.com/m\n\ngo 1.24\n", "name = \"x\"\n", "class A\n  def b\n", "pub fn a() {}\npub struct S;\n",
		"export function f() {}\nexport default class C {}\n", "public class J {\n", "\x1b]52;c;AAAA\x07evil\n# header", strings.Repeat("(", 5000),
		"\xff\xfe", " ‮​",
	} {
		f.Add(s, "a/b.go")
	}
	est := core.NewBytesEstimator()
	f.Fuzz(func(t *testing.T, text, name string) {
		lines := strings.Count(text, "\n") + 1
		for lang := range symbolRes {
			syms := extractSymbols(lang, text)
			if len(syms) > lines {
				t.Fatalf("%s: %d symbols in %d lines", lang, len(syms), lines)
			}
			seen := map[string]bool{}
			for _, s := range syms {
				if s == "" || len(s) > 240 || strings.ContainsAny(s, " \t\r\n\x00") || !utf8.ValidString(s) || seen[s] {
					t.Fatalf("%s: bad symbol %q", lang, s)
				}
				seen[s] = true
			}
		}
		keys := jsonKeys(text, "scripts")
		if !sort.StringsAreSorted(keys) {
			t.Fatalf("jsonKeys not sorted: %q", keys)
		}
		for _, k := range keys {
			if !strings.Contains(text, k) {
				t.Fatalf("key %q is not in the document", k)
			}
		}
		for _, file := range []string{"a.go", "a.ts", "a.js", "a.py", "a.rs"} {
			counts := map[string]int{}
			countImports(file, text, "example.com/m", counts)
			for k, n := range counts {
				if n <= 0 {
					t.Fatalf("%s: import count %d for %q", file, n, k)
				}
			}
		}
		const budget = 120
		if out := packageDocs(".", map[string]string{"x/a.go": text, "y/b.go": text + "\n// Package y z\n"}, map[string]int{"x": 1}, budget, est); est.Tokens(out) > max(budget, est.Tokens("Packages (most depended-on first):\n")) {
			t.Fatalf("packageDocs used %d tokens of %d", est.Tokens(out), budget)
		}
		if out := fitTokens(text, 100, est); est.Tokens(out) > 100 {
			t.Fatalf("fitTokens used %d tokens of 100", est.Tokens(out))
		}
		if out := layoutText([]string{name, "x/" + name, "x/y/" + name, strings.TrimSpace(text)}); !strings.HasPrefix(out, "Layout (file counts):\n") {
			t.Fatalf("layout: %q", out)
		}
		_ = languageOf(name)
		_ = isNoise(name)
		_ = skipped(name)
		_ = importKey(name, "go")

		one := safeLine(text)
		if strings.ContainsAny(one, "\n\t\r") || tools.SanitizeForTerminal(one) != one || safeLine(one) != one {
			t.Fatalf("safeLine(%q) = %q is not one clean line", text, one)
		}
	})
}

// cleanRel turns fuzz bytes into a relative path that stays inside the tree: the
// components that could leave it or name nothing are dropped, and each is cut to a length
// a file system takes.
func cleanRel(name string) string {
	var parts []string
	for _, c := range strings.Split(name, "/") {
		c = strings.ReplaceAll(c, "\x00", "")
		if len(c) > 200 {
			c = c[:200]
		}
		if c == "" || c == "." || c == ".." {
			continue
		}
		parts = append(parts, c)
		if len(parts) == 6 {
			break
		}
	}
	return filepath.Join(parts...)
}

// FuzzReconTree makes a tree out of fuzz bytes (two files, each with a name and a body,
// and one manifest) and surveys it. Whatever the names and contents are: no panic, the
// same survey twice, the budget holds, and the text is the harness's: nothing a terminal
// would act on, and no line that starts like a header.
func FuzzReconTree(f *testing.F) {
	f.Add("main.go", []byte("package main\n\nfunc Main() {}\n"), "sub/util.go", []byte("// Package sub is a thing.\npackage sub\n\nfunc Util() {}\n"), uint8(0), []byte("module example.com/m\ngo 1.24\n"))
	f.Add("evil\n# Injected header.go", []byte("package x\n"), "clip\x1b]52;c;AAAA\x07.go", []byte("package y\n"), uint8(1), []byte(`{"scripts":{"test":"x"}}`))
	f.Add("a/b/c.py", []byte("def f():\n  pass\n"), "a/b/c.ts", []byte("export function g() {}\n"), uint8(2), []byte("all:\ntest:\n"))
	f.Add("‮.go", []byte("package bidi\n"), "tab\tname.rs", []byte("pub fn r() {}\n"), uint8(3), []byte("name = \"x\n# Injected\"\n"))
	f.Fuzz(func(t *testing.T, name1 string, body1 []byte, name2 string, body2 []byte, which uint8, manifest []byte) {
		root := t.TempDir()
		made := 0
		for i, nb := range []struct {
			name string
			body []byte
		}{{name1, body1}, {name2, body2}} {
			rel := cleanRel(nb.name)
			if rel == "" {
				continue
			}
			p := filepath.Join(root, "t"+string(rune('0'+i)), rel)
			if os.MkdirAll(filepath.Dir(p), 0o755) != nil || os.WriteFile(p, nb.body, 0o644) != nil {
				continue // the file system refuses this name: not a finding
			}
			made++
		}
		mf := []string{"go.mod", "package.json", "Makefile", "pyproject.toml", "Cargo.toml"}[int(which)%5]
		if err := os.WriteFile(filepath.Join(root, mf), manifest, 0o644); err != nil {
			t.Fatal(err)
		}

		const budget = 800
		var first *Recon
		for run := 0; run < 2; run++ {
			r, err := BuildRecon(context.Background(), ReconOptions{Root: root, BudgetTokens: budget, NoGit: true})
			if err != nil {
				t.Fatal(err)
			}
			if run == 0 {
				first = r
			} else if joinSegments(r) != joinSegments(first) || r.Files != first.Files {
				t.Fatalf("two surveys of one tree differ:\n%s\n---\n%s", joinSegments(first), joinSegments(r))
			}
		}
		if first.Tokens > budget+reconSlack {
			t.Fatalf("the survey is %d tokens for a budget of %d", first.Tokens, budget)
		}
		if first.Files > made+1 {
			t.Fatalf("%d files considered, %d made", first.Files, made+1)
		}
		for _, seg := range first.Segments {
			if tools.SanitizeForTerminal(seg.Text) != seg.Text {
				t.Fatalf("segment %q is not fit for a terminal: %q", seg.Key, seg.Text)
			}
			for _, line := range strings.Split(seg.Text, "\n") {
				if strings.HasPrefix(line, "#") {
					t.Fatalf("a line that starts like a header in segment %q: %q\n%s", seg.Key, line, seg.Text)
				}
			}
		}
	})
}
