package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// tree creates files (path -> content) under dir; a key ending in "/" makes a
// directory.
func tree(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		writeFile(t, filepath.Join(dir, p), c)
	}
}

// globList runs glob and returns the listed paths (footer notes dropped).
func globList(t *testing.T, env *tools.Env, in map[string]any) []string {
	t.Helper()
	text := mustOK(t, run(t, Glob{}, env, in))
	if strings.HasPrefix(text, "No files matched") {
		return nil
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "[") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestGlobPatterns(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{
		"main.go":                     "",
		"README.md":                   "",
		"go.mod":                      "",
		"src/a.go":                    "",
		"src/b.go":                    "",
		"src/a_test.go":               "",
		"src/sub/c.go":                "",
		"src/sub/deep/d.go":           "",
		"docs/guide.md":               "",
		"docs/img/logo.png":           "",
		".github/workflows/ci.yml":    "",
		".hidden":                     "",
		"file1.txt":                   "",
		"file2.txt":                   "",
		"fileA.txt":                   "",
		"tests/":                      "",
		"src/tests/":                  "",
		"src/sub/tests/case.go":       "",
		"UPPER.GO":                    "",
		"dir with space/file x.txt":   "",
		"unicode/日本語.md":              "",
		"src/main.go/nested_file.txt": "",
	})
	tests := []struct {
		name    string
		pattern string
		path    string
		want    []string
	}{
		{"top level star", "*.go", "", []string{"main.go"}},
		{"top level md", "*.md", "", []string{"README.md"}},
		{"recursive go", "**/*.go", "", []string{"main.go", "src/a.go", "src/a_test.go", "src/b.go", "src/sub/c.go", "src/sub/deep/d.go", "src/sub/tests/case.go"}},
		{"under src only", "src/**/*.go", "", []string{"src/a.go", "src/a_test.go", "src/b.go", "src/sub/c.go", "src/sub/deep/d.go", "src/sub/tests/case.go"}},
		{"src children only", "src/*.go", "", []string{"src/a.go", "src/a_test.go", "src/b.go"}},
		{"src/** lists everything below", "src/sub/**", "", []string{"src/sub/c.go", "src/sub/deep/d.go", "src/sub/tests/case.go"}},
		{"braces", "**/*.{md,mod}", "", []string{"README.md", "docs/guide.md", "go.mod", "unicode/日本語.md"}},
		{"brace dirs", "{src,docs}/*.*", "", []string{"docs/guide.md", "src/a.go", "src/a_test.go", "src/b.go"}},
		{"question mark", "file?.txt", "", []string{"file1.txt", "file2.txt", "fileA.txt"}},
		{"class", "file[0-9].txt", "", []string{"file1.txt", "file2.txt"}},
		{"negated class", "file[!0-9].txt", "", []string{"fileA.txt"}},
		{"hidden files match", ".*", "", []string{".hidden"}},
		{"hidden directories are searched", "**/*.yml", "", []string{".github/workflows/ci.yml"}},
		{"case sensitive", "*.go", "", []string{"main.go"}},
		{"case sensitive upper", "*.GO", "", []string{"UPPER.GO"}},
		{"spaces", "dir with space/*.txt", "", []string{"dir with space/file x.txt"}},
		{"unicode name", "unicode/*", "", []string{"unicode/日本語.md"}},
		{"unicode question mark", "unicode/???.md", "", []string{"unicode/日本語.md"}},
		{"leading dot slash", "./src/*.go", "", []string{"src/a.go", "src/a_test.go", "src/b.go"}},
		{"double slash", "src//b.go", "", []string{"src/b.go"}},
		{"file with dir-like name", "src/main.go/*", "", []string{"src/main.go/nested_file.txt"}},
		{"dirs only", "**/tests/", "", []string{"src/sub/tests/", "src/tests/", "tests/"}},
		{"dirs only under src", "src/*/", "", []string{"src/main.go/", "src/sub/", "src/tests/"}},
		{"path argument", "*.go", "src", []string{"src/a.go", "src/a_test.go", "src/b.go"}},
		{"path argument recursive", "**/*.go", "src/sub", []string{"src/sub/c.go", "src/sub/deep/d.go", "src/sub/tests/case.go"}},
		{"path with dot", "*.go", ".", []string{"main.go"}},
		{"star does not cross slash", "*", "src/sub", []string{"src/sub/c.go"}},
		{"no match", "*.rs", "", nil},
		{"only star star", "**", "docs", []string{"docs/guide.md", "docs/img/logo.png"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{"pattern": tc.pattern}
			if tc.path != "" {
				in["path"] = tc.path
			}
			got := sortedCopy(globList(t, env, in))
			want := sortedCopy(tc.want)
			if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
				t.Errorf("got  %v\nwant %v", got, want)
			}
		})
	}
}

