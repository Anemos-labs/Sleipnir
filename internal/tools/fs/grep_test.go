package fs

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

type engine struct {
	name string
	tool *Grep
}

func engines(t testing.TB) []engine {
	es := []engine{{"go", &Grep{DisableRipgrep: true}}}
	if _, err := exec.LookPath("rg"); err == nil {
		es = append(es, engine{"rg", &Grep{}})
	} else {
		t.Log("ripgrep not on PATH: only the pure-Go engine is exercised")
	}
	return es
}

// forEachEngine runs the body once per available engine.
func forEachEngine(t *testing.T, body func(t *testing.T, g *Grep)) {
	t.Helper()
	for _, e := range engines(t) {
		e := e
		t.Run(e.name, func(t *testing.T) { body(t, e.tool) })
	}
}

func grep(t testing.TB, g *Grep, env *tools.Env, in map[string]any) string {
	t.Helper()
	return mustOK(t, run(t, g, env, in))
}

func grepFixture(t testing.TB) *tools.Env {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{
		"a.go":      "package a\n\nfunc Alpha() {}\nfunc Beta() {}\n// TODO: alpha\n",
		"b.go":      "package b\nfunc alpha() {}\nvar x = 1\n",
		"sub/c.txt": "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n",
		"sub/d.md":  "# Title\nAlpha beta\n",
		"notes.txt": "alpha\nALPHA\nAlpha\n",
	})
	return env
}

func TestGrepContentBasics(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := grepFixture(t)
		tests := []struct {
			name  string
			input map[string]any
			want  string
		}{
			{"case sensitive", map[string]any{"pattern": "alpha"},
				"a.go:5:// TODO: alpha\nb.go:2:func alpha() {}\nnotes.txt:1:alpha"},
			{"ignore case", map[string]any{"pattern": "alpha", "-i": true},
				"a.go:3:func Alpha() {}\na.go:5:// TODO: alpha\nb.go:2:func alpha() {}\nnotes.txt:1:alpha\nnotes.txt:2:ALPHA\nnotes.txt:3:Alpha\nsub/d.md:2:Alpha beta"},
			{"no line numbers", map[string]any{"pattern": "alpha", "-n": false},
				"a.go:// TODO: alpha\nb.go:func alpha() {}\nnotes.txt:alpha"},
			{"explicit line numbers", map[string]any{"pattern": "alpha", "-n": true},
				"a.go:5:// TODO: alpha\nb.go:2:func alpha() {}\nnotes.txt:1:alpha"},
			{"regex", map[string]any{"pattern": `^func [A-Z]\w+\(`},
				"a.go:3:func Alpha() {}\na.go:4:func Beta() {}"},
			{"alternation", map[string]any{"pattern": "Alpha|Beta"},
				"a.go:3:func Alpha() {}\na.go:4:func Beta() {}\nnotes.txt:3:Alpha\nsub/d.md:2:Alpha beta"},
			{"anchored end", map[string]any{"pattern": `\{\}$`},
				"a.go:3:func Alpha() {}\na.go:4:func Beta() {}\nb.go:2:func alpha() {}"},
			{"path subdirectory", map[string]any{"pattern": "e", "path": "sub"},
				"sub/c.txt:1:one\nsub/c.txt:3:three\nsub/c.txt:5:five\nsub/c.txt:7:seven\nsub/c.txt:8:eight\nsub/c.txt:9:nine\nsub/c.txt:10:ten\nsub/d.md:1:# Title\nsub/d.md:2:Alpha beta"},
			{"single file", map[string]any{"pattern": "t", "path": "sub/c.txt"},
				"sub/c.txt:2:two\nsub/c.txt:3:three\nsub/c.txt:8:eight\nsub/c.txt:10:ten"},
			{"absolute path", map[string]any{"pattern": "Beta", "path": filepath.Join(env.Cwd, "a.go")},
				"a.go:4:func Beta() {}"},
			{"empty-match pattern matches every line", map[string]any{"pattern": "x*", "path": "b.go"},
				"b.go:1:package b\nb.go:2:func alpha() {}\nb.go:3:var x = 1"},
			{"literal special characters", map[string]any{"pattern": `func Beta\(\) \{\}`},
				"a.go:4:func Beta() {}"},
			{"dot star", map[string]any{"pattern": "f.*a"},
				"a.go:3:func Alpha() {}\na.go:4:func Beta() {}\nb.go:2:func alpha() {}"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := grep(t, g, env, tc.input); got != tc.want {
					t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
				}
			})
		}
	})
}

func TestGrepModes(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := grepFixture(t)
		tests := []struct {
			name  string
			input map[string]any
			want  string
		}{
			{"files with matches", map[string]any{"pattern": "alpha", "-i": true, "output_mode": "files_with_matches"},
				"a.go\nb.go\nnotes.txt\nsub/d.md"},
			{"count", map[string]any{"pattern": "alpha", "-i": true, "output_mode": "count"},
				"a.go:2\nb.go:1\nnotes.txt:3\nsub/d.md:1"},
			{"count counts lines not matches", map[string]any{"pattern": "a", "output_mode": "count", "path": "notes.txt", "-i": true},
				"notes.txt:3"},
			{"files no match", map[string]any{"pattern": "zzzzz", "output_mode": "files_with_matches"}, "No matches found."},
			{"content no match", map[string]any{"pattern": "zzzzz"}, "No matches found."},
			{"count no match", map[string]any{"pattern": "zzzzz", "output_mode": "count"}, "No matches found."},
			{"context ignored in files mode", map[string]any{"pattern": "Beta", "output_mode": "files_with_matches", "-C": 3}, "a.go"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := grep(t, g, env, tc.input); got != tc.want {
					t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
				}
			})
		}
	})
}

