package widget

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/widget/widgettest"
)

func unifiedText(patch string, w int) string {
	return widgettest.Flatten(UnifiedDiff(patch, w, MonoTheme()))
}

const udGitShow = "commit 3f2a9c1d0e\n" +
	"Author: A Dev <dev@example.com>\n" +
	"Date:   Tue Sep 29 10:00:00 2026 +0000\n" +
	"\n" +
	"    orders: page is one-based\n" +
	"\n" +
	"diff --git a/orders/list.go b/orders/list.go\n" +
	"index 83db48f..bf2a1c3 100644\n" +
	"--- a/orders/list.go\n" +
	"+++ b/orders/list.go\n" +
	"@@ -56,7 +56,7 @@ func (s *Store) List(ctx context.Context, page, size int) ([]Order, error) {\n" +
	" \tif size <= 0 {\n" +
	" \t\tsize = 50\n" +
	" \t}\n" +
	"-\toffset := page * size\n" +
	"+\toffset := (page - 1) * size\n" +
	" \trows, err := s.db.QueryContext(ctx, listSQL, size, offset)\n" +
	" \tif err != nil {\n" +
	" \t\treturn nil, err\n" +
	"@@ -120,6 +120,8 @@ func scan(rows *sql.Rows) ([]Order, error) {\n" +
	" \tvar out []Order\n" +
	" \tfor rows.Next() {\n" +
	" \t\tvar o Order\n" +
	"+\t\t// Scan fails on a NULL total\n" +
	"+\t\tvar total sql.NullInt64\n" +
	" \t\tif err := rows.Scan(&o.ID, &o.Name); err != nil {\n" +
	" \t\t\treturn nil, err\n" +
	" \t\t}\n" +
	"diff --git a/orders/list_test.go b/orders/list_test.go\n" +
	"new file mode 100644\n" +
	"index 0000000..5d1e0a2\n" +
	"--- /dev/null\n" +
	"+++ b/orders/list_test.go\n" +
	"@@ -0,0 +1,4 @@\n" +
	"+package orders\n" +
	"+\n" +
	"+func TestList(t *testing.T) {\n" +
	"+}\n" +
	"\\ No newline at end of file\n" +
	"diff --git a/old_name.txt b/new_name.txt\n" +
	"similarity index 100%\n" +
	"rename from old_name.txt\n" +
	"rename to new_name.txt\n" +
	"diff --git a/logo.png b/logo.png\n" +
	"index 1111111..2222222 100644\n" +
	"Binary files a/logo.png and b/logo.png differ\n" +
	"diff --git a/gone.txt b/gone.txt\n" +
	"deleted file mode 100644\n" +
	"index 9999999..0000000\n" +
	"--- a/gone.txt\n" +
	"+++ /dev/null\n" +
	"@@ -1,2 +0,0 @@\n" +
	"-first\n" +
	"-second\n"

func TestUnifiedDiffGolden(t *testing.T) {
	for _, nt := range []themeCase{{"mono", MonoTheme()}, {"default", DefaultTheme()}} {
		checkGoldenLines(t, "unified_"+nt.name+"_72", nt.th, UnifiedDiff(udGitShow, 72, nt.th))
	}
	checkGoldenLines(t, "unified_mono_34", MonoTheme(), UnifiedDiff(udGitShow, 34, MonoTheme()))
}

