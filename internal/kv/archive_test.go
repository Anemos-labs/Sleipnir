package kv

import (
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

func TestArchiveRecallRoundTrip(t *testing.T) {
	a := NewArchive(events.NewMemBlobs())
	th := buildThread(5, 400)
	for _, tr := range th.Snapshot().Turns {
		if err := a.Put("be-1", tr); err != nil {
			t.Fatal(err)
		}
	}
	if a.Len("be-1") != 11 || a.Len("nobody") != 0 {
		t.Fatalf("len = %d", a.Len("be-1"))
	}
	got, err := a.Range("be-1", 3, 5)
	if err != nil || len(got) != 3 || got[0].ID != 3 {
		t.Fatalf("range: %v %+v", err, got)
	}
	txt := FormatTurns(got, 0)
	if !strings.Contains(txt, "── t3 user ──") || !strings.Contains(txt, "[call bash") {
		t.Fatalf("format:\n%s", txt)
	}
	hits := a.Search("be-1", "pkg3 test", 5)
	if len(hits) == 0 || hits[0].Turn != 6 {
		t.Fatalf("search should find the assistant turn that ran pkg3 tests: %+v", hits)
	}
	if h := a.Search("be-1", "zzzzzz", 5); len(h) != 0 {
		t.Fatal("no match expected")
	}
	// Idempotent re-put keeps a single entry.
	a.Put("be-1", th.Snapshot().Turns[0])
	if a.Len("be-1") != 11 {
		t.Fatal("re-put duplicated an entry")
	}
	if _, ok := interface{}(core.Turn{}).(core.Turn); !ok {
		t.Fatal("unreachable")
	}
}
