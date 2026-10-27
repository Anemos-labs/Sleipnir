//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A project's configuration is not written through a repository link that leads out of the project.
func TestCheckProjectTargetRefusesLinksOutOfTheProject(t *testing.T) {
	dir := t.TempDir()
	root, home := filepath.Join(dir, "repo"), filepath.Join(dir, "home", ".sleipnir")
	for _, d := range []string{root, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckProjectTarget(root, ProjectConfigPath(root)); err != nil {
		t.Fatalf("a plain project path: %v", err)
	}
	if err := os.Symlink(home, filepath.Join(root, ".sleipnir")); err != nil {
		t.Fatal(err)
	}
	if err := CheckProjectTarget(root, ProjectConfigPath(root)); err == nil {
		t.Fatal("a .sleipnir that links to the user's directory was accepted")
	}
	os.Remove(filepath.Join(root, ".sleipnir"))
	os.MkdirAll(filepath.Join(root, ".sleipnir"), 0o755)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "config.json"), LocalConfigPath(root)); err != nil {
		t.Fatal(err)
	}
	if err := CheckProjectTarget(root, LocalConfigPath(root)); err == nil {
		t.Fatal("a local file that links out of the project was accepted")
	}
}
