package export_test

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
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

// A branch's parent reference is redacted with the episode's own identifiers.
func TestRedactionCoversABranchParent(t *testing.T) {
	const addr = "jane.doe@privatecorp.io"
	src, _ := leaky(t, false)
	ep := *src.Episode
	ep.Fork = &rl.Fork{Parent: "run/task-" + addr + "/0", Savepoint: 1, Sibling: 1, Siblings: 2}
	src.Episode = &ep
	out, _ := run(t, []export.Source{src}, export.Options{Format: export.FormatCanonical, KeepFlat: true, DropFlagged: true, Redactor: newRedactor()})
	if !strings.Contains(out, `"parent"`) {
		t.Fatal("no fork in the output")
	}
	if strings.Contains(out, addr) {
		t.Error("the address survived in the branch's parent")
	}
	if src.Episode.Fork.Parent != "run/task-"+addr+"/0" {
		t.Error("the caller's episode was changed")
	}
}

// Two tasks whose ids redact alike are still two tasks to select from: redaction does not change which episodes are kept.
func TestRedactionDoesNotMergeTasks(t *testing.T) {
	var srcs []export.Source
	for _, user := range []string{"alice", "bob"} {
		src, _ := leaky(t, false)
		ep := *src.Episode
		ep.ID, ep.TaskID, ep.Group = "ep-"+user, "/home/"+user+"/task", ""
		src.Episode = &ep
		srcs = append(srcs, src)
	}
	o := export.Options{Format: export.FormatSFT, KeepFlat: true, DropFlagged: true, TopK: 1}
	_, plain := run(t, srcs, o)
	o.Redactor = newRedactor()
	_, redacted := run(t, srcs, o)
	if plain.Records == 0 || redacted.Records != plain.Records {
		t.Fatalf("%d records redacted, %d without redaction", redacted.Records, plain.Records)
	}
}
