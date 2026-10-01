package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// editCase runs one edit against a fresh file that the agent has read.
type editCase struct {
	name    string
	initial string
	input   map[string]any // path is filled in
	want    string         // expected file content on success
	wantErr []string       // substrings of the error; empty means success expected
}

func runEditCases(t *testing.T, cases []editCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			p := filepath.Join(env.Cwd, "f.txt")
			writeFile(t, p, tc.initial)
			mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
			in := map[string]any{"path": "f.txt"}
			for k, v := range tc.input {
				in[k] = v
			}
			res := run(t, Edit{}, env, in)
			if len(tc.wantErr) > 0 {
				contains(t, mustErr(t, res), tc.wantErr...)
				if got := readFileT(t, p); got != tc.initial {
					t.Errorf("a failed edit changed the file: %q", got)
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

func TestEditBasics(t *testing.T) {
	runEditCases(t, []editCase{
		{name: "simple", initial: "hello world\n", input: map[string]any{"old_string": "world", "new_string": "there"}, want: "hello there\n"},
		{name: "multi-line old", initial: "a\nb\nc\n", input: map[string]any{"old_string": "a\nb", "new_string": "X"}, want: "X\nc\n"},
		{name: "delete text", initial: "keep drop keep\n", input: map[string]any{"old_string": " drop", "new_string": ""}, want: "keep keep\n"},
		{name: "insert lines", initial: "a\nb\n", input: map[string]any{"old_string": "a\n", "new_string": "a\nnew1\nnew2\n"}, want: "a\nnew1\nnew2\nb\n"},
		{name: "replace whole content", initial: "only\n", input: map[string]any{"old_string": "only\n", "new_string": "other\n"}, want: "other\n"},
		{name: "unicode", initial: "héllo 日本語 🎉\n", input: map[string]any{"old_string": "日本語", "new_string": "中文"}, want: "héllo 中文 🎉\n"},
		{name: "special characters literal", initial: "a.b*c(d)[e]{f}$g^h|i\\j\n", input: map[string]any{"old_string": ".b*c(d)[e]{f}$g^h|i\\", "new_string": "-"}, want: "a-j\n"},
		{name: "regex-looking text is literal", initial: "x+ y\n", input: map[string]any{"old_string": "x+", "new_string": "z"}, want: "z y\n"},
		{name: "empty file with empty old sets content", initial: "", input: map[string]any{"old_string": "", "new_string": "fresh\n"}, want: "fresh\n"},
		{name: "no trailing newline kept", initial: "a\nb", input: map[string]any{"old_string": "b", "new_string": "c"}, want: "a\nc"},
		{name: "trailing newline kept", initial: "a\nb\n", input: map[string]any{"old_string": "b", "new_string": "c"}, want: "a\nc\n"},
		{name: "append at end without newline", initial: "a\nb", input: map[string]any{"old_string": "b", "new_string": "b\nc"}, want: "a\nb\nc"},
		{name: "replace_all", initial: "x y x z x\n", input: map[string]any{"old_string": "x", "new_string": "Q", "replace_all": true}, want: "Q y Q z Q\n"},
		{name: "replace_all single occurrence", initial: "x y\n", input: map[string]any{"old_string": "x", "new_string": "Q", "replace_all": true}, want: "Q y\n"},
		{name: "replace_all non-overlapping", initial: "aaaa\n", input: map[string]any{"old_string": "aa", "new_string": "b", "replace_all": true}, want: "bb\n"},
		{name: "new contains old", initial: "foo\n", input: map[string]any{"old_string": "foo", "new_string": "foofoo"}, want: "foofoo\n"},
		{name: "leading and trailing spaces significant", initial: "a  b\n", input: map[string]any{"old_string": "  ", "new_string": " "}, want: "a b\n"},
	})
}

func TestEditErrors(t *testing.T) {
	runEditCases(t, []editCase{
		{name: "not found", initial: "hello\n", input: map[string]any{"old_string": "goodbye", "new_string": "x"},
			wantErr: []string{"old_string not found in f.txt", "match the file exactly", "whitespace and indentation"}},
		{name: "multiple matches list lines", initial: "x\nfoo\nx\nbar\nx\n", input: map[string]any{"old_string": "x", "new_string": "y"},
			wantErr: []string{"matches 3 places in f.txt", "lines 1, 3, 5", "replace_all=true"}},
		{name: "overlapping matches are ambiguous", initial: "aaa\n", input: map[string]any{"old_string": "aa", "new_string": "b"},
			wantErr: []string{"matches 2 places"}},
		{name: "identical", initial: "a\n", input: map[string]any{"old_string": "a", "new_string": "a"},
			wantErr: []string{"identical"}},
		{name: "empty old on non-empty file", initial: "a\n", input: map[string]any{"old_string": "", "new_string": "b"},
			wantErr: []string{"old_string is empty", "write"}},
		{name: "missing new_string", initial: "a\n", input: map[string]any{"old_string": "a"},
			wantErr: []string{"new_string is required"}},
		{name: "missing old_string", initial: "a\n", input: map[string]any{"new_string": "a"},
			wantErr: []string{"old_string is required"}},
		{name: "both forms", initial: "a\n", input: map[string]any{"old_string": "a", "new_string": "b", "edits": []map[string]any{{"old_string": "a", "new_string": "c"}}},
			wantErr: []string{"either old_string/new_string or edits"}},
		{name: "batch item missing new_string", initial: "a\n", input: map[string]any{"edits": []map[string]any{{"old_string": "a"}}},
			wantErr: []string{"edits[0]: new_string is required"}},
		{name: "batch item missing old_string", initial: "a\n", input: map[string]any{"edits": []map[string]any{{"new_string": "a"}}},
			wantErr: []string{"edits[0]: old_string is required"}},
		{name: "many matches summarised", initial: strings.Repeat("x\n", 30), input: map[string]any{"old_string": "x", "new_string": "y"},
			wantErr: []string{"matches 30 places", "lines 1, 2, 3, 4, 5, 6, 7, 8, and 22 more"}},
	})
}

func TestEditNotFoundHints(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		old     string
		want    []string
		notWant []string
	}{
		{
			name:    "tabs vs spaces",
			initial: "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
			old:     "    if x {\n        return 1\n    }",
			want:    []string{"same text exists ignoring whitespace at lines 2-4", "line 2 differs in indentation", "file has 1 tab", "old_string has 4 spaces"},
		},
		{
			name:    "spaces vs tabs",
			initial: "    a\n    b\n",
			old:     "\ta\n\tb",
			want:    []string{"line 1 differs in indentation", "file has 4 spaces", "old_string has 1 tab"},
		},
		{
			name:    "trailing whitespace in file",
			initial: "alpha  \nbeta\n",
			old:     "alpha\nbeta",
			want:    []string{"differs in trailing whitespace", "file has 2 spaces", "old_string has none"},
		},
		{
			name:    "internal spacing",
			initial: "x  =  1\n",
			old:     "x = 1",
			want:    []string{"differs in the spacing inside the line"},
		},
		{
			name:    "extra blank line",
			initial: "a\nb\n",
			old:     "a\n\nb",
			want:    []string{"ignoring whitespace at lines 1-2", "blank lines"},
		},
		{
			name:    "old has leading newline",
			initial: "a\nb\n",
			old:     "\n\nb",
			want:    []string{"once leading and trailing whitespace is trimmed", "line 2"},
		},
		{
			name:    "old ends with newline the file lacks",
			initial: "a\nb",
			old:     "b\n",
			want:    []string{"once leading and trailing whitespace is trimmed", "line 2"},
		},
		{
			name:    "line number prefixes copied from read",
			initial: "alpha\nbeta\n",
			old:     "     1\talpha\n     2\tbeta",
			want:    []string{"line-number prefixes"},
		},
		{
			name:    "closest line",
			initial: "package main\n\nfunc calculateTotal(items []Item) int {\n\treturn 0\n}\n",
			old:     "func calculateTotal(items []Items) int {",
			want:    []string{"closest line is line 3", "% similar", `"func calculateTotal(items []Item) int {"`},
		},
		{
			name:    "closest line shows exact whitespace",
			initial: "a\n\t\tresult := compute(x, y)\nb\n",
			old:     "result := compute(x, z)",
			want:    []string{`"\t\tresult := compute(x, y)"`},
		},
		{
			name:    "nothing similar",
			initial: "alpha\nbeta\ngamma\n",
			old:     "zzzzzzzz qqqqqqq",
			want:    []string{"Re-read the file", "may have changed"},
			notWant: []string{"closest line"},
		},
		{
			name:    "replacement char from read",
			initial: "caf\xe9\n",
			old:     "caf�",
			want:    []string{"U+FFFD", "not valid UTF-8"},
		},
		{
			name:    "empty file",
			initial: "",
			old:     "anything",
			want:    []string{"not found"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			writeFile(t, filepath.Join(env.Cwd, "f.txt"), tc.initial)
			mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
			text := mustErr(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": tc.old, "new_string": "X"}))
			contains(t, text, tc.want...)
			notContains(t, text, tc.notWant...)
			if len(text) > 700 {
				t.Errorf("hint too long (%d chars): %s", len(text), text)
			}
		})
	}
}

func TestEditBatch(t *testing.T) {
	runEditCases(t, []editCase{
		{name: "sequential", initial: "one two three\n",
			input: map[string]any{"edits": []map[string]any{
				{"old_string": "one", "new_string": "1"},
				{"old_string": "two", "new_string": "2"},
				{"old_string": "three", "new_string": "3"},
			}}, want: "1 2 3\n"},
		{name: "later edit sees earlier result", initial: "a\n",
			input: map[string]any{"edits": []map[string]any{
				{"old_string": "a", "new_string": "b"},
				{"old_string": "b", "new_string": "c"},
			}}, want: "c\n"},
		{name: "replace_all in batch", initial: "x x y\n",
			input: map[string]any{"edits": []map[string]any{
				{"old_string": "x", "new_string": "z", "replace_all": true},
				{"old_string": "y", "new_string": "w"},
			}}, want: "z z w\n"},
		{name: "single edit in list", initial: "a\n",
			input: map[string]any{"edits": []map[string]any{{"old_string": "a", "new_string": "b"}}}, want: "b\n"},
		{name: "second edit fails so nothing is written", initial: "one two\n",
			input: map[string]any{"edits": []map[string]any{
				{"old_string": "one", "new_string": "1"},
				{"old_string": "missing", "new_string": "x"},
			}},
			wantErr: []string{"edit 2 of 2", "not found", "nothing was written"}},
		{name: "ambiguity after earlier edit", initial: "a b\n",
			input: map[string]any{"edits": []map[string]any{
				{"old_string": "a", "new_string": "b"},
				{"old_string": "b", "new_string": "c"},
			}},
			wantErr: []string{"edit 2 of 2", "matches 2 places"}},
		{name: "identical edit in batch", initial: "a\n",
			input: map[string]any{"edits": []map[string]any{
				{"old_string": "a", "new_string": "b"},
				{"old_string": "b", "new_string": "b"},
			}},
			wantErr: []string{"edit 2 of 2", "identical"}},
	})
}

func TestEditBatchNoNetChange(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "f.txt")
	writeFile(t, p, "a\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	before, _ := os.Stat(p)
	text := mustOK(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "edits": []map[string]any{
		{"old_string": "a", "new_string": "b"}, {"old_string": "b", "new_string": "a"}}}))
	contains(t, text, "No change")
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("file touched although content is unchanged")
	}
}

