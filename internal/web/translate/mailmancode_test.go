package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// A replay's roster is built by the page from the ids its events name (live.js rosterOf), and the mailman's own cache breaks name it:
// the page knows the mailman by the short name of its role, which must be the swarm's, or a replay counts the mailman as a worker.
func TestThePageKnowsTheMailmanByItsRoleCode(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "ui", "js", "live.js"))
	if err != nil {
		t.Fatal(err)
	}
	want := "const MAILMAN_CODE = '" + swarm.MailmanRole().Short + "';"
	if !strings.Contains(string(src), want) {
		t.Fatalf("live.js does not say %q: the mailman's role code is %q (swarm.MailmanRole)", want, swarm.MailmanRole().Short)
	}
}
