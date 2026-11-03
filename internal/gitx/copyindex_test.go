package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

// An index that is a link is not copied: it would put the file it names, a host file, into the git directory the sandbox shares.
func TestCopyIndexRefusesALink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(dir, "index")
	if err := os.Symlink(secret, idx); err != nil {
		t.Skip(err)
	}
	dst, err := os.Create(filepath.Join(dir, "copy"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := copyIndex(dst, idx); err == nil {
		t.Fatal("a linked index was copied")
	}
	if fi, _ := dst.Stat(); fi.Size() != 0 {
		t.Fatalf("%d bytes were copied", fi.Size())
	}
}