func TestGrepContext(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := grepFixture(t)
		tests := []struct {
			name  string
			input map[string]any
			want  string
		}{
			{"after", map[string]any{"pattern": "three", "path": "sub/c.txt", "-A": 2},
				"sub/c.txt:3:three\nsub/c.txt-4-four\nsub/c.txt-5-five"},
			{"before", map[string]any{"pattern": "three", "path": "sub/c.txt", "-B": 2},
				"sub/c.txt-1-one\nsub/c.txt-2-two\nsub/c.txt:3:three"},
			{"both via -C", map[string]any{"pattern": "three", "path": "sub/c.txt", "-C": 1},
				"sub/c.txt-2-two\nsub/c.txt:3:three\nsub/c.txt-4-four"},
			{"context alias", map[string]any{"pattern": "three", "path": "sub/c.txt", "context": 1},
				"sub/c.txt-2-two\nsub/c.txt:3:three\nsub/c.txt-4-four"},
			{"-A overrides -C for after", map[string]any{"pattern": "three", "path": "sub/c.txt", "-C": 1, "-A": 3},
				"sub/c.txt-2-two\nsub/c.txt:3:three\nsub/c.txt-4-four\nsub/c.txt-5-five\nsub/c.txt-6-six"},
			{"-B overrides -C for before", map[string]any{"pattern": "three", "path": "sub/c.txt", "-C": 3, "-B": 0},
				"sub/c.txt:3:three\nsub/c.txt-4-four\nsub/c.txt-5-five\nsub/c.txt-6-six"},
			{"clipped at file start", map[string]any{"pattern": "one", "path": "sub/c.txt", "-B": 5},
				"sub/c.txt:1:one"},
			{"clipped at file end", map[string]any{"pattern": "ten", "path": "sub/c.txt", "-A": 5},
				"sub/c.txt:10:ten"},
			{"separate groups get a separator", map[string]any{"pattern": "two|nine", "path": "sub/c.txt", "-C": 1},
				"sub/c.txt-1-one\nsub/c.txt:2:two\nsub/c.txt-3-three\n--\nsub/c.txt-8-eight\nsub/c.txt:9:nine\nsub/c.txt-10-ten"},
			{"touching windows merge without separator", map[string]any{"pattern": "two|five", "path": "sub/c.txt", "-C": 1},
				"sub/c.txt-1-one\nsub/c.txt:2:two\nsub/c.txt-3-three\nsub/c.txt-4-four\nsub/c.txt:5:five\nsub/c.txt-6-six"},
			{"overlapping windows merge", map[string]any{"pattern": "two|four", "path": "sub/c.txt", "-C": 2},
				"sub/c.txt-1-one\nsub/c.txt:2:two\nsub/c.txt-3-three\nsub/c.txt:4:four\nsub/c.txt-5-five\nsub/c.txt-6-six"},
			{"adjacent matches are one group", map[string]any{"pattern": "two|three", "path": "sub/c.txt", "-A": 1},
				"sub/c.txt:2:two\nsub/c.txt:3:three\nsub/c.txt-4-four"},
			{"separator between files", map[string]any{"pattern": "Beta|Title", "-C": 1},
				"a.go-3-func Alpha() {}\na.go:4:func Beta() {}\na.go-5-// TODO: alpha\n--\nsub/d.md:1:# Title\nsub/d.md-2-Alpha beta"},
			{"no line numbers with context", map[string]any{"pattern": "three", "path": "sub/c.txt", "-C": 1, "-n": false},
				"sub/c.txt-two\nsub/c.txt:three\nsub/c.txt-four"},
			{"context on the very first line", map[string]any{"pattern": "package a", "path": "a.go", "-A": 1},
				"a.go:1:package a\na.go-2-"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := grep(t, g, env, tc.input); got != tc.want {
					t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
				}
			})
		}
	})
}

func TestGrepGlobFilter(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		tree(t, env.Cwd, map[string]string{
			"a.go":               "needle\n",
			"a_test.go":          "needle\n",
			"b.txt":              "needle\n",
			"c.md":               "needle\n",
			"sub/d.go":           "needle\n",
			"sub/d_test.go":      "needle\n",
			"sub/deep/e.ts":      "needle\n",
			"sub/deep/f.tsx":     "needle\n",
			"vendor/v.go":        "needle\n",
			"src/lib/x.go":       "needle\n",
			"other/src/lib/y.go": "needle\n",
		})
		tests := []struct {
			name string
			glob string
			want string
		}{
			{"extension any depth", "*.go", "a.go\na_test.go\nother/src/lib/y.go\nsrc/lib/x.go\nsub/d.go\nsub/d_test.go\nvendor/v.go"},
			{"braces", "*.{ts,tsx}", "sub/deep/e.ts\nsub/deep/f.tsx"},
			{"two globs by space", "*.md *.txt", "b.txt\nc.md"},
			{"two globs by comma", "*.md,*.txt", "b.txt\nc.md"},
			{"exclusion", "*.go !*_test.go", "a.go\nother/src/lib/y.go\nsrc/lib/x.go\nsub/d.go\nvendor/v.go"},
			// The last glob that matches wins, as in ripgrep: *.go re-includes the tests.
			{"later glob wins", "!*_test.go *.go", "a.go\na_test.go\nother/src/lib/y.go\nsrc/lib/x.go\nsub/d.go\nsub/d_test.go\nvendor/v.go"},
			{"exclude a directory", "*.go !vendor", "a.go\na_test.go\nother/src/lib/y.go\nsrc/lib/x.go\nsub/d.go\nsub/d_test.go"},
			{"anchored path", "sub/*.go", "sub/d.go\nsub/d_test.go"},
			{"double star", "sub/**/*.ts*", "sub/deep/e.ts\nsub/deep/f.tsx"},
			{"anchored at search root only", "src/lib/*.go", "src/lib/x.go"},
			{"any depth with double star", "**/src/lib/*.go", "other/src/lib/y.go\nsrc/lib/x.go"},
			{"exact name", "a.go", "a.go"},
			{"no match", "*.rs", "No matches found."},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches", "glob": tc.glob})
				if got != tc.want {
					t.Errorf("glob %q:\ngot:\n%s\nwant:\n%s", tc.glob, got, tc.want)
				}
			})
		}
		// The glob is relative to the searched directory.
		got := grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches", "glob": "*.go", "path": "sub"})
		if got != "sub/d.go\nsub/d_test.go" {
			t.Errorf("glob with path: %s", got)
		}
	})
}

