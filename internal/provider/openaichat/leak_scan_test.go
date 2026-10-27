package openaichat

import (
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// The search for a bare </think> reads each byte of the answer about once: an answer of many one-byte fragments and no
// marker takes time in proportion to its length, and a marker split across fragments is still found.
func TestTheThinkingMarkerSearchIsIncremental(t *testing.T) {
	a := newAccumulator()
	start := time.Now()
	for i := 0; i < 300_000; i++ {
		c := "x"
		if err := a.applyDelta(&delta{Content: &c}, func(provider.Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	for _, c := range []string{"</", "thi", "nk", ">", "answer"} {
		c := c
		if err := a.applyDelta(&delta{Content: &c}, func(provider.Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	if a.text.String() != "answer" || a.reasoning.Len() != 300_000 || !strings.HasPrefix(a.reasoning.String(), "xxx") {
		t.Fatalf("text %q, reasoning %d bytes", a.text.String(), a.reasoning.Len())
	}
}
