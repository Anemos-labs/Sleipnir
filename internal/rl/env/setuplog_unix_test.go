//go:build unix

package env

import (
	"os"
	"path/filepath"
	"testing"
)

// The setup log is written as a new file in the snapshot directory: a link that setup left under its name is replaced, not
// followed, so a file outside keeps its content.
func TestTheSetupLogDoesNotFollowALinkSetupLeft(t *testing.T) {
	dir, outside := t.TempDir(), filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "setup.log")); err != nil {
		t.Fatal(err)
	}
	if err := writeInDir(dir, "setup.log", []byte("log")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatalf("the file outside became %q", b)
	}
	fi, err := os.Lstat(filepath.Join(dir, "setup.log"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%v %v", fi, err)
	}
}
