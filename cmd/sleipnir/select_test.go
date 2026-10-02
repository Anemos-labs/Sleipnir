package main

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func selectWith(t *testing.T, keys string, labels []string, searchable bool) (int, error) {
	t.Helper()
	rawMode = func() (func(), error) { return func() {}, nil }
	return selectRows(bufio.NewReader(strings.NewReader(keys)), io.Discard, "pick", labels, labels, searchable, 3)
}

// The menu is driven by the arrow keys; a digit jumps in a short list, typing narrows a long one, Esc or Ctrl-C backs out.
func TestSelectRowsArrowsDigitsSearchAndCancel(t *testing.T) {
	labels := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	if i, err := selectWith(t, "\x1b[B\x1b[B\r", labels, false); err != nil || i != 2 {
		t.Errorf("two downs: %d %v", i, err)
	}
	if i, err := selectWith(t, "\x1b[B\x1b[A\x1b[A\r", labels, false); err != nil || i != 0 {
		t.Errorf("up stops at the top: %d %v", i, err)
	}
	if i, err := selectWith(t, "4\r", labels, false); err != nil || i != 3 {
		t.Errorf("a digit jumps: %d %v", i, err)
	}
	if i, err := selectWith(t, "eps\r", labels, true); err != nil || i != 4 {
		t.Errorf("search: %d %v", i, err)
	}
	if i, err := selectWith(t, "a\x1b[B\r", labels, true); err != nil || i != 1 {
		t.Errorf("search then arrow (alpha, beta, gamma, delta match 'a'): %d %v", i, err)
	}
	if _, err := selectWith(t, "\x03", labels, false); err == nil {
		t.Error("ctrl-c backs out")
	}
	if _, err := selectWith(t, "zzz\r", labels, true); err == nil {
		t.Error("enter on nothing chooses nothing (the input ends: cancelled)")
	}
}
