package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

func patchText(parts ...string) string {
	return "*** Begin Patch\n" + strings.Join(parts, "\n") + "\n*** End Patch"
}

func TestParsePatch(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		want  []patchOp
		err   string
	}{
		{
			name:  "add file",
			patch: patchText("*** Add File: a.txt", "+hello", "+", "+world"),
			want:  []patchOp{{kind: opAdd, path: "a.txt", add: []string{"hello", "", "world"}, line: 2}},
		},
		{
			name:  "add empty file",
			patch: patchText("*** Add File: empty.txt"),
			want:  []patchOp{{kind: opAdd, path: "empty.txt", line: 2}},
		},
		{
			name:  "delete file",
			patch: patchText("*** Delete File: old.txt"),
			want:  []patchOp{{kind: opDelete, path: "old.txt", line: 2}},
		},
		{
			name:  "update with anchor",
			patch: patchText("*** Update File: a.go", "@@ func main() {", " keep", "-old", "+new"),
			want: []patchOp{{kind: opUpdate, path: "a.go", line: 2, hunks: []patchHunk{{
				anchors: []string{"func main() {"}, line: 3,
				lines: []hunkLine{{' ', "keep"}, {'-', "old"}, {'+', "new"}}}}}},
		},
		{
			name:  "update with move and bare @@",
			patch: patchText("*** Update File: a.go", "*** Move to: b/c.go", "@@", "-old", "+new"),
			want: []patchOp{{kind: opUpdate, path: "a.go", move: "b/c.go", line: 2, hunks: []patchHunk{{
				line: 4, lines: []hunkLine{{'-', "old"}, {'+', "new"}}}}}},
		},
		{
			name:  "first hunk may omit @@",
			patch: patchText("*** Update File: a.go", " ctx", "-old", "+new"),
			want: []patchOp{{kind: opUpdate, path: "a.go", line: 2, hunks: []patchHunk{{
				line: 3, lines: []hunkLine{{' ', "ctx"}, {'-', "old"}, {'+', "new"}}}}}},
		},
		{
			name:  "two anchors",
			patch: patchText("*** Update File: a.py", "@@ class A:", "@@     def f(self):", "-x", "+y"),
			want: []patchOp{{kind: opUpdate, path: "a.py", line: 2, hunks: []patchHunk{{
				anchors: []string{"class A:", "def f(self):"}, line: 3, lines: []hunkLine{{'-', "x"}, {'+', "y"}}}}}},
		},
		{
			name:  "unified diff header is treated as bare @@",
			patch: patchText("*** Update File: a.go", "@@ -10,7 +10,8 @@", "-a", "+b"),
			want: []patchOp{{kind: opUpdate, path: "a.go", line: 2, hunks: []patchHunk{{
				line: 3, lines: []hunkLine{{'-', "a"}, {'+', "b"}}}}}},
		},
		{
			name:  "unified diff header keeps its section text as anchor",
			patch: patchText("*** Update File: a.go", "@@ -10,7 +10,8 @@ func Foo() {", "-a", "+b"),
			want: []patchOp{{kind: opUpdate, path: "a.go", line: 2, hunks: []patchHunk{{
				anchors: []string{"func Foo() {"}, line: 3, lines: []hunkLine{{'-', "a"}, {'+', "b"}}}}}},
		},
		{
			name:  "blank line inside hunk is an empty context line",
			patch: patchText("*** Update File: a.txt", "@@", " a", "", " b", "-x", "+y"),
			want: []patchOp{{kind: opUpdate, path: "a.txt", line: 2, hunks: []patchHunk{{
				line: 3, lines: []hunkLine{{' ', "a"}, {' ', ""}, {' ', "b"}, {'-', "x"}, {'+', "y"}}}}}},
		},
		{
			name:  "end of file marker",
			patch: patchText("*** Update File: a.txt", "@@", " a", "+b", "*** End of File"),
			want: []patchOp{{kind: opUpdate, path: "a.txt", line: 2, hunks: []patchHunk{{
				line: 3, eof: true, lines: []hunkLine{{' ', "a"}, {'+', "b"}}}}}},
		},
		{
			name:  "no-newline marker ignored",
			patch: patchText("*** Update File: a.txt", "@@", "-a", "\\ No newline at end of file", "+b"),
			want: []patchOp{{kind: opUpdate, path: "a.txt", line: 2, hunks: []patchHunk{{
				line: 3, lines: []hunkLine{{'-', "a"}, {'+', "b"}}}}}},
		},
		{
			name:  "multiple files with blank separators",
			patch: patchText("*** Add File: a", "+1", "", "*** Delete File: b", "", "*** Update File: c", "@@", "-x", "+y", "", "@@", "-z", "+w"),
			want: []patchOp{
				{kind: opAdd, path: "a", add: []string{"1"}, line: 2},
				{kind: opDelete, path: "b", line: 5},
				{kind: opUpdate, path: "c", line: 7, hunks: []patchHunk{
					{line: 8, lines: []hunkLine{{'-', "x"}, {'+', "y"}, {' ', ""}}},
					{line: 12, lines: []hunkLine{{'-', "z"}, {'+', "w"}}},
				}},
			},
		},
		{
			name:  "crlf patch text",
			patch: strings.ReplaceAll(patchText("*** Add File: a", "+x"), "\n", "\r\n"),
			want:  []patchOp{{kind: opAdd, path: "a", add: []string{"x"}, line: 2}},
		},
		{
			name:  "heredoc wrapper",
			patch: "apply_patch <<'EOF'\n" + patchText("*** Add File: a", "+x") + "\nEOF\n",
			want:  []patchOp{{kind: opAdd, path: "a", add: []string{"x"}, line: 3}},
		},
		{
			name:  "surrounding blank lines and header spacing",
			patch: "\n\n  *** Begin Patch  \n*** Delete File: x   \n*** End Patch \n\n",
			want:  []patchOp{{kind: opDelete, path: "x", line: 4}},
		},
		{
			name:  "context line that looks like a header stays a context line",
			patch: patchText("*** Update File: a.txt", "@@", " *** Add File: not-a-header", "-x", "+y"),
			want: []patchOp{{kind: opUpdate, path: "a.txt", line: 2, hunks: []patchHunk{{
				line: 3, lines: []hunkLine{{' ', "*** Add File: not-a-header"}, {'-', "x"}, {'+', "y"}}}}}},
		},
		{
			name:  "uniformly indented patch",
			patch: "    *** Begin Patch\n    *** Update File: a.go\n    @@\n     ctx\n    -old\n    +new\n    *** End Patch\n",
			want: []patchOp{{kind: opUpdate, path: "a.go", line: 2, hunks: []patchHunk{{
				line: 3, lines: []hunkLine{{' ', "ctx"}, {'-', "old"}, {'+', "new"}}}}}},
		},
		{name: "empty", patch: "", err: "patch is empty"},
		{name: "whitespace only", patch: "  \n \n", err: "patch is empty"},
		{name: "no begin", patch: "*** Add File: a\n+x\n*** End Patch", err: `must start with "*** Begin Patch"`},
		{name: "no end", patch: "*** Begin Patch\n*** Add File: a\n+x", err: `must end with "*** End Patch"`},
		{name: "nothing inside", patch: "*** Begin Patch\n*** End Patch", err: "no operations"},
		{name: "unknown directive", patch: patchText("*** Rename File: a"), err: `patch line 2: unexpected "*** Rename File: a"`},
		{name: "stray text between ops", patch: patchText("*** Add File: a", "+x", "oops", "*** Delete File: b"), err: `patch line 4: unexpected "oops"`},
		{name: "bad hunk line", patch: patchText("*** Update File: a", "@@", "?what"), err: `patch line 4: unexpected "?what" inside a hunk`},
		{name: "second hunk without @@", patch: patchText("*** Update File: a", "@@", "-x", "*** End of File", "-y"), err: `expected a hunk starting with @@`},
		{name: "update without hunks", patch: patchText("*** Update File: a"), err: "has no hunks"},
		{name: "empty hunk", patch: patchText("*** Update File: a", "@@", "@@ x", "*** Delete File: b"), err: "hunk has no lines"},
		{name: "add without path", patch: patchText("*** Add File:"), err: "needs a path"},
		{name: "move without path", patch: patchText("*** Update File: a", "*** Move to:", "@@", "-x"), err: "needs a path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := parsePatch(tc.patch)
			if tc.err != "" {
				if !strings.Contains(msg, tc.err) {
					t.Fatalf("error %q does not contain %q (ops=%v)", msg, tc.err, got)
				}
				return
			}
			if msg != "" {
				t.Fatalf("unexpected error: %s", msg)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestApplyPatchAddDeleteUpdate(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "keep.txt"), "one\ntwo\nthree\n")
	writeFile(t, filepath.Join(env.Cwd, "gone.txt"), "bye\n")
	writeFile(t, filepath.Join(env.Cwd, "sub", "u.go"), "package u\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B() int {\n\treturn 2\n}\n")

	patch := patchText(
		"*** Add File: docs/new.md",
		"+# Title",
		"+",
		"+body",
		"*** Delete File: gone.txt",
		"*** Update File: keep.txt",
		"@@",
		" one",
		"-two",
		"+TWO",
		"+2.5",
		" three",
		"*** Update File: sub/u.go",
		"@@ func B() int {",
		"-\treturn 2",
		"+\treturn 22",
	)
	text := mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patch}))
	contains(t, text, "4 files changed", "A docs/new.md (+3)", "D gone.txt", "M keep.txt (+2 -1)", "M sub/u.go (+1 -1)")

	if got := readFileT(t, filepath.Join(env.Cwd, "docs/new.md")); got != "# Title\n\nbody\n" {
		t.Errorf("new.md = %q", got)
	}
	if exists(filepath.Join(env.Cwd, "gone.txt")) {
		t.Errorf("gone.txt should be deleted")
	}
	if got := readFileT(t, filepath.Join(env.Cwd, "keep.txt")); got != "one\nTWO\n2.5\nthree\n" {
		t.Errorf("keep.txt = %q", got)
	}
	if got := readFileT(t, filepath.Join(env.Cwd, "sub/u.go")); !strings.Contains(got, "return 1\n}") || !strings.Contains(got, "return 22") {
		t.Errorf("u.go = %q", got)
	}
}

