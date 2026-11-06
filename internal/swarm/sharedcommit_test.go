package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In a shared tree the manager's accept says to commit the change with its worker's message, in the repository that holds the
// work (a project in a subdirectory of the session's directory included); nothing is said when the work is in no repository. A run
// accepted seven tasks and left the tree dirty at the starting commit.
func TestSharedTreeAcceptSaysToCommitWithTheWorkersMessage(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "check")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SessionID: "t"}, Deps{Root: root}, nil)
	task := Task{ID: "T3", Title: "store", Files: []string{"check/internal/store/**"}}
	s.noteSharedCommit(task, "feat(store): keep on-the-day records\n\nThe store keeps a record dated today.")
	got := s.sharedCommitHint(task)
	if !strings.Contains(got, "commit this change now in "+repo) || !strings.Contains(got, `"feat(store): keep on-the-day records"`) || !strings.Contains(got, "check/internal/store/**") {
		t.Fatalf("hint = %q", got)
	}
	if again := s.sharedCommitHint(task); !strings.Contains(again, `"Store"`) {
		t.Fatalf("without a kept message the subject comes from the title: %q", again)
	}
	if none := s.sharedCommitHint(Task{ID: "T4", Title: "web", Files: []string{"web/**"}}); none != "" {
		t.Fatalf("hint for work in no repository below the root: %q", none)
	}
	plan := Task{ID: "T5", Title: "plan", Kind: TaskKindPlan, Files: []string{"check/**"}}
	if got := s.sharedCommitHint(plan); got != "" {
		t.Fatalf("a plan commits nothing: %q", got)
	}
}
