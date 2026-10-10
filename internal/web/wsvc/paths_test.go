package wsvc

import (
	"strings"
	"testing"
)

// cleanRel is the one door a path the page names goes through: it returns the slash-separated clean form of a project-relative path
// and refuses everything else.
func TestCleanRel(t *testing.T) {
	accepted := map[string]string{
		"a.txt":            "a.txt",
		"dir/a.txt":        "dir/a.txt",
		"dir//a.txt":       "dir/a.txt",
		"./dir/a.txt":      "dir/a.txt",
		"dir/../a.txt":     "a.txt",
		"dir/./sub/a.txt/": "dir/sub/a.txt",
		"..a/b..":          "..a/b..",
		"naïve/ünï.go":     "naïve/ünï.go",
		"a b/c d":          "a b/c d",
	}
	for in, want := range accepted {
		if got, err := cleanRel(in); err != nil || got != want {
			t.Errorf("cleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	refused := []string{
		"", ".", "..", "../a", "a/../..", "a/../../b", "/", "/etc/passwd", "//host/share", "a\\b", "a\x00b", "a\nb", "a\x7fb", "\xff\xfe",
		strings.Repeat("a/", maxPathBytes),
	}
	for _, in := range refused {
		if got, err := cleanRel(in); err == nil {
			t.Errorf("cleanRel(%.30q) = %q, want a refusal", in, got)
		}
	}
}
