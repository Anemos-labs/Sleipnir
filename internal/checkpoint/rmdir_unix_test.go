//go:build unix

package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

// The removal of a directory a restore emptied removes a directory only: a file or a link put in its place since it was
// checked stays.
func TestRmdirInRemovesOnlyADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, name := range []string{"file", "link"} {
		if err := rmdirIn(r, name); err == nil {
			t.Errorf("rmdirIn(%s) succeeded", name)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	if err := rmdirIn(r, "empty"); err != nil {
		t.Errorf("rmdirIn(empty): %v", err)
	}
}
