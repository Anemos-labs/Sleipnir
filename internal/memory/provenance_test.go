package memory

import (
	"strings"
	"testing"
)

// Without the notes, an import does not bring them in either: not the user's (from the user's file) and not the project's
// (from the project's).
func TestNoNotesHoldsForImports(t *testing.T) {
	w := newWorld(t)
	tree(t, w.home, map[string]string{".sleipnir/SLEIPNIR.md": "prefs\n@MEMORY.md\n", ".sleipnir/MEMORY.md": "- user note"})
	tree(t, w.root, map[string]string{"AGENTS.md": "rules\n@.sleipnir/MEMORY.local.md\n", ".sleipnir/MEMORY.local.md": "- project note"})
	srcs, _ := Load(Opts{Root: w.root, Cwd: w.root, Home: w.home, NoNotes: true})
	for _, s := range srcs {
		if strings.Contains(s.Text, "note") {
			t.Errorf("%s brought in a note: %q", s.Path, s.Text)
		}
	}
	srcs, _ = Load(Opts{Root: w.root, Cwd: w.root, Home: w.home})
	n := 0
	for _, s := range srcs {
		if strings.Contains(s.Text, "note") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("with the notes, %d of them loaded: %+v", n, srcs)
	}
}

// A project file in a directory named "~" is the project's, however its path reads: it is not taken for the user's, and is
// not shown with the user's prefix.
func TestAProjectTildeDirectoryIsNotTheUsers(t *testing.T) {
	w := newWorld(t)
	tree(t, w.root, map[string]string{"AGENTS.md": "rules\n@~/x.md\n@./~/y.md\n", "~/y.md": "from the repo"})
	tree(t, w.home, map[string]string{"x.md": "home file"})
	srcs, _ := w.load("")
	for _, s := range srcs {
		if strings.Contains(s.Text, "from the repo") {
			if s.FromUser() || strings.HasPrefix(s.Path, "~/") {
				t.Errorf("the repository's file passes for the user's: %+v", s)
			}
			return
		}
	}
	t.Fatalf("the project's import was not loaded: %+v", srcs)
}
