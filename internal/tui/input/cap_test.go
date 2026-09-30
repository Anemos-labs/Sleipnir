package input

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTheBufferIsCapped(t *testing.T) {
	r := newRig(t, Options{})
	r.send(strings.Repeat("a", maxBufferRunes+5000))
	if r.ed.Len() != maxBufferRunes || r.ed.Cursor() != maxBufferRunes {
		t.Fatalf("the buffer holds %d runes, cursor %d", r.ed.Len(), r.ed.Cursor())
	}
	r.send("b", paste("pasted"), paste(lines(10)), "\n")
	if r.ed.Len() != maxBufferRunes {
		t.Errorf("a full buffer takes nothing more: %d", r.ed.Len())
	}
	r.send(kBS, kBS, "xy")
	if got := r.ed.Text(); len(got) != maxBufferRunes || !strings.HasSuffix(got, "axy") {
		t.Errorf("deleting makes room again: %d runes, ends %q", len(got), got[len(got)-4:])
	}
	// the cap applies to loaded text too
	r.ed.SetText(strings.Repeat("z", 3*maxBufferRunes))
	if r.ed.Len() != maxBufferRunes {
		t.Errorf("SetText: %d", r.ed.Len())
	}
	// and an insertion in the middle takes only what fits
	r.ed.SetText(strings.Repeat("q", maxBufferRunes-2))
	r.send(kHome, kRight, paste("12345"))
	if r.ed.Len() != maxBufferRunes || r.state()[:6] != "q12|qq" {
		t.Errorf("a paste that does not fit whole goes in as far as it fits: %d runes, %q", r.ed.Len(), r.state()[:8])
	}
}

func TestLayoutCacheHoldsOnlyTheLatestLayout(t *testing.T) {
	e := NewEditor(Options{})
	e.SetText(strings.Repeat("word ", 300)) // a long line that changes at every key: the cache must not pile up its old versions
	for i := 0; i < 60; i++ {
		e.Handle(RuneKey('x', 0))
		e.View(40)
		if len(e.lcache) > 1 {
			t.Fatalf("after %d keys the cache holds %d lines of a one-line buffer", i+1, len(e.lcache))
		}
	}
	e.SetText("a\nb\nc\na")
	e.View(40)
	if len(e.lcache) != 3 {
		t.Errorf("three distinct lines: %d entries", len(e.lcache))
	}
	e.View(20) // another width replaces them
	for k := range e.lcache {
		if !strings.HasPrefix(k, "18:") {
			t.Errorf("stale entry %q", k)
		}
	}
}

func TestChipsHoldAtMostMaxChipBytes(t *testing.T) {
	r := newRig(t, Options{MaxPasteBytes: maxChipBytes})
	chunk := strings.Repeat("0123456789abcdef\n", 1<<16) // 1 MiB
	for i := 0; i < 20; i++ {
		r.ed.Handle(PasteKey(chunk))
	}
	r.check()
	if r.ed.chipBytes > maxChipBytes {
		t.Errorf("chips hold %d bytes", r.ed.chipBytes)
	}
	if len(r.ed.chips) != maxChipBytes/len(chunk) {
		t.Errorf("%d chips of 1 MiB", len(r.ed.chips))
	}
	if !strings.Contains(r.ed.Display(), truncatedMarker) {
		t.Error("the pastes that no longer fit as chips say they were cut")
	}
	r.send(kEnter)
	if r.ed.chipBytes != 0 || len(r.ed.chips) != 0 {
		t.Error("a submit releases the chips")
	}
}

func TestHistoryInMemoryIsBoundedByBytes(t *testing.T) {
	h := NewHistory()
	for i := 0; i < 100; i++ {
		h.Add(fmt.Sprintf("%03d ", i) + strings.Repeat("x", 60_000))
	}
	total := 0
	for _, e := range h.Entries() {
		total += len(e)
	}
	if total > DefaultHistoryBytes+60_100 || h.Len() < 2 {
		t.Errorf("%d entries, %d bytes", h.Len(), total)
	}
	if last := h.At(h.Len() - 1); !strings.HasPrefix(last, "099 ") {
		t.Errorf("the newest entry stays: %.8q", last)
	}
}

func TestSearchQueryIsBounded(t *testing.T) {
	r := searchRig(t)
	r.send(ctrl('r'), strings.Repeat("q", 5000))
	if n := len(r.ed.search.query); n != maxQuery {
		t.Errorf("query of %d runes", n)
	}
	r.check()
}

func TestCursorPositionReportIsNotAKey(t *testing.T) {
	for _, in := range []string{"\x1b[1;1R", "\x1b[24;80R", "\x1b[1;80R", "\x1b[5;1R"} {
		if got := decodeWhole(in + "x"); !reflect.DeepEqual(got, one(kr('x', 0))) {
			t.Errorf("%q: %v", in, got)
		}
	}
	if got := decodeWhole("\x1b[1;5R"); !reflect.DeepEqual(got, one(ks(F3, Ctrl))) {
		t.Errorf("Ctrl+F3 still works: %v", got)
	}
}

func TestClusterSearchIsBounded(t *testing.T) {
	// a base followed by a thousand combining marks is not a cluster anyone has; looking for where it starts gives up after
	// maxCluster runes instead of reading the whole line, and never goes out of range
	buf := []rune("a" + strings.Repeat("\U00000301", 1000) + "b")
	for i := 0; i <= len(buf); i += 37 {
		if p := prevBoundary(buf, i); p < 0 || p > i {
			t.Fatalf("prevBoundary(%d) = %d", i, p)
		}
	}
	long := []rune(strings.Repeat("x", 100000))
	if prevBoundary(long, 99999) != 99998 || !isBoundary(long, 50000) {
		t.Error("ordinary text has a boundary everywhere")
	}
	// a word longer than maxWord is not a command or a path
	e := NewEditor(Options{})
	e.SetText("@" + strings.Repeat("w", 400))
	if _, ok := e.triggerWord(); ok {
		t.Error("a 400-rune word is not a trigger")
	}
}