func TestUnifiedDiffHeadersNumbersAndNotes(t *testing.T) {
	cases := []struct {
		name, patch, want string
	}{
		{"a modification", "--- a/f.go\n+++ b/f.go\n@@ -3,3 +3,3 @@\n a\n-b\n+B\n c\n", "f.go  +1 −1\n3 3   a\n4   - b\n  4 + B\n5 5   c"},
		{"counts default to one", "--- a/f\n+++ b/f\n@@ -7 +7 @@\n-x\n+y\n", "f  +1 −1\n7   - x\n  7 + y"},
		{"a section is shown", "--- a/f\n+++ b/f\n@@ -7 +7 @@ func main()\n-x\n+y\n", "f  +1 −1\n    ⋯ func main()\n7   - x\n  7 + y"},
		{"a new file", "--- /dev/null\n+++ b/n.txt\n@@ -0,0 +1,2 @@\n+a\n+b\n", "n.txt  +2 −0 (new file)\n  1 + a\n  2 + b"},
		{"a deleted file", "--- a/d.txt\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n", "d.txt  +0 −2 (deleted)\n1   - a\n2   - b"},
		{"a rename without changes", "diff --git a/o b/n\nsimilarity index 100%\nrename from o\nrename to n\n", "n (renamed from o)"},
		{"a binary change", "diff --git a/x.png b/x.png\nindex 1..2 100644\nBinary files a/x.png and b/x.png differ\n", "x.png (binary file changed)"},
		{"a mode change is a note", "diff --git a/s.sh b/s.sh\nold mode 100644\nnew mode 100755\n", "s.sh (mode 100644 -> 100755)"},
		{"two files", "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-1\n+2\ndiff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n-3\n+4\n", "a  +1 −1\n1   - 1\n  1 + 2\n\nb  +1 −1\n1   - 3\n  1 + 4"},
		{"a plain diff -u (no diff line) with timestamps", "--- old.txt\t2026-01-01 10:00:00\n+++ new.txt\t2026-01-02 10:00:00\n@@ -1 +1 @@\n-a\n+b\n", "new.txt  +1 −1\n1   - a\n  1 + b"},
		{"two files in a plain diff -u", "--- a.txt\n+++ a.txt\n@@ -1 +1 @@\n-a\n+b\n--- b.txt\n+++ b.txt\n@@ -1 +1 @@\n-c\n+d\n", "a.txt  +1 −1\n1   - a\n  1 + b\n\nb.txt  +1 −1\n1   - c\n  1 + d"},
		{"no newline at end of file", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n", "f  +1 −1\n1   - a\n    \\ No newline at end of file\n  1 + b"},
		{"a gap between hunks", "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n-a\n+A\n b\n@@ -10,2 +10,2 @@\n c\n-d\n+D\n", "f  +2 −2\n 1    - a\n    1 + A\n 2  2   b\n      ⋯ 7 unchanged lines\n10 10   c\n11    - d\n   11 + D"},
		{"adjacent hunks", "--- a/f\n+++ b/f\n@@ -1,1 +1,1 @@\n-a\n+A\n@@ -2,1 +2,1 @@\n-b\n+B\n", "f  +2 −2\n1   - a\n  1 + A\n    ⋯\n2   - b\n  2 + B"},
		{"pure insertions count the gap right", "--- a/f\n+++ b/f\n@@ -5,0 +6,1 @@\n+x\n@@ -10,0 +12,1 @@\n+y\n", "f  +2 −0\n   6 + x\n     ⋯ 5 unchanged lines\n  12 + y"},
		{"an empty context line whose space was stripped", "--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n a\n\n-c\n+C\n", "f  +1 −1\n1 1   a\n2 2   \n3   - c\n  3 + C"},
		{"CRLF", "--- a/f\r\n+++ b/f\r\n@@ -1 +1 @@\r\n-a\r\n+b\r\n", "f  +1 −1\n1   - a\n  1 + b"},
		{"tabs in content", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-\tx\n+\t\ty\n", "f  +1 −1\n1   -     x\n  1 +         y"},
	}
	for _, c := range cases {
		got := unifiedText(c.patch, 60)
		// a context line with nothing on it leaves trailing spaces that Flatten trims
		want := strings.ReplaceAll(c.want, "2 2   \n", "2 2\n")
		if got != want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, got, want)
		}
	}
}