func TestEditLineEndings(t *testing.T) {
	runEditCases(t, []editCase{
		{name: "crlf file, LF single-line old", initial: "a\r\nb\r\nc\r\n",
			input: map[string]any{"old_string": "b", "new_string": "B"}, want: "a\r\nB\r\nc\r\n"},
		{name: "crlf file, LF multi-line old", initial: "a\r\nb\r\nc\r\n",
			input: map[string]any{"old_string": "a\nb", "new_string": "X\nY"}, want: "X\r\nY\r\nc\r\n"},
		{name: "crlf file, LF old with trailing newline", initial: "a\r\nb\r\nc\r\n",
			input: map[string]any{"old_string": "b\n", "new_string": "B\n"}, want: "a\r\nB\r\nc\r\n"},
		{name: "crlf file, old starting with newline", initial: "a\r\nb\r\nc\r\n",
			input: map[string]any{"old_string": "\nb", "new_string": "\nBB"}, want: "a\r\nBB\r\nc\r\n"},
		{name: "crlf file, new text grows", initial: "a\r\nb\r\n",
			input: map[string]any{"old_string": "a", "new_string": "a1\na2\na3"}, want: "a1\r\na2\r\na3\r\nb\r\n"},
		{name: "crlf file, delete a line", initial: "a\r\nb\r\nc\r\n",
			input: map[string]any{"old_string": "b\n", "new_string": ""}, want: "a\r\nc\r\n"},
		{name: "crlf file, explicit CRLF old", initial: "a\r\nb\r\n",
			input: map[string]any{"old_string": "a\r\nb", "new_string": "X"}, want: "X\r\n"},
		{name: "crlf file, replace_all across lines", initial: "x\r\ny\r\nx\r\ny\r\n",
			input: map[string]any{"old_string": "x\ny", "new_string": "z", "replace_all": true}, want: "z\r\nz\r\n"},
		{name: "crlf file, ambiguity reported with line numbers", initial: "x\r\ny\r\nx\r\ny\r\n",
			input:   map[string]any{"old_string": "x\ny", "new_string": "z"},
			wantErr: []string{"matches 2 places", "lines 1, 3"}},
		{name: "mixed endings: only the touched lines change", initial: "a\nb\r\nc\nd\r\ne\n",
			input: map[string]any{"old_string": "b\nc", "new_string": "B\nC"}, want: "a\nB\r\nC\nd\r\ne\n"},
		{name: "mixed endings, dominant CRLF", initial: "a\r\nb\r\nc\nd\r\n",
			input: map[string]any{"old_string": "a\nb", "new_string": "x\ny\nz"}, want: "x\r\ny\r\nz\r\nc\nd\r\n"},
		{name: "crlf file, no trailing newline", initial: "a\r\nb",
			input: map[string]any{"old_string": "a\nb", "new_string": "c\nd"}, want: "c\r\nd"},
		{name: "lf file, CRLF old normalised", initial: "a\nb\nc\n",
			input: map[string]any{"old_string": "a\r\nb", "new_string": "X\r\nY"}, want: "X\nY\nc\n"},
		{name: "lf file never gains CRs", initial: "a\nb\n",
			input: map[string]any{"old_string": "a", "new_string": "x\r\ny"}, want: "x\ny\nb\n"},
		{name: "crlf: bare LF in new text blends into the line it lands on", initial: "a\r\nb\r\n",
			input: map[string]any{"old_string": "b\r\n", "new_string": "c\n"}, want: "a\r\nc\r\n"},
		{name: "mixed: blends with the local ending, lf region", initial: "a\r\nb\nc\r\n",
			input: map[string]any{"old_string": "b", "new_string": "b1\nb2"}, want: "a\r\nb1\nb2\nc\r\n"},
		{name: "mixed: blends with the local ending, crlf region", initial: "a\nb\r\nc\n",
			input: map[string]any{"old_string": "b", "new_string": "b1\nb2"}, want: "a\nb1\r\nb2\r\nc\n"},
		{name: "mixed: replace_all blends per match", initial: "x\r\nx\nx\r\n",
			input: map[string]any{"old_string": "x", "new_string": "p\nq", "replace_all": true}, want: "p\r\nq\r\np\nq\np\r\nq\r\n"},
		{name: "explicit CR in new text is respected", initial: "a\r\nb\r\n",
			input: map[string]any{"old_string": "b", "new_string": "b1\nb2\r"}, want: "a\r\nb1\nb2\r\r\n"},
	})
}

