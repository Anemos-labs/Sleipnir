package session

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// An extension directory that is a link (a dotfiles checkout's skills, a project's .claude/skills linked elsewhere in the project) is
// guarded where it really is, not only where the link is: the loaders follow the link, so a write to the target plants a definition
// for the next session just as a write through the link does.
func TestLinkedExtensionDirectoriesAreGuardedAtTheirTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, root := filepath.Join(base, "home"), filepath.Join(base, "project")
	dotfiles := filepath.Join(home, "dotfiles", "skills")
	shared := filepath.Join(root, "shared-skills")
	for _, d := range []string{dotfiles, shared, filepath.Join(home, ".agents"), filepath.Join(root, ".claude")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(dotfiles, filepath.Join(home, ".agents", "skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(root, ".claude", "skills")); err != nil {
		t.Fatal(err)
	}

	rules := linkedConfigDirs(root, home, true)
	want := []string{"Edit(" + filepath.ToSlash(shared) + "/**)", "Edit(" + filepath.ToSlash(dotfiles) + "/**)"}
	if strings.Join(rules, " ") != strings.Join(want, " ") {
		t.Fatalf("rules = %q, want %q", rules, want)
	}
	if got := linkedConfigDirs(root, home, false); len(got) != 1 || got[0] != want[1] {
		t.Errorf("an untrusted project's links are not read, so not guarded: %q", got)
	}

	edit := func(e *perm.Engine, path string) perm.Decision {
		return e.Check(context.Background(), perm.Request{Agent: "main", Tool: "Edit", Summary: "edit", Paths: []string{path}, Writes: true})
	}
	without, err := perm.NewEngine(perm.Config{Mode: perm.ModeAcceptEdits, Root: root, Home: home, Ask: protectedConfigDirs})
	if err != nil {
		t.Fatal(err)
	}
	if d := edit(without, filepath.Join(shared, "x", "SKILL.md")); !d.Allow {
		t.Fatalf("the link's own rule already covers its target, so this test proves nothing: %+v", d)
	}
	ask := append(append([]string(nil), protectedConfigDirs...), rules...)
	e, err := perm.NewEngine(perm.Config{Mode: perm.ModeAcceptEdits, Root: root, Home: home, Ask: ask})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(shared, "x", "SKILL.md"), filepath.Join(root, ".claude", "skills", "x", "SKILL.md")} {
		if d := edit(e, p); d.Allow || !strings.Contains(d.Reason, "approval required") {
			t.Errorf("a write to %s: %+v", p, d)
		}
	}
	if d := edit(e, filepath.Join(root, "main.go")); !d.Allow {
		t.Errorf("an ordinary edit is still allowed: %+v", d)
	}
}
