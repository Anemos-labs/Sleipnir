package reward

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func lineTexts(f *fileDiff, op byte) []string {
	var out []string
	f.eachLine(func(o byte, text string) {
		if o == op {
			out = append(out, text)
		}
	})
	return out
}

func TestParseDiffBasics(t *testing.T) {
	d := gitDiff("pkg/a.go", hunkOf(" keep", "-old", "+new1", "+new2"))
	files := parseDiff(d)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	f := files[0]
	if f.path() != "pkg/a.go" || f.status != statusModified || f.binary {
		t.Errorf("file = %+v", f)
	}
	if got := strings.Join(lineTexts(f, '+'), "|"); got != "new1|new2" {
		t.Errorf("added = %q", got)
	}
	if got := strings.Join(lineTexts(f, '-'), "|"); got != "old" {
		t.Errorf("removed = %q", got)
	}
	if got := strings.Join(lineTexts(f, ' '), "|"); got != "keep" {
		t.Errorf("context = %q", got)
	}
}

func TestParseDiffStatuses(t *testing.T) {
	tests := []struct {
		name       string
		diff       string
		wantOld    string
		wantNew    string
		wantStatus string
		binary     bool
	}{
		{"added", newFileDiff("n/x.go", "package n"), "", "n/x.go", statusAdded, false},
		{"deleted", deletedFileDiff("d/x.go", "package d"), "d/x.go", "", statusDeleted, false},
		{"rename only", "diff --git a/old/name.go b/new/name.go\nsimilarity index 100%\nrename from old/name.go\nrename to new/name.go\n", "old/name.go", "new/name.go", statusRenamed, false},
		{"rename with edit", "diff --git a/o.go b/n.go\nsimilarity index 88%\nrename from o.go\nrename to n.go\nindex 1..2 100644\n--- a/o.go\n+++ b/n.go\n@@ -1,1 +1,1 @@\n-x\n+y\n", "o.go", "n.go", statusRenamed, false},
		{"copy", "diff --git a/o.go b/n.go\nsimilarity index 100%\ncopy from o.go\ncopy to n.go\n", "o.go", "n.go", statusCopied, false},
		{"mode only", "diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n", "run.sh", "run.sh", statusMode, false},
		{"binary", "diff --git a/img.png b/img.png\nindex 1..2 100644\nBinary files a/img.png and b/img.png differ\n", "img.png", "img.png", statusModified, true},
		{"binary new", "diff --git a/img.png b/img.png\nnew file mode 100644\nindex 0..2\nBinary files /dev/null and b/img.png differ\n", "", "img.png", statusAdded, true},
		{"git binary patch", "diff --git a/b.bin b/b.bin\nindex 1..2 100644\nGIT binary patch\nliteral 12\nzc$@(0bc2c+\n\nliteral 8\nyc$@(0\n\n", "b.bin", "b.bin", statusModified, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := parseDiff(tc.diff)
			if len(files) != 1 {
				t.Fatalf("files = %d: %+v", len(files), files)
			}
			f := files[0]
			if f.oldPath != tc.wantOld || f.newPath != tc.wantNew || f.status != tc.wantStatus || f.binary != tc.binary {
				t.Errorf("got old=%q new=%q status=%s binary=%v; want %q %q %s %v", f.oldPath, f.newPath, f.status, f.binary, tc.wantOld, tc.wantNew, tc.wantStatus, tc.binary)
			}
		})
	}
}

func TestParseDiffMultiFile(t *testing.T) {
	d := gitDiff("a.go", hunkOf("-x", "+y")) +
		newFileDiff("b.go", "package b") +
		deletedFileDiff("c.go", "package c") +
		gitDiff("dir with space/d.go", hunkOf("+z"))
	files := parseDiff(d)
	if len(files) != 4 {
		t.Fatalf("files = %d", len(files))
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.path())
	}
	if got := strings.Join(paths, ","); got != "a.go,b.go,c.go,dir with space/d.go" {
		t.Errorf("paths = %s", got)
	}
}

func TestHunkContentThatLooksLikeHeaders(t *testing.T) {
	// Added and removed lines that imitate diff headers must stay hunk content:
	// the second file below is NOT a change to .github/workflows/ci.yml.
	d := gitDiff("notes.txt", hunkOf(
		"+diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml",
		"+--- a/.github/workflows/ci.yml",
		"++++ b/.github/workflows/ci.yml",
		"+@@ -1 +1 @@",
		"--- a/other",
		"-+++ b/other",
	))
	files := parseDiff(d)
	if len(files) != 1 || files[0].path() != "notes.txt" {
		t.Fatalf("content was mistaken for headers: %d files, %+v", len(files), files)
	}
	if got := len(lineTexts(files[0], '+')); got != 4 {
		t.Errorf("added lines = %d, want 4", got)
	}
}

