package fs

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// applyOps replays an edit script against a and checks it yields b.
func applyOps(t *testing.T, a, b []string, ops []byte) {
	t.Helper()
	ai, bi := 0, 0
	for _, op := range ops {
		switch op {
		case '=':
			if ai >= len(a) || bi >= len(b) || a[ai] != b[bi] {
				t.Fatalf("bad '=' at a[%d] b[%d]", ai, bi)
			}
			ai++
			bi++
		case '-':
			if ai >= len(a) {
				t.Fatalf("delete past the end")
			}
			ai++
		case '+':
			if bi >= len(b) {
				t.Fatalf("insert past the end")
			}
			bi++
		}
	}
	if ai != len(a) || bi != len(b) {
		t.Fatalf("script consumed %d/%d and %d/%d", ai, len(a), bi, len(b))
	}
}

func TestMyersProducesValidMinimalScripts(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "b", "c", "d", "e"}
	for iter := 0; iter < 3000; iter++ {
		a := make([]string, rng.Intn(14))
		b := make([]string, rng.Intn(14))
		for i := range a {
			a[i] = alphabet[rng.Intn(len(alphabet))]
		}
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		ops, ok := myersOps(a, b, 0)
		if !ok {
			t.Fatalf("unlimited diff failed for %v -> %v", a, b)
		}
		applyOps(t, a, b, ops)
		// Minimal: edits == len(a)+len(b)-2*LCS.
		edits := 0
		for _, o := range ops {
			if o != '=' {
				edits++
			}
		}
		if want := len(a) + len(b) - 2*lcsLen(a, b); edits != want {
			t.Fatalf("%v -> %v: %d edits, minimal is %d", a, b, edits, want)
		}
	}
}

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	return dp[len(a)][len(b)]
}

func TestMyersEdgeCases(t *testing.T) {
	tests := []struct{ a, b []string }{
		{nil, nil},
		{nil, []string{"x"}},
		{[]string{"x"}, nil},
		{[]string{"x"}, []string{"x"}},
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{[]string{"a", "b", "c"}, []string{"c", "b", "a"}},
		{[]string{"a"}, []string{"b"}},
		{strings.Split(strings.Repeat("x,", 200), ","), strings.Split(strings.Repeat("y,", 200), ",")},
	}
	for _, tc := range tests {
		ops, ok := myersOps(tc.a, tc.b, 0)
		if !ok {
			t.Fatalf("failed on %v -> %v", tc.a, tc.b)
		}
		applyOps(t, tc.a, tc.b, ops)
	}
}

func TestMyersCapFallsBackToWholeReplacement(t *testing.T) {
	var a, b []string
	for i := 0; i < 3000; i++ {
		a = append(a, "a"+strings.Repeat("x", i%7)+string(rune('A'+i%26)))
		b = append(b, "b"+strings.Repeat("y", i%5)+string(rune('a'+i%26)))
	}
	if _, ok := myersOps(a, b, 200); ok {
		t.Fatalf("cap should have been exceeded")
	}
	// diffFiles must still yield a correct (if not minimal) diff.
	d := diffFiles(strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", 2, 0)
	if d.removed != len(a) || d.added != len(b) {
		t.Errorf("fallback counts: -%d +%d", d.removed, d.added)
	}
}

func TestDiffFilesFormat(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		want     string
		add, del int
	}{
		{"identical", "a\nb\n", "a\nb\n", "", 0, 0},
		{"single change", "1\n2\n3\n4\n5\n", "1\n2\nX\n4\n5\n", "@@ -1,5 +1,5 @@\n 1\n 2\n-3\n+X\n 4\n 5", 1, 1},
		{"change at start", "a\nb\nc\nd\n", "A\nb\nc\nd\n", "@@ -1,3 +1,3 @@\n-a\n+A\n b\n c", 1, 1},
		{"change at end", "a\nb\nc\nd\n", "a\nb\nc\nD\n", "@@ -2,3 +2,3 @@\n b\n c\n-d\n+D", 1, 1},
		{"pure insertion", "a\nb\n", "a\nnew\nb\n", "@@ -1,2 +1,3 @@\n a\n+new\n b", 1, 0},
		{"pure deletion", "a\ngone\nb\n", "a\nb\n", "@@ -1,3 +1,2 @@\n a\n-gone\n b", 0, 1},
		{"insert into empty", "", "x\ny\n", "@@ -0,0 +1,2 @@\n+x\n+y", 2, 0},
		{"delete everything", "x\ny\n", "", "@@ -1,2 +0,0 @@\n-x\n-y", 0, 2},
		{"two distant hunks", numbered(1, 20), replaceLines(numbered(1, 20), map[int]string{2: "TWO", 19: "NINETEEN"}),
			"@@ -1,4 +1,4 @@\n 1\n-2\n+TWO\n 3\n 4\n@@ -17,4 +17,4 @@\n 17\n 18\n-19\n+NINETEEN\n 20", 2, 2},
		{"close changes merge", numbered(1, 10), replaceLines(numbered(1, 10), map[int]string{3: "THREE", 6: "SIX"}),
			"@@ -1,8 +1,8 @@\n 1\n 2\n-3\n+THREE\n 4\n 5\n-6\n+SIX\n 7\n 8", 2, 2},
		{"crlf ignored in comparison", "a\r\nb\r\n", "a\r\nB\r\n", "@@ -1,2 +1,2 @@\n a\n-b\n+B", 1, 1},
		{"no final newline", "a\nb", "a\nc", "@@ -1,2 +1,2 @@\n a\n-b\n+c", 1, 1},
		{"invalid utf8 shown safely", "a\xff\n", "b\n", "@@ -1 +1 @@\n-a�\n+b", 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := diffFiles(tc.a, tc.b, 2, 0)
			if got := d.text(0); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			if d.added != tc.add || d.removed != tc.del {
				t.Errorf("counts +%d -%d, want +%d -%d", d.added, d.removed, tc.add, tc.del)
			}
		})
	}
}

