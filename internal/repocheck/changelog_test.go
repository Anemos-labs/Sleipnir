package repocheck

import (
	"regexp"
	"testing"
)

// The changelog is where a change to prompt bytes is declared and priced (scripts/check-declared.sh insists on an added
// line there), and what a release's archive carries. It needs a place for new entries: an [Unreleased] section, or the
// heading of the version in progress.
func TestChangelogHasAHeadingForNewEntries(t *testing.T) {
	text := read(t, "CHANGELOG.md")
	if !regexp.MustCompile(`(?m)^## \[(Unreleased|[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)\]`).MatchString(text) {
		t.Error("CHANGELOG.md has no `## [Unreleased]` heading and no `## [X.Y.Z]` heading: there is no place for the entry that a change to prompt bytes needs")
	}
}
