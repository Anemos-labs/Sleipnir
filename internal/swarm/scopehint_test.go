package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A scope that names a directory missing under the project root but present one level down gets a note naming the scope that
// would hold the work: a manager scoped tasks to internal/store/** for a project made in a subdirectory, and every write was
// refused. Scopes that exist, or that nothing matches, get none. A refused write names its path relative to the root.
func TestScopeHintNamesTheProjectSubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "check", "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SessionID: "t"}, Deps{Root: root}, nil)
	if got := s.scopeHint([]string{"docs/**", "internal/store/**"}); !strings.Contains(got, "check/internal/store/**") || !strings.Contains(got, "relative to the project root") {
		t.Fatalf("hint = %q", got)
	}
	for _, files := range [][]string{{"docs/**"}, {"web/**"}, {"*.md"}, nil} {
		if got := s.scopeHint(files); got != "" {
			t.Errorf("%v: unexpected hint %q", files, got)
		}
	}
	s.Leases.SetRoots(root)
	task, _ := s.Board.CreateTask("mgr", TaskSpec{Title: "store", Files: []string{"internal/store/**"}})
	if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	err := s.Leases.BeforeWrite("be-1", filepath.Join(root, "check", "internal", "store", "a.go"))
	if err == nil || !strings.Contains(err.Error(), "relative to the project root") || !strings.Contains(err.Error(), "check/internal/store/a.go") {
		t.Fatalf("refusal = %v", err)
	}
}