func TestApplyPatchHunkSemantics(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		patch   []string // lines between the Update File header and End Patch
		want    string
		err     []string
	}{
		{
			name: "multiple hunks in order", initial: "a\nb\nc\nd\ne\nf\n",
			patch: []string{"@@", "-b", "+B", "@@", "-e", "+E"}, want: "a\nB\nc\nd\nE\nf\n",
		},
		{
			name: "anchor selects the right duplicate", initial: "def f():\n    x = 1\ndef g():\n    x = 1\n",
			patch: []string{"@@ def g():", "-    x = 1", "+    x = 2"}, want: "def f():\n    x = 1\ndef g():\n    x = 2\n",
		},
		{
			name: "without anchor the first match wins", initial: "x\nmid\nx\n",
			patch: []string{"@@", "-x", "+Y"}, want: "Y\nmid\nx\n",
		},
		{
			name: "two anchors narrow twice", initial: "class A:\n  def f(self):\n    pass\nclass B:\n  def f(self):\n    pass\n",
			patch: []string{"@@ class B:", "@@   def f(self):", "-    pass", "+    return 1"},
			want:  "class A:\n  def f(self):\n    pass\nclass B:\n  def f(self):\n    return 1\n",
		},
		{
			name: "anchor matched ignoring indentation", initial: "    def method(self):\n        x = 1\n",
			patch: []string{"@@ def method(self):", "-        x = 1", "+        x = 2"}, want: "    def method(self):\n        x = 2\n",
		},
		{
			name: "end of file hunk", initial: "x\ny\nx\ny\n",
			patch: []string{"@@", " x", "-y", "+Z", "*** End of File"}, want: "x\ny\nx\nZ\n",
		},
		{
			name: "pure addition appends at end", initial: "a\nb\n",
			patch: []string{"@@", "+c", "+d"}, want: "a\nb\nc\nd\n",
		},
		{
			name: "pure addition after anchor", initial: "a\nb\nc\n",
			patch: []string{"@@ b", "+inserted"}, want: "a\nb\ninserted\nc\n",
		},
		{
			name: "pure addition to empty file", initial: "",
			patch: []string{"@@", "+first"}, want: "first\n",
		},
		{
			name: "delete lines", initial: "a\nb\nc\n",
			patch: []string{"@@", " a", "-b", " c"}, want: "a\nc\n",
		},
		{
			name: "delete everything", initial: "a\nb\n",
			patch: []string{"@@", "-a", "-b"}, want: "",
		},
		{
			name: "no trailing newline preserved", initial: "a\nb",
			patch: []string{"@@", " a", "-b", "+c"}, want: "a\nc",
		},
		{
			name: "append after a no-newline last line", initial: "a\nb",
			patch: []string{"@@", " b", "+c"}, want: "a\nb\nc",
		},
		{
			name: "trailing whitespace in file tolerated", initial: "keep   \nold\t\nnext\n",
			patch: []string{"@@", " keep", "-old", "+new"}, want: "keep   \nnew\nnext\n",
		},
		{
			name: "trailing whitespace in patch tolerated", initial: "keep\nold\nnext\n",
			patch: []string{"@@", " keep  ", "-old\t", "+new"}, want: "keep\nnew\nnext\n",
		},
		{
			name: "exact match beats a fuzzy one earlier", initial: "x  \nx\n",
			patch: []string{"@@", "-x", "+Y"}, want: "x  \nY\n",
		},
		{
			name: "blank context lines", initial: "a\n\nb\n",
			patch: []string{"@@", " a", "", " b", "+c"}, want: "a\n\nb\nc\n",
		},
		{
			name: "trailing blank context line dropped when the file lacks it", initial: "a\nb\n",
			patch: []string{"@@", " a", "-b", "+B", "", ""}, want: "a\nB\n",
		},
		{
			name: "first hunk without @@", initial: "a\nb\n",
			patch: []string{" a", "-b", "+c"}, want: "a\nc\n",
		},
		{
			name: "lines that look like patch syntax", initial: "*** End Patch\n@@ x\n",
			patch: []string{"@@", "-*** End Patch", "+*** Begin Patch", " @@ x"}, want: "*** Begin Patch\n@@ x\n",
		},
		{
			name: "unicode", initial: "héllo\n日本語\n",
			patch: []string{"@@", " héllo", "-日本語", "+中文"}, want: "héllo\n中文\n",
		},
		{
			name: "invalid utf8 elsewhere preserved", initial: "caf\xe9\nplain\n",
			patch: []string{"@@", "-plain", "+fine"}, want: "caf\xe9\nfine\n",
		},
		// Failures.
		{
			name: "indentation is never fuzzed", initial: "def f():\n\treturn 1\n",
			patch: []string{"@@", " def f():", "-    return 1", "+    return 2"},
			err:   []string{"f.txt, hunk 1", "not found", `"    return 1"`, `"\treturn 1"`, "first difference at line 2"},
		},
		{
			name: "context mismatch reports expected vs found", initial: "one\ntwo\nthree\n",
			patch: []string{"@@", " one", "-TWO", " three"},
			err:   []string{"hunk 1 (patch line 3)", "expected:", "| one", "| TWO", "closest match: lines 1-3 (2 of 3 lines equal)", `expected: "TWO"`, `found:    "two"`},
		},
		{
			name: "hunks out of order", initial: "a\nb\nc\nd\n",
			patch: []string{"@@", "-c", "+C", "@@", "-a", "+A"},
			err:   []string{"hunk 2", "before the previous hunk", "in the order they appear"},
		},
		{
			name: "missing anchor", initial: "a\nb\n",
			patch: []string{"@@ func nothere() {", "-a", "+b"},
			err:   []string{"f.txt, hunk 1", "@@ context line", "func nothere() {", "was not found"},
		},
		{
			name: "anchor after the hunk position", initial: "x\nanchor\n",
			patch: []string{"@@ anchor", "-x", "+y"},
			err:   []string{"hunk 1", "not found"},
		},
		{
			name: "eof hunk that does not end the file", initial: "x\ny\nz\n",
			patch: []string{"@@", " x", "-y", "*** End of File"},
			err:   []string{"hunk 1", "not found"},
		},
		{
			name: "second hunk fails", initial: "a\nb\nc\n",
			patch: []string{"@@", "-a", "+A", "@@", "-zzz", "+y"},
			err:   []string{"hunk 2", "| zzz"},
		},
		{
			name: "file shorter than the hunk", initial: "a\n",
			patch: []string{"@@", " a", " b", " c", "-d"},
			err:   []string{"hunk 1", "only 1 lines"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			p := filepath.Join(env.Cwd, "f.txt")
			writeFile(t, p, tc.initial)
			patch := patchText(append([]string{"*** Update File: f.txt"}, tc.patch...)...)
			res := run(t, ApplyPatch{}, env, map[string]any{"patch": patch})
			if len(tc.err) > 0 {
				text := mustErr(t, res)
				contains(t, text, tc.err...)
				contains(t, text, "nothing was written")
				if got := readFileT(t, p); got != tc.initial {
					t.Errorf("failed patch changed the file: %q", got)
				}
				return
			}
			mustOK(t, res)
			if got := readFileT(t, p); got != tc.want {
				t.Errorf("content = %q\nwant      %q", got, tc.want)
			}
		})
	}
}

