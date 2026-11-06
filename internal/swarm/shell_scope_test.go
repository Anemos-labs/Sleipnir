package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In a shared tree the writes a shell command is known to make are held to the scope of the writer's task, as its file-tool
// writes are: a worker scoped to internal/store/** wrote files outside it with cat > and cp. A write inside the scope, the creation
// of a directory that leads to it, the .git directory and paths outside the repository pass; an agent with no scoped task is not
// held; an isolated run leaves it to the merge queue.
func TestShellWritesAreHeldToTheTaskScopeInASharedTree(t *testing.T) {
	s := New(Config{SessionID: "t"}, Deps{Root: "/repo"}, nil)
	s.Leases.SetRoots("/repo")
	task, _ := s.Board.CreateTask("mgr", TaskSpec{Title: "store", Files: []string{"internal/store/**"}})
	if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		tree bool
	}{{"/repo/internal/store/a.go", false}, {"/repo/internal/store", true}, {"/repo/internal", false}, {"/repo/.git/index", false}, {"/elsewhere/x", false}} {
		if why := s.Leases.ShellWriteCheck("be-1", c.path, c.tree); why != "" {
			t.Errorf("%s (tree %v) refused: %s", c.path, c.tree, why)
		}
	}
	for _, c := range []struct {
		path string
		tree bool
	}{{"/repo/cmd/main.go", false}, {"/repo/internal", true}, {"/repo/README.md", false}} {
		if why := s.Leases.ShellWriteCheck("be-1", c.path, c.tree); !strings.Contains(why, "outside the scope of your task "+task.ID) {
			t.Errorf("%s (tree %v): %q", c.path, c.tree, why)
		}
	}
	if why := s.Leases.ShellWriteCheck("mgr", "/repo/cmd/main.go", false); why != "" {
		t.Errorf("an agent with no scoped task was held: %s", why)
	}
	s.Leases.Isolate()
	if why := s.Leases.ShellWriteCheck("be-1", "/repo/cmd/main.go", false); why != "" {
		t.Errorf("an isolated run was held: %s", why)
	}
}

// A root reached through a symlink is held in its resolved form too: the permission engine checks a shell write's resolved
// target, and that target must still fall in the repository rather than be taken for a path outside it.
func TestAShellWriteResolvedThroughASymlinkedRootIsStillHeld(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{SessionID: "t"}, Deps{Root: link}, nil)
	s.Leases.SetRoots(link)
	task, _ := s.Board.CreateTask("mgr", TaskSpec{Title: "store", Files: []string{"internal/store/**"}})
	if err := s.Board.Assign("mgr", "be-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if why := s.Leases.ShellWriteCheck("be-1", filepath.Join(resolved, "cmd", "main.go"), false); !strings.Contains(why, "outside the scope of your task "+task.ID) {
		t.Errorf("a resolved write under the symlinked root was not held: %q", why)
	}
	if why := s.Leases.ShellWriteCheck("be-1", filepath.Join(resolved, "internal", "store", "a.go"), false); why != "" {
		t.Errorf("a resolved write inside the scope was refused: %s", why)
	}
}