func TestEditPreservesBOMAndMode(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "f.txt")
	writeFile(t, p, "\xef\xbb\xbfhello\r\nworld\r\n")
	if err := os.Chmod(p, 0o750); err != nil {
		t.Fatal(err)
	}
	text := mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	notContains(t, text, "\xef\xbb\xbf", "\r")
	mustOK(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "hello\nworld", "new_string": "HELLO\nWORLD"}))
	if got := readFileT(t, p); got != "\xef\xbb\xbfHELLO\r\nWORLD\r\n" {
		t.Errorf("content = %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o750 {
		t.Errorf("mode = %v, want 0750", fi.Mode().Perm())
	}
}

func TestEditBOMOnlyFile(t *testing.T) {
	runEditCases(t, []editCase{
		{name: "bom-only file, empty old", initial: "\xef\xbb\xbf", input: map[string]any{"old_string": "", "new_string": "x"}, want: "\xef\xbb\xbfx"},
	})
}

func TestEditInvalidUTF8ElsewhereIsPreserved(t *testing.T) {
	runEditCases(t, []editCase{
		{name: "latin1 byte on another line", initial: "caf\xe9\nplain\n",
			input: map[string]any{"old_string": "plain", "new_string": "changed"}, want: "caf\xe9\nchanged\n"},
		{name: "stray continuation bytes", initial: "\xff\xfe ok \x80\n",
			input: map[string]any{"old_string": "ok", "new_string": "OK"}, want: "\xff\xfe OK \x80\n"},
	})
}

