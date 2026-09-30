package repocheck

import (
	"regexp"
	"strings"
	"testing"
)

// codeownersPattern turns a CODEOWNERS (gitignore-style) pattern into a regular expression over slash-separated paths
// relative to the root: a leading or inner slash anchors it, a trailing slash means a directory, and what names a
// directory matches everything under it.
func codeownersPattern(p string) *regexp.Regexp {
	anchored := strings.HasPrefix(p, "/") || strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.TrimPrefix(p, "/")
	dirOnly := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			i++
			if i+1 < len(p) && p[i+1] == '/' { // "**/": any number of directories, or none
				i++
				b.WriteString(`(?:.*/)?`)
			} else {
				b.WriteString(`.*`)
			}
		case c == '*':
			b.WriteString(`[^/]*`)
		case c == '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	body := b.String()
	if dirOnly {
		body += `/.*`
	} else {
		body += `(?:/.*)?`
	}
	if anchored {
		return regexp.MustCompile(`^` + body + `$`)
	}
	return regexp.MustCompile(`(?:^|/)` + body + `$`)
}

// Ownership that names a file that no longer exists protects nothing: after a rename the new place silently has no owner
// but the catch-all. Every pattern must match something.
func TestCodeownersPatternsMatchFiles(t *testing.T) {
	files := treeFiles(t)
	n := 0
	for i, l := range lines(read(t, ".github/CODEOWNERS")) {
		if blankOrComment(l) {
			continue
		}
		fields := strings.Fields(uncomment(l))
		if len(fields) < 2 {
			t.Errorf(".github/CODEOWNERS:%d: %q has a pattern and no owner", i+1, strings.TrimSpace(l))
			continue
		}
		n++
		re := codeownersPattern(fields[0])
		matched := false
		for _, f := range files {
			if re.MatchString(f) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf(".github/CODEOWNERS:%d: the pattern %q matches no file of the repository (renamed or removed? update the line, so the new place keeps its owner)", i+1, fields[0])
		}
		for _, o := range fields[1:] {
			if !strings.HasPrefix(o, "@") {
				t.Errorf(".github/CODEOWNERS:%d: %q is not an @user or @org/team owner", i+1, o)
			}
		}
	}
	if n == 0 {
		t.Fatal(".github/CODEOWNERS has no rules")
	}
}

// The hand-edited security zones must each have an explicit line, not only the catch-all: the list is the point of the file.
func TestCodeownersNamesTheSecurityZones(t *testing.T) {
	text := read(t, ".github/CODEOWNERS")
	for _, zone := range []string{
		"internal/kv/", "internal/core/canon.go", "internal/agent/prompt.go", "internal/perm/", "internal/harden/",
		"internal/provider/endpoint.go", "internal/config/trust.go", "internal/events/log.go", "internal/checkpoint/",
		"internal/workspace/", "internal/gitx/", ".github/", "scripts/", "go.mod", "go.sum",
	} {
		found := false
		for _, l := range lines(text) {
			if f := strings.Fields(uncomment(l)); len(f) > 0 && strings.TrimPrefix(f[0], "/") == zone {
				found = true
			}
		}
		if !found {
			t.Errorf(".github/CODEOWNERS has no line for %s", zone)
		}
	}
	if f := strings.Fields(uncomment(firstRule(text))); len(f) == 0 || f[0] != "*" {
		t.Error(".github/CODEOWNERS must start with the catch-all `*` (later lines win)")
	}
}

func firstRule(text string) string {
	for _, l := range lines(text) {
		if !blankOrComment(l) {
			return l
		}
	}
	return ""
}

func TestCodeownersPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"*", "go.mod", true},
		{"*", "internal/kv/x.go", true},
		{"/internal/kv/", "internal/kv/x.go", true},
		{"/internal/kv/", "internal/kv/testdata/golden/a.txt", true},
		{"/internal/kv/", "internal/kv", false},
		{"/internal/kv/", "internal/kvx/x.go", false},
		{"/internal/kv/", "x/internal/kv/x.go", false},
		{"internal/kv/", "x/internal/kv/x.go", false}, // a slash inside anchors the pattern
		{"docs/", "docs/A.md", true},
		{"/go.mod", "go.mod", true},
		{"/go.mod", "bench/go.mod", false},
		{"go.mod", "bench/fixtures/go.mod", true}, // no slash: any depth
		{"*.md", "docs/A.md", true},
		{"/*.md", "docs/A.md", false},
		{"/*.md", "README.md", true},
		{"/docs/**/*.md", "docs/a/b/C.md", true},
		{"/docs/**/*.md", "docs/C.md", true},
		{"/internal/core/canon.go", "internal/core/canon.go", true},
		{"/internal/core/canon.go", "internal/core/canon_test.go", false},
		{"/scripts", "scripts/check.sh", true}, // names a directory: everything under it
		{"/a?c", "abc", true},
		{"/a?c", "a/c", false},
	} {
		if got := codeownersPattern(tc.pattern).MatchString(tc.path); got != tc.want {
			t.Errorf("pattern %q on %q = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}
