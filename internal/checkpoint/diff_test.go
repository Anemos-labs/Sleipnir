package checkpoint

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// applyUnified applies a unified diff produced by unifiedDiff to old. It is a
// deliberately small, strict implementation used to prove the generated hunks
// are well-formed: wrong ranges or wrong context make it fail.
func applyUnified(t *testing.T, old, diff string) string {
	t.Helper()
	if diff == "" {
		return old
	}
	oldLines := splitLines(old)
	lines := strings.SplitAfter(diff, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "--- ") || !strings.HasPrefix(lines[1], "+++ ") {
		t.Fatalf("bad diff header:\n%s", diff)
	}
	var out []string
	pos := 0 // next unconsumed old line
	i := 2
	for i < len(lines) {
		hdr := lines[i]
		if hdr == "" {
			break
		}
		if !strings.HasPrefix(hdr, "@@ -") {
			t.Fatalf("expected hunk header, got %q", hdr)
		}
		rest := strings.TrimPrefix(hdr, "@@ -")
		aPart, rest, _ := strings.Cut(rest, " +")
		bPart, _, _ := strings.Cut(rest, " @@")
		aStart, aCount := parseRange(t, aPart)
		bStart, bCount := parseRange(t, bPart)
		// 0-based index of the first line of each side's range; an empty range
		// is written "N,0" where N is the line before it.
		aFirst, bFirst := aStart-1, bStart-1
		if aCount == 0 {
			aFirst = aStart
		}
		if bCount == 0 {
			bFirst = bStart
		}
		// copy untouched lines before the hunk
		for pos < aFirst {
			out = append(out, oldLines[pos])
			pos++
		}
		if len(out) != bFirst {
			t.Fatalf("hunk %q: new-side start says line %d but %d lines precede it", strings.TrimSpace(hdr), bFirst+1, len(out))
		}
		i++
		gotA, gotB := 0, 0
		for i < len(lines) && lines[i] != "" && !strings.HasPrefix(lines[i], "@@ -") {
			l := lines[i]
			i++
			if strings.HasPrefix(l, "\\ No newline") {
				continue // handled when the line it qualifies was read
			}
			body := l[1:]
			// A following "\ No newline" marker means this line has no terminator.
			noNL := i < len(lines) && strings.HasPrefix(lines[i], "\\ No newline")
			if noNL {
				body = strings.TrimSuffix(body, "\n")
			}
			switch l[0] {
			case ' ':
				if pos >= len(oldLines) || oldLines[pos] != body {
					t.Fatalf("context mismatch at old line %d: want %q got %q", pos+1, body, safeAt(oldLines, pos))
				}
				out = append(out, body)
				pos++
				gotA++
				gotB++
			case '-':
				if pos >= len(oldLines) || oldLines[pos] != body {
					t.Fatalf("removal mismatch at old line %d: want %q got %q", pos+1, body, safeAt(oldLines, pos))
				}
				pos++
				gotA++
			case '+':
				out = append(out, body)
				gotB++
			default:
				t.Fatalf("bad body line %q", l)
			}
		}
		if gotA != aCount || gotB != bCount {
			t.Fatalf("hunk %q counts: old %d/%d new %d/%d", strings.TrimSpace(hdr), gotA, aCount, gotB, bCount)
		}
	}
	for pos < len(oldLines) {
		out = append(out, oldLines[pos])
		pos++
	}
	return strings.Join(out, "")
}

func safeAt(l []string, i int) string {
	if i < len(l) {
		return l[i]
	}
	return "<eof>"
}

func parseRange(t *testing.T, s string) (start, count int) {
	t.Helper()
	a, b, has := strings.Cut(s, ",")
	start, err := strconv.Atoi(a)
	if err != nil {
		t.Fatalf("bad range %q", s)
	}
	count = 1
	if has {
		if count, err = strconv.Atoi(b); err != nil {
			t.Fatalf("bad range %q", s)
		}
	}
	return start, count
}