func TestGlobLeadingSlashIsAnAbsolutePath(t *testing.T) {
	// "/src/*.go" names the filesystem root, as in a shell; a missing directory
	// is reported as such rather than silently reinterpreted.
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"src/a.go": ""})
	contains(t, mustErr(t, run(t, Glob{}, env, map[string]any{"pattern": "/definitely-not-a-dir/*.go"})), "not found")
}

func TestGlobNoMatchHints(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"src/a.go": ""})
	text := mustOK(t, run(t, Glob{}, env, map[string]any{"pattern": "*.go"}))
	contains(t, text, "No files matched", `"**/*.go"`, ".gitignore")
	text = mustOK(t, run(t, Glob{}, env, map[string]any{"pattern": "**/*.rs"}))
	contains(t, text, "No files matched")
	notContains(t, text, "use \"**/")
}

func TestGlobSortsByModificationTimeNewestFirst(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"old.txt": "", "mid.txt": "", "new.txt": "", "b_tie.txt": "", "a_tie.txt": "", "sub/newest.txt": ""})
	base := time.Now().Add(-10 * time.Hour)
	setMtime(t, filepath.Join(env.Cwd, "old.txt"), base)
	setMtime(t, filepath.Join(env.Cwd, "mid.txt"), base.Add(2*time.Hour))
	setMtime(t, filepath.Join(env.Cwd, "b_tie.txt"), base.Add(4*time.Hour))
	setMtime(t, filepath.Join(env.Cwd, "a_tie.txt"), base.Add(4*time.Hour))
	setMtime(t, filepath.Join(env.Cwd, "new.txt"), base.Add(6*time.Hour))
	setMtime(t, filepath.Join(env.Cwd, "sub/newest.txt"), base.Add(8*time.Hour))
	got := globList(t, env, map[string]any{"pattern": "**/*.txt"})
	want := []string{"sub/newest.txt", "new.txt", "a_tie.txt", "b_tie.txt", "mid.txt", "old.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	// Identical modification times fall back to path order: deterministic output.
	same := base
	for _, f := range []string{"old.txt", "mid.txt", "new.txt", "b_tie.txt", "a_tie.txt", "sub/newest.txt"} {
		setMtime(t, filepath.Join(env.Cwd, f), same)
	}
	got = globList(t, env, map[string]any{"pattern": "**/*.txt"})
	want = []string{"a_tie.txt", "b_tie.txt", "mid.txt", "new.txt", "old.txt", "sub/newest.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tie order: got %v\nwant %v", got, want)
	}
}

func TestGlobLimitsResultsAndSaysHowManyMore(t *testing.T) {
	env := testEnv(t)
	for i := 0; i < 250; i++ {
		writeFile(t, filepath.Join(env.Cwd, "files", fmt.Sprintf("f%03d.txt", i)), "")
	}
	text := mustOK(t, run(t, Glob{}, env, map[string]any{"pattern": "**/*.txt"}))
	lines := strings.Split(text, "\n")
	if len(lines) != 201 {
		t.Fatalf("got %d lines, want 200 results + 1 note", len(lines))
	}
	if lines[200] != "[50 more not shown; narrow the pattern or path]" {
		t.Errorf("note = %q", lines[200])
	}
	res := run(t, Glob{}, env, map[string]any{"pattern": "**/*.txt"})
	if res.Meta["matches"] != 250 {
		t.Errorf("meta = %v", res.Meta)
	}
}

