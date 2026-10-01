package widget

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

// The diff algorithm and the word-level highlight, without any rendering.

func repeatLines(n int, f func(i int) string) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(f(i))
		sb.WriteString("\n")
	}
	return sb.String()
}

func numberedLines(n int) string {
	return repeatLines(n, func(i int) string { return fmt.Sprintf("line %d", i+1) })
}

// applyScript rebuilds the "after" lines from a script and the two inputs, checking that the runs are consistent on the way.
func applyScript(t testing.TB, runs []dfRun, before, after []string) []string {
	t.Helper()
	var out []string
	ai, bi := 0, 0
	for _, r := range runs {
		if r.a != ai || r.b != bi {
			t.Fatalf("run %+v does not start where the last one ended (%d, %d)", r, ai, bi)
		}
		switch r.kind {
		case ' ':
			for k := 0; k < r.n; k++ {
				if before[ai+k] != after[bi+k] {
					t.Fatalf("run %+v keeps unequal lines %q and %q", r, before[ai+k], after[bi+k])
				}
				out = append(out, before[ai+k])
			}
			ai, bi = ai+r.n, bi+r.n
		case '-':
			ai += r.n
		case '+':
			out = append(out, after[bi:bi+r.n]...)
			bi += r.n
		default:
			t.Fatalf("run kind %q", r.kind)
		}
		if r.n < 1 {
			t.Fatalf("empty run %+v", r)
		}
	}
	if ai != len(before) || bi != len(after) {
		t.Fatalf("the script covers %d of %d and %d of %d lines", ai, len(before), bi, len(after))
	}
	return out
}

func diffLCSLen(a, b []string) int {
	prev := make([]int, len(b)+1)
	for i := range a {
		cur := make([]int, len(b)+1)
		for j := range b {
			switch {
			case a[i] == b[j]:
				cur[j+1] = prev[j] + 1
			case prev[j+1] >= cur[j]:
				cur[j+1] = prev[j+1]
			default:
				cur[j+1] = cur[j]
			}
		}
		prev = cur
	}
	return prev[len(b)]
}

func TestDiffScriptIsCorrectAndMinimal(t *testing.T) {
	g := widgettest.NewRand(61)
	alphabet := []string{"a", "b", "c", "d", "", "e"}
	gen := func(n, k int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = alphabet[g.Intn(k)]
		}
		return out
	}
	for i := 0; i < 4000; i++ {
		before, after := gen(g.Intn(14), 2+g.Intn(5)), gen(g.Intn(14), 2+g.Intn(5))
		runs := diffScript(before, after)
		if got := applyScript(t, runs, before, after); !reflect.DeepEqual(got, append([]string(nil), after...)) && !(len(got) == 0 && len(after) == 0) {
			t.Fatalf("the script does not produce after: %q -> %q gives %q (runs %+v)", before, after, got, runs)
		}
		edits, prevKind := 0, byte(0)
		for _, r := range runs {
			if r.kind != ' ' {
				edits += r.n
			}
			if r.kind == prevKind {
				t.Fatalf("two runs of one kind in a row: %+v", runs)
			}
			if r.kind == '-' && prevKind == '+' {
				t.Fatalf("removed lines come before the added lines of a change: %+v", runs)
			}
			prevKind = r.kind
		}
		if want := len(before) + len(after) - 2*diffLCSLen(before, after); edits != want {
			t.Fatalf("%q -> %q: %d edits, the minimum is %d (runs %+v)", before, after, edits, want, runs)
		}
	}
}

func TestDiffScriptFallsBackToACoarseHunk(t *testing.T) {
	// more edits than the search allows: the whole middle is replaced, and the script is still a valid one
	before := strings.Split(strings.TrimSuffix(repeatLines(700, func(i int) string { return fmt.Sprintf("old %d", i) }), "\n"), "\n")
	after := strings.Split(strings.TrimSuffix(repeatLines(700, func(i int) string { return fmt.Sprintf("new %d", i) }), "\n"), "\n")
	runs := diffScript(before, after)
	want := []dfRun{{'-', 0, 0, 700}, {'+', 700, 0, 700}}
	if !reflect.DeepEqual(runs, want) {
		t.Errorf("%+v", runs)
	}
	applyScript(t, runs, before, after)
	// a shared head and tail are still found
	b2 := append(append([]string{"head"}, before...), "tail")
	a2 := append(append([]string{"head"}, after...), "tail")
	runs = diffScript(b2, a2)
	if len(runs) != 4 || runs[0].kind != ' ' || runs[3].kind != ' ' || runs[0].n != 1 || runs[3].n != 1 {
		t.Errorf("%+v", runs)
	}
	applyScript(t, runs, b2, a2)
	// the moves of the search itself refuse when the distance is over the cap
	if _, ok := myersMoves([]int32{1, 2, 3, 4, 5}, []int32{6, 7, 8, 9, 10}, 4, 1000); ok {
		t.Error("ten edits do not fit in a cap of four")
	}
	if _, ok := myersMoves([]int32{1, 2, 3, 4, 5}, []int32{6, 7, 8, 9, 10}, 100, 3); ok {
		t.Error("a budget of three steps is not enough")
	}
	moves, ok := myersMoves([]int32{1, 2, 3}, []int32{1, 9, 3}, 10, 1000)
	if !ok || len(moves) != 4 || moves[0] != 'e' || moves[3] != 'e' || moves[1] == moves[2] {
		t.Errorf("one line replaced between two equal ones: %q %v", moves, ok)
	}
}