func TestUnifiedDiffToleratesGarbage(t *testing.T) {
	cases := []struct {
		name, patch, want string
	}{
		{"no diff at all", "hello\nworld\n", "hello\nworld"},
		{"headers only", "--- a/f\n+++ b/f\n", "f"},
		{"a hunk header that does not parse", "--- a/f\n+++ b/f\n@@ garbage @@\n+x\n-y\n z\nplain text\n", "f  +1 −1\n    ⋯ garbage\n    + x\n    - y\n      z\n\nplain text"},
		{"the file ends before the counts are met", "--- a/f\n+++ b/f\n@@ -1,5 +1,5 @@\n a\n-b\n", "f  +0 −1\n1 1   a\n2   - b"},
		{"more lines than the counts promise", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n+c\n", "f  +1 −1\n1   - a\n  1 + b\n\n+c"},
		{"prose after a patch is not part of it", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n\n- a list item\n- another\n", "f  +1 −1\n1   - a\n  1 + b\n\n- a list item\n- another"},
		{"a removed line that looks like a header", "--- a/f\n+++ b/f\n@@ -1,2 +1,1 @@\n--- comment\n a\n", "f  +0 −1\n1   - -- comment\n2 1   a"},
		{"an added line that looks like a header", "--- a/f\n+++ b/f\n@@ -1 +1,2 @@\n a\n+++ x\n", "f  +1 −0\n1 1   a\n  2 + ++ x"},
		{"a combined diff is text", "@@@ -1,2 -1,2 +1,2 @@@\n  a\n", "@@@ -1,2 -1,2 +1,2 @@@\n  a"},
		{"huge and negative numbers", "--- a/f\n+++ b/f\n@@ -99999999999,1 +1,1 @@\n-a\n+b\n@@ -1,-2 +1,1 @@\n-c\n", "f  +1 −2\n    ⋯ -99999999999,1 +1,1\n    - a\n    + b\n    ⋯ -1,-2 +1,1\n    - c"},
		{"a hunk with nothing in it", "--- a/f\n+++ b/f\n@@ -1,0 +1,0 @@\n", "f"},
		{"blank lines around", "\n\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n\n\n", "f  +1 −1\n1   - a\n  1 + b"},
		{"only a hunk header", "@@ -1 +1 @@\n-a\n+b\n", "+1 −1\n1   - a\n  1 + b"},
	}
	for _, c := range cases {
		got := unifiedText(c.patch, 60)
		if got != c.want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, got, c.want)
		}
		for _, w := range []int{10, 20, 60} {
			checkLines(t, c.name, UnifiedDiff(c.patch, w, DefaultTheme()), w)
		}
	}
	if UnifiedDiff("", 40, MonoTheme()) != nil || UnifiedDiff("x", 0, MonoTheme()) != nil || UnifiedDiff("x", -1, MonoTheme()) != nil {
		t.Error("an empty patch or a width below 1 gives nil")
	}
}

func TestUnifiedDiffLooseTextIsDimAndKeepsItsIndent(t *testing.T) {
	th := DefaultTheme()
	got := UnifiedDiff("    indented message\n\ndiff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n", 40, th)
	if got[0].Plain() != "    indented message" || got[0][0].Style != th.Dim {
		t.Errorf("loose text is dim and its spaces are kept: %+v", got[0])
	}
}

func TestUnifiedDiffWordHighlightsAndColours(t *testing.T) {
	th := DefaultTheme()
	got := UnifiedDiff("--- a/f\n+++ b/f\n@@ -1 +1 @@\n-let x = 1\n+let y = 1\n", 40, th)
	var words []string
	for _, l := range got[1:] {
		for _, sp := range l {
			if sp.Style.BG == th.DiffDelWord.BG || sp.Style.BG == th.DiffAddWord.BG {
				words = append(words, sp.Text)
			}
		}
		if l.Width() != 40 {
			t.Errorf("a removed or added row is full width: %d", l.Width())
		}
	}
	if strings.Join(words, ",") != "x,y" {
		t.Errorf("changed words: %q", words)
	}
}