func TestEditMultipleOccurrencesInsideLongLines(t *testing.T) {
	env := testEnv(t)
	line := strings.Repeat("ab", 10000)
	writeFile(t, filepath.Join(env.Cwd, "f"), line+"\nunique-token\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f"}))
	mustOK(t, run(t, Edit{}, env, map[string]any{"path": "f", "old_string": "unique-token", "new_string": "done"}))
	if got := readFileT(t, filepath.Join(env.Cwd, "f")); got != line+"\ndone\n" {
		t.Errorf("long line corrupted")
	}
}

func TestEditResultShowsCompactDiff(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	res := run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "5\n", "new_string": "five\nfive-b\n"})
	text := mustOK(t, res)
	want := "Edited f.txt: 1 replacement (+2 -1 lines)\n@@ -3,5 +3,6 @@\n 3\n 4\n-5\n+five\n+five-b\n 6\n 7"
	if text != want {
		t.Errorf("got:\n%s\nwant:\n%s", text, want)
	}
	if res.Meta["diff"] == nil || res.Meta["path"] != "f.txt" {
		t.Errorf("meta = %v", res.Meta)
	}
}

func TestEditDiffTruncation(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), lines(200))
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	res := run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "line", "new_string": "LINE", "replace_all": true})
	text := mustOK(t, res)
	if n := strings.Count(text, "\n"); n > 45 {
		t.Errorf("diff has %d lines, want at most ~40", n)
	}
	contains(t, text, "200 replacements", "+200 -200", "diff truncated", "more lines")
	if d, _ := res.Meta["diff"].(string); strings.Count(d, "\n") < 100 {
		t.Errorf("meta diff should carry more than the truncated text")
	}
}

