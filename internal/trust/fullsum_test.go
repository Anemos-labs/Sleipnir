package trust

import (
	"strings"
	"testing"
)

// The ledger keeps each file's whole hash: a file that only shares the first digits of the trusted one's is a change.
func TestTheLedgerComparesWholeHashes(t *testing.T) {
	sum := strings.Repeat("a", 64)
	fp := &Footprint{Files: []File{{Path: ".sleipnir/config.json", Sum: sum}}}
	if got := EntryOf(fp).Files[".sleipnir/config.json"]; got != sum {
		t.Fatalf("recorded %q", got)
	}
	prepared := &Footprint{Files: []File{{Path: ".sleipnir/config.json", Sum: sum[:16] + strings.Repeat("b", 48)}}}
	if cs := Changes(EntryOf(fp), prepared); len(cs) != 1 || cs[0].What != "changed" {
		t.Errorf("changes = %v", cs)
	}
	if cs := Changes(EntryOf(fp), fp); len(cs) != 0 {
		t.Errorf("no change reads as %v", cs)
	}
}