func TestGrepMultiline(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		tree(t, env.Cwd, map[string]string{
			"a.txt":    "start\nmiddle one\nmiddle two\nend\nother\nstart\nend\n",
			"crlf.txt": "start\r\nend\r\n",
			"none.txt": "nothing here\n",
		})
		tests := []struct {
			name  string
			input map[string]any
			want  string
		}{
			{"across lines", map[string]any{"pattern": `start\nmiddle`, "multiline": true},
				"a.txt:1:start\na.txt:2:middle one"},
			{"dot does not cross newlines", map[string]any{"pattern": `start.*end`, "multiline": true}, "No matches found."},
			{"dotall flag", map[string]any{"pattern": `(?s)start.*?end`, "multiline": true},
				"a.txt:1:start\na.txt:2:middle one\na.txt:3:middle two\na.txt:4:end\na.txt:6:start\na.txt:7:end\ncrlf.txt:1:start\ncrlf.txt:2:end"},
			{"crlf files", map[string]any{"pattern": `start\nend`, "multiline": true, "path": "crlf.txt"},
				"crlf.txt:1:start\ncrlf.txt:2:end"},
			{"anchors are per line", map[string]any{"pattern": `^end$`, "multiline": true, "path": "a.txt"},
				"a.txt:4:end\na.txt:7:end"},
			{"files mode", map[string]any{"pattern": `start\nend`, "multiline": true, "output_mode": "files_with_matches"},
				"a.txt\ncrlf.txt"},
			{"count mode counts matching lines", map[string]any{"pattern": `middle one\nmiddle two`, "multiline": true, "output_mode": "count"},
				"a.txt:2"},
			{"context", map[string]any{"pattern": `end\nother`, "multiline": true, "-B": 1, "path": "a.txt"},
				"a.txt-3-middle two\na.txt:4:end\na.txt:5:other"},
			{"match ending in newline covers that line only", map[string]any{"pattern": `other\n`, "multiline": true, "path": "a.txt"},
				"a.txt:5:other"},
			{"empty matches at end of file are ignored", map[string]any{"pattern": `^$`, "multiline": true, "path": "none.txt"},
				"No matches found."},
			{"single line pattern still works", map[string]any{"pattern": "nothing", "multiline": true},
				"none.txt:1:nothing here"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := grep(t, g, env, tc.input); got != tc.want {
					t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
				}
			})
		}
		// Without multiline, a pattern that spells out a newline can never match
		// and is reported instead of silently returning nothing.
		contains(t, mustErr(t, run(t, g, env, map[string]any{"pattern": `start\nmiddle`})), "multiline=true")
	})
}

func TestGrepHeadLimit(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		for i := 0; i < 30; i++ {
			writeFile(t, filepath.Join(env.Cwd, fmt.Sprintf("f%02d.txt", i)), "hit one\nhit two\nmiss\n")
		}
		out := grep(t, g, env, map[string]any{"pattern": "hit"})
		if n := len(strings.Split(out, "\n")); n != 60 {
			t.Errorf("default: %d lines, want 60 (under the 250 default, no note)", n)
		}
		notContains(t, out, "truncated")

		out = grep(t, g, env, map[string]any{"pattern": "hit", "head_limit": 5})
		lines := strings.Split(out, "\n")
		if len(lines) != 6 || lines[0] != "f00.txt:1:hit one" || lines[4] != "f02.txt:1:hit one" {
			t.Errorf("limited output:\n%s", out)
		}
		contains(t, lines[5], "output truncated at head_limit=5")

		// Exactly at the limit: nothing was cut, so no note.
		out = grep(t, g, env, map[string]any{"pattern": "hit", "head_limit": 60})
		notContains(t, out, "truncated")
		out = grep(t, g, env, map[string]any{"pattern": "hit", "head_limit": 59})
		contains(t, out, "truncated")

		out = grep(t, g, env, map[string]any{"pattern": "hit", "output_mode": "files_with_matches", "head_limit": 3})
		if out != "f00.txt\nf01.txt\nf02.txt\n[output truncated at head_limit=3 lines; narrow with path or glob, or raise head_limit]" {
			t.Errorf("files mode: %s", out)
		}
		out = grep(t, g, env, map[string]any{"pattern": "hit", "output_mode": "count", "head_limit": 2})
		contains(t, out, "f00.txt:2\nf01.txt:2\n[output truncated")

		// 0 lifts the default cap.
		big := testEnv(t)
		for i := 0; i < 400; i++ {
			writeFile(t, filepath.Join(big.Cwd, fmt.Sprintf("g%03d.txt", i)), "hit\n")
		}
		out = grep(t, g, big, map[string]any{"pattern": "hit", "output_mode": "files_with_matches"})
		if n := len(strings.Split(out, "\n")); n != 251 {
			t.Errorf("default cap: %d lines, want 250 + note", n)
		}
		out = grep(t, g, big, map[string]any{"pattern": "hit", "output_mode": "files_with_matches", "head_limit": 0})
		if n := len(strings.Split(out, "\n")); n != 400 {
			t.Errorf("unlimited: %d lines, want 400", n)
		}
		contains(t, mustErr(t, run(t, g, big, map[string]any{"pattern": "hit", "head_limit": -1})), "head_limit must not be negative")

		// A separator is never the last line of a truncated result.
		ctxEnv := testEnv(t)
		writeFile(t, filepath.Join(ctxEnv.Cwd, "a.txt"), "x\n\n\ny\n\n\nx\n")
		out = grep(t, g, ctxEnv, map[string]any{"pattern": "x", "-A": 0, "-B": 1, "head_limit": 2})
		if body := strings.Split(out, "\n[output truncated")[0]; strings.HasSuffix(body, "--") {
			t.Errorf("dangling separator:\n%s", out)
		}
	})
}