func TestApplyPatchLineEndingsAndBOM(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		patch   []string
		want    string
	}{
		{"crlf file, LF patch", "a\r\nb\r\nc\r\n", []string{"@@", " a", "-b", "+B1", "+B2", " c"}, "a\r\nB1\r\nB2\r\nc\r\n"},
		{"bom kept", "\xef\xbb\xbfa\nb\n", []string{"@@", " a", "-b", "+c"}, "\xef\xbb\xbfa\nc\n"},
		{"bom and crlf", "\xef\xbb\xbfa\r\nb\r\n", []string{"@@", "-b", "+c"}, "\xef\xbb\xbfa\r\nc\r\n"},
		{"mixed endings: untouched lines keep theirs, a replaced line keeps its own", "a\nb\r\nc\nd\r\n", []string{"@@", " a", "-b", "+B", " c"}, "a\nB\r\nc\nd\r\n"},
		{"crlf no trailing newline", "a\r\nb", []string{"@@", " a", "-b", "+c"}, "a\r\nc"},
		{"append to crlf file", "a\r\n", []string{"@@", "+b"}, "a\r\nb\r\n"},
		{"crlf patch text itself", "a\nb\n", []string{"@@\r", " a\r", "-b\r", "+c\r"}, "a\nc\n"},
		{"added lines blend with the line they replace (crlf line in lf-dominant file)", "a\nb\r\nc\nd\n", []string{"@@", " a", "-b", "+B1", "+B2", " c"}, "a\nB1\r\nB2\r\nc\nd\n"},
		{"added lines blend with the line they replace (lf line in crlf-dominant file)", "a\r\nb\nc\r\nd\r\n", []string{"@@", " a", "-b", "+B1", "+B2", " c"}, "a\r\nB1\nB2\nc\r\nd\r\n"},
		{"pure addition blends with the following line", "a\r\nb\nc\n", []string{"@@ b", "+x"}, "a\r\nb\nx\nc\n"},
		{"pure addition after a crlf line", "a\r\nb\nc\n", []string{"@@ a", "+x"}, "a\r\nx\r\nb\nc\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			p := filepath.Join(env.Cwd, "f.txt")
			writeFile(t, p, tc.initial)
			patch := patchText(append([]string{"*** Update File: f.txt"}, tc.patch...)...)
			mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patch}))
			if got := readFileT(t, p); got != tc.want {
				t.Errorf("content = %q\nwant      %q", got, tc.want)
			}
		})
	}
}

