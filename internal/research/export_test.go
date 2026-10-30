package research

import (
	"os"
	"path/filepath"
	"strings"
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
	if err := os.WriteFile(filepath.Join(root, "docs/research/mine.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Export(root, "docs/research", files); err == nil {
		t.Error("an export replaced an earlier one that now holds a file it did not write")
	}
	if b, err := os.ReadFile(filepath.Join(root, "docs/research/mine.md")); err != nil || string(b) != "mine" {
		t.Errorf("mine.md = %q, %v", b, err)
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
	if err := os.WriteFile(filepath.Join(root, "docs/own", exportMark), []byte("a research wiki exported by sleipnir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Export(root, "docs/own", files); err == nil {
		t.Error("a forged mark let an export replace a directory it did not write")
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

// A mark too large to be one an export wrote is refused before it is read whole, even when its header is right.
func TestExportRefusesAnOversizedMark(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs", "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := markHeader + strings.Repeat("x\n", maxMarkBytes/2+1)
	if err := os.WriteFile(filepath.Join(dir, exportMark), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Export(root, "docs/wiki", map[string][]byte{"index.html": []byte("new")}); err == nil {
		t.Error("an export replaced a directory whose mark is larger than any export writes")
	}
}

// A link planted at .sleipnir/research does not lead a new run, its ledger or its brief out of the project.
func TestRunFilesStayInsideTheProject(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".sleipnir", "research")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := writeAtomic(root, filepath.Join(".sleipnir", "research", "r", "run.json"), []byte("{}")); err == nil {
		t.Error("a ledger was written through a link out of the project")
	}
	if err := mkdirIn(root, filepath.Join(".sleipnir", "research", "r", "pages")); err == nil {
		t.Error("a run directory was created through a link out of the project")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("outside holds %v", ents)
	}
}