func TestGrepSkipsBinaryAndHugeFiles(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		tree(t, env.Cwd, map[string]string{
			"text.txt":   "needle\n",
			"nul.bin":    "needle\x00binary",
			"nul_late":   strings.Repeat("filler line\n", 10000) + "needle\n\x00",
			"png.dat":    "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRneedle",
			"utf16.txt":  "\xff\xfen\x00e\x00e\x00d\x00l\x00e\x00",
			"almost.txt": "needle at the end\n" + strings.Repeat("x", 1000),
		})
		// 4MB is the limit: a file of exactly 4MB is searched, one byte more is not.
		limit := 4 << 20
		writeFile(t, filepath.Join(env.Cwd, "exactly4mb.txt"), "needle\n"+strings.Repeat("y", limit-7))
		writeFile(t, filepath.Join(env.Cwd, "over4mb.txt"), "needle\n"+strings.Repeat("y", limit-6))
		got := grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches"})
		want := "almost.txt\nexactly4mb.txt\ntext.txt"
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
		// Explicitly naming a binary file does not search it either.
		if got := grep(t, g, env, map[string]any{"pattern": "needle", "path": "nul.bin"}); got != "No matches found." {
			t.Errorf("explicit binary file: %s", got)
		}
	})
}

func TestGrepGitignoreAndHiddenFiles(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		tree(t, env.Cwd, map[string]string{
			".gitignore":            "*.log\nbuild/\n/root_only.txt\n!important.log\n",
			"a.txt":                 "needle\n",
			"debug.log":             "needle\n",
			"important.log":         "needle\n",
			"build/out.txt":         "needle\n",
			"src/build/x.txt":       "needle\n",
			"root_only.txt":         "needle\n",
			"sub/root_only.txt":     "needle\n",
			"sub/.gitignore":        "*.tmp\n!keep.tmp\n",
			"sub/a.tmp":             "needle\n",
			"sub/keep.tmp":          "needle\n",
			".hidden/h.txt":         "needle\n",
			".env":                  "needle\n",
			".git/config":           "needle\n",
			".git/objects/pack/p":   "needle\n",
			"node_modules/x/y.js":   "needle\n",
			"deep/er/.gitignore":    "secret*\n",
			"deep/er/secret.txt":    "needle\n",
			"deep/er/public.txt":    "needle\n",
			"deep/secret_other.txt": "needle\n",
		})
		got := grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches"})
		// "build/" is unanchored, so src/build is ignored too; "/root_only.txt" is
		// anchored, so sub/root_only.txt is not.
		want := strings.Join([]string{
			".env", ".hidden/h.txt", "a.txt", "deep/er/public.txt", "deep/secret_other.txt", "important.log",
			"node_modules/x/y.js", "sub/keep.tmp", "sub/root_only.txt",
		}, "\n")
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
		// Searching a subdirectory still obeys the parent's rules.
		got = grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches", "path": "sub"})
		if got != "sub/keep.tmp\nsub/root_only.txt" {
			t.Errorf("sub: %s", got)
		}
		// Explicitly searching an ignored directory works.
		got = grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches", "path": "build"})
		if got != "build/out.txt" {
			t.Errorf("explicit ignored dir: %s", got)
		}
		// An explicitly named file is searched even if ignored.
		got = grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches", "path": "debug.log"})
		if got != "debug.log" {
			t.Errorf("explicit ignored file: %s", got)
		}
	})
}

