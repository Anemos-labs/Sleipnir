package app

import "testing"

// A failed row says why it failed in the words of the web board's failed card.
func TestClosureWordsMatchTheWebCard(t *testing.T) {
	for _, c := range []struct {
		closure  string
		attempts int
		want     string
	}{
		{"verifier", 3, "verification kept failing"},
		{"exhausted", 4, "out of attempts (4)"},
		{"exhausted", 1, "out of attempts"},
		{"denied", 0, "the manager refused the work"},
		{"superseded(T3)", 0, "replaced by T3"},
		{"blocked_on(T9)", 0, "blocked on T9"},
		{"canceled", 0, "canceled"},
		{"", 0, "failed"},
		{"odd", 0, "failed (odd)"},
	} {
		if got := closureWords(c.closure, c.attempts); got != c.want {
			t.Errorf("closureWords(%q, %d) = %q, want %q", c.closure, c.attempts, got, c.want)
		}
	}
}
