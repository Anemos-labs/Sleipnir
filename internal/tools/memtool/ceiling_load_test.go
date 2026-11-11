package memtool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/memory"
)

// Notes the tool saves within its ceilings are all read into the next session: the instruction loader does not cut a notes file
// the tool wrote.
func TestNotesAtTheToolsCeilingAreAllLoadedAgain(t *testing.T) {
	if memory.MaxFileBytes < MaxFileBytes {
		t.Fatalf("the loader reads %d bytes of a file, less than a notes file holds (%d)", memory.MaxFileBytes, MaxFileBytes)
	}
	home := t.TempDir()
	var notes []string
	for size := 0; size+MaxNoteRunes+8 < MaxBytes; size += MaxNoteRunes + 3 {
		notes = append(notes, fmt.Sprintf("N%04d ", len(notes))+strings.Repeat("x", MaxNoteRunes-6))
	}
	path := filepath.Join(home, ".sleipnir", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteNotes(path, notes); err != nil {
		t.Fatal(err)
	}
	srcs, err := memory.Load(memory.Opts{Root: t.TempDir(), Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if strings.HasSuffix(s.Path, "MEMORY.md") {
			last := fmt.Sprintf("N%04d ", len(notes)-1)
			if !strings.Contains(s.Text, last) || strings.Contains(s.Text, "truncated") {
				t.Fatalf("the loaded notes (%d bytes) lost the last of %d notes", len(s.Text), len(notes))
			}
			return
		}
	}
	t.Fatal("MEMORY.md was not loaded")
}