func TestGrepLineTextHandling(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		long := strings.Repeat("x", 400) + "NEEDLE" + strings.Repeat("y", 400)
		uni := strings.Repeat("é", 350) + "NEEDLE"
		tree(t, env.Cwd, map[string]string{
			"long.txt":    long + "\n",
			"uni.txt":     uni + "\n",
			"latin1.txt":  "caf\xe9 NEEDLE\n",
			"crlf.txt":    "one NEEDLE\r\ntwo\r\n",
			"unicode.txt": "日本語 NEEDLE 🎉\n",
			"tabs.txt":    "\tindented NEEDLE\t\n",
			"empty.txt":   "",
		})
		got := grep(t, g, env, map[string]any{"pattern": "NEEDLE"})
		lines := strings.Split(got, "\n")
		byFile := map[string]string{}
		for _, l := range lines {
			byFile[l[:strings.Index(l, ":")]] = l
		}
		if l := byFile["long.txt"]; !strings.HasPrefix(l, "long.txt:1:"+strings.Repeat("x", 300)) || !strings.HasSuffix(l, "… [+506 chars]") {
			t.Errorf("long line: %q", l[:min(len(l), 80)])
		}
		if l := byFile["uni.txt"]; !strings.HasSuffix(l, "… [+56 chars]") || !strings.HasPrefix(l, "uni.txt:1:"+strings.Repeat("é", 300)) {
			t.Errorf("long unicode line: %q", l[:min(len(l), 80)])
		}
		if l := byFile["latin1.txt"]; l != "latin1.txt:1:caf\uFFFD NEEDLE" {
			t.Errorf("invalid utf-8 line: %q", l)
		}
		if l := byFile["crlf.txt"]; l != "crlf.txt:1:one NEEDLE" {
			t.Errorf("crlf line should lose its CR: %q", l)
		}
		if l := byFile["unicode.txt"]; l != "unicode.txt:1:日本語 NEEDLE 🎉" {
			t.Errorf("unicode: %q", l)
		}
		if l := byFile["tabs.txt"]; l != "tabs.txt:1:\tindented NEEDLE\t" {
			t.Errorf("tabs: %q", l)
		}
		// $ matches before the CR of a CRLF line.
		if got := grep(t, g, env, map[string]any{"pattern": "NEEDLE$", "path": "crlf.txt"}); got != "crlf.txt:1:one NEEDLE" {
			t.Errorf("crlf anchor: %q", got)
		}
		// Unicode-aware literal and case folding.
		if got := grep(t, g, env, map[string]any{"pattern": "日本語", "path": "unicode.txt"}); !strings.Contains(got, "日本語") {
			t.Errorf("unicode search failed: %q", got)
		}
		if got := grep(t, g, env, map[string]any{"pattern": "CAFÉ", "-i": true, "path": "uni.txt"}); got != "No matches found." {
			t.Errorf("unexpected: %q", got)
		}
	})
}

func TestGrepPatternErrors(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := grepFixture(t)
		tests := []struct {
			name  string
			input any
			want  string
		}{
			{"empty", map[string]any{"pattern": ""}, "pattern is required"},
			{"missing", map[string]any{}, "pattern is required"},
			{"unclosed paren", map[string]any{"pattern": "(abc"}, `invalid regex "(abc"`},
			{"lookahead", map[string]any{"pattern": `foo(?=bar)`}, "invalid regex"},
			{"backreference", map[string]any{"pattern": `(a)\1`}, "invalid regex"},
			{"unclosed class", map[string]any{"pattern": "[abc"}, "invalid regex"},
			{"bad repetition", map[string]any{"pattern": "a**"}, "invalid regex"},
			{"newline escape without multiline", map[string]any{"pattern": `a\nb`}, "multiline=true"},
			{"raw newline without multiline", map[string]any{"pattern": "a\nb"}, "multiline=true"},
			{"bad mode", map[string]any{"pattern": "x", "output_mode": "lines"}, "output_mode must be"},
			{"negative context", map[string]any{"pattern": "x", "-C": -1}, "-C must not be negative"},
			{"negative after", map[string]any{"pattern": "x", "-A": -3}, "-A must not be negative"},
			{"wrong type", `{"pattern":"x","-i":"yes"}`, `argument "-i" must be a boolean`},
			{"wrong type context", `{"pattern":"x","-A":"2"}`, `argument "-A" must be an integer`},
			{"path missing", map[string]any{"pattern": "x", "path": "nope"}, "not found"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				contains(t, mustErr(t, run(t, g, env, tc.input)), tc.want)
			})
		}
	})
}

func TestGrepPatternsRipgrepRejectsButGoAccepts(t *testing.T) {
	// ripgrep's regex dialect refuses an unescaped brace that Go treats as a
	// literal. The tool must fall back to the Go engine instead of failing.
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		tree(t, env.Cwd, map[string]string{"x.go": "var v interface{}\nfoo{\n", "y.go": "nothing\n"})
		if got := grep(t, g, env, map[string]any{"pattern": "interface{}"}); got != "x.go:1:var v interface{}" {
			t.Errorf("interface{}: %q", got)
		}
		if got := grep(t, g, env, map[string]any{"pattern": "foo{"}); got != "x.go:2:foo{" {
			t.Errorf("foo{: %q", got)
		}
	})
}

func TestGrepCaseFoldingAndFlags(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		writeFile(t, filepath.Join(env.Cwd, "f.txt"), "Hello\nhello\nHELLO\nhELLo\n")
		if got := grep(t, g, env, map[string]any{"pattern": "(?i)hello", "output_mode": "count"}); got != "f.txt:4" {
			t.Errorf("inline flag: %q", got)
		}
		if got := grep(t, g, env, map[string]any{"pattern": "hello", "-i": true, "output_mode": "count"}); got != "f.txt:4" {
			t.Errorf("-i: %q", got)
		}
		if got := grep(t, g, env, map[string]any{"pattern": "hello", "-i": false, "output_mode": "count"}); got != "f.txt:1" {
			t.Errorf("-i false: %q", got)
		}
	})
}