func TestApplyPatchAddFileBlankLineLeniency(t *testing.T) {
	// A blank line between two + lines is an empty line the model forgot to
	// prefix; blank lines before the next operation are separators.
	env := testEnv(t)
	mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": "*** Begin Patch\n*** Add File: a.txt\n+one\n\n\n+four\n\n*** Add File: b.txt\n+x\n\n*** End Patch"}))
	if got := readFileT(t, filepath.Join(env.Cwd, "a.txt")); got != "one\n\n\nfour\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := readFileT(t, filepath.Join(env.Cwd, "b.txt")); got != "x\n" {
		t.Errorf("b.txt = %q", got)
	}
}

func TestApplyPatchPreservesMode(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "run.sh")
	writeFile(t, p, "#!/bin/sh\necho old\n")
	os.Chmod(p, 0o755)
	mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Update File: run.sh", "@@", "-echo old", "+echo new")}))
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	// A move keeps the mode too.
	mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Update File: run.sh", "*** Move to: bin/run.sh", "@@", "-echo new", "+echo moved")}))
	if fi, err := os.Stat(filepath.Join(env.Cwd, "bin/run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("moved file mode: %v %v", fi, err)
	}
}

func TestApplyPatchMove(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "old", "a.txt"), "1\n2\n3\n")
	text := mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
		"*** Update File: old/a.txt", "*** Move to: new/dir/b.txt", "@@", " 1", "-2", "+two", " 3")}))
	contains(t, text, "R old/a.txt -> new/dir/b.txt (+1 -1)")
	if exists(filepath.Join(env.Cwd, "old/a.txt")) {
		t.Errorf("source still exists")
	}
	if got := readFileT(t, filepath.Join(env.Cwd, "new/dir/b.txt")); got != "1\ntwo\n3\n" {
		t.Errorf("dest = %q", got)
	}

	t.Run("pure rename without hunks", func(t *testing.T) {
		writeFile(t, filepath.Join(env.Cwd, "r1.txt"), "same\n")
		mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Update File: r1.txt", "*** Move to: r2.txt")}))
		if readFileT(t, filepath.Join(env.Cwd, "r2.txt")) != "same\n" || exists(filepath.Join(env.Cwd, "r1.txt")) {
			t.Errorf("rename failed")
		}
	})
	t.Run("destination exists", func(t *testing.T) {
		writeFile(t, filepath.Join(env.Cwd, "m1.txt"), "x\n")
		writeFile(t, filepath.Join(env.Cwd, "m2.txt"), "y\n")
		text := mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Update File: m1.txt", "*** Move to: m2.txt", "@@", "-x", "+z")}))
		contains(t, text, "already exists", "m2.txt")
		if readFileT(t, filepath.Join(env.Cwd, "m1.txt")) != "x\n" || readFileT(t, filepath.Join(env.Cwd, "m2.txt")) != "y\n" {
			t.Errorf("failed move changed files")
		}
	})
	t.Run("move onto itself is an in-place update", func(t *testing.T) {
		writeFile(t, filepath.Join(env.Cwd, "s.txt"), "x\n")
		mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Update File: s.txt", "*** Move to: ./s.txt", "@@", "-x", "+y")}))
		if readFileT(t, filepath.Join(env.Cwd, "s.txt")) != "y\n" {
			t.Errorf("in-place update failed")
		}
	})
}