func TestGlobGitignore(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"star pattern", map[string]string{".gitignore": "*.log\n", "a.log": "", "b.txt": "", "sub/c.log": "", "sub/d.txt": ""},
			[]string{".gitignore", "b.txt", "sub/d.txt"}},
		{"negation", map[string]string{".gitignore": "*.log\n!keep.log\n", "a.log": "", "keep.log": "", "sub/keep.log": ""},
			[]string{".gitignore", "keep.log", "sub/keep.log"}},
		{"negation before is overridden", map[string]string{".gitignore": "!keep.log\n*.log\n", "keep.log": "", "a.log": ""},
			[]string{".gitignore"}},
		{"dir only pattern", map[string]string{".gitignore": "build/\n", "build/out.o": "", "src/build/x.o": "", "sub/build": "file named build", "ok.txt": ""},
			[]string{".gitignore", "ok.txt", "sub/build"}},
		{"anchored to root", map[string]string{".gitignore": "/build\n", "build/a": "", "sub/build/b": "", "c": ""},
			[]string{".gitignore", "c", "sub/build/b"}},
		{"anchored with middle slash", map[string]string{".gitignore": "docs/*.tmp\n", "docs/a.tmp": "", "docs/sub/b.tmp": "", "x/docs/c.tmp": ""},
			[]string{".gitignore", "docs/sub/b.tmp", "x/docs/c.tmp"}},
		{"double star prefix", map[string]string{".gitignore": "**/cache\n", "cache/a": "", "x/y/cache/b": "", "x/keep": ""},
			[]string{".gitignore", "x/keep"}},
		{"double star middle", map[string]string{".gitignore": "a/**/z\n", "a/z": "", "a/b/c/z": "", "a/b/y": "", "b/z": ""},
			[]string{".gitignore", "a/b/y", "b/z"}},
		{"trailing double star with re-include", map[string]string{".gitignore": "logs/**\n!logs/keep.txt\n", "logs/a.txt": "", "logs/keep.txt": "", "logs/sub/b.txt": ""},
			[]string{".gitignore", "logs/keep.txt"}},
		{"excluded parent cannot be re-included", map[string]string{".gitignore": "vendor/\n!vendor/keep.txt\n", "vendor/keep.txt": "", "vendor/other.txt": "", "a.txt": ""},
			[]string{".gitignore", "a.txt"}},
		{"nested gitignore adds rules", map[string]string{"sub/.gitignore": "*.tmp\n", "sub/a.tmp": "", "sub/b.txt": "", "top.tmp": "", "sub/deep/c.tmp": ""},
			[]string{"sub/.gitignore", "sub/b.txt", "top.tmp"}},
		{"nested gitignore negation beats parent", map[string]string{".gitignore": "*.txt\n", "sub/.gitignore": "!a.txt\n", "sub/a.txt": "", "sub/b.txt": "", "c.txt": "", "keep.md": ""},
			[]string{".gitignore", "keep.md", "sub/.gitignore", "sub/a.txt"}},
		{"nested rules are relative to their directory", map[string]string{"sub/.gitignore": "/only-here.txt\n", "sub/only-here.txt": "", "sub/x/only-here.txt": "", "only-here.txt": ""},
			[]string{"only-here.txt", "sub/.gitignore", "sub/x/only-here.txt"}},
		{"comments blanks and escapes", map[string]string{".gitignore": "# comment\n\n\\#hash.txt\n\\!bang.txt\n", "#hash.txt": "", "!bang.txt": "", "comment": "", "ok.txt": ""},
			[]string{".gitignore", "comment", "ok.txt"}},
		{"trailing spaces trimmed unless escaped", map[string]string{".gitignore": "trim.txt   \nkeep\\ \n", "trim.txt": "", "keep ": "", "keep": ""},
			[]string{".gitignore", "keep"}},
		{"crlf gitignore", map[string]string{".gitignore": "*.log\r\nbuild/\r\n", "a.log": "", "build/x": "", "ok": ""},
			[]string{".gitignore", "ok"}},
		{"character class and question mark", map[string]string{".gitignore": "[ab].txt\nc?.md\n", "a.txt": "", "b.txt": "", "c.txt": "", "c1.md": "", "c12.md": ""},
			[]string{".gitignore", "c.txt", "c12.md"}},
		{"ignore everything then whitelist", map[string]string{".gitignore": "*\n!*/\n!*.go\n", "a.go": "", "b.txt": "", "sub/c.go": "", "sub/d.txt": ""},
			[]string{"a.go", "sub/c.go"}},
		{".git is always skipped", map[string]string{".git/config": "", ".git/objects/ab/cd": "", ".gitignore": "!.git\n", "a": ""},
			[]string{".gitignore", "a"}},
		{".git file (worktree) is skipped too", map[string]string{".git": "gitdir: /elsewhere", "a": ""},
			[]string{"a"}},
		{"leading slash dir", map[string]string{".gitignore": "/out/\n", "out/a": "", "x/out/b": ""},
			[]string{".gitignore", "x/out/b"}},
		{"empty gitignore", map[string]string{".gitignore": "", "a": ""},
			[]string{".gitignore", "a"}},
		{"only comments", map[string]string{".gitignore": "# nothing\n", "a": ""},
			[]string{".gitignore", "a"}},
		{"pattern with regex chars is literal", map[string]string{".gitignore": "a+b.txt\n(x).txt\n", "a+b.txt": "", "aab.txt": "", "(x).txt": "", "x.txt": ""},
			[]string{".gitignore", "aab.txt", "x.txt"}},
		{"unicode names", map[string]string{".gitignore": "日本*\n", "日本語.txt": "", "中文.txt": ""},
			[]string{".gitignore", "中文.txt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			tree(t, env.Cwd, tc.files)
			got := sortedCopy(globList(t, env, map[string]any{"pattern": "**"}))
			want := sortedCopy(tc.want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %v\nwant %v", got, want)
			}
		})
	}
}