func TestEditDiffMarksFinalNewlineOnlyChanges(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), "a\nb\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	text := mustOK(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "b\n", "new_string": "b"}))
	contains(t, text, "no line-level difference")
	if got := readFileT(t, filepath.Join(env.Cwd, "f.txt")); got != "a\nb" {
		t.Errorf("content = %q", got)
	}
}

func TestEditStaleness(t *testing.T) {
	a := testEnv(t)
	b := agentEnv(a, "b")
	p := filepath.Join(a.Cwd, "f.txt")
	writeFile(t, p, "one\ntwo\n")

	// Never read.
	contains(t, mustErr(t, run(t, Edit{}, a, map[string]any{"path": "f.txt", "old_string": "one", "new_string": "1"})),
		"f.txt has not been read by you yet")

	mustOK(t, run(t, Read{}, a, map[string]any{"path": "f.txt"}))
	mustOK(t, run(t, Read{}, b, map[string]any{"path": "f.txt"}))
	mustOK(t, run(t, Edit{}, b, map[string]any{"path": "f.txt", "old_string": "two", "new_string": "2"}))

	text := mustErr(t, run(t, Edit{}, a, map[string]any{"path": "f.txt", "old_string": "one", "new_string": "1"}))
	contains(t, text, "f.txt changed since you last read it", "modified by agent b")
	if readFileT(t, p) != "one\n2\n" {
		t.Errorf("stale edit modified the file: %q", readFileT(t, p))
	}

	// External modification of a file no agent has ever written: the message
	// cannot name an agent.
	q := filepath.Join(a.Cwd, "ext.txt")
	writeFile(t, q, "before\n")
	mustOK(t, run(t, Read{}, a, map[string]any{"path": "ext.txt"}))
	writeFile(t, q, "external\n")
	contains(t, mustErr(t, run(t, Edit{}, a, map[string]any{"path": "ext.txt", "old_string": "external", "new_string": "x"})), "another process")

	// The writer of the current content may keep editing without re-reading.
	mustOK(t, run(t, Read{}, a, map[string]any{"path": "ext.txt"}))
	mustOK(t, run(t, Edit{}, a, map[string]any{"path": "ext.txt", "old_string": "external", "new_string": "e1"}))
	mustOK(t, run(t, Edit{}, a, map[string]any{"path": "ext.txt", "old_string": "e1", "new_string": "e2"}))
}