func numbered(from, to int) string {
	var sb strings.Builder
	for i := from; i <= to; i++ {
		sb.WriteString(itoa(i))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func replaceLines(s string, repl map[int]string) string {
	ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for n, r := range repl {
		ls[n-1] = r
	}
	return strings.Join(ls, "\n") + "\n"
}

func TestDiffTextTruncates(t *testing.T) {
	d := diffFiles(numbered(1, 100), strings.ReplaceAll(numbered(1, 100), "\n", "x\n"), 2, 0)
	txt := d.text(10)
	if got := strings.Count(txt, "\n"); got != 10 {
		t.Errorf("lines = %d", got+1)
	}
	if !strings.Contains(txt, "diff truncated: 191 more lines") {
		t.Errorf("no truncation marker: %s", txt)
	}
	if long := diffText(strings.Repeat("x", 500)); len(long) > 210 {
		t.Errorf("long line not clipped: %d", len(long))
	}
}

func TestDiffRandomRoundTrip(t *testing.T) {
	// Reconstruct b from a and the hunks to prove the rendered diff is complete.
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 500; iter++ {
		a := make([]string, rng.Intn(40))
		for i := range a {
			a[i] = "line" + itoa(rng.Intn(15))
		}
		b := append([]string(nil), a...)
		for k := rng.Intn(6); k > 0 && len(b) > 0; k-- {
			switch rng.Intn(3) {
			case 0:
				b[rng.Intn(len(b))] = "new" + itoa(rng.Intn(100))
			case 1:
				i := rng.Intn(len(b))
				b = append(b[:i], b[i+1:]...)
			default:
				i := rng.Intn(len(b) + 1)
				b = append(b[:i], append([]string{"ins" + itoa(rng.Intn(100))}, b[i:]...)...)
			}
		}
		as, bs := strings.Join(a, "\n"), strings.Join(b, "\n")
		if as != "" {
			as += "\n"
		}
		if bs != "" {
			bs += "\n"
		}
		d := diffFiles(as, bs, 2, 0)
		got := replayHunks(t, a, d.lines)
		if strings.Join(got, "\n") != strings.Join(b, "\n") {
			t.Fatalf("iteration %d: replay mismatch\na=%v\nb=%v\ngot=%v\ndiff=%v", iter, a, b, got, d.lines)
		}
	}
}

// replayHunks applies unified hunks (as produced by diffFiles) to a.
func replayHunks(t *testing.T, a []string, hunk []string) []string {
	t.Helper()
	var out []string
	pos := 0
	for _, l := range hunk {
		if strings.HasPrefix(l, "@@") {
			var as, ac, bs, bc int
			rest := strings.TrimPrefix(l, "@@ -")
			rest = strings.TrimSuffix(rest, " @@")
			parts := strings.Split(rest, " +")
			as, ac = parseRange(parts[0])
			bs, bc = parseRange(parts[1])
			_, _ = bs, bc
			start := as - 1
			if ac == 0 {
				start = as
			}
			for pos < start {
				out = append(out, a[pos])
				pos++
			}
			continue
		}
		switch l[0] {
		case ' ':
			if a[pos] != l[1:] {
				t.Fatalf("context mismatch %q vs %q", a[pos], l[1:])
			}
			out = append(out, a[pos])
			pos++
		case '-':
			if a[pos] != l[1:] {
				t.Fatalf("delete mismatch %q vs %q", a[pos], l[1:])
			}
			pos++
		case '+':
			out = append(out, l[1:])
		}
	}
	out = append(out, a[pos:]...)
	return out
}

func parseRange(s string) (start, count int) {
	parts := strings.Split(s, ",")
	start = atoi(parts[0])
	count = 1
	if len(parts) == 2 {
		count = atoi(parts[1])
	}
	return
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestDiffCapsMaterialisedLinesButCountsAll(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	// A coarse diff of a big file must not build millions of strings.
	var a, b strings.Builder
	for i := 0; i < 200_000; i++ {
		a.WriteString("old line " + itoa(i) + "\n")
		b.WriteString("new line " + itoa(i) + "\n")
	}
	d := diffFiles(a.String(), b.String(), 2, 50)
	if len(d.lines) != 50 {
		t.Errorf("materialised %d lines, want 50", len(d.lines))
	}
	if d.total != 1+200_000+200_000 || d.added != 200_000 || d.removed != 200_000 {
		t.Errorf("total=%d added=%d removed=%d", d.total, d.added, d.removed)
	}
	txt := d.text(10)
	if !strings.Contains(txt, "diff truncated: 399991 more lines") {
		t.Errorf("truncation note: %s", txt[len(txt)-60:])
	}
}

func TestDiffOfHugeScatteredChangesIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	var a, b strings.Builder
	for i := 0; i < 300_000; i++ {
		a.WriteString("line " + itoa(i) + "\n")
		if i%50 == 0 {
			b.WriteString("CHANGED " + itoa(i) + "\n")
		} else {
			b.WriteString("line " + itoa(i) + "\n")
		}
	}
	start := time.Now()
	d := diffFiles(a.String(), b.String(), 2, 400)
	if el := time.Since(start); el > 30*time.Second {
		t.Errorf("diff took %v", el)
	}
	if d.total == 0 || d.removed < 6000 || d.added < 6000 {
		t.Errorf("counts: total=%d -%d +%d", d.total, d.removed, d.added)
	}
	if len(d.lines) > 400 {
		t.Errorf("materialised %d lines", len(d.lines))
	}
}