func TestUnifiedDiffHostileText(t *testing.T) {
	for _, h := range widgettest.Hostile() {
		patch := "diff --git a/" + h + " b/" + h + "\n--- a/" + h + "\n+++ b/" + h + "\n@@ -1 +1 @@ " + h + "\n-" + h + "\n+" + h + "x\n" + h + "\n"
		for _, nt := range allTestThemes() {
			for _, w := range []int{10, 33, 100} {
				checkLines(t, "hostile unified/"+nt.name, UnifiedDiff(patch, w, nt.th), w)
			}
		}
	}
}

func TestUnifiedDiffMaxLines(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("--- a/f\n+++ b/f\n@@ -1,50 +1,50 @@\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "-old %d\n+new %d\n", i, i)
	}
	got := UnifiedDiffWith(sb.String(), 40, MonoTheme(), DiffOptions{MaxLines: 10})
	if len(got) != 10 || got[9].Plain() != "… 92 more lines" {
		t.Errorf("%d rows, footer %q", len(got), got[len(got)-1].Plain())
	}
	if got := UnifiedDiffWith("--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n", 40, MonoTheme(), DiffOptions{NoHeader: true, NoLineNumbers: true}); widgettest.Flatten(got) != "- a\n+ b" {
		t.Errorf("options: %q", widgettest.Flatten(got))
	}
}

// ---- Diff and UnifiedDiff are two routes to the same picture ----

type udPatchOp struct {
	kind byte
	text string
	a, b int
}

// udMakePatch writes what `git diff -U<ctx>` writes for two texts without a trailing newline problem. It shares the edit script with
// Diff (the algorithm has its own tests) but nothing of the rendering: the hunks are cut from the script again, here.
func udMakePatch(path, before, after string, ctx int) string {
	bl, al := splitDiffLines(before), splitDiffLines(after)
	var ops []udPatchOp
	for _, r := range diffScript(bl, al) {
		for k := 0; k < r.n; k++ {
			switch r.kind {
			case ' ':
				ops = append(ops, udPatchOp{' ', bl[r.a+k], r.a + k, r.b + k})
			case '-':
				ops = append(ops, udPatchOp{'-', bl[r.a+k], r.a + k, r.b})
			default:
				ops = append(ops, udPatchOp{'+', al[r.b+k], r.a, r.b + k})
			}
		}
	}
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind != ' ' {
			for j := max(0, i-ctx); j <= min(len(ops)-1, i+ctx); j++ {
				keep[j] = true
			}
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", path, path)
	if before == "" {
		sb.WriteString("--- /dev/null\n")
	} else {
		fmt.Fprintf(&sb, "--- a/%s\n", path)
	}
	if after == "" {
		sb.WriteString("+++ /dev/null\n")
	} else {
		fmt.Fprintf(&sb, "+++ b/%s\n", path)
	}
	count := func(n int) string {
		if n == 1 {
			return ""
		}
		return fmt.Sprintf(",%d", n)
	}
	for i := 0; i < len(ops); {
		if !keep[i] {
			i++
			continue
		}
		j := i
		for j < len(ops) && keep[j] {
			j++
		}
		oc, nc := 0, 0
		os, ns := ops[i].a, ops[i].b // with no old (new) lines, the line before
		seenOld, seenNew := false, false
		for _, o := range ops[i:j] {
			if o.kind != '+' {
				oc++
				if !seenOld {
					os, seenOld = o.a+1, true
				}
			}
			if o.kind != '-' {
				nc++
				if !seenNew {
					ns, seenNew = o.b+1, true
				}
			}
		}
		fmt.Fprintf(&sb, "@@ -%d%s +%d%s @@\n", os, count(oc), ns, count(nc))
		for _, o := range ops[i:j] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.text)
			sb.WriteByte('\n')
		}
		i = j
	}
	return sb.String()
}

