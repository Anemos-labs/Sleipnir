package fs

import (
	"strings"
	"testing"
	"time"
)

// indexSeq agrees with a naive search, including overlapping prefixes.
func TestIndexSeqMatchesNaiveSearch(t *testing.T) {
	hay := strings.Split("a a b a a a b a b x", " ")
	at := func(i int) string { return hay[i] }
	naive := func(seq []string, from int) int {
		for i := from; i+len(seq) <= len(hay); i++ {
			ok := true
			for j, s := range seq {
				if hay[i+j] != s {
					ok = false
					break
				}
			}
			if ok {
				return i
			}
		}
		return -1
	}
	for _, q := range []string{"a", "a b", "a a b", "a a a b", "a b a b", "b x", "x a", "a b x", "b a b x"} {
		seq := strings.Split(q, " ")
		for from := 0; from < len(hay); from++ {
			if got, want := indexSeq(len(hay), at, seq, from), naive(seq, from); got != want {
				t.Errorf("indexSeq(%q, %d) = %d, want %d", q, from, got, want)
			}
		}
	}
}

// Repetitive content and a hunk that differs only at its end are searched in linear time.
func TestFindSeqIsLinearOnRepetitiveContent(t *testing.T) {
	const fileLines, hunkLines = 200_000, 5_000
	lines := make([]docLine, fileLines)
	tr := make([]string, fileLines)
	for i := range lines {
		lines[i].text = "x"
		tr[i] = "x"
	}
	seq := make([]string, hunkLines)
	for i := range seq {
		seq[i] = "x"
	}
	seq[hunkLines-1] = "y"
	start := time.Now()
	if got := findSeq(lines, tr, seq, 0, false); got != -1 {
		t.Fatalf("findSeq = %d, want -1", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("findSeq took %v", d)
	}
}

// A chain of replace-all edits that would outgrow the file limit is refused before the text is built.
func TestReplaceAllGrowthIsBounded(t *testing.T) {
	text := strings.Repeat("a", 1<<20)
	specs := []editSpec{
		{old: "a", new: "bb", all: true},
		{old: "b", new: strings.Repeat("c", 32), all: true},
	}
	_, _, msg := applyEdits(text, specs, "f.txt")
	if !strings.Contains(msg, "over the") || !strings.Contains(msg, "edit 2 of 2") {
		t.Fatalf("msg = %q", msg)
	}
	if out, n, msg := applyEdits("x a x a", []editSpec{{old: "a", new: "b", all: true}}, "f.txt"); msg != "" || n != 2 || out != "x b x b" {
		t.Fatalf("got %q %d %q", out, n, msg)
	}
}