func TestGrepOrdering(t *testing.T) {
	// Sorted by full path, byte-wise: "a.txt" < "a/x.txt" because '.' < '/'.
	// A directory-by-directory traversal (what ripgrep does) would put a/x.txt
	// first, so this pins the ordering both engines must reproduce.
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		tree(t, env.Cwd, map[string]string{
			"a.txt": "hit\n", "a/x.txt": "hit\n", "a-b.txt": "hit\n", "a_b.txt": "hit\n", "B.txt": "hit\n",
			"b/c/d.txt": "hit\n", "b/c.txt": "hit\n", "é.txt": "hit\n", "z.txt": "hit\n", "10.txt": "hit\n", "9.txt": "hit\n",
		})
		got := grep(t, g, env, map[string]any{"pattern": "hit", "output_mode": "files_with_matches"})
		want := "10.txt\n9.txt\nB.txt\na-b.txt\na.txt\na/x.txt\na_b.txt\nb/c.txt\nb/c/d.txt\nz.txt\né.txt"
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestGrepManyFilesIsDeterministic(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		for i := 0; i < 300; i++ {
			writeFile(t, filepath.Join(env.Cwd, fmt.Sprintf("d%d/f%03d.txt", i%7, i)), fmt.Sprintf("line a\nneedle %d\nline c\n", i))
		}
		first := grep(t, g, env, map[string]any{"pattern": "needle", "head_limit": 0})
		for i := 0; i < 5; i++ {
			if again := grep(t, g, env, map[string]any{"pattern": "needle", "head_limit": 0}); again != first {
				t.Fatalf("run %d differs from the first", i)
			}
		}
		lines := strings.Split(first, "\n")
		if len(lines) != 300 {
			t.Fatalf("%d lines", len(lines))
		}
		paths := make([]string, len(lines))
		for i, l := range lines {
			paths[i] = l[:strings.Index(l, ":")]
		}
		for i := 1; i < len(paths); i++ {
			if paths[i-1] >= paths[i] {
				t.Fatalf("not sorted at %d: %s >= %s", i, paths[i-1], paths[i])
			}
		}
	})
}

func TestGrepDoesNotFollowSymlinks(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		outside := realTemp(t)
		tree(t, outside, map[string]string{"secret.txt": "needle outside\n", "dir/inner.txt": "needle outside dir\n"})
		tree(t, env.Cwd, map[string]string{"real.txt": "needle real\n", "sub/x.txt": "needle sub\n"})
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(env.Cwd, "link_file.txt")); err != nil {
			t.Skip("symlinks unsupported")
		}
		os.Symlink(filepath.Join(outside, "dir"), filepath.Join(env.Cwd, "link_dir"))
		os.Symlink(".", filepath.Join(env.Cwd, "sub", "loop"))
		os.Symlink("real.txt", filepath.Join(env.Cwd, "alias.txt"))
		done := make(chan string, 1)
		go func() {
			done <- grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files_with_matches"})
		}()
		select {
		case got := <-done:
			if got != "real.txt\nsub/x.txt" {
				t.Errorf("symlinks must not be searched:\n%s", got)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("grep did not terminate on a symlink loop")
		}
		// Naming the link explicitly is a read of its target and goes through
		// permission with the target's real path.
		h := &hooks{}
		withHooks(env, h)
		got := grep(t, g, env, map[string]any{"pattern": "needle", "path": "link_file.txt"})
		if !strings.Contains(got, "needle outside") {
			t.Errorf("explicit symlinked file: %q", got)
		}
		if p := h.Requests()[0].Paths[0]; p != filepath.Join(outside, "secret.txt") {
			t.Errorf("permission engine saw %q", p)
		}
	})
}

func TestGrepPermission(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := grepFixture(t)
		h := &hooks{}
		withHooks(env, h)
		grep(t, g, env, map[string]any{"pattern": "Beta", "path": "sub"})
		r := h.Requests()[0]
		if r.Tool != "grep" || r.Writes || len(r.Paths) != 1 || r.Paths[0] != filepath.Join(env.Cwd, "sub") || r.Risk != perm.RiskLow {
			t.Errorf("request = %+v", r)
		}
		other := realTemp(t)
		writeFile(t, filepath.Join(other, "o.txt"), "Beta\n")
		h.denyPerm = func(r perm.Request) string {
			if !strings.HasPrefix(r.Paths[0], env.Root) {
				return "outside the project"
			}
			return ""
		}
		contains(t, mustErr(t, run(t, g, env, map[string]any{"pattern": "Beta", "path": other})), "permission denied", "outside the project")
		// Inside the root it is fine.
		grep(t, g, env, map[string]any{"pattern": "Beta"})
	})
}

func TestGrepContextCancellation(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := grepFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := g.Run(ctx, &tools.Call{Input: []byte(`{"pattern":"alpha"}`), Env: env})
		if err != nil {
			t.Fatal(err)
		}
		contains(t, mustErr(t, res), "cancelled")
	})
}

func needShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh: the fake ripgrep scripts need one")
	}
}

func TestGrepRipgrepUnavailableOrBrokenFallsBack(t *testing.T) {
	needShell(t)
	env := grepFixture(t)
	want := grep(t, &Grep{DisableRipgrep: true}, env, map[string]any{"pattern": "alpha", "-i": true})
	// Missing binary.
	if got := grep(t, &Grep{RipgrepPath: filepath.Join(env.Cwd, "no-such-rg")}, env, map[string]any{"pattern": "alpha", "-i": true}); got != want {
		t.Errorf("missing rg: %q", got)
	}
	// A "ripgrep" that fails without output.
	bad := filepath.Join(realTemp(t), "rg")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho boom >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := grep(t, &Grep{RipgrepPath: bad}, env, map[string]any{"pattern": "alpha", "-i": true}); got != want {
		t.Errorf("failing rg: %q", got)
	}
	// One that crashes.
	crash := filepath.Join(realTemp(t), "rg")
	os.WriteFile(crash, []byte("#!/bin/sh\nkill -9 $$\n"), 0o755)
	if got := grep(t, &Grep{RipgrepPath: crash}, env, map[string]any{"pattern": "alpha", "-i": true}); got != want {
		t.Errorf("crashing rg: %q", got)
	}
}

