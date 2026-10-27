//go:build unix

package memtool

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNotesFilesAreReadBoundedAndOnlyWhenRegular(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	if _, _, err := LoadNotes(fifo); err == nil {
		t.Error("a FIFO was read as notes")
	}
	big := filepath.Join(dir, "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("- x\n", MaxFileBytes/4+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadNotes(big); err == nil {
		t.Error("a file past MaxFileBytes was read")
	}
	if notes, err := ReadNotes(filepath.Join(dir, "missing.md")); err != nil || notes != nil {
		t.Errorf("a missing file: %v %v", notes, err)
	}

	project, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "n.md"), []byte("- host secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, ".sleipnir")); err != nil {
		t.Fatal(err)
	}
	if _, notes, err := LoadNotesIn(project, filepath.Join(".sleipnir", "n.md")); err == nil {
		t.Errorf("read notes through a link out of the project: %v", notes)
	}
}