func TestApplyPatchOperationsSeeEarlierOnes(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "swap.txt"), "old content\n")

	t.Run("add then update the same file", func(t *testing.T) {
		mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
			"*** Add File: n.txt", "+a", "+b",
			"*** Update File: n.txt", "@@", " a", "-b", "+c")}))
		if got := readFileT(t, filepath.Join(env.Cwd, "n.txt")); got != "a\nc\n" {
			t.Errorf("n.txt = %q", got)
		}
	})
	t.Run("delete then add replaces a file", func(t *testing.T) {
		mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
			"*** Delete File: swap.txt",
			"*** Add File: swap.txt", "+brand new")}))
		if got := readFileT(t, filepath.Join(env.Cwd, "swap.txt")); got != "brand new\n" {
			t.Errorf("swap.txt = %q", got)
		}
	})
	t.Run("add then delete leaves nothing", func(t *testing.T) {
		text := mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
			"*** Add File: tmp.txt", "+x",
			"*** Delete File: tmp.txt")}))
		contains(t, text, "No changes")
		if exists(filepath.Join(env.Cwd, "tmp.txt")) {
			t.Errorf("tmp.txt should not exist")
		}
	})
	t.Run("chained moves", func(t *testing.T) {
		writeFile(t, filepath.Join(env.Cwd, "c1.txt"), "x\n")
		mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
			"*** Update File: c1.txt", "*** Move to: c2.txt", "@@", "-x", "+y",
			"*** Update File: c2.txt", "*** Move to: c3.txt", "@@", "-y", "+z")}))
		if exists(filepath.Join(env.Cwd, "c1.txt")) || exists(filepath.Join(env.Cwd, "c2.txt")) || readFileT(t, filepath.Join(env.Cwd, "c3.txt")) != "z\n" {
			t.Errorf("chained moves wrong")
		}
	})
	t.Run("identical result is a no-op", func(t *testing.T) {
		writeFile(t, filepath.Join(env.Cwd, "same.txt"), "x\n")
		p := filepath.Join(env.Cwd, "same.txt")
		before, _ := os.Stat(p)
		text := mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Update File: same.txt", "@@", "-x", "+x")}))
		contains(t, text, "No changes")
		after, _ := os.Stat(p)
		if !after.ModTime().Equal(before.ModTime()) {
			t.Errorf("no-op patch touched the file")
		}
	})
}