func TestEditFileErrors(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "src", "main.go"), "package main\n")
	writeFile(t, filepath.Join(env.Cwd, "bin.dat"), "a\x00b")
	os.Mkdir(filepath.Join(env.Cwd, "dir"), 0o755)
	run(t, Read{}, env, map[string]any{"path": "bin.dat"}) // reports "binary" but counts as read
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{"missing", map[string]any{"path": "nope.go", "old_string": "a", "new_string": "b"}, []string{"file not found: nope.go", "use write"}},
		{"missing with typo", map[string]any{"path": "src/mian.go", "old_string": "a", "new_string": "b"}, []string{"did you mean src/main.go"}},
		{"directory", map[string]any{"path": "dir", "old_string": "a", "new_string": "b"}, []string{"is a directory"}},
		{"binary", map[string]any{"path": "bin.dat", "old_string": "a", "new_string": "b"}, []string{"binary file"}},
		{"no path", map[string]any{"old_string": "a", "new_string": "b"}, []string{"path is required"}},
		{"wrong types", `{"path":"src/main.go","old_string":5,"new_string":"b"}`, []string{`argument "old_string" must be a string (got number)`}},
		{"wrong type in batch", `{"path":"src/main.go","edits":[{"old_string":"a","new_string":7}]}`, []string{`"edits.new_string" must be a string`}},
		{"edits not array", `{"path":"src/main.go","edits":"x"}`, []string{`argument "edits" must be an array`}},
		{"replace_all wrong type", `{"path":"src/main.go","old_string":"a","new_string":"b","replace_all":"yes"}`, []string{`argument "replace_all" must be a boolean`}},
		{"too many edits", map[string]any{"path": "src/main.go", "edits": manyEdits(600)}, []string{"too many edits"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contains(t, mustErr(t, run(t, Edit{}, env, tc.input)), tc.want...)
		})
	}
}

func manyEdits(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"old_string": fmt.Sprint(i), "new_string": "x"}
	}
	return out
}

