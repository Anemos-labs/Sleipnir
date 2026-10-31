package memtool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The last .gitignore line whose pattern matches the notes file decides: a later wildcard negation includes it again, so the
// name is listed at the end; a wildcard that ignores it needs nothing more.
func TestIgnoreBesideReadsPatterns(t *testing.T) {
	for _, c := range []struct {
		have  string
		added bool
	}{
		{"notes.md\n!*.md\n", true},
		{"*.md\n", false},
		{"notes.md\n", false},
		{"*.md\n!notes.md\n", true},
	} {
		dir := t.TempDir()
		gi := filepath.Join(dir, ".gitignore")
		if err := os.WriteFile(gi, []byte(c.have), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ignoreBeside(filepath.Join(dir, "notes.md")); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(gi)
		if added := strings.TrimPrefix(string(b), c.have) == "notes.md\n"; added != c.added {
			t.Errorf("%q became %q", c.have, b)
		}
	}
}