func TestApplyPatchOperationErrors(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "exists.txt"), "x\n")
	os.Mkdir(filepath.Join(env.Cwd, "dir"), 0o755)
	writeFile(t, filepath.Join(env.Cwd, "bin.dat"), "a\x00b")
	tests := []struct {
		name  string
		patch string
		want  []string
	}{
		{"add existing", patchText("*** Add File: exists.txt", "+y"), []string{"Add File exists.txt", "already exists", "Update File"}},
		{"update missing", patchText("*** Update File: nope.txt", "@@", "-a", "+b"), []string{"Update File nope.txt", "does not exist", "Add File"}},
		{"delete missing", patchText("*** Delete File: nope.txt"), []string{"Delete File nope.txt", "does not exist"}},
		{"update a directory", patchText("*** Update File: dir", "@@", "-a", "+b"), []string{"dir is a directory"}},
		{"delete a directory", patchText("*** Delete File: dir"), []string{"dir is a directory"}},
		{"update a binary", patchText("*** Update File: bin.dat", "@@", "-a", "+b"), []string{"binary"}},
		{"parent is a file", patchText("*** Add File: exists.txt/child", "+x"), []string{"not a directory"}},
		{"nul in path", patchText("*** Add File: a\x00b", "+x"), []string{"NUL"}},
		{"parse error is reported first", "not a patch", []string{"apply_patch:", "must start with"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contains(t, mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": tc.patch})), tc.want...)
		})
	}
	contains(t, mustErr(t, run(t, ApplyPatch{}, env, map[string]any{})), "patch is required")
	contains(t, mustErr(t, run(t, ApplyPatch{}, env, `{"patch":5}`)), `argument "patch" must be a string`)
	if readFileT(t, filepath.Join(env.Cwd, "exists.txt")) != "x\n" {
		t.Errorf("exists.txt changed")
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() {
			out[rel+"/"] = ""
		} else {
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func TestApplyPatchIsAtomicWhenALaterFileFails(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		want  string
	}{
		{
			"second update does not match",
			patchText(
				"*** Add File: new/created.txt", "+x",
				"*** Update File: a.txt", "@@", "-a1", "+A1",
				"*** Update File: b.txt", "@@", "-zzz", "+y"),
			"b.txt, hunk 1",
		},
		{
			"third op deletes a missing file",
			patchText(
				"*** Update File: a.txt", "@@", "-a1", "+A1",
				"*** Delete File: b.txt",
				"*** Delete File: missing.txt"),
			"missing.txt",
		},
		{
			"add over an existing file",
			patchText(
				"*** Delete File: b.txt",
				"*** Update File: a.txt", "@@", "-a1", "+A1",
				"*** Add File: a.txt", "+dup"),
			"already exists",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			writeFile(t, filepath.Join(env.Cwd, "a.txt"), "a1\na2\n")
			writeFile(t, filepath.Join(env.Cwd, "b.txt"), "b1\nb2\n")
			before := snapshotTree(t, env.Cwd)
			contains(t, mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": tc.patch})), tc.want, "nothing was written")
			if after := snapshotTree(t, env.Cwd); !reflect.DeepEqual(before, after) {
				t.Errorf("tree changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

func TestApplyPatchRollsBackWhenWritingFails(t *testing.T) {
	// A guard runs after planning and before any write, so it can sabotage the
	// filesystem to force a failure mid-commit: turn the parent directory of the
	// last file into a regular file so MkdirAll fails there.
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "a.txt"), "a1\n")
	writeFile(t, filepath.Join(env.Cwd, "b.txt"), "b1\n")
	h := &hooks{}
	withHooks(env, h)
	h.onBeforeFn = func(path string) {
		if strings.HasSuffix(path, "z.txt") {
			os.WriteFile(filepath.Join(env.Cwd, "blocked"), []byte("file"), 0o644)
		}
	}
	before := snapshotTree(t, env.Cwd)
	patch := patchText(
		"*** Update File: a.txt", "@@", "-a1", "+A1",
		"*** Update File: b.txt", "@@", "-b1", "+B1",
		"*** Add File: blocked/z.txt", "+z")
	text := mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patch}))
	contains(t, text, "failed while writing blocked/z.txt", "restored")
	delete(before, "blocked")
	after := snapshotTree(t, env.Cwd)
	delete(after, "blocked")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("rollback incomplete:\nbefore %v\nafter  %v", before, after)
	}
	// FileState must not have recorded writes that were rolled back.
	if err := env.Files.CheckFresh("a1", filepath.Join(env.Cwd, "a.txt"), []byte("a1\n"), false); err != nil {
		t.Errorf("state polluted by a rolled-back write: %v", err)
	}
	for _, l := range h.Log() {
		if strings.HasPrefix(l, "guard.after") {
			t.Errorf("AfterWrite must not run for a failed patch: %v", h.Log())
		}
	}
}

func TestApplyPatchStaleness(t *testing.T) {
	a := testEnv(t)
	b := agentEnv(a, "b")
	p := filepath.Join(a.Cwd, "f.txt")
	writeFile(t, p, "1\n2\n3\n")
	writeFile(t, filepath.Join(a.Cwd, "g.txt"), "g\n")

	// An agent that never read the file may patch it: the context validates it.
	mustOK(t, run(t, ApplyPatch{}, a, map[string]any{"patch": patchText("*** Update File: g.txt", "@@", "-g", "+G")}))

	mustOK(t, run(t, Read{}, a, map[string]any{"path": "f.txt"}))
	mustOK(t, run(t, Read{}, b, map[string]any{"path": "f.txt"}))
	mustOK(t, run(t, ApplyPatch{}, b, map[string]any{"patch": patchText("*** Update File: f.txt", "@@", "-1", "+one")}))

	// a read the file before b changed it: rejected, even though the hunk would apply.
	text := mustErr(t, run(t, ApplyPatch{}, a, map[string]any{"patch": patchText("*** Update File: f.txt", "@@", "-3", "+three")}))
	contains(t, text, "f.txt changed since you last read it", "modified by agent b")
	// Deleting a stale file is refused as well.
	contains(t, mustErr(t, run(t, ApplyPatch{}, a, map[string]any{"patch": patchText("*** Delete File: f.txt")})), "changed since you last read it")
	if readFileT(t, p) != "one\n2\n3\n" {
		t.Errorf("file = %q", readFileT(t, p))
	}
	// A patch is all-or-nothing across files even for staleness.
	writeFile(t, filepath.Join(a.Cwd, "h.txt"), "h\n")
	contains(t, mustErr(t, run(t, ApplyPatch{}, a, map[string]any{"patch": patchText(
		"*** Update File: h.txt", "@@", "-h", "+H",
		"*** Update File: f.txt", "@@", "-3", "+three")})), "changed since you last read it")
	if readFileT(t, filepath.Join(a.Cwd, "h.txt")) != "h\n" {
		t.Errorf("h.txt changed although the patch failed")
	}
	// After the patch, the patching agent has effectively read the new content.
	mustOK(t, run(t, Read{}, a, map[string]any{"path": "f.txt"}))
	mustOK(t, run(t, ApplyPatch{}, a, map[string]any{"patch": patchText("*** Update File: f.txt", "@@", "-3", "+three")}))
	mustOK(t, run(t, Edit{}, a, map[string]any{"path": "f.txt", "old_string": "three", "new_string": "III"}))
}

func TestApplyPatchRecordsFileStateForWrittenFiles(t *testing.T) {
	a := testEnv(t)
	b := agentEnv(a, "b")
	mustOK(t, run(t, ApplyPatch{}, a, map[string]any{"patch": patchText("*** Add File: n.txt", "+one")}))
	// The creator can edit without reading; b cannot.
	mustOK(t, run(t, Edit{}, a, map[string]any{"path": "n.txt", "old_string": "one", "new_string": "two"}))
	contains(t, mustErr(t, run(t, Edit{}, b, map[string]any{"path": "n.txt", "old_string": "two", "new_string": "x"})), "not been read")
}

func TestApplyPatchPermissionsAreCheckedPerFileBeforeAnyWrite(t *testing.T) {
	env := testEnv(t)
	h := &hooks{denyPerm: func(r perm.Request) string {
		if len(r.Paths) == 1 && strings.HasSuffix(r.Paths[0], "secret.txt") {
			return "secret.txt is protected"
		}
		return ""
	}}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "a.txt"), "a\n")
	writeFile(t, filepath.Join(env.Cwd, "secret.txt"), "s\n")
	before := snapshotTree(t, env.Cwd)
	text := mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
		"*** Update File: a.txt", "@@", "-a", "+A",
		"*** Update File: secret.txt", "@@", "-s", "+S",
		"*** Add File: z.txt", "+z")}))
	contains(t, text, "permission denied (patch secret.txt): secret.txt is protected")
	notContains(t, text, "\ns\n")
	if after := snapshotTree(t, env.Cwd); !reflect.DeepEqual(before, after) {
		t.Errorf("tree changed")
	}
	reqs := h.Requests()
	if len(reqs) != 2 {
		t.Fatalf("permission was asked %d times (should stop at the denial): %+v", len(reqs), reqs)
	}
	for i, want := range []string{"a.txt", "secret.txt"} {
		if r := reqs[i]; r.Tool != "apply_patch" || !r.Writes || len(r.Paths) != 1 || r.Paths[0] != filepath.Join(env.Cwd, want) {
			t.Errorf("request %d = %+v", i, r)
		}
	}
	for _, l := range h.Log() {
		if strings.HasPrefix(l, "guard.") || strings.HasPrefix(l, "snap:") {
			t.Errorf("no guard/snapshot before permission is granted: %v", h.Log())
		}
	}
}