func TestEditCreatesFileWithEmptyOld(t *testing.T) {
	env := testEnv(t)
	text := mustOK(t, run(t, Edit{}, env, map[string]any{"path": "new/dir/x.txt", "old_string": "", "new_string": "created\n"}))
	contains(t, text, "Created new/dir/x.txt")
	if readFileT(t, filepath.Join(env.Cwd, "new/dir/x.txt")) != "created\n" {
		t.Errorf("file not created")
	}
	// Only the single-edit form can create.
	contains(t, mustErr(t, run(t, Edit{}, env, map[string]any{"path": "n2.txt", "edits": []map[string]any{
		{"old_string": "", "new_string": "a"}, {"old_string": "a", "new_string": "b"}}})), "file not found")
}

func TestEditGuardOrderAndVetoes(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), "a\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	h.log = nil
	mustOK(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "a", "new_string": "b"}))
	want := []string{"perm:edit", "guard.before:f.txt", "snap:f.txt", "guard.after:f.txt"}
	if got := h.Log(); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if r := h.Requests()[1]; r.Tool != "edit" || !r.Writes || r.Paths[0] != filepath.Join(env.Cwd, "f.txt") {
		t.Errorf("bad request %+v", r)
	}

	h.denyGuard = func(string) error { return errors.New("leased by b until 12:00") }
	contains(t, mustErr(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "b", "new_string": "c"})), "leased by b until 12:00")
	h.denyGuard = nil
	h.failSnap = func(string) error { return errors.New("no space") }
	contains(t, mustErr(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "b", "new_string": "c"})), "cannot checkpoint")
	h.failSnap = nil
	h.denyPerm = func(perm.Request) string { return "plan mode" }
	contains(t, mustErr(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "b", "new_string": "c"})), "permission denied", "plan mode")
	if readFileT(t, filepath.Join(env.Cwd, "f.txt")) != "b\n" {
		t.Errorf("vetoed edits changed the file")
	}
}

func TestEditFailedEditsDoNotCheckpoint(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), "a\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	h.log = nil
	mustErr(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "zzz", "new_string": "b"}))
	for _, l := range h.Log() {
		if strings.HasPrefix(l, "guard.") || strings.HasPrefix(l, "snap:") {
			t.Errorf("an edit that cannot apply must not take a snapshot: %v", h.Log())
		}
	}
}

func TestEditLargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	env := testEnv(t)
	var sb strings.Builder
	for i := 0; i < 300_000; i++ {
		fmt.Fprintf(&sb, "line number %d of the big file\n", i)
	}
	content := sb.String() // ~9MB
	p := filepath.Join(env.Cwd, "big.txt")
	writeFile(t, p, content)
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "big.txt", "offset": 1, "limit": 1}))
	start := time.Now()
	text := mustOK(t, run(t, Edit{}, env, map[string]any{"path": "big.txt", "old_string": "line number 150000 of", "new_string": "LINE NUMBER 150000 of"}))
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("edit of a 9MB file took %v", d)
	}
	contains(t, text, "-line number 150000 of the big file", "+LINE NUMBER 150000 of the big file", "@@ -149999,5 +149999,5 @@")
	want := strings.Replace(content, "line number 150000 of", "LINE NUMBER 150000 of", 1)
	if readFileT(t, p) != want {
		t.Errorf("big file content wrong")
	}
}

func TestEditNotFoundOnLargeFileIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	env := testEnv(t)
	var sb strings.Builder
	for i := 0; i < 200_000; i++ {
		fmt.Fprintf(&sb, "some content on line %d\n", i)
	}
	writeFile(t, filepath.Join(env.Cwd, "big.txt"), sb.String())
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "big.txt", "limit": 1}))
	start := time.Now()
	mustErr(t, run(t, Edit{}, env, map[string]any{"path": "big.txt", "old_string": "does not exist anywhere in here", "new_string": "x"}))
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("hint generation took %v", d)
	}
}

