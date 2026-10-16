package agent

import "testing"

// The record benches a compactor after compactorBenchAt failures in its window, asks it again every compactorProbeEvery-th
// compaction while benched, and trusts it again only after compactorRestoreAfter usable probes in a row.
func TestCompactorHealthBenchesProbesAndRestores(t *testing.T) {
	h := NewCompactorHealth()
	for i := 1; i < compactorBenchAt; i++ {
		ask, gen := h.use()
		if !ask {
			t.Fatalf("failure %d: a trusted compactor was not asked", i)
		}
		if changed, _, _, _ := h.record(gen, false); changed {
			t.Fatalf("benched after %d failures, want %d", i, compactorBenchAt)
		}
	}
	_, gen := h.use()
	changed, benched, failed, n := h.record(gen, false)
	if !changed || !benched || failed != compactorBenchAt || n != compactorBenchAt || !h.Benched() {
		t.Fatalf("after %d failures: changed %v benched %v (%d of %d)", compactorBenchAt, changed, benched, failed, n)
	}
	probe := func() int {
		t.Helper()
		for i := 1; i < compactorProbeEvery; i++ {
			if ask, _ := h.use(); ask {
				t.Fatalf("a benched compactor was asked at compaction %d of a probe cycle", i)
			}
		}
		ask, gen := h.use()
		if !ask {
			t.Fatalf("the %dth compaction is no probe", compactorProbeEvery)
		}
		return gen
	}
	// a probe that fails, then two usable ones and a failure: not three in a row, still benched
	for _, ok := range []bool{false, true, true, false} {
		if changed, _, _, _ := h.record(probe(), ok); changed {
			t.Fatalf("restored after probe %v", ok)
		}
	}
	for i := 1; i <= compactorRestoreAfter; i++ {
		changed, benched, _, _ := h.record(probe(), true)
		if want := i == compactorRestoreAfter; changed != want || benched == want {
			t.Fatalf("usable probe %d: changed %v benched %v", i, changed, benched)
		}
	}
	if ask, _ := h.use(); !ask || h.Benched() {
		t.Fatal("a restored compactor was not asked")
	}
	// trusted again with a clean record: one failure does not bench it
	_, gen = h.use()
	if changed, benched, failed, _ := h.record(gen, false); changed || benched || failed != 1 {
		t.Fatalf("a restored compactor carried its old failures: changed %v benched %v failed %d", changed, benched, failed)
	}
	var none *CompactorHealth
	if ask, _ := none.use(); !ask || none.Benched() {
		t.Fatal("a nil record must trust the compactor")
	}
	if changed, _, _, _ := none.record(0, false); changed {
		t.Fatal("a nil record changed")
	}
}

// Compactions run in the background, so a patch asked under one standing can end under another: it is recorded only under the
// standing it was asked under. Usable patches asked while the compactor was trusted, ending after another agent's failures benched
// it, are no probes and do not restore it; a probe that ends after the restore is no failure of the trusted compactor.
func TestCompactorHealthRecordsAPatchUnderTheStandingItWasAskedUnder(t *testing.T) {
	h := NewCompactorHealth()
	var late []int // asked while trusted, still running
	for i := 0; i < compactorRestoreAfter; i++ {
		_, gen := h.use()
		late = append(late, gen)
	}
	for i := 0; i < compactorBenchAt; i++ {
		_, gen := h.use()
		h.record(gen, false)
	}
	if !h.Benched() {
		t.Fatal("not benched")
	}
	for _, gen := range late {
		if changed, _, _, _ := h.record(gen, true); changed {
			t.Fatal("a patch asked while trusted restored the benched compactor")
		}
	}
	if !h.Benched() {
		t.Fatal("restored by patches that were no probes")
	}
	// restore it with real probes, while one probe is still running
	var running int
	for i := 0; i < compactorRestoreAfter; i++ {
		var gen int
		for ask := false; !ask; {
			ask, gen = h.use()
		}
		if i == 0 {
			for ask := false; !ask; {
				ask, running = h.use()
			}
		}
		h.record(gen, true)
	}
	if h.Benched() {
		t.Fatal("three usable probes in a row did not restore it")
	}
	if _, _, failed, _ := h.record(running, false); failed != 0 {
		t.Fatal("a probe that ended after the restore counted against the trusted compactor")
	}
}