func TestGrepRipgrepIsNeverGivenUnsafeArguments(t *testing.T) {
	// Patterns and globs reach ripgrep as data, never through a shell and never
	// as flags. A fake rg records its argv.
	needShell(t)
	dir := realTemp(t)
	record := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "rg")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + record + "; done\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := grepFixture(t)
	g := &Grep{RipgrepPath: fake}
	nasty := "$(touch " + filepath.Join(dir, "pwned") + ");`id`;--files -e"
	grep(t, g, env, map[string]any{"pattern": nasty, "glob": "-x; touch " + filepath.Join(dir, "pwned2"), "-i": true})
	argv := readFileT(t, record)
	contains(t, argv, "--regexp="+nasty, "--glob=-x;", "--ignore-case", "--no-config", "--files-with-matches")
	if exists(filepath.Join(dir, "pwned")) || exists(filepath.Join(dir, "pwned2")) {
		t.Fatalf("a shell interpreted the arguments")
	}
	// The pattern is a single argv element attached to --regexp=.
	for _, line := range strings.Split(argv, "\n") {
		if line == nasty {
			t.Errorf("pattern passed as a bare argument")
		}
	}
}

func TestEnginesProduceIdenticalOutput(t *testing.T) {
	es := engines(t)
	if len(es) < 2 {
		t.Skip("ripgrep not installed")
	}
	env := testEnv(t)
	rng := rand.New(rand.NewSource(42))
	words := []string{"alpha", "beta", "gamma", "delta", "Alpha", "BETA", "needle", "haystack", "foo_bar", "x", "", "  indented", "tab\tsep", "trailing ", "日本語", "café", "12345", "end."}
	exts := []string{".go", ".txt", ".md", ".log", ".json", ""}
	for i := 0; i < 150; i++ {
		var sb strings.Builder
		for l := 0; l < 5+rng.Intn(60); l++ {
			for w := 0; w < rng.Intn(6); w++ {
				if w > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(words[rng.Intn(len(words))])
			}
			if rng.Intn(15) == 0 {
				sb.WriteString("\r")
			}
			sb.WriteByte('\n')
		}
		content := sb.String()
		switch i % 25 {
		case 3:
			content += "\x00binary"
		case 9:
			content = strings.TrimSuffix(content, "\n")
		case 14:
			content = ""
		}
		dir := fmt.Sprintf("d%d", i%9)
		if i%11 == 0 {
			dir += "/.hidden"
		}
		if i%17 == 0 {
			dir += "/build"
		}
		writeFile(t, filepath.Join(env.Cwd, dir, fmt.Sprintf("file%03d%s", i, exts[i%len(exts)])), content)
	}
	tree(t, env.Cwd, map[string]string{
		".gitignore": "*.log\nbuild/\n!keep.log\n", "d1/.gitignore": "*.json\n", "d2/keep.log": "needle\n", "d3/x.log": "needle\n",
		".git/config": "needle\n",
	})

	queries := []map[string]any{
		{"pattern": "needle"},
		{"pattern": "needle", "-i": true, "-n": false},
		{"pattern": "alpha|beta", "-i": true, "-C": 2},
		{"pattern": "^foo", "-B": 1},
		{"pattern": "end\\.$", "-A": 1},
		{"pattern": "haystack", "output_mode": "files_with_matches"},
		{"pattern": "haystack", "output_mode": "count"},
		{"pattern": "haystack", "glob": "*.go", "output_mode": "files_with_matches"},
		{"pattern": "haystack", "glob": "*.{md,txt} !d3/**", "-C": 1},
		{"pattern": "haystack", "glob": "d4/*.txt"},
		{"pattern": "\\bgamma\\b", "output_mode": "count"},
		{"pattern": "[A-Z]{4}", "-C": 1, "head_limit": 40},
		{"pattern": "日本語", "output_mode": "count"},
		{"pattern": "caf.", "-i": true},
		{"pattern": "  indented", "-A": 1},
		{"pattern": "trailing $"},
		{"pattern": "alpha\\nbeta", "multiline": true},
		{"pattern": "gamma\\s+delta", "multiline": true, "output_mode": "count"},
		{"pattern": "x*", "path": "d1", "output_mode": "count"},
		{"pattern": "needle", "path": "d2"},
		{"pattern": "12345", "head_limit": 7},
		{"pattern": "zzzz-no-match"},
	}
	for _, q := range queries {
		t.Run(fmt.Sprint(q), func(t *testing.T) {
			outs := make([]string, len(es))
			for i, e := range es {
				res := run(t, e.tool, env, q)
				outs[i] = res.Text
				if res.IsError {
					t.Fatalf("%s engine failed: %s", e.name, res.Text)
				}
			}
			if outs[0] != outs[1] {
				a := strings.Split(outs[0], "\n")
				b := strings.Split(outs[1], "\n")
				for i := 0; i < len(a) && i < len(b); i++ {
					if a[i] != b[i] {
						t.Fatalf("engines differ at line %d:\n %s: %q\n %s: %q\n(%d vs %d lines)", i+1, es[0].name, a[i], es[1].name, b[i], len(a), len(b))
					}
				}
				t.Fatalf("engines differ in length: %d vs %d lines", len(a), len(b))
			}
		})
	}
}

func TestGrepLargeTreePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := testEnv(t)
		for d := 0; d < 40; d++ {
			for f := 0; f < 50; f++ {
				content := strings.Repeat("some ordinary source line here\n", 40)
				if f == 7 {
					content += "the needle is here\n"
				}
				writeFile(t, filepath.Join(env.Cwd, fmt.Sprintf("pkg%02d/f%02d.go", d, f)), content)
			}
		}
		start := time.Now()
		got := grep(t, g, env, map[string]any{"pattern": "needle", "output_mode": "count"})
		if d := time.Since(start); d > 90*time.Second {
			t.Errorf("2000 files took %v", d)
		}
		if n := len(strings.Split(got, "\n")); n != 40 {
			t.Errorf("%d matching files, want 40", n)
		}
	})
}

