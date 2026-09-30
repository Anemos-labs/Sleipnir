package reward

import (
	"strings"
	"testing"
	"time"
)

func TestCleanRel(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		escapes bool
	}{
		{"a/b.go", "a/b.go", false},
		{"./a/b.go", "a/b.go", false},
		{"a//b///c.go", "a/b/c.go", false},
		{`a\b\c.go`, "a/b/c.go", false},
		{"a/../b.go", "b.go", false},
		{"a/./b.go", "a/b.go", false},
		{"../b.go", "b.go", true},
		{"../../etc/passwd", "etc/passwd", true},
		{"a/../../b.go", "b.go", true},
		{"/etc/passwd", "etc/passwd", true},
		{"..", "", true},
		{".", "", false},
		{"", "", false},
		{"  spaced.go  ", "spaced.go", false},
		{"dir/", "dir", false},
		{"ünï/cödé.go", "ünï/cödé.go", false},
	}
	for _, tc := range tests {
		got, esc := cleanRel(tc.in)
		if got != tc.want || esc != tc.escapes {
			t.Errorf("cleanRel(%q) = %q,%v want %q,%v", tc.in, got, esc, tc.want, tc.escapes)
		}
	}
}

func TestUnquoteGit(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		quoted bool
	}{
		{`plain.go`, "plain.go", false},
		{`"a b.go"`, "a b.go", true},
		{`"tab\there"`, "tab\there", true},
		{`"q\"uote"`, `q"uote`, true},
		{`"back\\slash"`, `back\slash`, true},
		{`"caf\303\251.go"`, "café.go", true},
		{`"nl\nx"`, "nl\nx", true},
		{`"trailing\"`, `trailing\`, true}, // malformed: a dangling backslash stays literal
		{`"`, `"`, false},
		{`""`, "", true},
		{`"\9"`, `\9`, true},
	}
	for _, tc := range tests {
		got, q := unquoteGit(tc.in)
		if got != tc.want || q != tc.quoted {
			t.Errorf("unquoteGit(%q) = %q,%v want %q,%v", tc.in, got, q, tc.want, tc.quoted)
		}
	}
}

func TestGlobMatching(t *testing.T) {
	tests := []struct {
		patterns []string
		path     string
		want     bool
	}{
		// from the doc's example
		{[]string{"*_test.go", "go.mod", ".github/**"}, "x_test.go", true},
		{[]string{"*_test.go"}, "pkg/deep/x_test.go", true},
		{[]string{"*_test.go"}, "x_test.go.bak", false},
		{[]string{"*_test.go"}, "x_test.gox", false},
		{[]string{"*_test.go"}, "x.go", false},
		{[]string{"go.mod"}, "go.mod", true},
		{[]string{"go.mod"}, "sub/go.mod", true},
		{[]string{"go.mod"}, "go.mod.orig", false},
		{[]string{"go.mod"}, "mygo.mod", false},
		{[]string{".github/**"}, ".github/workflows/ci.yml", true},
		{[]string{".github/**"}, ".github", true},
		{[]string{".github/**"}, "docs/.github/x", false},
		{[]string{".github/**"}, ".githubx/y", false},
		// case folding: the verifier checkout may be case-insensitive
		{[]string{"go.mod"}, "GO.MOD", true},
		{[]string{".github/**"}, ".GitHub/Workflows/CI.yml", true},
		{[]string{"*_TEST.GO"}, "a_test.go", true},
		// directories
		{[]string{"testdata/"}, "pkg/testdata/a.golden", true},
		{[]string{"testdata/"}, "testdata", false},
		{[]string{"testdata"}, "pkg/testdata/a.golden", true},
		{[]string{"internal/foo"}, "internal/foo/x.go", true},
		{[]string{"internal/foo"}, "internal/foobar/x.go", false},
		{[]string{"internal/foo"}, "other/internal/foo/x.go", false},
		{[]string{"/README.md"}, "README.md", true},
		{[]string{"/README.md"}, "docs/README.md", false},
		{[]string{"**/*.golden"}, "a/b/c.golden", true},
		{[]string{"**/*.golden"}, "c.golden", true},
		{[]string{"a/**/z.txt"}, "a/z.txt", true},
		{[]string{"a/**/z.txt"}, "a/b/c/z.txt", true},
		{[]string{"a/**/z.txt"}, "b/a/z.txt", false},
		{[]string{"src/*/gen.go"}, "src/x/gen.go", true},
		{[]string{"src/*/gen.go"}, "src/x/y/gen.go", false},
		{[]string{"?.txt"}, "a.txt", true},
		{[]string{"?.txt"}, "ab.txt", false},
		{[]string{"[ab].txt"}, "a.txt", true},
		{[]string{"[ab].txt"}, "c.txt", false},
		// braces
		{[]string{"*.{yml,yaml}"}, "ci.yaml", true},
		{[]string{"*.{yml,yaml}"}, "ci.json", false},
		// oddities
		{[]string{"["}, "[", true}, // malformed: literal compare, never silently off
		{[]string{"["}, "a", false},
		{[]string{"!go.mod"}, "go.mod", false}, // negation unsupported: ignored
		{[]string{""}, "go.mod", false},
		{[]string{"  "}, "go.mod", false},
		{nil, "go.mod", false},
		{[]string{"go.mod"}, "", false},
		{[]string{"./go.mod"}, "go.mod", true},
		{[]string{`.github\**`}, ".github/x", true},
		{[]string{"ünï/*.go"}, "ünï/cödé.go", true},
		{[]string{"**"}, "anything/at/all", true},
		{[]string{"*"}, "x", true},
	}
	for _, tc := range tests {
		set := compileGlobs(tc.patterns)
		p, _ := cleanRel(tc.path)
		_, got := set.match(p)
		if got != tc.want {
			t.Errorf("patterns %q vs %q = %v, want %v", tc.patterns, tc.path, got, tc.want)
		}
	}
}

func TestGlobReturnsTheUsersPattern(t *testing.T) {
	set := compileGlobs([]string{"*.{yml,yaml}", "go.mod"})
	if pat, ok := set.match("ci.yaml"); !ok || pat != "*.{yml,yaml}" {
		t.Errorf("match reported %q,%v", pat, ok)
	}
}

func TestGlobIsBoundedOnAdversarialPatterns(t *testing.T) {
	// A pattern of many ** against a long path must not go exponential.
	pat := strings.Repeat("**/", 40) + "x.txt"
	path := strings.Repeat("d/", 200) + "y.txt"
	set := compileGlobs([]string{pat})
	start := time.Now()
	if _, ok := set.match(path); ok {
		t.Error("should not match")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("matching took %v", d)
	}
	// Brace explosion is capped.
	if n := len(expandBraces(strings.Repeat("{a,b}", 30), maxBraceAlternatives)); n > maxBraceAlternatives {
		t.Errorf("brace expansion produced %d alternatives", n)
	}
}

func TestCleanAbs(t *testing.T) {
	tests := map[string]string{
		"/a/b/../c": "/a/c", "/a//b": "/a/b", "rel/path": "", "": "", "/": "/", `\a\b`: "/a/b",
	}
	for in, want := range tests {
		if got := cleanAbs(in); got != want {
			t.Errorf("cleanAbs(%q) = %q, want %q", in, got, want)
		}
	}
}
