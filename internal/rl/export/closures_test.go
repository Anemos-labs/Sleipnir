package export_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
)

// A swarm episode's board closures (rl.Outcome.Closures) ride along in the lossless and the ATIF
// exports next to the efficiency signals, take no part in the best-of-n ranking, and are not
// shared with the caller's episodes.
func TestBoardClosuresAreExportedAndDoNotChangeTheRanking(t *testing.T) {
	srcs := bestOfFour(t)
	o := export.DefaultOptions(export.FormatSFT)
	o.Select = export.SelectBest
	before, _ := run(t, srcs, o)

	closures := []map[string]int{
		{"done:verified": 3, "handed_off": 1},
		{"done:verified": 1},
		{"done:verified": 1, "failed:superseded": 2},
		{"failed:canceled": 4},
	}
	for i, s := range srcs {
		own := map[string]int{} // the episode's own map: closures keeps the expected values apart
		for k, n := range closures[i] {
			own[k] = n
		}
		s.Episode.Outcome.Closures = own
	}
	after, _ := run(t, srcs, o)
	if after != before {
		t.Fatalf("closures changed the sft selection:\n%s\nvs\n%s", before, after)
	}

	out, _ := run(t, srcs, export.Options{Format: export.FormatCanonical, Inline: true})
	seen := 0
	for _, l := range lines(out) {
		var ep rl.Episode
		if err := json.Unmarshal([]byte(l), &ep); err != nil || ep.TaskID == "" {
			continue
		}
		seen++
		if !reflect.DeepEqual(ep.Outcome.Closures, closures[ep.Sample]) {
			t.Errorf("canonical sample %d closures = %v, want %v", ep.Sample, ep.Outcome.Closures, closures[ep.Sample])
		}
		if _, ok := ep.Signals[rl.SigToolCalls]; !ok {
			t.Errorf("canonical sample %d lost its efficiency signals: %v", ep.Sample, ep.Signals)
		}
	}
	if seen != 4 {
		t.Fatalf("canonical export has %d episodes, want 4", seen)
	}

	out, _ = run(t, srcs, export.Options{Format: export.FormatATIF})
	atif := decode(t, out)
	if len(atif) != 4 {
		t.Fatalf("atif export has %d trajectories, want 4", len(atif))
	}
	for _, tr := range atif {
		sl := asMap(asMap(tr["extra"])["sleipnir"])
		if len(asMap(asMap(sl["outcome"])["closures"])) == 0 || sl["signals"] == nil {
			t.Errorf("atif trajectory %v lacks closures or signals: %v", tr["session_id"], sl)
		}
	}
	// The advantage hook writes to clones of the episodes: what it adds to the closure counts stays out
	// of the caller's maps.
	run(t, srcs, export.Options{Format: export.FormatCanonical, Advantage: func(eps []*rl.Episode) error {
		for _, ep := range eps {
			if ep.Outcome.Closures == nil {
				ep.Outcome.Closures = map[string]int{}
			}
			ep.Outcome.Closures["written_by_the_hook"] = 1
		}
		return nil
	}})
	for i, s := range srcs {
		if !reflect.DeepEqual(s.Episode.Outcome.Closures, closures[i]) {
			t.Errorf("export changed the caller's closures of sample %d: %v", i, s.Episode.Outcome.Closures)
		}
	}
}