func TestMiscountedHunksNeverHideChanges(t *testing.T) {
	t.Run("count too large swallows the next file header", func(t *testing.T) {
		d := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1,50 +1,50 @@\n+x\n" +
			"diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-a\n+b\n"
		files := parseDiff(d)
		if len(files) != 2 || files[1].path() != "go.mod" {
			t.Fatalf("second file lost: %+v", files)
		}
	})
	t.Run("count too small leaves extra lines that are kept", func(t *testing.T) {
		d := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,1 @@\n-a\n+b\n+sneaky t.Skip()\n+more\n"
		files := parseDiff(d)
		if len(files) != 1 {
			t.Fatalf("files = %d", len(files))
		}
		if got := strings.Join(lineTexts(files[0], '+'), "|"); got != "b|sneaky t.Skip()|more" {
			t.Errorf("added = %q", got)
		}
	})
	t.Run("unparseable hunk header is consumed leniently", func(t *testing.T) {
		d := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ nonsense @@\n-a\n+b\n c\n"
		files := parseDiff(d)
		if len(files) != 1 || len(lineTexts(files[0], '+')) != 1 || len(lineTexts(files[0], '-')) != 1 {
			t.Fatalf("lenient hunk lost lines: %+v", files)
		}
	})
	t.Run("plain --- lines in lenient hunk start a new file only with +++", func(t *testing.T) {
		d := "--- a/x.txt\n+++ b/x.txt\n@@ bad @@\n-1\n+2\n--- a/y.txt\n+++ b/y.txt\n@@ -1 +1 @@\n-3\n+4\n"
		files := parseDiff(d)
		if len(files) != 2 || files[1].path() != "y.txt" {
			t.Fatalf("files = %+v", files)
		}
	})
}

func TestPlainUnifiedDiff(t *testing.T) {
	d := "--- old/pkg/a.go\t2026-01-01 10:00:00\n+++ new/pkg/a.go\t2026-01-02 10:00:00\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n"
	files := parseDiff(d)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	// Plain diffs keep whatever prefix the tool used; the raw names are exposed
	// so detectors can test both readings.
	if files[0].oldRaw != "old/pkg/a.go" || files[0].newRaw != "new/pkg/a.go" {
		t.Errorf("raw paths = %q %q", files[0].oldRaw, files[0].newRaw)
	}
	d2 := "--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1 @@\n-x\n+y\n"
	if f := parseDiff(d2); len(f) != 1 || f[0].path() != "pkg/a.go" {
		t.Errorf("a/ b/ prefixes must be stripped: %+v", f)
	}
}

func TestPathQuotingAndPrefixes(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want string
	}{
		{"quoted with space and octal",
			"diff --git \"a/caf\\303\\251 x.go\" \"b/caf\\303\\251 x.go\"\n--- \"a/caf\\303\\251 x.go\"\n+++ \"b/caf\\303\\251 x.go\"\n@@ -1 +1 @@\n-a\n+b\n", "café x.go"},
		{"unquoted with spaces", "diff --git a/dir with space/f x.go b/dir with space/f x.go\n--- a/dir with space/f x.go\n+++ b/dir with space/f x.go\n@@ -1 +1 @@\n-a\n+b\n", "dir with space/f x.go"},
		{"no prefix", "diff --git x/y.go x/y.go\n--- x/y.go\n+++ x/y.go\n@@ -1 +1 @@\n-a\n+b\n", "x/y.go"},
		{"real directories named a and b", "diff --git a/a/x.go b/a/x.go\n--- a/a/x.go\n+++ b/a/x.go\n@@ -1 +1 @@\n-a\n+b\n", "a/x.go"},
		{"traversal is cleaned", "diff --git a/../go.mod b/../go.mod\n--- a/../go.mod\n+++ b/../go.mod\n@@ -1 +1 @@\n-a\n+b\n", "go.mod"},
		{"header with tab timestamp", "diff --git a/t.go b/t.go\n--- a/t.go\t2026-01-01\n+++ b/t.go\t2026-01-01\n@@ -1 +1 @@\n-a\n+b\n", "t.go"},
		{"header without diff --git line", "Index: t.go\n===\n--- a/t.go\n+++ b/t.go\n@@ -1 +1 @@\n-a\n+b\n", "t.go"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := parseDiff(tc.diff)
			if len(files) != 1 {
				t.Fatalf("files = %d", len(files))
			}
			if files[0].path() != tc.want {
				t.Errorf("path = %q, want %q", files[0].path(), tc.want)
			}
		})
	}
	// A path that climbs out of the root is reported as such.
	f := parseDiff("diff --git a/../../etc/x b/../../etc/x\n--- a/../../etc/x\n+++ b/../../etc/x\n@@ -1 +1 @@\n-a\n+b\n")
	if len(f) != 1 || !f[0].escapes {
		t.Errorf("escape not flagged: %+v", f)
	}
}

