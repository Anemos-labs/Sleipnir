package wsvc

import (
	"slices"
	"strings"
	"testing"
)

func TestMatchesOfFindsEveryPlaceOnce(t *testing.T) {
	lines := strings.SplitAfter("a\nb\na\nb\na\nb\nc\n", "\n")
	if got := matchesOf(lines, []string{"a", "b", "a"}); !slices.Equal(got, []int{0, 2}) {
		t.Errorf("overlapping matches: %v", got)
	}
	if got := matchesOf(lines, []string{"b", "c"}); !slices.Equal(got, []int{5}) {
		t.Errorf("a match at the end: %v", got)
	}
	if got := matchesOf(lines, []string{"z"}); got != nil {
		t.Errorf("no match: %v", got)
	}
	// Agrees with the direct scan on repetitive text.
	rep := strings.SplitAfter(strings.Repeat("x\n", 300)+"y\n", "\n")
	want := append(slices.Repeat([]string{"x"}, 40), "y")
	var direct []int
	for i := 0; i+len(want) <= len(rep); i++ {
		if shownAt(rep, want, i) {
			direct = append(direct, i)
		}
	}
	if got := matchesOf(rep, want); !slices.Equal(got, direct) {
		t.Errorf("KMP %v, direct %v", got, direct)
	}
}

func TestSectionsFindTheNearestHeaderAbove(t *testing.T) {
	sec := sections([]byte("func a() {\n\tx\n\ty\nfunc b() {\n\tz\n"))
	for start, want := range map[int]string{1: "", 2: "func a() {", 4: "func a() {", 5: "func b() {", 99: "func b() {"} {
		if got := sec(start); got != want {
			t.Errorf("section(%d) = %q, want %q", start, got, want)
		}
	}
}