func TestEditSymlinkedPath(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "real", "f.txt"), "one\n")
	if err := os.Symlink("real", filepath.Join(env.Cwd, "alias")); err != nil {
		t.Skip("symlinks unsupported")
	}
	other := agentEnv(env, "b")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "alias/f.txt"}))
	mustOK(t, run(t, Read{}, other, map[string]any{"path": "real/f.txt"}))
	mustOK(t, run(t, Edit{}, other, map[string]any{"path": "real/f.txt", "old_string": "one", "new_string": "two"}))
	// Same file through another name: the first agent's read is stale.
	contains(t, mustErr(t, run(t, Edit{}, env, map[string]any{"path": "alias/f.txt", "old_string": "one", "new_string": "x"})), "modified by agent b")
}

func TestEditConcurrentAgentsNeverLoseUpdates(t *testing.T) {
	// Every agent repeatedly reads the file and appends its own token, retrying
	// when staleness rejects it. If read-check-write were not atomic, two agents
	// could both pass the check and one update would vanish.
	base := testEnv(t)
	p := filepath.Join(base.Cwd, "log.txt")
	writeFile(t, p, "start\n")
	const agents = 12
	const perAgent = 5
	var wg sync.WaitGroup
	var rejected sync.Map
	for a := 0; a < agents; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			env := agentEnv(base, fmt.Sprintf("agent%02d", a))
			for n := 0; n < perAgent; n++ {
				token := fmt.Sprintf("a%02d-n%d", a, n)
				for {
					r := run(t, Read{}, env, map[string]any{"path": "log.txt"})
					if r.IsError {
						t.Errorf("read failed: %s", r.Text)
						return
					}
					res := run(t, Edit{}, env, map[string]any{"path": "log.txt", "old_string": "start\n", "new_string": "start\n" + token + "\n"})
					if !res.IsError {
						break
					}
					if !strings.Contains(res.Text, "changed since you last read it") {
						t.Errorf("unexpected failure: %s", res.Text)
						return
					}
					rejected.Store(token+fmt.Sprint(time.Now().UnixNano()), true)
				}
			}
		}(a)
	}
	wg.Wait()
	got := readFileT(t, p)
	for a := 0; a < agents; a++ {
		for n := 0; n < perAgent; n++ {
			if token := fmt.Sprintf("a%02d-n%d\n", a, n); !strings.Contains(got, token) {
				t.Errorf("lost update: %q missing", token)
			}
		}
	}
	if c := strings.Count(got, "\n"); c != 1+agents*perAgent {
		t.Errorf("line count = %d, want %d", c, 1+agents*perAgent)
	}
	entries, _ := os.ReadDir(base.Cwd)
	if len(entries) != 1 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestEditConcurrentRaceHasOneWinnerPerVersion(t *testing.T) {
	base := testEnv(t)
	p := filepath.Join(base.Cwd, "f.txt")
	writeFile(t, p, "value: 0\n")
	const n = 20
	envs := make([]*tools.Env, n)
	for i := range envs {
		envs[i] = agentEnv(base, fmt.Sprintf("w%02d", i))
		mustOK(t, run(t, Read{}, envs[i], map[string]any{"path": "f.txt"}))
	}
	results := make([]*tools.Result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range envs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = run(t, Edit{}, envs[i], map[string]any{"path": "f.txt", "old_string": "value: 0", "new_string": fmt.Sprintf("value: %d", i+1)})
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for i, r := range results {
		if r.IsError {
			// Losers are told the file changed (or, if they ran after the winner
			// and had somehow re-read, that the text is gone): never anything else.
			if !strings.Contains(r.Text, "changed since you last read it") {
				t.Errorf("agent %d: %s", i, r.Text)
			}
			continue
		}
		wins++
	}
	if wins != 1 {
		t.Errorf("winners = %d, want 1", wins)
	}
	if got := readFileT(t, p); !strings.HasPrefix(got, "value: ") || strings.Count(got, "\n") != 1 {
		t.Errorf("file = %q", got)
	}
}