func TestUnifiedDiffAndDiffAgree(t *testing.T) {
	g := widgettest.NewRand(81)
	alphabet := []string{"alpha", "beta gamma", "", "func main() {", "}", "x := 1", "return", "    indented line", "日本語"}
	gen := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString(alphabet[g.Intn(len(alphabet))])
			sb.WriteString("\n")
		}
		return sb.String()
	}
	checked := 0
	for i := 0; i < 400; i++ {
		before := gen(g.Intn(30))
		after := before
		switch g.Intn(4) {
		case 0:
			after = gen(g.Intn(30))
		default: // mostly a few edits of the same text, which is what a diff is for
			ls := strings.Split(strings.TrimSuffix(before, "\n"), "\n")
			for k := 0; k < 1+g.Intn(4) && len(ls) > 0; k++ {
				p := g.Intn(len(ls))
				switch g.Intn(3) {
				case 0:
					ls[p] = alphabet[g.Intn(len(alphabet))]
				case 1:
					ls = append(ls[:p], ls[p+1:]...)
				default:
					ls = append(ls[:p], append([]string{alphabet[g.Intn(len(alphabet))]}, ls[p:]...)...)
				}
			}
			after = strings.Join(ls, "\n") + "\n"
		}
		if before == after || before == "\n" || after == "\n" {
			continue
		}
		for _, ctx := range []int{0, 1, 3} {
			patch := udMakePatch("f.go", before, after, ctx)
			for _, w := range []int{80, 30} {
				o := DiffOptions{Context: ctx}
				if ctx == 0 {
					o.Context = -1
				}
				a := widgettest.Flatten(Diff("f.go", before, after, w, MonoTheme(), o))
				b := widgettest.Flatten(UnifiedDiff(patch, w, MonoTheme()))
				if a != b {
					t.Fatalf("ctx %d width %d: Diff and UnifiedDiff differ for\n%q -> %q\npatch:\n%s\nDiff:\n%s\nUnifiedDiff:\n%s", ctx, w, before, after, patch, a, b)
				}
				checked++
			}
		}
	}
	if checked < 500 {
		t.Errorf("only %d comparisons", checked)
	}
}

func TestUnifiedDiffPropertyRandomPatches(t *testing.T) {
	g := widgettest.NewRand(91)
	for i := 0; i < 60; i++ {
		before, after := g.Text(g.Intn(30)), g.Text(g.Intn(30))
		patch := udMakePatch(g.Text(2), before, after, g.Intn(4))
		// and the same thing damaged: cut anywhere, with random lines dropped
		cut := patch
		if len(cut) > 0 {
			cut = cut[:g.Intn(len(cut))]
		}
		for _, p := range []string{patch, cut, strings.ReplaceAll(patch, "\n", "\r\n"), g.Text(10) + patch + g.Text(5)} {
			for _, nt := range allTestThemes() {
				for w := 10; w <= 120; w += 22 {
					got := UnifiedDiff(p, w, nt.th)
					checkLines(t, "random patch/"+nt.name, got, w)
					if len(got) > 2*len(p)+64 {
						t.Fatalf("%d rows for %d bytes", len(got), len(p))
					}
				}
			}
		}
	}
}

func TestUnifiedDiffScalesLinearly(t *testing.T) {
	hunk := "@@ -1,3 +1,3 @@ section\n a\n-b\n+B\n c\n"
	for _, unit := range []string{hunk, "-a\n", "+a\n", " a\n", "diff --git a/f b/f\n--- a/f\n+++ b/f\n" + hunk, "x\n", "@@ -1 +1 @@\n-a long line to wrap " + strings.Repeat("word ", 20) + "\n"} {
		n := 12_000 / len(unit)
		requireLinear(t, fmt.Sprintf("UnifiedDiff(%q*n)", unit[:min(len(unit), 12)]), n, func(n int) {
			patch := "--- a/f\n+++ b/f\n" + strings.Repeat(unit, n)
			UnifiedDiff(patch, 80, DefaultTheme())
		})
	}
	requireLinear(t, "Diff of n scattered changes", 300, func(n int) {
		before := repeatLines(10*n, func(i int) string { return fmt.Sprintf("line %d", i) })
		after := repeatLines(10*n, func(i int) string {
			if i%10 == 5 {
				return "changed"
			}
			return fmt.Sprintf("line %d", i)
		})
		Diff("f", before, after, 80, DefaultTheme(), DiffOptions{})
	})
}
