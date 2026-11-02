package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A .sleipnir/scan that is a link to another project's ledger is not read as this project's.
func TestLoadRefusesAScanDirectoryLinkedOutOfTheProject(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "s1", "scan.json"), []byte(`{"slug":"s1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, ".sleipnir", "scan")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if _, err := Load(root, "s1"); err == nil || !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("Load = %v, want a refusal", err)
	}
	if err := insideRoot(root, filepath.Join(root, ".sleipnir", "new", "x")); err != nil {
		t.Errorf("a directory not made yet: %v", err)
	}
}
