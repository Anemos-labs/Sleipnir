//go:build unix

package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A journal another user owns is not written to (it could read the session whatever its mode), and an open one of ours is
// made private.
func TestTheJournalIsTheUsersOwnAndPrivate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if os.Getuid() != 0 {
		t.Skip("needs root to give the file to another user")
	}
	if err := os.Chown(p, 12345, 12345); err != nil {
		t.Skip(err)
	}
	if _, err := Open(dir, "s1"); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Fatalf("another user's journal: %v", err)
	}
}
