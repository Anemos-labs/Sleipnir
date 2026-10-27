package traj

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A blob file far larger than any real blob (here a sparse one) is refused before it is read into memory.
func TestAnOversizedBlobIsNotRead(t *testing.T) {
	dir := t.TempDir()
	b, err := events.NewDirBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := b.Put([]byte("small"))
	if err != nil {
		t.Fatal(err)
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if err := os.Truncate(p, maxBlobBytes+1); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	r := newRun(nil, b)
	if _, err := r.blob(h); !errors.Is(err, events.ErrBlobTooLarge) {
		t.Fatalf("err = %v", err)
	}
}
