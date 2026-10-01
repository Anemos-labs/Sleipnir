//go:build unix

package events

import (
	"github.com/reee344/sleipnir/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A blob that cannot be placed is an error to the caller, and not a temporary file left in the store: the temp file of a Put that failed is
// removed, so a disk that fills, a directory that is read-only or a name that is taken does not leave the store with litter that every later
// listing meets. These are the failures of Put that a test can make on any unix: the rest (a write that fails half way) needs a full disk.

func putLitter(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), ".put-") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestPutWhereTheNameIsTakenByADirectoryFailsAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	b, err := NewDirBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("the content that will not fit its name")
	// where the blob goes is a directory with something in it: a rename over it cannot succeed
	p := b.path(core.HashBytes(data))
	if err := os.MkdirAll(filepath.Join(p, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(data); err == nil {
		t.Fatal("a blob was put over a directory")
	}
	if litter := putLitter(t, dir); len(litter) != 0 {
		t.Errorf("the failed Put left %v", litter)
	}
	if _, err := b.Get(core.HashBytes(data)); err == nil {
		t.Error("a blob that was never stored is there")
	}
}

func TestPutWhereAShardDirectoryIsAFileFails(t *testing.T) {
	dir := t.TempDir()
	b, err := NewDirBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("a blob whose first directory is taken by a file")
	h := core.HashBytes(data)
	if err := os.WriteFile(filepath.Join(dir, string(h)[:2]), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(data); err == nil {
		t.Fatal("a blob was put below a file")
	}
	if litter := putLitter(t, dir); len(litter) != 0 {
		t.Errorf("the failed Put left %v", litter)
	}
}

func TestPutIntoAReadOnlyShardDirectoryFailsAndLeavesNoTempFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory that is read-only to everyone else")
	}
	dir := t.TempDir()
	b, err := NewDirBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("a blob whose directory cannot be written")
	shard := filepath.Dir(b.path(core.HashBytes(data)))
	if err := os.MkdirAll(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shard, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shard, 0o700) }) // so that the temp directory can be removed
	if _, err := b.Put(data); err == nil {
		t.Fatal("a blob was put into a directory nobody can write")
	}
	if litter := putLitter(t, dir); len(litter) != 0 {
		t.Errorf("the failed Put left %v", litter)
	}
}

func TestNewDirBlobsBelowAFileFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirBlobs(filepath.Join(file, "blobs")); err == nil {
		t.Fatal("a blob directory was made below a file")
	}
}

// What is in the memory store is what the file store would say: a blob that is there, the list of them, how many.
func TestMemBlobsHasAllAndLen(t *testing.T) {
	m := NewMemBlobs()
	a, _ := m.Put([]byte("alpha"))
	c, _ := m.Put([]byte("charlie"))
	if _, err := m.Put([]byte("alpha")); err != nil { // the same content again is the same blob
		t.Fatal(err)
	}
	if !m.Has(a) || !m.Has(c) || m.Has(core.HashBytes([]byte("never put"))) {
		t.Errorf("Has: alpha %v, charlie %v, a blob never put %v", m.Has(a), m.Has(c), m.Has(core.HashBytes([]byte("never put"))))
	}
	if m.Len() != 2 {
		t.Errorf("Len = %d, want 2 (alpha twice is one blob)", m.Len())
	}
	all := m.All()
	if len(all) != 2 || string(all[a]) != "alpha" || string(all[c]) != "charlie" {
		t.Errorf("All = %v, want the two blobs with their bytes", all)
	}
}
