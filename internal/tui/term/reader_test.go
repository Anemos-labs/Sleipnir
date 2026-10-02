//go:build linux

package term

import (
	"errors"
	"os"
	"testing"
	"time"
)

// A reader that is cancelled while it waits for a key leaves, and takes nothing after it: the next program on the terminal reads every key.
// (A plain read of the terminal that was left waiting took the first line typed for the next one, a key at the prompt of /login included.)
func TestACancelledReaderLeavesAndTakesNothingFromTheNextProgram(t *testing.T) {
	m, s := openPTY(t)
	restore, err := MakeRaw(s) // keys arrive as they are typed, with no line to wait for
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	r := NewReader(s)
	left := make(chan error, 1)
	go func() { _, err := r.Read(make([]byte, 16)); left <- err }()

	r.Cancel() // the read is waiting (or is about to): either way it must end
	select {
	case err := <-left:
		if !errors.Is(err, os.ErrClosed) {
			t.Errorf("a cancelled read says %v, want os.ErrClosed", err)
		}
	case <-time.After(hangGuard):
		t.Fatal("a cancelled read did not return")
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("a read after Cancel says %v, want os.ErrClosed", err)
	}
	r.Cancel() // twice is the same as once

	// what is typed now is for whoever reads next
	if _, err := m.Write([]byte("k")); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 1)
		n, _ := s.Read(b)
		got <- string(b[:n])
	}()
	select {
	case k := <-got:
		if k != "k" {
			t.Errorf("the next program read %q, want \"k\"", k)
		}
	case <-time.After(hangGuard):
		t.Fatal("the key typed after Cancel was taken by someone: the next program waited for it")
	}
}

// Until it is cancelled a reader gives what is typed, whole.
func TestAReaderGivesWhatIsTypedUntilItIsCancelled(t *testing.T) {
	m, s := openPTY(t)
	restore, err := MakeRaw(s)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	r := NewReader(s)
	defer r.Cancel()
	if _, err := m.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	type result struct {
		s   string
		err error
	}
	got := make(chan result, 1)
	go func() {
		b := make([]byte, 8)
		n, err := r.Read(b)
		got <- result{string(b[:n]), err}
	}()
	select {
	case g := <-got:
		if g.err != nil || g.s != "ab" {
			t.Errorf("read %q, %v; want \"ab\"", g.s, g.err)
		}
	case <-time.After(hangGuard):
		t.Fatal("the reader did not give what was typed")
	}
}