func TestApplyPatchPermissionRequestsForMovesAndDeletes(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "a.txt"), "a\n")
	writeFile(t, filepath.Join(env.Cwd, "d.txt"), "d\n")
	mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
		"*** Update File: a.txt", "*** Move to: sub/b.txt", "@@", "-a", "+b",
		"*** Delete File: d.txt",
		"*** Add File: n.txt", "+n")}))
	var summaries []string
	for _, r := range h.Requests() {
		summaries = append(summaries, fmt.Sprintf("%s|%s", r.Summary, filepath.Base(r.Paths[0])))
	}
	want := []string{"move a.txt -> sub/b.txt|a.txt", "move a.txt -> sub/b.txt|b.txt", "delete d.txt|d.txt", "create n.txt|n.txt"}
	if !reflect.DeepEqual(summaries, want) {
		t.Errorf("requests = %v, want %v", summaries, want)
	}
}

func TestApplyPatchGuardAndSnapshotPerChangedFile(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "a.txt"), "a\n")
	writeFile(t, filepath.Join(env.Cwd, "b.txt"), "b\n")
	writeFile(t, filepath.Join(env.Cwd, "same.txt"), "s\n")
	mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
		"*** Update File: b.txt", "@@", "-b", "+B",
		"*** Update File: a.txt", "@@", "-a", "+A",
		"*** Update File: same.txt", "@@", "-s", "+s",
		"*** Add File: c.txt", "+c")}))
	got := h.Log()
	var filtered []string
	for _, l := range got {
		if !strings.HasPrefix(l, "perm:") {
			filtered = append(filtered, l)
		}
	}
	// Deterministic path order; every guard/snapshot precedes every write's AfterWrite;
	// the no-op file is skipped entirely.
	want := []string{
		"guard.before:a.txt", "snap:a.txt", "guard.before:b.txt", "snap:b.txt", "guard.before:c.txt", "snap:c.txt",
		"guard.after:a.txt", "guard.after:b.txt", "guard.after:c.txt",
	}
	if !reflect.DeepEqual(filtered, want) {
		t.Errorf("hook order:\n got %v\nwant %v", filtered, want)
	}
}

func TestApplyPatchGuardOrSnapshotVetoLeavesEverythingUntouched(t *testing.T) {
	for _, name := range []string{"guard", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			h := &hooks{}
			withHooks(env, h)
			writeFile(t, filepath.Join(env.Cwd, "a.txt"), "a\n")
			writeFile(t, filepath.Join(env.Cwd, "b.txt"), "b\n")
			if name == "guard" {
				h.denyGuard = func(p string) error {
					if strings.HasSuffix(p, "b.txt") {
						return errors.New("b.txt is leased to agent z")
					}
					return nil
				}
			} else {
				h.failSnap = func(p string) error {
					if strings.HasSuffix(p, "b.txt") {
						return errors.New("snapshot store unavailable")
					}
					return nil
				}
			}
			before := snapshotTree(t, env.Cwd)
			text := mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
				"*** Update File: a.txt", "@@", "-a", "+A",
				"*** Update File: b.txt", "@@", "-b", "+B")}))
			if name == "guard" {
				contains(t, text, "b.txt is leased to agent z")
			} else {
				contains(t, text, "cannot checkpoint b.txt")
			}
			if after := snapshotTree(t, env.Cwd); !reflect.DeepEqual(before, after) {
				t.Errorf("tree changed: %v", after)
			}
			for _, l := range h.Log() {
				if strings.HasPrefix(l, "guard.after") {
					t.Errorf("AfterWrite ran for a vetoed patch: %v", h.Log())
				}
			}
		})
	}
}

func TestApplyPatchAbsoluteAndCleanedPaths(t *testing.T) {
	env := testEnv(t)
	abs := filepath.Join(env.Cwd, "abs.txt")
	writeFile(t, abs, "x\n")
	mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
		"*** Update File: "+abs, "@@", "-x", "+y",
		"*** Add File: ./sub/../made.txt", "+m")}))
	if readFileT(t, abs) != "y\n" || readFileT(t, filepath.Join(env.Cwd, "made.txt")) != "m\n" {
		t.Errorf("paths not resolved")
	}
}

