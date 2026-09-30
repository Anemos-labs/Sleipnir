package env

import (
	"os"
	"path/filepath"
	"testing"
)

// NewGit makes a scratch HOME for git when it is not given one. That directory belongs to
// the Git: Close removes it (once), and a directory the caller named is the caller's.
func TestGitCloseRemovesOnlyTheDirectoryNewGitMade(t *testing.T) {
	// NewGit only locates the binary; nothing here runs it.
	bin := os.Args[0]

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // os.MkdirTemp("", ...) reads it on every call
	made, err := NewGit(GitOptions{Bin: bin})
	if err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 1 {
		t.Fatalf("NewGit without a scratch directory made %d entries in the temp dir, want 1", len(ents))
	}
	if err := made.Close(); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("Close left %d entries in the temp dir", len(ents))
	}
	if err := made.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}

	mine := filepath.Join(t.TempDir(), "git-home")
	given, err := NewGit(GitOptions{Bin: bin, Scratch: mine})
	if err != nil {
		t.Fatal(err)
	}
	if err := given.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("Close removed a scratch directory the caller named: %v", err)
	}

	var none *Git
	if err := none.Close(); err != nil {
		t.Errorf("Close on a nil Git: %v", err)
	}
}