func TestGrepTinyOutputBudgetStillWorks(t *testing.T) {
	forEachEngine(t, func(t *testing.T, g *Grep) {
		env := grepFixture(t)
		env.Limits.MaxOutputChars = 60
		res := run(t, g, env, map[string]any{"pattern": "alpha", "-i": true})
		if !res.Truncated || res.Handle == "" {
			t.Errorf("oversized output should be truncated with a recall handle: %+v", res)
		}
	})
}

func TestGrepActuallyUsesRipgrepForCandidates(t *testing.T) {
	needShell(t)
	realRg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	dir := realTemp(t)
	logFile := filepath.Join(dir, "calls")
	wrapper := filepath.Join(dir, "rg")
	script := "#!/bin/sh\necho \"$*\" >> " + logFile + "\nexec " + realRg + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := grepFixture(t)
	g := &Grep{RipgrepPath: wrapper}

	want := grep(t, &Grep{DisableRipgrep: true}, env, map[string]any{"pattern": "alpha", "-i": true})
	if got := grep(t, g, env, map[string]any{"pattern": "alpha", "-i": true}); got != want {
		t.Errorf("output differs from the Go engine:\n%s\nvs\n%s", got, want)
	}
	calls := readFileT(t, logFile)
	contains(t, calls, "--files-with-matches", "--null", "--hidden", "--no-require-git", "--no-config", "--regexp=alpha", "--ignore-case", "--glob=!.git", "--max-filesize=4M", "-- .")
	if strings.Count(calls, "\n") != 1 {
		t.Errorf("ripgrep should run exactly once per search:\n%s", calls)
	}

	// A single-file search and a multiline search never start ripgrep.
	os.Remove(logFile)
	grep(t, g, env, map[string]any{"pattern": "Beta", "path": "a.go"})
	grep(t, g, env, map[string]any{"pattern": "Alpha\nbeta", "multiline": true})
	if exists(logFile) {
		t.Errorf("ripgrep should not be used here:\n%s", readFileT(t, logFile))
	}

	// interface{} is rejected by ripgrep (exit 2, no output): the Go engine answers.
	tree(t, env.Cwd, map[string]string{"x.go": "var v interface{}\n"})
	if got := grep(t, g, env, map[string]any{"pattern": "interface{}"}); got != "x.go:1:var v interface{}" {
		t.Errorf("fallback result: %q", got)
	}
	if !exists(logFile) {
		t.Errorf("ripgrep should have been tried first")
	}
}

func TestGrepRipgrepOutputOverflowFallsBackWithoutHanging(t *testing.T) {
	// A runaway "ripgrep" that floods stdout must be killed at the cap (an
	// undrained pipe would block it forever) and the Go engine must answer.
	needShell(t)
	dir := realTemp(t)
	flood := filepath.Join(dir, "rg")
	script := "#!/bin/sh\nhead -c 80000000 /dev/zero | tr '\\000' 'x'\n"
	if err := os.WriteFile(flood, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := grepFixture(t)
	want := grep(t, &Grep{DisableRipgrep: true}, env, map[string]any{"pattern": "alpha", "-i": true})
	start := time.Now()
	got := grep(t, &Grep{RipgrepPath: flood}, env, map[string]any{"pattern": "alpha", "-i": true})
	if got != want {
		t.Errorf("fallback output differs:\n%s\nvs\n%s", got, want)
	}
	if d := time.Since(start); d > rgTimeout {
		t.Errorf("took %v: the flooding process was not killed", d)
	}
}

func TestGrepWordBoundaryPatternsAreNotDelegatedToRipgrep(t *testing.T) {
	// Ripgrep's \b is Unicode-aware, Go's is ASCII-only, so ripgrep would skip
	// "éfoo" for \bfoo\b although the RE2 semantics the tool documents match it.
	needShell(t)
	realRg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	dir := realTemp(t)
	logFile := filepath.Join(dir, "calls")
	wrapper := filepath.Join(dir, "rg")
	os.WriteFile(wrapper, []byte("#!/bin/sh\necho \"$*\" >> "+logFile+"\nexec "+realRg+" \"$@\"\n"), 0o755)
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"a.txt": "éfoo\n", "b.txt": "a foo b\n"})
	for _, e := range []*Grep{{DisableRipgrep: true}, {RipgrepPath: wrapper}} {
		got := grep(t, e, env, map[string]any{"pattern": `\bfoo\b`, "output_mode": "files_with_matches"})
		if got != "a.txt\nb.txt" {
			t.Errorf("got %q", got)
		}
	}
	if exists(logFile) {
		t.Errorf("ripgrep must not be consulted for a pattern whose semantics differ:\n%s", readFileT(t, logFile))
	}
	// A pattern whose semantics agree still goes to ripgrep.
	grep(t, &Grep{RipgrepPath: wrapper}, env, map[string]any{"pattern": `foo`, "output_mode": "files_with_matches"})
	if !exists(logFile) {
		t.Errorf("ripgrep should have been used for a plain pattern")
	}
}

func TestRgCompatible(t *testing.T) {
	for pat, want := range map[string]bool{
		`foo`: true, `foo.*bar`: true, `\w+\d`: true, `[^)]*`: true, `\s`: true, `\.go$`: true,
		`\bfoo`: false, `foo\B`: false, `\W`: false, `\D+`: false, `\S`: false, `\PL`: false, `[[:^alpha:]]`: false,
	} {
		if got := rgCompatible(pat); got != want {
			t.Errorf("rgCompatible(%q) = %v, want %v", pat, got, want)
		}
	}
}
