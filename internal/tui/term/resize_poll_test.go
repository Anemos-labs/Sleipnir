package term

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// A platform without a resize signal is told of a window that grows or shrinks by looking at its size.
func TestWatchBySizeSendsTheNewSizeWhenItChanges(t *testing.T) {
	var mu sync.Mutex
	cur, fail := Size{Width: 80, Height: 24}, false
	read := func() (Size, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return Size{}, errors.New("not a terminal")
		}
		return cur, nil
	}
	set := func(s Size) { mu.Lock(); cur = s; mu.Unlock() }
	sizes, stop := watchBySize(read, 5*time.Millisecond)
	defer stop()

	select {
	case s := <-sizes:
		t.Fatalf("the size the program started with is not news: %+v", s)
	case <-time.After(40 * time.Millisecond):
	}
	set(Size{Width: 120, Height: 40}) // the window grows
	if s := wait(t, sizes); s != (Size{Width: 120, Height: 40}) {
		t.Errorf("got %+v after the window grew", s)
	}
	set(Size{Width: 50, Height: 20}) // and shrinks
	if s := wait(t, sizes); s != (Size{Width: 50, Height: 20}) {
		t.Errorf("got %+v after the window shrank", s)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	select {
	case s := <-sizes:
		t.Errorf("a size that cannot be read sent %+v", s)
	case <-time.After(40 * time.Millisecond):
	}
	stop()
	stop() // idempotent
	if _, ok := <-sizes; ok {
		t.Error("stop closes the channel")
	}
}

func wait(t *testing.T, c <-chan Size) Size {
	t.Helper()
	select {
	case s := <-c:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("no size arrived")
		return Size{}
	}
}
