package workspace

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A node_modules junction out of the tree (mklink /J, which Lstat reports as irregular rather than as a symlink) is
// reported like a symbolic link.
func TestEscapingStoresSeesAJunction(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "node_modules"), outside).CombinedOutput(); err != nil {
		t.Skipf("mklink /J unavailable: %v %s", err, out)
	}
	if got := strings.Join(EscapingStores(root), "\n"); !strings.Contains(got, "node_modules -> ") {
		t.Fatalf("junction not reported: %q", got)
	}
}
