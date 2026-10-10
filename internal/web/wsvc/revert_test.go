package wsvc

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
)

// Property: reverting every hunk of a diff, one at a time from the last, turns the new
// text back into the old one; reverting one hunk changes only its lines.
func TestReverseHunkProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for trial := range 300 {
		var old []string
		for i := range 10 + rng.Intn(40) {
			old = append(old, fmt.Sprintf("l%d-%d\n", trial, i))
		}
		cur := append([]string(nil), old...)
		for range 1 + rng.Intn(5) {
			at := rng.Intn(len(cur))
			switch rng.Intn(3) {
			case 0:
				cur = append(cur[:at], append([]string{fmt.Sprintf("new %d\n", rng.Intn(1e6))}, cur[at:]...)...)
			case 1:
				if len(cur) > 1 {
					cur = append(cur[:at], cur[at+1:]...)
				}
			default:
				cur[at] = fmt.Sprintf("changed %d\n", rng.Intn(1e6))
			}
		}
		a, b := []byte(strings.Join(old, "")), []byte(strings.Join(cur, ""))
		d := checkpoint.DiffLines(a, b)
		live := b
		for i := len(d.Hunks) - 1; i >= 0; i-- {
			next, err := reverseHunk(a, live, d.Hunks[i], false)
			if err != nil {
				t.Fatalf("trial %d: hunk %d does not reverse:\n%+v", trial, i, d.Hunks[i])
			}
			live = next
		}
		if string(live) != string(a) {
			t.Fatalf("trial %d: reverting every hunk gave\n%q\nwant\n%q", trial, live, a)
		}
		if len(d.Hunks) > 1 {
			one, err := reverseHunk(a, b, d.Hunks[0], false)
			rest := checkpoint.DiffLines(a, one)
			if err != nil || len(rest.Hunks) != len(d.Hunks)-1 {
				t.Fatalf("trial %d: one hunk reverted leaves %d of %d hunks", trial, len(rest.Hunks), len(d.Hunks))
			}
		}
	}
}

func TestReverseHunkFindsMovedLinesAndRefusesAmbiguity(t *testing.T) {
	old := []byte("a\nb\nc\nd\ne\nf\ng\n")
	new := []byte("a\nb\nc\nD\ne\nf\ng\n")
	d := checkpoint.DiffLines(old, new)
	// The live file has two lines more at the top: the hunk is found further down.
	live := []byte("x\ny\na\nb\nc\nD\ne\nf\ng\n")
	got, err := reverseHunk(old, live, d.Hunks[0], false)
	if err != nil || string(got) != "x\ny\na\nb\nc\nd\ne\nf\ng\n" {
		t.Fatalf("moved: %q %v", got, err)
	}
	// The hunk's lines are gone: refused.
	if _, err := reverseHunk(old, []byte("something else\n"), d.Hunks[0], false); err == nil {
		t.Fatal("a hunk whose lines are not in the file must be refused")
	}
	// Twice, equally far from where the hunk was: refused rather than guessed.
	var head strings.Builder
	for i := range 20 {
		fmt.Fprintf(&head, "h%d\n", i)
	}
	old2 := []byte(head.String() + "1\n2\n3\na\n4\n5\n6\n")
	new2 := []byte(head.String() + "1\n2\n3\nb\n4\n5\n6\n")
	d2 := checkpoint.DiffLines(old2, new2)
	h := d2.Hunks[0]
	block := "1\n2\n3\nb\n4\n5\n6\n"
	pad := func(n int) string { return strings.Repeat("z\n", n) }
	nStart := h.NewStart - 1
	live2 := []byte(pad(nStart-8) + block + pad(9) + block)
	if _, err := reverseHunk(old2, live2, h, false); err == nil {
		t.Fatal("an ambiguous place must be refused")
	}
	// One copy nearer than the other: that one.
	live3 := []byte(pad(nStart-8) + block + pad(12) + block)
	got3, err := reverseHunk(old2, live3, h, false)
	if err != nil || !strings.HasPrefix(string(got3), pad(nStart-8)+"1\n2\n3\na\n") {
		t.Fatalf("nearest copy: %v %q", err, got3)
	}
}
