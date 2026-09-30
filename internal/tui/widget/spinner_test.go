package widget

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

func TestSpinnerFrames(t *testing.T) {
	seen := map[string]bool{}
	for f := 0; f < 10; f++ {
		s := Spinner(f)
		if cell.StringWidth(s) != 1 || seen[s] {
			t.Errorf("frame %d: %q is not a distinct one-cell glyph", f, s)
		}
		seen[s] = true
		if r := []rune(s)[0]; r < 0x2800 || r > 0x28ff {
			t.Errorf("frame %d: %q is not braille", f, s)
		}
		if Spinner(f) != Spinner(f+10) || Spinner(f) != Spinner(f+10000) || Spinner(f) != Spinner(f-10) {
			t.Errorf("frame %d does not repeat every ten", f)
		}
	}
	for _, f := range []int{-1, -9, -10, -11, math.MinInt, math.MaxInt} {
		if s := Spinner(f); cell.StringWidth(s) != 1 {
			t.Errorf("Spinner(%d) = %q", f, s)
		}
	}
	if Spinner(-1) != Spinner(9) {
		t.Error("a negative frame counts backwards")
	}
}

func TestSpinnerASCII(t *testing.T) {
	want := []string{"|", "/", "-", "\\"}
	for f, w := range want {
		if got := SpinnerASCII(f); got != w || SpinnerASCII(f+4) != w || SpinnerASCII(f-4) != w {
			t.Errorf("SpinnerASCII(%d) = %q, want %q", f, got, w)
		}
	}
	for _, f := range []int{math.MinInt, math.MaxInt, -3} {
		if s := SpinnerASCII(f); len(s) != 1 {
			t.Errorf("SpinnerASCII(%d) = %q", f, s)
		}
	}
}

func TestVerbsAreCalmParticiples(t *testing.T) {
	if len(Verbs) < 8 {
		t.Fatalf("only %d verbs", len(Verbs))
	}
	seen := map[string]bool{}
	for _, v := range Verbs {
		if !strings.HasSuffix(v, "ing") || strings.ContainsAny(v, " \t") || v[0] < 'A' || v[0] > 'Z' || seen[v] {
			t.Errorf("verb %q: want one capitalised present participle, once", v)
		}
		seen[v] = true
	}
	for _, need := range []string{"Thinking", "Reading", "Weighing", "Tracing", "Checking"} {
		if !seen[need] {
			t.Errorf("missing %q", need)
		}
	}
}

func TestVerbIsDeterministicAndSlow(t *testing.T) {
	for _, seed := range []int{0, 1, 42, -7, math.MaxInt, math.MinInt} {
		for slot := -3; slot < 40; slot++ {
			first := Verb(seed, slot*VerbFrames)
			for k := 0; k < VerbFrames; k++ {
				if got := Verb(seed, slot*VerbFrames+k); got != first {
					t.Fatalf("seed %d: the verb changed inside slot %d at frame %d: %q then %q", seed, slot, k, first, got)
				}
			}
			if next := Verb(seed, (slot+1)*VerbFrames); next == first {
				t.Fatalf("seed %d: the same verb %q twice in a row at slot %d", seed, first, slot)
			}
		}
		if Verb(seed, 123) != Verb(seed, 123) {
			t.Error("not deterministic")
		}
	}
}

func TestVerbTourVisitsEveryVerbOnce(t *testing.T) {
	for seed := -5; seed < 30; seed++ {
		seen := map[string]int{}
		for slot := 0; slot < len(verbList); slot++ {
			seen[Verb(seed, slot*VerbFrames)]++
		}
		if len(seen) != len(verbList) {
			t.Fatalf("seed %d: a tour of %d verbs shows only %d different ones: %v", seed, len(verbList), len(seen), seen)
		}
	}
}

func TestVerbSeedsDiffer(t *testing.T) {
	orders := map[string]bool{}
	for seed := 0; seed < 50; seed++ {
		var sb strings.Builder
		for slot := 0; slot < 4; slot++ {
			sb.WriteString(Verb(seed, slot*VerbFrames) + ",")
		}
		orders[sb.String()] = true
	}
	if len(orders) < 10 {
		t.Errorf("50 seeds give only %d different openings: two agents would flicker in step", len(orders))
	}
}

func TestVerbsIsACopy(t *testing.T) {
	before := Verb(3, 100)
	saved := Verbs[0]
	Verbs[0] = "Mutated"
	defer func() { Verbs[0] = saved }()
	if Verb(3, 100) != before {
		t.Error("changing Verbs must not change Verb")
	}
}

// The verb a seed starts with and the order it walks in are pinned, so that changing them is a decision (two agents with
// different seeds must keep showing different verbs at the same time).
func TestVerbGolden(t *testing.T) {
	var sb strings.Builder
	for seed := 0; seed < 6; seed++ {
		fmt.Fprintf(&sb, "seed %d:", seed)
		for slot := 0; slot < len(verbList); slot++ {
			sb.WriteString(" " + Verb(seed, slot*VerbFrames))
		}
		sb.WriteString("\n")
	}
	checkGolden(t, "verbs", sb.String())
}
