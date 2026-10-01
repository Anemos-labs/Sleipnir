package widget

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/widget/widgettest"
)

// `go test -fuzz FuzzUnifiedDiff ./internal/tui/widget` (and FuzzDiff) search for input that breaks the promises; without -fuzz the
// seeds run as ordinary tests. A crasher is saved under testdata/fuzz: once it is fixed, keep the file there or add the input to
// the seeds below.

func patchSeeds() []string {
	seeds := append([]string(nil), widgettest.Hostile()...)
	seeds = append(seeds, widgettest.Unicode()...)
	seeds = append(seeds, udGitShow,
		"", "\n", "@@", "@@ @@", "@@ -1 +1 @@", "@@ -1,0 +1,0 @@\n", "@@ -1,2 +1,2 @@\n a\n", "@@@ -1 -1 +1 @@@\n  a\n", "--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n",
		"--- a/f\n+++ b/f\n@@ -99999999999,1 +1,1 @@\n-a\n", "--- a/f\n+++ /dev/null\n@@ -1 +0,0 @@\n-a\n\\ No newline at end of file\n",
		"diff --git a/x b/y\nrename from x\nrename to y\n", "Binary files a and b differ\n", "diff --git \"a/x y\" \"b/x y\"\n", "diff", "diff --git ",
		"+++ only\n", "--- only\n", "-\n+\n \n", " \n \n \n", "\\", "\\\n\\\n", "--- a\n+++ b\n--- c\n+++ d\n@@ -1 +1 @@\n--- e\n+++ f\n@@ -1 +1 @@\n",
	)
	return seeds
}

func FuzzUnifiedDiff(f *testing.F) {
	for _, s := range patchSeeds() {
		f.Add(s, uint8(40), uint8(0))
		f.Add(s, uint8(10), uint8(2))
	}
	themes := allTestThemes()
	f.Fuzz(func(t *testing.T, patch string, width, theme uint8) {
		w := 1 + int(width)%130
		nt := themes[int(theme)%len(themes)]
		got := UnifiedDiff(patch, w, nt.th)
		checkLines(t, "fuzz/"+nt.name, got, w)
		if len(got) > 5*len(patch)+64 { // a tab is one byte and up to four cells, and at a width of a few cells each can be a row
			t.Fatalf("%d rows for %d bytes of patch", len(got), len(patch))
		}
		capped := UnifiedDiffWith(patch, w, nt.th, DiffOptions{MaxLines: 5})
		if len(capped) > 5 {
			t.Fatalf("MaxLines 5 gave %d rows", len(capped))
		}
	})
}

func FuzzDiff(f *testing.F) {
	for _, s := range patchSeeds() {
		f.Add("f.go", s, s+"x\n", uint8(40), uint8(0), int8(0))
		f.Add(s, "", s, uint8(12), uint8(2), int8(2))
		f.Add(s, s, "", uint8(80), uint8(1), int8(-1))
	}
	f.Add("f", "a\nb\nc\n", "a\nB\nc\n", uint8(30), uint8(3), int8(1))
	themes := allTestThemes()
	f.Fuzz(func(t *testing.T, path, before, after string, width, theme uint8, ctx int8) {
		w := 1 + int(width)%130
		nt := themes[int(theme)%len(themes)]
		o := DiffOptions{Context: int(ctx), NoLineNumbers: theme%7 == 3}
		got := Diff(path, before, after, w, nt.th, o)
		checkLines(t, "fuzz/"+nt.name, got, w)
		if len(got) > 5*(len(before)+len(after)+len(path))+64 { // tabs (see above); the +64 is the header note at a width of one cell
			t.Fatalf("%d rows for %d and %d bytes", len(got), len(before), len(after))
		}
		o.MaxLines = 3
		if capped := Diff(path, before, after, w, nt.th, o); len(capped) > 3 {
			t.Fatalf("MaxLines 3 gave %d rows", len(capped))
		}
	})
}
