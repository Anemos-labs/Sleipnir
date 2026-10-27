//go:build unix

package inspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobReadsDoNotFollowAnAncestorSymlinkOut(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	h := strings.Repeat("ab", 32)
	if err := os.MkdirAll(filepath.Join(outside, "cd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "cd", h), []byte("host secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "blobs", "ab")); err != nil {
		t.Fatal(err)
	}
	bs := openBlobs(filepath.Join(dir, "blobs"))
	if data, _, ok := bs.read(h, 100); ok {
		t.Fatalf("read through blobs/ab -> outside: %q", data)
	}
	if _, ok := bs.size(h); ok {
		t.Fatal("sized a blob through an escaping symlink")
	}
}

func TestASessionDirectorySwappedForASymlinkIsNotRead(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "events.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "s1")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(root, "s1"), Options{base: root}); err == nil {
		t.Fatal("a session directory that links out of the inspection root was opened")
	}
}