func TestUnifiedDiffExact(t *testing.T) {
	tests := []struct {
		name       string
		old, new   string
		want       string
		add, remov int
	}{
		{name: "identical", old: "a\nb\n", new: "a\nb\n", want: ""},
		{name: "both empty"},
		{
			name: "add to empty", old: "", new: "x\ny\n",
			want: "--- A\n+++ B\n@@ -0,0 +1,2 @@\n+x\n+y\n", add: 2,
		},
		{
			name: "delete all", old: "x\ny\n", new: "",
			want: "--- A\n+++ B\n@@ -1,2 +0,0 @@\n-x\n-y\n", remov: 2,
		},
		{
			name: "replace middle line", old: "a\nb\nc\n", new: "a\nB\nc\n",
			want: "--- A\n+++ B\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n", add: 1, remov: 1,
		},
		{
			name: "no newline at end changes", old: "a\nb", new: "a\nb\n",
			want: "--- A\n+++ B\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+b\n", add: 1, remov: 1,
		},
		{
			name: "crlf is a change", old: "a\r\nb\r\n", new: "a\nb\n",
			want: "--- A\n+++ B\n@@ -1,2 +1,2 @@\n-a\r\n-b\r\n+a\n+b\n", add: 2, remov: 2,
		},
		{
			name: "single line range has no count", old: "only\n", new: "one\n",
			want: "--- A\n+++ B\n@@ -1 +1 @@\n-only\n+one\n", add: 1, remov: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, add, rem := unifiedDiff("A", "B", tc.old, tc.new)
			if got != tc.want {
				t.Fatalf("diff mismatch\n got: %q\nwant: %q", got, tc.want)
			}
			if add != tc.add || rem != tc.remov {
				t.Fatalf("counts = +%d -%d, want +%d -%d", add, rem, tc.add, tc.remov)
			}
			if applied := applyUnified(t, tc.old, got); applied != tc.new {
				t.Fatalf("applying the diff gave %q, want %q", applied, tc.new)
			}
		})
	}
}

func TestUnifiedDiffMergesNearbyHunksAndSplitsFarOnes(t *testing.T) {
	var old []string
	for i := 1; i <= 40; i++ {
		old = append(old, fmt.Sprintf("line %d\n", i))
	}
	edit := func(idx ...int) string {
		cp := append([]string(nil), old...)
		for _, i := range idx {
			cp[i-1] = fmt.Sprintf("CHANGED %d\n", i)
		}
		return strings.Join(cp, "")
	}
	oldText := strings.Join(old, "")

	near, _, _ := unifiedDiff("A", "B", oldText, edit(10, 15)) // 4 unchanged lines between: one hunk
	if n := strings.Count(near, "@@ -"); n != 1 {
		t.Fatalf("near edits produced %d hunks, want 1:\n%s", n, near)
	}
	far, _, _ := unifiedDiff("A", "B", oldText, edit(5, 30)) // far apart: two hunks
	if n := strings.Count(far, "@@ -"); n != 2 {
		t.Fatalf("far edits produced %d hunks, want 2:\n%s", n, far)
	}
	for _, d := range []string{near, far} {
		got := applyUnified(t, oldText, d)
		if got != edit(10, 15) && got != edit(5, 30) {
			t.Fatalf("applied diff is neither expected text:\n%s", d)
		}
	}
}

func TestMyersScriptIsConsistent(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a\n", "b\n", "c\n", "d\n", "e\n", "f\n", "\n", "x"}
	gen := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return out
	}
	for iter := 0; iter < 400; iter++ {
		a, b := gen(rng.Intn(40)), gen(rng.Intn(40))
		// Mutate b from a sometimes so the scripts are mostly 'e'.
		if iter%2 == 0 && len(a) > 0 {
			b = append([]string(nil), a...)
			for k := 0; k < rng.Intn(6); k++ {
				switch rng.Intn(3) {
				case 0:
					if len(b) > 0 {
						b = append(b[:rng.Intn(len(b))], b[rng.Intn(len(b)):]...)
					}
				case 1:
					i := rng.Intn(len(b) + 1)
					b = append(b[:i], append([]string{alphabet[rng.Intn(len(alphabet))]}, b[i:]...)...)
				default:
					if len(b) > 0 {
						b[rng.Intn(len(b))] = alphabet[rng.Intn(len(alphabet))]
					}
				}
			}
		}
		script := editScript(a, b)
		var gotA, gotB []string
		ai, bi := 0, 0
		for _, op := range script {
			switch op {
			case 'e':
				if a[ai] != b[bi] {
					t.Fatalf("iter %d: 'e' pairs unequal lines %q %q", iter, a[ai], b[bi])
				}
				gotA, gotB = append(gotA, a[ai]), append(gotB, b[bi])
				ai++
				bi++
			case 'd':
				gotA = append(gotA, a[ai])
				ai++
			case 'i':
				gotB = append(gotB, b[bi])
				bi++
			}
		}
		if ai != len(a) || bi != len(b) || strings.Join(gotA, "") != strings.Join(a, "") || strings.Join(gotB, "") != strings.Join(b, "") {
			t.Fatalf("iter %d: script does not reproduce inputs\na=%q\nb=%q\nscript=%s", iter, a, b, script)
		}
		// And the rendered unified diff must apply cleanly.
		d, _, _ := unifiedDiff("A", "B", strings.Join(a, ""), strings.Join(b, ""))
		if got := applyUnified(t, strings.Join(a, ""), d); got != strings.Join(b, "") {
			t.Fatalf("iter %d: applied diff differs\nold=%q\nnew=%q\ngot=%q\ndiff=%s", iter, strings.Join(a, ""), strings.Join(b, ""), got, d)
		}
	}
}