func TestDiffWorstCasesFinishQuickly(t *testing.T) {
	n := 30_000
	if widgetUnderRace() {
		n = 6_000
	}
	same := repeatLines(n, func(i int) string { return fmt.Sprintf("line %d", i) })
	cases := map[string][2]string{
		"identical":          {same, same},
		"one change":         {same, strings.Replace(same, "line 100\n", "changed\n", 1)},
		"every 50th":         {same, repeatLines(n, func(i int) string { return map[bool]string{true: "x", false: fmt.Sprintf("line %d", i)}[i%50 == 0] })},
		"nothing in common":  {same, repeatLines(n, func(i int) string { return fmt.Sprintf("other %d", i) })},
		"reversed":           {same, repeatLines(n, func(i int) string { return fmt.Sprintf("line %d", n-1-i) })},
		"shifted by one":     {same, "new first line\n" + same},
		"all lines equal":    {strings.Repeat("x\n", n), strings.Repeat("x\n", n-1) + "y\n"},
		"alternating":        {strings.Repeat("a\nb\n", n/2), strings.Repeat("b\na\n", n/2)},
		"one huge line":      {strings.Repeat("word ", 2*n), strings.Repeat("word ", 2*n) + "x"},
		"many tiny changes":  {strings.Repeat("a b c d e f g h\n", n/8), strings.Repeat("a b c d e f g x\n", n/8)},
		"empty against huge": {"", same},
	}
	for name, c := range cases {
		out := Diff("f", c[0], c[1], 80, DefaultTheme(), DiffOptions{})
		checkLines(t, name, out, 80)
		out = Diff("f", c[0], c[1], 80, MonoTheme(), DiffOptions{MaxLines: 50})
		if len(out) > 50 {
			t.Errorf("%s: %d lines with MaxLines 50", name, len(out))
		}
	}
}

func TestTokenize(t *testing.T) {
	toks, offs := diffTokenize("foo(a, b_2) + 日本語  x")
	want := []string{"foo", "(", "a", ",", " ", "b_2", ")", " ", "+", " ", "日本語", "  ", "x"}
	if !reflect.DeepEqual(toks, want) {
		t.Errorf("%q", toks)
	}
	for i, tk := range toks {
		if !strings.HasPrefix("foo(a, b_2) + 日本語  x"[offs[i]:], tk) {
			t.Errorf("token %d %q is not at offset %d", i, tk, offs[i])
		}
	}
	if toks, _ := diffTokenize(""); len(toks) != 0 {
		t.Error("empty")
	}
}

func diffMarksOf(s string, ms []diffMark) []string {
	var out []string
	for _, m := range ms {
		out = append(out, s[m.lo:m.hi])
	}
	return out
}

func TestWordMarks(t *testing.T) {
	cases := []struct {
		a, b   string
		ma, mb []string
		ok     bool
	}{
		{"foo(a, b)", "foo(a, c)", []string{"b"}, []string{"c"}, true},
		{"offset := page * size", "offset := (page - 1) * size", nil, []string{"(", "- 1) "}, true},
		{"let x = 1", "let y = 1", []string{"x"}, []string{"y"}, true},
		{"one two three four", "one 2 3 four", []string{"two three"}, []string{"2 3"}, true},
		{"same line", "same line", nil, nil, true},
		{"completely different words", "nothing shared at all", nil, nil, false},
		{"    indented", "    other", nil, nil, false},
		{"a", "", nil, nil, false},
		{"日本語 テスト", "日本語 テキスト", []string{"テスト"}, []string{"テキスト"}, true},
		{"return x + y", "return x - y", []string{"+"}, []string{"-"}, true},
	}
	for _, c := range cases {
		budget := 10_000
		ma, mb, ok := wordMarks(c.a, c.b, &budget)
		if ok != c.ok || !reflect.DeepEqual(diffMarksOf(c.a, ma), c.ma) || !reflect.DeepEqual(diffMarksOf(c.b, mb), c.mb) {
			t.Errorf("wordMarks(%q, %q) = %q %q %v, want %q %q %v", c.a, c.b, diffMarksOf(c.a, ma), diffMarksOf(c.b, mb), ok, c.ma, c.mb, c.ok)
		}
		for _, m := range append(ma, mb...) {
			if m.lo >= m.hi {
				t.Errorf("empty mark %+v", m)
			}
		}
	}
	budget := 3
	if _, _, ok := wordMarks("a b c d", "a b c e", &budget); ok {
		t.Error("an exhausted budget gives no highlight")
	}
	long := strings.Repeat("x ", 500)
	budget = 1 << 20
	if _, _, ok := wordMarks(long, long+"y", &budget); ok {
		t.Error("a line with too many tokens gets no highlight")
	}
}
