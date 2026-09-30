package kv

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

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
}

func turnOf(id int, text string) core.Turn {
	return core.Turn{ID: core.TurnID(id), Role: core.RoleUser, Blocks: []core.Block{core.Text(text)}}
}

func TestArchiveKeepsIdsOrderedWhateverTheArrivalOrder(t *testing.T) {
	a := NewArchive(events.NewMemBlobs())
	for _, id := range []int{5, 3, 9, 1, 7, 3, 8, 2} {
		if err := a.Put("be-1", turnOf(id, fmt.Sprintf("turn %d marker%d", id, id))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Range("be-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []core.TurnID
	for _, tr := range got {
		ids = append(ids, tr.ID)
	}
	if fmt.Sprint(ids) != "[1 2 3 5 7 8 9]" || a.Len("be-1") != 7 {
		t.Fatalf("ids = %v, len = %d", ids, a.Len("be-1"))
	}
	// A re-put replaces the entry: the new text is searchable, the old is not.
	if err := a.Put("be-1", turnOf(5, "replaced with uniquewordfive")); err != nil {
		t.Fatal(err)
	}
	if h := a.Search("be-1", "uniquewordfive", 3); len(h) != 1 || h[0].Turn != 5 {
		t.Fatalf("search after re-put: %+v", h)
	}
	if h := a.Search("be-1", "marker5", 3); len(h) != 0 {
		t.Fatalf("the replaced text is still indexed: %+v", h)
	}
	if a.Len("be-1") != 7 {
		t.Fatalf("len = %d", a.Len("be-1"))
	}
}

// One recall must not decode the whole archive (S47): a range is answered with the first
// turns that fit and the caller is told there is more.
func TestArchiveRangeLimitIsBoundedAndSaysWhenItCut(t *testing.T) {
	cb := &secRevCountingBlobs{m: map[core.Hash][]byte{}}
	a := NewArchive(cb)
	body := strings.Repeat("output line\n", 100) // ~1.2 KB
	for i := 1; i <= 500; i++ {
		if err := a.Put("be-1", turnOf(i, body)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("turn budget", func(t *testing.T) {
		got, more, err := a.RangeLimit("be-1", 1, 500, 10, 0)
		if err != nil || len(got) != 10 || !more || got[0].ID != 1 || got[9].ID != 10 {
			t.Fatalf("got %d turns, more=%v, err=%v", len(got), more, err)
		}
	})
	t.Run("byte budget", func(t *testing.T) {
		cb.reads, cb.bytes = 0, 0
		got, more, err := a.RangeLimit("be-1", 1, 500, 500, 10_000)
		if err != nil || !more || len(got) == 0 || len(got) > 10 {
			t.Fatalf("got %d turns, more=%v, err=%v", len(got), more, err)
		}
		if cb.bytes > 10_000 {
			t.Fatalf("read %d bytes against a 10000 byte budget", cb.bytes)
		}
	})
	t.Run("a range that fits is not marked as cut", func(t *testing.T) {
		got, more, err := a.RangeLimit("be-1", 100, 104, 200, 1<<20)
		if err != nil || len(got) != 5 || more {
			t.Fatalf("got %d turns, more=%v, err=%v", len(got), more, err)
		}
	})
	t.Run("the first turn is always returned", func(t *testing.T) {
		got, more, err := a.RangeLimit("be-1", 7, 9, 10, 1) // budget smaller than any turn
		if err != nil || len(got) != 1 || got[0].ID != 7 || !more {
			t.Fatalf("got %d turns, more=%v, err=%v", len(got), more, err)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		cb.reads, cb.bytes = 0, 0
		got, err := a.Range("be-1", 1, 1<<62)
		if err != nil || len(got) == 0 || len(got) > MaxRangeTurns || cb.bytes > MaxRangeBytes+2000 {
			t.Fatalf("Range returned %d turns after reading %d bytes (limits %d turns, %d bytes): %v", len(got), cb.bytes, MaxRangeTurns, MaxRangeBytes, err)
		}
	})
	t.Run("edges", func(t *testing.T) {
		if got, more, err := a.RangeLimit("be-1", 400, 100, 10, 0); err != nil || len(got) != 0 || more {
			t.Fatalf("an inverted range: %d turns, more=%v, %v", len(got), more, err)
		}
		if got, more, err := a.RangeLimit("be-1", 501, 900, 10, 0); err != nil || len(got) != 0 || more {
			t.Fatalf("past the end: %d turns, more=%v, %v", len(got), more, err)
		}
		if got, more, err := a.RangeLimit("nobody", 1, 5, 10, 0); err != nil || got != nil || more {
			t.Fatalf("unknown agent: %v, %v, %v", got, more, err)
		}
		if got, _, _ := a.RangeLimit("be-1", 1, 1, 10, 0); len(got) != 1 || got[0].ID != 1 {
			t.Fatalf("a single turn: %+v", got)
		}
	})
}

// The index keeps a bounded preview per turn, from both ends, so a long tool output can
// still be found by its start and by its end (where the failure usually is).
func TestArchiveSearchSeesTheEndsOfALongTurn(t *testing.T) {
	a := NewArchive(events.NewMemBlobs())
	long := "BUILD-START " + strings.Repeat("compiling package after package\n", 3000) + "--- FAIL: TestRefreshRace"
	if err := a.Put("be-1", turnOf(1, long)); err != nil {
		t.Fatal(err)
	}
	if err := a.Put("be-1", turnOf(2, "something else entirely")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"build-start", "testrefreshrace"} {
		if h := a.Search("be-1", q, 5); len(h) != 1 || h[0].Turn != 1 {
			t.Errorf("Search(%q) = %+v", q, h)
		}
	}
	// Multi-byte text is cut on character boundaries, in the index and in the snippets.
	jp := strings.Repeat("日本語のテキスト、", 500)
	if err := a.Put("be-1", turnOf(3, jp+"終わりの印")); err != nil {
		t.Fatal(err)
	}
	h := a.Search("be-1", "終わりの印", 5)
	if len(h) != 1 || !utf8.ValidString(h[0].Snippet) || !strings.Contains(h[0].Snippet, "終わり") {
		t.Fatalf("multi-byte search: %+v", h)
	}
	for _, e := range a.byAgent["be-1"].entries {
		if !utf8.ValidString(e.preview) || len(e.preview) > previewHead+previewTail+16 {
			t.Fatalf("preview of t%d: %d bytes, valid=%v", e.id, len(e.preview), utf8.ValidString(e.preview))
		}
	}
}

// The index is small however big the turns are (S08, C-09): it must not keep the turn
// text alive behind a substring.
func TestArchiveIndexDoesNotRetainTurnBodies(t *testing.T) {
	a := NewArchive(secRevNullBlobs{})
	body := strings.Repeat("build output line with details and more words\n", 4000) // ~180 KB
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	const n = 300
	for i := 1; i <= n; i++ {
		if err := a.Put("be-1", turnOf(i, fmt.Sprintf("turn %d %s", i, body))); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(a)
	per := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / n
	t.Logf("%d turns of %d KB: index %d bytes/turn", n, len(body)>>10, per)
	if per > 2048 {
		t.Fatalf("the index holds %d bytes per turn of %d KB: it is keeping turn text", per, len(body)>>10)
	}
}

func TestArchiveReleaseAndClose(t *testing.T) {
	a := NewArchive(events.NewMemBlobs())
	for _, agent := range []string{"be-1", "be-2"} {
		for i := 1; i <= 5; i++ {
			if err := a.Put(agent, turnOf(i, fmt.Sprintf("%s turn %d", agent, i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	a.Release("be-1")
	if a.Len("be-1") != 0 {
		t.Fatalf("be-1 still has %d entries after Release", a.Len("be-1"))
	}
	if got, err := a.Range("be-1", 1, 5); err != nil || len(got) != 0 {
		t.Fatalf("Range after Release: %d turns, %v", len(got), err)
	}
	if h := a.Search("be-1", "turn", 5); len(h) != 0 {
		t.Fatalf("Search after Release: %+v", h)
	}
	if a.Len("be-2") != 5 {
		t.Fatal("releasing one agent must not touch another")
	}
	a.Release("be-1") // idempotent
	a.Release("nobody")
	// A late Put (a run that was still winding down) starts a fresh, small index.
	if err := a.Put("be-1", turnOf(6, "late")); err != nil || a.Len("be-1") != 1 {
		t.Fatalf("late put: len %d, %v", a.Len("be-1"), err)
	}
	a.Close()
	if a.Len("be-1") != 0 || a.Len("be-2") != 0 {
		t.Fatal("Close left entries behind")
	}
	if err := a.Put("be-3", turnOf(1, "after close")); err != nil || a.Len("be-3") != 1 {
		t.Fatalf("an archive stays usable after Close: %d, %v", a.Len("be-3"), err)
	}
}

func TestArchiveIsSafeForConcurrentUse(t *testing.T) {
	a := NewArchive(events.NewMemBlobs())
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		w := w
		agent := fmt.Sprintf("w-%d", w)
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 1; i <= 300; i++ {
				id := i
				if i%7 == 0 {
					id = 300 - i // some out-of-order inserts
				}
				_ = a.Put(agent, turnOf(id+1, fmt.Sprintf("worker %d turn %d common", w, id)))
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _, _ = a.RangeLimit(agent, 1, 1000, 20, 4096)
				_ = a.Search(agent, "common", 5)
				_ = a.Len(agent)
				if i%50 == 49 {
					a.Release(agent)
				}
			}
		}()
	}
	wg.Wait()
}

func TestFormatTurnsAndResultCutOnCharacterBoundaries(t *testing.T) {
	tr := core.Turn{ID: 13, Role: core.RoleUser, Blocks: []core.Block{
		core.ToolResult("a", false, core.Text("FIRST "+strings.Repeat("あ", 300))),
		core.ToolResult("b", false, core.Text("SECOND result")),
		core.Text("trailing text"),
	}}
	out := FormatTurns([]core.Turn{tr}, 100)
	if !utf8.ValidString(out) || !strings.Contains(out, "truncated") {
		t.Fatalf("cut output must be valid UTF-8 and say it was cut: %q", out)
	}
	if got := len(strings.TrimSuffix(out, "\n… [truncated; narrow the turn range]")); got > 100 {
		t.Fatalf("output is %d bytes against a limit of 100", got)
	}
	full := FormatTurns([]core.Turn{tr}, 0)
	if !strings.Contains(full, "SECOND result") || !strings.Contains(full, "trailing text") {
		t.Fatalf("unbounded format lost content:\n%s", full)
	}
	if strings.Contains(FormatTurns([]core.Turn{tr}, 100000), "truncated") {
		t.Fatal("a fitting turn was marked as cut")
	}

	res, err := FormatResult(tr, 1, 0)
	if err != nil || !strings.Contains(res, "SECOND result") || strings.Contains(res, "FIRST") {
		t.Fatalf("FormatResult(1) = %q, %v", res, err)
	}
	res, err = FormatResult(tr, 0, 40)
	if err != nil || !utf8.ValidString(res) || !strings.Contains(res, "FIRST") || !strings.Contains(res, "truncated") {
		t.Fatalf("FormatResult(0, 40) = %q, %v", res, err)
	}
	for _, idx := range []int{-1, 2, 99} {
		if _, err := FormatResult(tr, idx, 0); err == nil {
			t.Errorf("FormatResult(%d) should fail", idx)
		}
	}
}

// What an excerpt or a mask placeholder points at ("recall t13.7") can be read back.
func TestExcerptPointersResolveAgainstTheArchive(t *testing.T) {
	e := safetyEst()
	a := NewArchive(events.NewMemBlobs())
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("read")}})
	cxExchange(th, "s0", 30)
	bigExchange(th, "big", 6, 5000)
	s := stackFor(th)
	for _, tr := range s.Thread.Turns {
		if err := a.Put("be-1", tr); err != nil {
			t.Fatal(err)
		}
	}
	pol := DefaultApplyPolicy()
	res := mustApply(t, s, MechanicalPatch(s, e, 5000, pol), pol)
	last := res.Replacement[len(res.Replacement)-1]
	for i, b := range last.Blocks {
		if !strings.Contains(b.Result[0].Text, fmt.Sprintf("recall t%d.%d", last.ID, i)) {
			t.Fatalf("result %d carries no pointer: %.150s", i, b.Result[0].Text)
		}
		orig, err := a.Range("be-1", last.ID, last.ID)
		if err != nil || len(orig) != 1 {
			t.Fatalf("the archive lost t%d: %v", last.ID, err)
		}
		full, err := FormatResult(orig[0], i, 0)
		if err != nil || !strings.Contains(full, fmt.Sprintf("HEAD-big%d ", i)) || !strings.Contains(full, fmt.Sprintf("TAIL-big%d", i)) {
			t.Fatalf("the pointer of result %d does not lead to the whole result: %.100s ... %v", i, full, err)
		}
	}
}

func FuzzArchivePreview(f *testing.F) {
	f.Add("")
	f.Add("short")
	f.Add(strings.Repeat("word ", 1000))
	f.Add(strings.Repeat("日本語 ", 400))
	f.Add("İ" + strings.Repeat("İ", 900) + "\xff\xfe" + strings.Repeat("\n\t ", 500))
	f.Fuzz(func(t *testing.T, s string) {
		p := buildPreview(s)
		if len(p) > previewHead+previewTail+16 {
			t.Fatalf("preview of %d bytes for a %d byte text", len(p), len(s))
		}
		if !utf8.ValidString(p) && utf8.ValidString(s) {
			t.Fatalf("a valid text produced an invalid preview: %q", p)
		}
		if strings.ContainsAny(p, "\n\t\r") {
			t.Fatalf("preview keeps white space: %q", p)
		}
	})
}
