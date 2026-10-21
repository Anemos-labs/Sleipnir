package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// In an isolated team, the gate does not run the verifier in a tree whose node_modules links outside it, and says why; a
// tree without such a link is verified as before.
func TestGateRefusesAFalseGreenTree(t *testing.T) {
	ran := 0
	s := New(Config{VerifyTimeout: 5e9, VerifyCmd: "verify", Verify: func(context.Context, string, string) (string, int, error) {
		ran++
		return "ok", 0, nil
	}}, Deps{Isolation: &Isolation{Manager: &workspace.Manager{}, Queue: &workspace.Queue{}}}, nil)
	tree, outside := t.TempDir(), t.TempDir()
	if vr := s.runVerify(context.Background(), tree, nil); !vr.ok || ran != 1 {
		t.Fatalf("clean tree: %+v ran=%d", vr, ran)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "node_modules")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	vr := s.runVerify(context.Background(), tree, nil)
	if vr.ok || !vr.ran || vr.infra || ran != 1 || !strings.Contains(vr.out, "node_modules -> ") || !strings.Contains(vr.out, "false green") {
		t.Fatalf("linked tree: %+v ran=%d", vr, ran)
	}
}
