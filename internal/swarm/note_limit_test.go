package swarm

import (
	"strconv"
	"strings"
	"testing"
)

// The note tool states its length limit before a note is written: a manager wrote a note over the limit, had it refused, and wrote
// it again. The refusal names the limit too.
func TestNoteToolStatesItsLimit(t *testing.T) {
	spec := (&noteTool{}).Spec()
	limit := strconv.Itoa(maxNoteRunes)
	if !strings.Contains(spec.Description, "at most "+limit+" characters") || !strings.Contains(string(spec.InputSchema), limit+" characters") {
		t.Fatalf("the note tool does not state its %s-character limit:\n%s\n%s", limit, spec.Description, spec.InputSchema)
	}
	b := NewBoard(nil)
	if _, err := b.AddNote("mgr", "shared", "manager", strings.Repeat("x", maxNoteRunes+1)); err == nil || !strings.Contains(err.Error(), limit+"-character limit") {
		t.Fatalf("an over-long note: %v", err)
	}
}
