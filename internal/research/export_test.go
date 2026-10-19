package research

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExportStaysInsideTheProject checks that an export writes only below its directory of the project, refuses a directory
// it did not write, replaces an earlier export, and cannot follow a link out of the project.
func TestExportStaysInsideTheProject(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	files := map[string][]byte{"index.html": []byte("<p>hi</p>"), "assets/site.css": []byte("p{}")}
	for _, dir := range []string{"", ".", "..", "../x", filepath.Join(outside, "x")} {
		if err := Export(root, dir, files); err == nil {
			t.Errorf("Export(%q) wrote outside the project", dir)
		}
	}
	if err := Export(root, "docs/research", files); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "docs/research/assets/site.css")); err != nil || string(b) != "p{}" {
		t.Fatalf("site.css = %q, %v", b, err)
	}
	if err := Export(root, "docs/research", map[string][]byte{"index.html": []byte("new")}); err != nil {
		t.Fatalf("a second export did not replace the first: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs/research/assets/site.css")); err == nil {
		t.Error("the earlier export's files were kept")
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs/own"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/own/a.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Export(root, "docs/own", files); err == nil {
		t.Error("an export replaced a directory it did not write")
	}
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := Export(root, "out/wiki", files); err == nil {
		t.Error("an export followed a link out of the project")
	}
	if _, err := os.Stat(filepath.Join(outside, "wiki")); err == nil {
		t.Error("an export wrote outside the project through a link")
	}
}
