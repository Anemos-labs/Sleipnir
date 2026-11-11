package swarm

import (
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
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
	if _, err := b.AddNote("mgr", "shared", "manager", strings.Repeat("x", maxNoteRunes+1)); err == nil || !strings.Contains(err.Error(), limit+"-character ceiling") {
		t.Fatalf("an over-long note: %v", err)
	}
}

// A long note shows its opening on the live board with how to read the rest, and note read=<id> shows it whole: a whole long
// note could be dropped from the board to fit its budget, and nobody would see it.
func TestALongNoteIsShownInPartAndReadWhole(t *testing.T) {
	b := NewBoard(nil)
	long := strings.Repeat("the cache key includes the tenant ", 100) + "NOTE-END"
	id, err := b.AddNote("w-1", "shared", "backend", long)
	if err != nil {
		t.Fatal(err)
	}
	hot := RenderHot(b.Snapshot(), "w-2", "backend", false, DefaultHotConfig(), core.NewBytesEstimator().WithRatio(4))
	if !strings.Contains(hot, "note read="+strconv.Itoa(id)) || strings.Contains(hot, "NOTE-END") {
		t.Fatalf("the live board does not show the long note's opening and where the rest is:\n%s", hot)
	}
	sw := New(Config{SessionID: "t"}, Deps{}, nil)
	sw.Board = b
	res := secRevCall(t, &noteTool{s: sw}, "w-2", "backend", map[string]any{"read": id})
	if res.IsError || !strings.HasSuffix(res.Text, "NOTE-END") {
		t.Fatalf("note read: %s", res.Text)
	}
}
