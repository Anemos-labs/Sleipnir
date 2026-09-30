package env

import (
	"strings"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// unanchored names match at any depth
		{"*_test.go", "foo_test.go", true},
		{"*_test.go", "pkg/deep/foo_test.go", true},
		{"*_test.go", "foo_test.go.bak", false},
		{"*_test.go", "foo.go", false},
		{"go.mod", "go.mod", true},
		{"go.mod", "sub/go.mod", true},
		{"go.mod", "go.mod.orig", false},
		// anchored patterns
		{"/Makefile", "Makefile", true},
		{"/Makefile", "sub/Makefile", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/sub/a.md", false},
		{"docs/*.md", "x/docs/a.md", false},
		// double star
		{".github/**", ".github/workflows/ci.yml", true},
		{".github/**", ".github", true},
		{".github/**", "x/.github/ci.yml", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
		{"**/testdata/**", "pkg/testdata/f.txt", true},
		{"**/testdata/**", "testdata/f.txt", true},
		{"**", "anything/at/all", true},
		// directory patterns and prefix semantics
		{"vendor/", "vendor/x/y.go", true},
		{"vendor/", "a/vendor/y.go", true},
		{"vendor/", "vendor", false}, // a file named vendor is not a directory
		{"testdata", "testdata/x", true},
		{"testdata", "a/b/testdata", true},
		{"tests/**", "tests/unit/test_a.py", true},
		// wildcards
		{"test_?.py", "test_a.py", true},
		{"test_?.py", "test_ab.py", false},
		{"[ab]*.go", "a1.go", true},
		{"[ab]*.go", "c1.go", false},
		{"[!ab]*.go", "c1.go", true},
		{"[a-c]x", "bx", true},
		{"[a-c]x", "dx", false},
		{"a\\*b", "a*b", true},
		{"a\\*b", "axb", false},
		{"*", "x", true},
		{"a*", "a/b", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		// case folding: protection must not depend on the filesystem
		{"*_test.go", "FOO_TEST.GO", true},
		{"Makefile", "makefile", true},
		{"[A-Z]x", "bx", true},
		// paths are normalised before matching
		{"go.mod", "./go.mod", true},
		{"docs/a.md", "docs//a.md", true},
		{"go.mod", "sub\\go.mod", true},
		// empty path never matches
		{"*", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.pattern+"~"+tc.path, func(t *testing.T) {
			m, err := CompileGlobs([]string{tc.pattern})
			if err != nil {
				t.Fatal(err)
			}
			if got := m.Match(tc.path); got != tc.want {
				t.Fatalf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestGlobInvalid(t *testing.T) {
	for _, p := range []string{"", "   ", "!x", "a[b", "a\\", "../x", "a/../b", "/", "//", "[", "x/["} {
		if err := ValidateGlob(p); err == nil {
			t.Errorf("pattern %q accepted", p)
		}
	}
	for _, p := range []string{"a", "*.go", "a/b/**", "[]]x", "[a-]", "x\\[", "./a", "a//b"} {
		if err := ValidateGlob(p); err != nil {
			t.Errorf("pattern %q rejected: %v", p, err)
		}
	}
}

func TestGlobMatchWhichAndEmpty(t *testing.T) {
	m, _ := CompileGlobs([]string{"*.md", "src/**"})
	if p, ok := m.MatchWhich("src/a.go"); !ok || p != "src/**" {
		t.Fatalf("got %q %v", p, ok)
	}
	if _, ok := m.MatchWhich("main.go"); ok {
		t.Fatal("unexpected match")
	}
	var nilM *Matcher
	if nilM.Match("x") || !nilM.Empty() {
		t.Fatal("nil matcher misbehaves")
	}
	empty, _ := CompileGlobs(nil)
	if empty.Match("x") || !empty.Empty() {
		t.Fatal("empty matcher misbehaves")
	}
}

// Hostile patterns and paths must not make matching super-linear.
func TestGlobPathologicalInput(t *testing.T) {
	m, err := CompileGlobs([]string{"**/**/**/**/**/x", strings.Repeat("*a", 40) + "b"})
	if err != nil {
		t.Fatal(err)
	}
	deep := strings.Repeat("a/", 400) + "y"
	for i := 0; i < 5; i++ {
		m.Match(deep)
	}
	m.Match(strings.Repeat("a", 2000))
}

func TestGitLike(t *testing.T) {
	for _, n := range []string{".git", ".GIT", ".Git", "git~1", "GIT~1", ".git.", ".git ", ".git. .", ".g\xe2\x80\x8cit", ".git:$INDEX_ALLOCATION"} {
		if !gitLike(n) {
			t.Errorf("%q should be treated as .git", n)
		}
	}
	for _, n := range []string{".gitignore", ".github", "git", "gitx", ".git-crypt", "a.git", "digit~1"} {
		if gitLike(n) {
			t.Errorf("%q wrongly treated as .git", n)
		}
	}
}

func TestValidRelPath(t *testing.T) {
	bad := []string{
		"", "/abs", "../x", "a/../x", "a/..", "./a", "a//b", "a/", "a/./b", ".git/config", "x/.git/hooks/pre-commit",
		"a\\b", "a\x00b", strings.Repeat("a", 5000), strings.Repeat("b", 300), "sub/GIT~1/x", "\xff\xfe",
	}
	for _, p := range bad {
		if validRelPath(p) == nil {
			t.Errorf("%q accepted", p)
		}
	}
	for _, p := range []string{"a", "a/b/c.go", ".github/workflows/ci.yml", "we ird.txt", "ünï/çödé.go", ".gitignore", ".gitattributes"} {
		if err := validRelPath(p); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
}
