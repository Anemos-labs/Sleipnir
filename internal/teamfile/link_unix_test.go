//go:build unix

package teamfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project whose .sleipnir is a link out of it has no teams of its own: listing skips them, and a save or a removal through
// the link is refused, so the file outside is left alone.
func TestAProjectTeamIsNotReachedThroughALink(t *testing.T) {
	project, home := world(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "teams", "victim.json")
	if err := os.MkdirAll(filepath.Dir(victim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte(`{"slots":["backend"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, ".sleipnir")); err != nil {
		t.Fatal(err)
	}
	for _, e := range List(project, home) {
		if e.Scope == ScopeProject {
			t.Errorf("listed %+v", e)
		}
	}
	if err := Remove(project, home, ScopeProject, "victim"); err == nil || !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("remove: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the file outside was removed: %v", err)
	}
	if _, err := Save(project, home, ScopeProject, "x", &File{Slots: []string{"backend"}}, false); err == nil || !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "teams", "x.json")); err == nil {
		t.Error("a team was saved outside the project")
	}
}
