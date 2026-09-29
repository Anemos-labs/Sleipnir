package events

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
)

func TestLogAppendResumeAndTornTail(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := l.Emit("a1", "x", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash mid-write: append a torn line.
	p := filepath.Join(dir, "events.jsonl")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"seq":99,"ts":"2026-01-01T00:00:00Z","ses`)
	f.Close()

	l2, err := Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := l2.Emit("a1", "y", nil)
	if err != nil {
		t.Fatal(err)
	}
	// log.open + 5 events = 6; the torn line must not have consumed a seq.
	if seq != 7 {
		t.Fatalf("resumed seq = %d, want 7", seq)
	}
	l2.Close()

	var n int
	var last uint64
	err = Scan(p, func(e Event) error {
		n++
		if e.Seq != last+1 {
			t.Fatalf("gap: %d after %d", e.Seq, last)
		}
		last = e.Seq
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("scanned %d events, want 7", n)
	}
}

func TestLogConcurrentEmitKeepsOrder(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := l.Emit("a", "e", i); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	var last uint64
	if err := Scan(filepath.Join(l.Dir(), "events.jsonl"), func(e Event) error {
		if e.Seq != last+1 {
			t.Fatalf("out of order: %d after %d", e.Seq, last)
		}
		last = e.Seq
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if last != 16*200+1 {
		t.Fatalf("last seq %d", last)
	}
}

func TestSubscribeDropsInsteadOfBlocking(t *testing.T) {
	l, _ := Open(t.TempDir(), "s")
	defer l.Close()
	ch, cancel := l.Subscribe(2)
	defer cancel()
	for i := 0; i < 50; i++ {
		l.Emit("", "e", i) // must never block even though nobody reads
	}
	if len(ch) != 2 {
		t.Fatalf("buffered %d, want 2", len(ch))
	}
}

func TestBlobsDedupe(t *testing.T) {
	b, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := b.Put([]byte("hello"))
	h2, _ := b.Put([]byte("hello"))
	if h1 != h2 || h1 != core.HashString("hello") {
		t.Fatal("hash mismatch")
	}
	got, err := b.Get(h1)
	if err != nil || string(got) != "hello" {
		t.Fatalf("get: %q %v", got, err)
	}
	if _, err := b.Get("deadbeefdeadbeef"); err == nil {
		t.Fatal("expected not found")
	}
}
