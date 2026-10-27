package export_test

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl/export"
)

// The identifiers every record carries (episode, task, group) are redacted like the rest of its text, in every format.
func TestRedactionCoversEpisodeIdentifiers(t *testing.T) {
	const addr = "jane.doe@privatecorp.io"
	src, _ := leaky(t, false)
	ep := *src.Episode
	ep.ID, ep.TaskID, ep.Group = "ep-"+addr, "task-"+addr, "grp-"+addr
	src.Episode = &ep
	for _, f := range []export.Format{export.FormatSteps, export.FormatKTO, export.FormatSFT, export.FormatGroups, export.FormatATIF, export.FormatCanonical} {
		out, _ := run(t, []export.Source{src}, export.Options{Format: f, KeepFlat: true, DropFlagged: true, Redactor: newRedactor(), PackSegments: f == export.FormatSFT})
		if out == "" {
			t.Fatalf("%s: no output", f)
		}
		if strings.Contains(out, addr) {
			t.Errorf("%s: the address survived in an identifier", f)
		}
	}
}
