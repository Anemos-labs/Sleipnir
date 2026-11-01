package memtool

import (
	"os"
	"os/exec"
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
		{"notes.md\n!**/notes.md\n", true},
		{"**/notes.md\n", false},
		{" notes.md\n", true}, // a leading blank is part of the pattern
		{"notes.md   \n", false},
		{"notes.md/\n", true}, // names a directory only
		{"sub/notes.md\n", true},
		{"notes.md\n![\n", true}, // a negation it cannot read counts as taking the name back
		{"\\#notes.md\n#notes.md\n", true},
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
		// where git is at hand it says the same: what was there before did not ignore the file exactly when the name was added
		if git, err := exec.LookPath("git"); err == nil && !strings.Contains(c.have, "![") {
			check := exec.Command(git, "-c", "core.excludesFile=", "check-ignore", "--no-index", "-q", "notes.md")
			check.Dir = dir
			if err := os.WriteFile(gi, []byte(c.have), 0o644); err != nil {
				t.Fatal(err)
			}
			if exec.Command(git, "init", "-q", dir).Run() == nil {
				if ignoredByGit := check.Run() == nil; ignoredByGit == c.added {
					t.Errorf("%q: git says ignored=%v, the test expects the name added=%v", c.have, ignoredByGit, c.added)
				}
			}
			if err := os.WriteFile(gi, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if added := strings.TrimPrefix(string(b), c.have) == "notes.md\n"; added != c.added {
			t.Errorf("%q became %q", c.have, b)
		}
	}
}
