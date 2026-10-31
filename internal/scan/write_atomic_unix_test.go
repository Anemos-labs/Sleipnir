//go:build unix

package scan

import (
	"os"
	"path/filepath"
	"testing"
)

// A scan's files are written within the project: a directory on the way that is a link out of it takes no write, and a path
// outside the project is refused.
func TestWriteAtomicStaysInTheProject(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".sleipnir", "scan")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(root, filepath.Join(RunDir(root, "s1"), ReportFile), []byte("report")); err == nil {
		t.Error("a write through a link out of the project succeeded")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("the write landed outside the project: %v", entries)
	}
	if err := WriteAtomic(root, filepath.Join(outside, "x"), []byte("x")); err == nil {
		t.Error("a path outside the project was accepted")
	}
	if err := os.Remove(filepath.Join(root, ".sleipnir", "scan")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(RunDir(root, "s1"), ReportFile)
	if err := WriteAtomic(root, p, []byte("report")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "report" {
		t.Errorf("wrote %q %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 1 {
		t.Errorf("leftover temporary files: %v", entries)
	}
}