func TestGlobIgnoresRulesOfAncestorsWithinTheRepository(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{
		".git/HEAD":          "",
		".gitignore":         "*.gen.go\nnode_modules/\n/dist\n",
		"pkg/a.go":           "",
		"pkg/a.gen.go":       "",
		"pkg/node_modules/x": "",
		"pkg/sub/.gitignore": "*.tmp\n",
		"pkg/sub/b.tmp":      "",
		"pkg/sub/b.go":       "",
		"dist/out.js":        "",
		"pkg/dist/keep.js":   "",
	})
	got := sortedCopy(globList(t, env, map[string]any{"pattern": "**", "path": "pkg"}))
	want := []string{"pkg/a.go", "pkg/dist/keep.js", "pkg/sub/.gitignore", "pkg/sub/b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	// Explicitly searching an ignored directory works: the rule ignores it as a
	// child of its parent, not the files inside it.
	got = globList(t, env, map[string]any{"pattern": "**", "path": "pkg/node_modules"})
	if !reflect.DeepEqual(got, []string{"pkg/node_modules/x"}) {
		t.Errorf("explicit ignored dir: %v", got)
	}
	got = globList(t, env, map[string]any{"pattern": "*", "path": "dist"})
	if !reflect.DeepEqual(got, []string{"dist/out.js"}) {
		t.Errorf("explicit ignored dir: %v", got)
	}
}

func TestGlobIgnoreOriginStopsAtRepositoryRoot(t *testing.T) {
	// A .gitignore above the repository must not leak into it.
	outer := realTemp(t)
	repo := filepath.Join(outer, "repo")
	tree(t, outer, map[string]string{
		".gitignore":      "*.txt\n",
		"repo/.git/HEAD":  "",
		"repo/sub/a.txt":  "",
		"repo/sub/b.go":   "",
		"repo/.gitignore": "*.log\n",
		"repo/sub/c.log":  "",
	})
	env := testEnv(t)
	env.Cwd, env.Root = outer, outer
	got := sortedCopy(globList(t, env, map[string]any{"pattern": "**", "path": "repo/sub"}))
	want := []string{"repo/sub/a.txt", "repo/sub/b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v (repo=%s)", got, want, repo)
	}
	// Without a repository the project root bounds the search for ancestors' rules.
	env2 := testEnv(t)
	tree(t, env2.Cwd, map[string]string{".gitignore": "*.txt\n", "sub/a.txt": "", "sub/b.go": ""})
	got = globList(t, env2, map[string]any{"pattern": "**", "path": "sub"})
	if !reflect.DeepEqual(got, []string{"sub/b.go"}) {
		t.Errorf("root-bounded ancestors: %v", got)
	}
}

