//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A command's expansion notices can name a file of the project, whose name is anyone's to choose, so the chat sanitizes
// them before it prints them.
func TestExpansionNoticesAreSanitized(t *testing.T) {
	name := "a\x1b]0;owned\x07.txt"
	s := chatSession(t, true, map[string]string{
		".claude/commands/look.md": "see @big.txt",
		name:                       strings.Repeat("x\n", 64<<10),
	})
	if err := os.Symlink(name, filepath.Join(s.Cwd(), "big.txt")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	slashTo(context.Background(), s, "/look", &out, &out)
	if !strings.Contains(out.String(), "note:") {
		t.Fatalf("no notice: %q", out.String())
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Fatalf("a control character reached the terminal: %q", out.String())
	}
}
