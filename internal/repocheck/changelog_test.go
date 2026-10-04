package repocheck

import (
	"strings"
	"testing"
)

// Compatibility declarations travel with the source; the automatically published
// GitHub releases own version history so no manual version rollover is required.
func TestChangelogLinksTheCanonicalReleaseHistory(t *testing.T) {
	text := read(t, "CHANGELOG.md")
	if !strings.Contains(text, "https://github.com/Anemos-labs/Sleipnir/releases") {
		t.Error("CHANGELOG.md must link the canonical versioned release history")
	}
}