func TestGlobSymlinks(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"a/real.go": "", "a/sub/x.go": "", "outside/secret.go": ""})
	must := func(err error) {
		if err != nil {
			t.Skip("symlinks unsupported")
		}
	}
	must(os.Symlink("..", filepath.Join(env.Cwd, "a", "loop")))                   // a/loop -> . (parent): a cycle
	must(os.Symlink("real.go", filepath.Join(env.Cwd, "a", "link.go")))           // file link
	must(os.Symlink("../outside", filepath.Join(env.Cwd, "a", "dirlink")))        // dir link
	must(os.Symlink("nowhere.go", filepath.Join(env.Cwd, "a", "broken.go")))      // dangling
	must(os.Symlink("sub", filepath.Join(env.Cwd, "a", "sub_alias")))             // dir link within tree
	must(os.Symlink(filepath.Join(env.Cwd, "a"), filepath.Join(env.Cwd, "self"))) // absolute loop

	done := make(chan []string, 1)
	go func() { done <- globList(t, env, map[string]any{"pattern": "**/*.go"}) }()
	select {
	case got := <-done:
		want := []string{"a/link.go", "a/real.go", "a/sub/x.go", "outside/secret.go"}
		if !reflect.DeepEqual(sortedCopy(got), want) {
			t.Errorf("got  %v\nwant %v", sortedCopy(got), want)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("glob did not terminate on a symlink loop")
	}
}

func TestGlobAbsolutePattern(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"a/b/c.go": "", "a/b/d.txt": "", "a/e.go": ""})
	got := sortedCopy(globList(t, env, map[string]any{"pattern": filepath.Join(env.Cwd, "a") + "/**/*.go"}))
	if !reflect.DeepEqual(got, []string{"a/b/c.go", "a/e.go"}) {
		t.Errorf("got %v", got)
	}
	got = globList(t, env, map[string]any{"pattern": filepath.Join(env.Cwd, "a/b/d.txt")})
	if !reflect.DeepEqual(got, []string{"a/b/d.txt"}) {
		t.Errorf("literal absolute pattern: %v", got)
	}
}

func TestGlobErrors(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"f.txt": ""})
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"no pattern", map[string]any{}, "pattern is required"},
		{"blank pattern", map[string]any{"pattern": "  "}, "pattern is required"},
		{"dotdot", map[string]any{"pattern": "../*.go"}, "'..'"},
		{"too many braces", map[string]any{"pattern": strings.Repeat("{a,b}", 12)}, "too many"},
		{"path is a file", map[string]any{"pattern": "*", "path": "f.txt"}, "is a file, not a directory"},
		{"path missing", map[string]any{"pattern": "*", "path": "nodir"}, "not found"},
		{"nul in path", map[string]any{"pattern": "*", "path": "a\x00"}, "NUL"},
		{"wrong type", `{"pattern":5}`, `argument "pattern" must be a string`},
		{"nul in pattern", map[string]any{"pattern": "a\x00b"}, "NUL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contains(t, mustErr(t, run(t, Glob{}, env, tc.input)), tc.want)
		})
	}
}

func TestGlobPermissionRequest(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	tree(t, env.Cwd, map[string]string{"sub/a.go": ""})
	mustOK(t, run(t, Glob{}, env, map[string]any{"pattern": "*.go", "path": "sub"}))
	r := h.Requests()[0]
	if r.Tool != "glob" || r.Writes || r.Network || len(r.Paths) != 1 || r.Paths[0] != filepath.Join(env.Cwd, "sub") || r.Risk != perm.RiskLow {
		t.Errorf("request = %+v", r)
	}
	h.denyPerm = func(perm.Request) string { return "outside the workspace" }
	contains(t, mustErr(t, run(t, Glob{}, env, map[string]any{"pattern": "*", "path": "/etc"})), "permission denied", "outside the workspace")
}

