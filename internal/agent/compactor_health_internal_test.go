package agent

import "testing"

// The record benches a compactor after compactorBenchAt failures in its window, asks it again every compactorProbeEvery-th
// compaction while benched, and trusts it again only after compactorRestoreAfter usable probes in a row.
func TestCompactorHealthBenchesProbesAndRestores(t *testing.T) {
	h := NewCompactorHealth()
	for i := 1; i < compactorBenchAt; i++ {
		if !h.use() {
			t.Fatalf("failure %d: a trusted compactor was not asked", i)
		}
		if changed, _, _, _ := h.record(false); changed {
			t.Fatalf("benched after %d failures, want %d", i, compactorBenchAt)
		}
	}
	h.use()
	changed, benched, failed, n := h.record(false)
	if !changed || !benched || failed != compactorBenchAt || n != compactorBenchAt || !h.Benched() {
		t.Fatalf("after %d failures: changed %v benched %v (%d of %d)", compactorBenchAt, changed, benched, failed, n)
	}
	probe := func() {
		t.Helper()
		for i := 1; i < compactorProbeEvery; i++ {
			if h.use() {
				t.Fatalf("a benched compactor was asked at compaction %d of a probe cycle", i)
			}
		}
		if !h.use() {
			t.Fatalf("the %dth compaction is no probe", compactorProbeEvery)
		}
	}
	// a probe that fails, then two usable ones and a failure: not three in a row, still benched
	for _, ok := range []bool{false, true, true, false} {
		probe()
		if changed, _, _, _ := h.record(ok); changed {
			t.Fatalf("restored after probe %v", ok)
		}
	}
	for i := 1; i <= compactorRestoreAfter; i++ {
		probe()
		changed, benched, _, _ := h.record(true)
		if want := i == compactorRestoreAfter; changed != want || benched == want {
			t.Fatalf("usable probe %d: changed %v benched %v", i, changed, benched)
		}
	}
	if !h.use() || h.Benched() {
		t.Fatal("a restored compactor was not asked")
	}
	// trusted again with a clean record: one failure does not bench it
	if changed, benched, failed, _ := h.record(false); changed || benched || failed != 1 {
		t.Fatalf("a restored compactor carried its old failures: changed %v benched %v failed %d", changed, benched, failed)
	}
	var none *CompactorHealth
	if !none.use() || none.Benched() {
		t.Fatal("a nil record must trust the compactor")
	}
	if changed, _, _, _ := none.record(false); changed {
		t.Fatal("a nil record changed")
	}
}