func TestCRLFAndNoNewlineMarkers(t *testing.T) {
	d := "diff --git a/w.go b/w.go\r\n--- a/w.go\r\n+++ b/w.go\r\n@@ -1,2 +1,2 @@\r\n a\r\n-b\r\n\\ No newline at end of file\r\n+c\r\n\\ No newline at end of file\r\n"
	files := parseDiff(d)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	f := files[0]
	if got := lineTexts(f, '+'); len(got) != 1 || got[0] != "c" {
		t.Errorf("added = %q (CR must be stripped)", got)
	}
	if got := lineTexts(f, '-'); len(got) != 1 || got[0] != "b" {
		t.Errorf("removed = %q", got)
	}
}

func TestBinaryPayloadIsNotParsedAsHunks(t *testing.T) {
	// A binary patch payload can contain lines that look like anything.
	payload := "diff --git a/data.bin b/data.bin\nindex 1..2 100644\nGIT binary patch\nliteral 30\nzcmV+b0OI...\n+++ b/.github/workflows/ci.yml\n--- a/go.mod\n@@ -1 +1 @@\n\n" +
		gitDiff("real.go", hunkOf("-a", "+b"))
	files := parseDiff(payload)
	if len(files) != 2 {
		t.Fatalf("files = %d: %+v", len(files), files)
	}
	if files[0].path() != "data.bin" || !files[0].binary || len(files[0].hunks) != 0 {
		t.Errorf("binary file = %+v", files[0])
	}
	if files[1].path() != "real.go" {
		t.Errorf("second file = %q", files[1].path())
	}
}

func TestCombinedDiff(t *testing.T) {
	d := "diff --cc merged.go\nindex a,b..c\n--- a/merged.go\n+++ b/merged.go\n@@@ -1,3 -1,3 +1,4 @@@\n  ctx\n+ added in first\n +added in second\n- removed\n"
	files := parseDiff(d)
	if len(files) != 1 || files[0].path() != "merged.go" {
		t.Fatalf("files = %+v", files)
	}
	if got := len(lineTexts(files[0], '+')); got != 2 {
		t.Errorf("combined added lines = %d", got)
	}
}

func TestEmptyAndGarbageDiffs(t *testing.T) {
	for _, in := range []string{"", "\n\n", "not a diff at all", "commit abc\nAuthor: x\n\n    message\n", "@@ -1 +1 @@\n-a\n+b\n", "+++ only\n", "--- only\n"} {
		files := parseDiff(in) // must not panic
		for _, f := range files {
			_ = f.path()
		}
	}
	// A bare hunk is still analysed.
	if f := parseDiff("@@ -1 +1 @@\n-a\n+b\n"); len(f) != 1 || len(lineTexts(f[0], '+')) != 1 {
		t.Errorf("bare hunk lost: %+v", f)
	}
}

func TestParseDiffIsLinearOnHugeInput(t *testing.T) {
	var b strings.Builder
	const files = 400
	const perFile = 2000
	for i := 0; i < files; i++ {
		fmt.Fprintf(&b, "diff --git a/f%d.go b/f%d.go\n--- a/f%d.go\n+++ b/f%d.go\n@@ -1,%d +1,%d @@\n", i, i, i, i, perFile, perFile)
		for j := 0; j < perFile/2; j++ {
			b.WriteString("-old line of code here\n+new line of code here\n")
		}
	}
	text := b.String()
	start := time.Now()
	parsed := parseDiff(text)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("parsing %d MB took %v", len(text)>>20, d)
	}
	if len(parsed) != files {
		t.Fatalf("files = %d", len(parsed))
	}
}

func TestParseDiffPathologicalHeaders(t *testing.T) {
	// A 1 MB header line of spaces must not go quadratic in splitGitHeader.
	line := "diff --git " + strings.Repeat(" a", 500_000) + "\n"
	start := time.Now()
	parseDiff(line + "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("pathological header took %v", d)
	}
	// Many tiny files.
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "diff --git a/%d b/%d\nnew file mode 100644\n", i, i)
	}
	start = time.Now()
	if n := len(parseDiff(b.String())); n != 20000 {
		t.Errorf("files = %d", n)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("many files took %v", d)
	}
}

func TestSplitGitHeader(t *testing.T) {
	tests := []struct{ in, a, b string }{
		{"a/x.go b/x.go", "a/x.go", "b/x.go"},
		{"a/x y.go b/x y.go", "a/x y.go", "b/x y.go"},
		{"a/old.go b/new.go", "a/old.go", "b/new.go"},
		{"x.go x.go", "x.go", "x.go"},
		{`"a/q x" "b/q x"`, "a/q x", "b/q x"},
		{"single", "single", "single"},
	}
	for _, tc := range tests {
		a, b := splitGitHeader(tc.in)
		if a != tc.a || b != tc.b {
			t.Errorf("splitGitHeader(%q) = %q,%q want %q,%q", tc.in, a, b, tc.a, tc.b)
		}
	}
}
