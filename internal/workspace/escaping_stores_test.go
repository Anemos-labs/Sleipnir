package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dependency store linked out of the tree is reported with its target, at the top and nested; one linked inside the tree,
// a dangling one, an ordinary store directory and a link with another name are not.
func TestEscapingStores(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, at string) {
		mk(filepath.Dir(at))
		if err := os.Symlink(target, at); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	mk(filepath.Join(outside, "node_modules"))
	mk(filepath.Join(outside, "venv"))
	link(filepath.Join(outside, "node_modules"), filepath.Join(root, "node_modules"))
	link(filepath.Join(outside, "venv"), filepath.Join(root, "packages", "api", ".venv"))
	mk(filepath.Join(root, "shared"))
	link(filepath.Join(root, "shared"), filepath.Join(root, "web", "node_modules"))
	link(filepath.Join(outside, "gone"), filepath.Join(root, "tools", "node_modules"))
	mk(filepath.Join(root, "app", "node_modules"))
	link(filepath.Join(outside, "node_modules"), filepath.Join(root, "deps"))

	got := strings.Join(EscapingStores(root), "\n")
	for _, want := range []string{"node_modules -> ", "packages/api/.venv -> "} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"web/node_modules", "tools/node_modules", "app/node_modules", "deps"} {
		if strings.Contains(got, not) {
			t.Errorf("%q reported:\n%s", not, got)
		}
	}
	if n := len(EscapingStores(root)); n != 2 {
		t.Fatalf("%d reported, want 2:\n%s", n, got)
	}
}

// Copy mode leaves out a dependency store that links outside the project, and keeps one that stays inside and any other
// link out, so a copied tree never starts with a link EscapingStores refuses.
func TestCloneTreeLeavesOutStoresLinkedOutside(t *testing.T) {
	src, outside, dst := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(src, ".venv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("shared", filepath.Join(src, "venv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "docs-link")); err != nil {
		t.Fatal(err)
	}
	if err := cloneTree(tctx(t), src, dst, cloneOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".venv")); !os.IsNotExist(err) {
		t.Fatalf(".venv linked outside was copied: %v", err)
	}
	for _, kept := range []string{"venv", "docs-link"} {
		if _, err := os.Lstat(filepath.Join(dst, kept)); err != nil {
			t.Fatalf("%s was not copied: %v", kept, err)
		}
	}
	if esc := EscapingStores(dst); len(esc) != 0 {
		t.Fatalf("the copy has escaping stores: %v", esc)
	}
}