func TestApplyPatchManyFilesAndHunks(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	env := testEnv(t)
	var patch []string
	for f := 0; f < 40; f++ {
		var content strings.Builder
		for i := 0; i < 200; i++ {
			fmt.Fprintf(&content, "f%d line %d\n", f, i)
		}
		writeFile(t, filepath.Join(env.Cwd, fmt.Sprintf("d%d/file%d.txt", f%4, f)), content.String())
		patch = append(patch, fmt.Sprintf("*** Update File: d%d/file%d.txt", f%4, f))
		for _, l := range []int{5, 50, 150, 199} {
			patch = append(patch, "@@", fmt.Sprintf("-f%d line %d", f, l), fmt.Sprintf("+F%d LINE %d", f, l))
		}
	}
	text := mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(patch...)}))
	contains(t, text, "40 files changed", "+160 -160")
	got := readFileT(t, filepath.Join(env.Cwd, "d3/file39.txt"))
	if !strings.Contains(got, "F39 LINE 199\n") || !strings.Contains(got, "f39 line 6\n") || strings.Contains(got, "f39 line 5\n") {
		t.Errorf("patch not applied correctly")
	}
}

func TestApplyPatchResultMeta(t *testing.T) {
	env := testEnv(t)
	res := run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Add File: a.txt", "+1", "+2", "*** Add File: b.txt", "+3")})
	mustOK(t, res)
	files, _ := res.Meta["files"].([]string)
	sort.Strings(files)
	if !reflect.DeepEqual(files, []string{"a.txt", "b.txt"}) || res.Meta["added"] != 3 {
		t.Errorf("meta = %v", res.Meta)
	}
}

func TestApplyPatchOverlappingPatchesDoNotDeadlock(t *testing.T) {
	// Patches that name the same files in opposite orders must not deadlock:
	// locks are taken in sorted order whatever the patch order is.
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "a.txt"), "0\n")
	writeFile(t, filepath.Join(env.Cwd, "b.txt"), "0\n")
	var wg sync.WaitGroup
	const rounds = 30
	errs := make(chan string, 4*rounds)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				// A fresh agent id per patch: an agent that has already seen an
				// older version would (correctly) be refused by the staleness
				// check, and this test is about locking, not staleness.
				e := agentEnv(env, fmt.Sprintf("p%d-%d", w, i))
				first, second := "a.txt", "b.txt"
				if w%2 == 1 {
					first, second = second, first
				}
				res := run(t, ApplyPatch{}, e, map[string]any{"patch": patchText(
					"*** Update File: "+first, "@@", "+x",
					"*** Update File: "+second, "@@", "+y")})
				if res.IsError {
					errs <- res.Text
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("patch failed: %s", e)
	}
	a := strings.Count(readFileT(t, filepath.Join(env.Cwd, "a.txt")), "\n")
	b := strings.Count(readFileT(t, filepath.Join(env.Cwd, "b.txt")), "\n")
	if a != 1+4*rounds || b != 1+4*rounds {
		t.Errorf("lost updates: a=%d b=%d lines, want %d", a, b, 1+4*rounds)
	}
}

func TestApplyPatchLargeFileMismatchReportIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	env := testEnv(t)
	var sb strings.Builder
	for i := 0; i < 200_000; i++ {
		fmt.Fprintf(&sb, "row %d\n", i)
	}
	writeFile(t, filepath.Join(env.Cwd, "big.txt"), sb.String())
	var hunk []string
	hunk = append(hunk, "@@")
	for i := 0; i < 200; i++ {
		hunk = append(hunk, fmt.Sprintf(" row %d", 1000+i))
	}
	hunk = append(hunk, "-row 5000 not there", "+x")
	text := mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(append([]string{"*** Update File: big.txt"}, hunk...)...)}))
	contains(t, text, "hunk 1")
	if len(text) > 4000 {
		t.Errorf("error message too long: %d chars", len(text))
	}
}

func TestApplyPatchDeleteRemovesASymlinkNotItsTarget(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "shared", "lib.go"), "package lib\n")
	writeFile(t, filepath.Join(env.Cwd, "other.txt"), "o\n")
	if err := os.Symlink("../shared/lib.go", filepath.Join(env.Cwd, "vendor.go")); err != nil {
		t.Skip("symlinks unsupported")
	}
	h := &hooks{}
	withHooks(env, h)
	text := mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Delete File: vendor.go")}))
	contains(t, text, "D vendor.go")
	if exists(filepath.Join(env.Cwd, "vendor.go")) {
		t.Errorf("the link should be gone")
	}
	if readFileT(t, filepath.Join(env.Cwd, "shared", "lib.go")) != "package lib\n" {
		t.Errorf("the target must survive")
	}
	if r := h.Requests()[0]; r.Paths[0] != filepath.Join(env.Cwd, "vendor.go") {
		t.Errorf("permission engine should see the link itself, got %q", r.Paths[0])
	}

	t.Run("rollback restores the link", func(t *testing.T) {
		env := testEnv(t)
		writeFile(t, filepath.Join(env.Cwd, "real.txt"), "r\n")
		writeFile(t, filepath.Join(env.Cwd, "z_file.txt"), "z\n")
		os.Symlink("real.txt", filepath.Join(env.Cwd, "a_link.txt"))
		h := &hooks{}
		withHooks(env, h)
		// Deletions run last, in path order: the link goes first. Turn z_file.txt
		// into a non-empty directory just before the write phase so its removal
		// fails and the link deletion has to be undone.
		h.onBeforeFn = func(path string) {
			if strings.HasSuffix(path, "z_file.txt") {
				os.Remove(path)
				os.Mkdir(path, 0o755)
				os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o644)
			}
		}
		text := mustErr(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText(
			"*** Delete File: a_link.txt",
			"*** Delete File: z_file.txt")}))
		contains(t, text, "failed while writing z_file.txt", "restored")
		fi, err := os.Lstat(filepath.Join(env.Cwd, "a_link.txt"))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the deleted link was not restored: %v %v", fi, err)
		}
		if target, _ := os.Readlink(filepath.Join(env.Cwd, "a_link.txt")); target != "real.txt" {
			t.Errorf("link target = %q", target)
		}
	})
}