func TestMyersGivesUpGracefully(t *testing.T) {
	// Completely different large inputs exceed the edit-distance cap; the
	// fallback must still yield a valid script (delete everything, insert
	// everything) rather than hanging or failing.
	var a, b []string
	for i := 0; i < 3000; i++ {
		a = append(a, fmt.Sprintf("a%d\n", i))
		b = append(b, fmt.Sprintf("b%d\n", i))
	}
	script := editScript(a, b)
	if len(script) != len(a)+len(b) {
		t.Fatalf("fallback script has %d ops, want %d", len(script), len(a)+len(b))
	}
	d, add, rem := unifiedDiff("A", "B", strings.Join(a, ""), strings.Join(b, ""))
	if add != 3000 || rem != 3000 {
		t.Fatalf("counts = +%d -%d", add, rem)
	}
	if got := applyUnified(t, strings.Join(a, ""), d); got != strings.Join(b, "") {
		t.Fatal("fallback diff does not apply")
	}
}

func TestUnifiedDiffOutputIsCapped(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&a, "old line %06d with some padding to make it long enough to matter\n", i)
		if i%10 == 0 {
			fmt.Fprintf(&b, "new line %06d with some padding to make it long enough to matter\n", i)
		} else {
			fmt.Fprintf(&b, "old line %06d with some padding to make it long enough to matter\n", i)
		}
	}
	d, add, rem := unifiedDiff("A", "B", a.String(), b.String())
	if add != 2000 || rem != 2000 {
		t.Fatalf("counts = +%d -%d, want 2000 each (anchors should keep the diff exact)", add, rem)
	}
	if len(d) > maxDiffOutput+4096 {
		t.Fatalf("diff is %d bytes, cap is %d", len(d), maxDiffOutput)
	}
	if !strings.HasSuffix(d, "[diff truncated]\n") {
		t.Fatalf("truncated diff should say so, ends with %q", d[max(0, len(d)-40):])
	}
}

func TestPatienceFallbackKeepsScatteredEditsReadable(t *testing.T) {
	// 1000 scattered one-line edits need more than maxEditDist script steps, so
	// Myers gives up; the anchor-based fallback must still produce many small
	// hunks (not one wholesale replacement) that apply cleanly.
	var a, b strings.Builder
	for i := 0; i < 8000; i++ {
		fmt.Fprintf(&a, "line %05d\n", i)
		if i%8 == 3 {
			fmt.Fprintf(&b, "EDITED %05d\n", i)
		} else {
			fmt.Fprintf(&b, "line %05d\n", i)
		}
	}
	d, add, rem := unifiedDiff("A", "B", a.String(), b.String())
	if add != 1000 || rem != 1000 {
		t.Fatalf("counts = +%d -%d, want 1000 each", add, rem)
	}
	if n := strings.Count(d, "\n@@ -"); n < 900 {
		t.Fatalf("only %d hunks: the fallback collapsed the diff", n)
	}
	if strings.HasSuffix(d, "[diff truncated]\n") {
		t.Fatal("this diff fits the cap and must not be truncated")
	}
	if got := applyUnified(t, a.String(), d); got != b.String() {
		t.Fatal("fallback diff does not apply")
	}
}