func TestGlobOutsideRootIsUpToPermissions(t *testing.T) {
	// Root does not confine the tool by itself; the permission engine decides.
	env := testEnv(t)
	other := realTemp(t)
	tree(t, other, map[string]string{"x.txt": ""})
	got := globList(t, env, map[string]any{"pattern": "*.txt", "path": other})
	if !reflect.DeepEqual(got, []string{filepath.Join(other, "x.txt")}) {
		t.Errorf("outside results should be absolute: %v", got)
	}
	h := &hooks{denyPerm: func(r perm.Request) string {
		if !strings.HasPrefix(r.Paths[0], env.Root) {
			return "outside root"
		}
		return ""
	}}
	withHooks(env, h)
	contains(t, mustErr(t, run(t, Glob{}, env, map[string]any{"pattern": "*.txt", "path": other})), "outside root")
}

func TestGlobSymlinkEscapingRootIsReportedByRealPath(t *testing.T) {
	env := testEnv(t)
	other := realTemp(t)
	tree(t, other, map[string]string{"x.txt": "secret"})
	if err := os.Symlink(other, filepath.Join(env.Cwd, "escape")); err != nil {
		t.Skip("symlinks unsupported")
	}
	h := &hooks{}
	withHooks(env, h)
	mustOK(t, run(t, Glob{}, env, map[string]any{"pattern": "*", "path": "escape"}))
	if got := h.Requests()[0].Paths[0]; got != other {
		t.Errorf("permission engine saw %q, want the real location %q", got, other)
	}
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "escape/x.txt"}))
	if got := h.Requests()[1].Paths[0]; got != filepath.Join(other, "x.txt") {
		t.Errorf("read: permission engine saw %q", got)
	}
}

func TestGlobSymlinkedRootKeepsRootSpelling(t *testing.T) {
	// When the project directory is reached through a symlink, in-project files
	// must still be reported under Root's spelling, or every path rule written
	// against Root would treat the project as "outside".
	real := realTemp(t)
	tree(t, real, map[string]string{"src/a.go": ""})
	linkParent := realTemp(t)
	link := filepath.Join(linkParent, "proj")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	env := testEnv(t)
	env.Cwd, env.Root = link, link
	h := &hooks{}
	withHooks(env, h)
	got := globList(t, env, map[string]any{"pattern": "**/*.go"})
	if !reflect.DeepEqual(got, []string{"src/a.go"}) {
		t.Errorf("got %v", got)
	}
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "src/a.go"}))
	if p := h.Requests()[1].Paths[0]; p != filepath.Join(link, "src/a.go") {
		t.Errorf("read path seen by permissions: %q, want it under %q", p, link)
	}
}

func TestGlobCancellation(t *testing.T) {
	env := testEnv(t)
	tree(t, env.Cwd, map[string]string{"a/b/c.go": ""})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Glob{}.Run(ctx, &tools.Call{Input: []byte(`{"pattern":"**/*.go"}`), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, mustErr(t, res), "cancelled")
}

func TestGlobLargeTree(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	env := testEnv(t)
	for d := 0; d < 50; d++ {
		for f := 0; f < 40; f++ {
			writeFile(t, filepath.Join(env.Cwd, fmt.Sprintf("pkg%02d/sub%d/file%02d.go", d, f%4, f)), "")
		}
	}
	start := time.Now()
	res := run(t, Glob{}, env, map[string]any{"pattern": "**/*.go"})
	mustOK(t, res)
	if res.Meta["matches"] != 2000 {
		t.Errorf("matches = %v", res.Meta["matches"])
	}
	if d := time.Since(start); d > 60*time.Second {
		t.Errorf("2000 files took %v", d)
	}
	// An anchored pattern prunes the walk and returns the right answer.
	got := globList(t, env, map[string]any{"pattern": "pkg07/sub1/*.go"})
	if len(got) != 10 {
		t.Errorf("anchored pattern: %d results", len(got))
	}
}
