package fs

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSegMatch(t *testing.T) {
	tests := []struct {
		pat, name string
		want      bool
	}{
		{"*", "", true},
		{"*", "anything", true},
		{"*.go", "main.go", true},
		{"*.go", "main.go.bak", false},
		{"*.go", ".go", true},
		{"a*b", "ab", true},
		{"a*b", "axxb", true},
		{"a*b", "axxbc", false},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXbY", false},
		{"**", "abc", true},
		{"a**b", "axyb", true},
		{"?", "a", true},
		{"?", "", false},
		{"?", "ab", false},
		{"???", "abc", true},
		{"?", "é", true}, // one character, not one byte
		{"caf?", "café", true},
		{"日本?", "日本語", true},
		{"*語", "日本語", true},
		{"[abc]", "b", true},
		{"[abc]", "d", false},
		{"[a-c]x", "bx", true},
		{"[a-c]x", "dx", false},
		{"[!a-c]", "d", true},
		{"[!a-c]", "b", false},
		{"[^a-c]", "z", true},
		{"[]]", "]", true},
		{"[]a]", "a", true},
		{"[a-]", "-", true},
		{"[a-]", "a", true},
		{"[\\]]", "]", true},
		{"[[:digit:]]", "5", true},
		{"[[:digit:]]", "x", false},
		{"[[:alpha:]_]", "_", true},
		{"[[:upper:]]", "a", false},
		{"[[:nosuch:]]", "a", false},
		{"[é-ü]", "ö", true},
		{"[abc", "[abc", true}, // unterminated class is literal
		{"[abc", "a", false},
		{"a[", "a[", true},
		{"file[0-9].txt", "file7.txt", true},
		{"file[0-9].txt", "fileX.txt", false},
		{"\\*", "*", true},
		{"\\*", "a", false},
		{"a\\?b", "a?b", true},
		{"\\#x", "#x", true},
		{"\\!x", "!x", true},
		{"a\\", "a\\", true}, // trailing backslash is literal
		{"\\é", "é", true},
		{"", "", true},
		{"", "a", false},
		{"a", "", false},
		{"*a*a*a*a*a*b", strings.Repeat("a", 50), false}, // must not go exponential
		{"*.*", "noext", false},
		{"*.*", "a.b", true},
		{".*", ".hidden", true},
		{"*", ".hidden", true}, // dotfiles are matched: tools do not hide them
	}
	for _, tc := range tests {
		if got := segMatch(tc.pat, tc.name); got != tc.want {
			t.Errorf("segMatch(%q, %q) = %v, want %v", tc.pat, tc.name, got, tc.want)
		}
	}
}

func TestSegMatchIsFastOnPathologicalInput(t *testing.T) {
	start := time.Now()
	name := strings.Repeat("a", 5000)
	if segMatch(strings.Repeat("*a", 30)+"b", name) {
		t.Errorf("should not match")
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("pathological pattern took %v", d)
	}
}

func TestMatchSegs(t *testing.T) {
	tests := []struct {
		pat, path string
		want      bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/c/main.go", true},
		{"**/*.go", "a/b/c/main.txt", false},
		{"src/**", "src/a.go", true},
		{"src/**", "src/a/b/c.go", true},
		{"src/**", "other/a.go", false},
		{"src/**/test", "src/test", true},
		{"src/**/test", "src/a/b/test", true},
		{"src/**/test", "src/a/b/tests", false},
		{"**/a/**/b", "a/b", true},
		{"**/a/**/b", "x/a/y/z/b", true},
		{"**/a/**/b", "x/y/b", false},
		{"**", "a/b/c", true},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/d/c", false},
		{"a/*/c", "a/c", false},
		{"*/*", "a/b", true},
		{"*/*", "a", false},
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},
		{"**/**/**/x", "x", true},
		{"**/**/**/x", "a/b/c/d/x", true},
		{"a/**/**/b", "a/b", true},
	}
	for _, tc := range tests {
		pat := splitPath(tc.pat)
		path := splitPath(tc.path)
		if got := matchSegs(pat, path); got != tc.want {
			t.Errorf("matchSegs(%q, %q) = %v, want %v", tc.pat, tc.path, got, tc.want)
		}
	}
}

func TestMatchSegsManyStarsIsPolynomial(t *testing.T) {
	// Without memoisation this is exponential in the number of ** segments.
	pat := strings.Split(strings.Repeat("**/a/", 25)+"z", "/")
	path := strings.Split(strings.Repeat("a/", 60)+"y", "/")
	start := time.Now()
	if matchSegs(pat, path) {
		t.Errorf("must not match")
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestPrefixMatch(t *testing.T) {
	tests := []struct {
		pat, dir string
		want     bool
	}{
		{"src/*.go", "src", true},
		{"src/*.go", "other", false},
		{"src/*.go", "src/sub", false},
		{"src/**/*.go", "src/a/b", true},
		{"**/*.go", "anything/at/all", true},
		{"a/b/c", "a/b", true},
		{"a/b/c", "a/x", false},
		{"a/b", "a/b/c", false},
		{"*/x", "any", true},
	}
	for _, tc := range tests {
		if got := prefixMatch(splitPath(tc.pat), splitPath(tc.dir)); got != tc.want {
			t.Errorf("prefixMatch(%q, %q) = %v, want %v", tc.pat, tc.dir, got, tc.want)
		}
	}
}

func TestExpandBraces(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"plain", []string{"plain"}},
		{"*.{go,md}", []string{"*.go", "*.md"}},
		{"{a,b}{1,2}", []string{"a1", "a2", "b1", "b2"}},
		{"{a,{b,c}}x", []string{"ax", "bx", "cx"}},
		{"x{,y}", []string{"x", "xy"}},
		{"{a}", []string{"{a}"}},   // no comma: literal
		{"{a,b", []string{"{a,b"}}, // unterminated: literal
		{"a}b", []string{"a}b"}},
		{"\\{a,b\\}", []string{"\\{a,b\\}"}},
		{"[{]x", []string{"[{]x"}},
		{"src/{a,b}/*.{go,rs}", []string{"src/a/*.go", "src/a/*.rs", "src/b/*.go", "src/b/*.rs"}},
		{"", []string{""}},
	}
	for _, tc := range tests {
		got, ok := expandBraces(tc.in, 128)
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("expandBraces(%q) = %v ok=%v, want %v", tc.in, got, ok, tc.want)
		}
	}
	if _, ok := expandBraces(strings.Repeat("{a,b}", 12), 128); ok {
		t.Errorf("4096 alternatives must exceed the limit")
	}
}

func TestCompileGlob(t *testing.T) {
	tests := []struct {
		in      string
		alts    [][]string
		dirOnly bool
		err     string
	}{
		{in: "*.go", alts: [][]string{{"*.go"}}},
		{in: "./src/*.go", alts: [][]string{{"src", "*.go"}}},
		{in: "/src/*.go", alts: [][]string{{"src", "*.go"}}},
		{in: "src//a", alts: [][]string{{"src", "a"}}},
		{in: "**/**/x", alts: [][]string{{"**", "x"}}},
		{in: "**/*.{ts,tsx}", alts: [][]string{{"**", "*.ts"}, {"**", "*.tsx"}}},
		{in: "tests/", alts: [][]string{{"tests"}}, dirOnly: true},
		{in: "  x  ", alts: [][]string{{"x"}}},
		{in: "", err: "empty"},
		{in: "  ", err: "empty"},
		{in: ".", err: "empty"},
		{in: "../x", err: ".."},
		{in: "a/../x", err: ".."},
		{in: "a\x00b", err: "NUL"},
		{in: strings.Repeat("{a,b}", 12), err: "too many"},
		{in: strings.Repeat("{", 5000) + "a,b" + strings.Repeat("}", 5000), err: "too long"},
	}
	for _, tc := range tests {
		g, err := compileGlob(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("compileGlob(%q) err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("compileGlob(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(g.alts, tc.alts) || g.dirOnly != tc.dirOnly {
			t.Errorf("compileGlob(%q) = %v dirOnly=%v, want %v dirOnly=%v", tc.in, g.alts, g.dirOnly, tc.alts, tc.dirOnly)
		}
	}
}

func TestSplitAbsPattern(t *testing.T) {
	tests := []struct{ in, root, rest string }{
		{"/a/b/**/*.go", "/a/b", "**/*.go"},
		{"/a/b/file.go", "/a/b", "file.go"},
		{"/a/*/c", "/a", "*/c"},
		{"/*.go", "/", "*.go"},
		{"/a/b/", "/a", "b/"},
		{"/a/b/c.{go,md}", "/a/b", "c.{go,md}"},
	}
	for _, tc := range tests {
		root, rest := splitAbsPattern(tc.in)
		if root != tc.root || rest != tc.rest {
			t.Errorf("splitAbsPattern(%q) = (%q, %q), want (%q, %q)", tc.in, root, rest, tc.root, tc.rest)
		}
	}
}

func TestParseIgnoreLine(t *testing.T) {
	tests := []struct {
		line    string
		ok      bool
		segs    []string
		negate  bool
		dirOnly bool
	}{
		{line: "", ok: false},
		{line: "   ", ok: true, segs: []string{"**", ""}}, // a line of spaces trims to empty
		{line: "# comment", ok: false},
		{line: "\\#literal", ok: true, segs: []string{"**", "\\#literal"}},
		{line: "*.log", ok: true, segs: []string{"**", "*.log"}},
		{line: "!keep.log", ok: true, segs: []string{"**", "keep.log"}, negate: true},
		{line: "\\!important", ok: true, segs: []string{"**", "\\!important"}},
		{line: "build/", ok: true, segs: []string{"**", "build"}, dirOnly: true},
		{line: "/build", ok: true, segs: []string{"build"}},
		{line: "/build/", ok: true, segs: []string{"build"}, dirOnly: true},
		{line: "src/gen", ok: true, segs: []string{"src", "gen"}},
		{line: "src/gen/", ok: true, segs: []string{"src", "gen"}, dirOnly: true},
		{line: "a/**/b", ok: true, segs: []string{"a", "**", "b"}},
		{line: "**/b", ok: true, segs: []string{"**", "b"}},
		{line: "a/**", ok: true, segs: []string{"a", "*"}},
		{line: "**", ok: true, segs: []string{"**"}},
		{line: "trailing   ", ok: true, segs: []string{"**", "trailing"}},
		{line: "keep\\ ", ok: true, segs: []string{"**", "keep\\ "}},
		{line: "crlf.txt\r", ok: true, segs: []string{"**", "crlf.txt"}},
		{line: "/", ok: false},
		{line: "!", ok: false},
		{line: "//x", ok: true, segs: []string{"x"}},
	}
	for _, tc := range tests {
		if strings.TrimSpace(tc.line) == "" && tc.line != "" {
			continue // whitespace-only lines are not interesting
		}
		r, ok := parseIgnoreLine(tc.line)
		if ok != tc.ok {
			t.Errorf("parseIgnoreLine(%q) ok = %v, want %v", tc.line, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if !reflect.DeepEqual(r.segs, tc.segs) || r.negate != tc.negate || r.dirOnly != tc.dirOnly {
			t.Errorf("parseIgnoreLine(%q) = %+v, want segs=%v negate=%v dirOnly=%v", tc.line, r, tc.segs, tc.negate, tc.dirOnly)
		}
	}
}

func TestIgnoreFileDecisions(t *testing.T) {
	f := parseIgnoreFile([]byte(strings.Join([]string{
		"*.log",
		"!keep.log",
		"build/",
		"/root-only.txt",
		"docs/*.tmp",
		"vendor/**",
		"!vendor/keep.txt",
		"**/cache",
		"",
	}, "\n")), 0)
	tests := []struct {
		path  string
		isDir bool
		want  bool // ignored
	}{
		{"a.log", false, true},
		{"sub/a.log", false, true},
		{"keep.log", false, false},
		{"sub/keep.log", false, false},
		{"build", true, true},
		{"build", false, false}, // dir-only rule does not match a file
		{"sub/build", true, true},
		{"root-only.txt", false, true},
		{"sub/root-only.txt", false, false},
		{"docs/x.tmp", false, true},
		{"docs/sub/x.tmp", false, false},
		{"other/docs/x.tmp", false, false},
		{"vendor", true, false}, // "vendor/**" does not match vendor itself
		{"vendor/a", false, true},
		{"vendor/keep.txt", false, false}, // re-included: last rule wins
		{"vendor/sub", true, true},
		{"x/cache", true, true},
		{"x/cache", false, true},
		{"readme.md", false, false},
	}
	for _, tc := range tests {
		segs := splitPath(tc.path)
		ig, decided := f.decide(segs, tc.isDir)
		if got := ig && decided; got != tc.want {
			t.Errorf("ignored(%q, dir=%v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func TestIgnoreStackDeeperFilesWin(t *testing.T) {
	var st ignoreStack
	st.push(parseIgnoreFile([]byte("*.txt\n"), 0))
	st.push(parseIgnoreFile([]byte("!keep.txt\n"), 1)) // in directory "sub"
	tests := []struct {
		path string
		want bool
	}{
		{"a.txt", true},
		{"sub/a.txt", true},
		{"sub/keep.txt", false},
		{"keep.txt", true}, // the deeper file only speaks for paths under sub/
		{"sub/deeper/keep.txt", false},
	}
	for _, tc := range tests {
		if got := st.ignored(splitPath(tc.path), false); got != tc.want {
			t.Errorf("ignored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	// The directory itself is not judged by its own .gitignore.
	if st.ignored([]string{"sub"}, true) {
		t.Errorf("sub must not be ignored")
	}
}

func TestSplitGlobList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"*.go", []string{"*.go"}},
		{"*.go *.md", []string{"*.go", "*.md"}},
		{"*.go,*.md", []string{"*.go", "*.md"}},
		{"*.{go,md}", []string{"*.{go,md}"}},
		{"*.{go,md} !vendor", []string{"*.{go,md}", "!vendor"}},
		{"  a ,  b  ", []string{"a", "b"}},
		{"", nil},
		{"\\,", []string{"\\,"}},
	}
	for _, tc := range tests {
		if got := splitGlobList(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitGlobList(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestOverrideGlobs(t *testing.T) {
	tests := []struct {
		globs []string
		path  string
		isDir bool
		want  bool // excluded
	}{
		{[]string{"*.go"}, "a.go", false, false},
		{[]string{"*.go"}, "sub/a.go", false, false}, // no slash: any depth
		{[]string{"*.go"}, "a.txt", false, true},
		{[]string{"*.go"}, "sub", true, false}, // directories are still entered
		{[]string{"*.{go,md}"}, "r.md", false, false},
		{[]string{"src/*.go"}, "src/a.go", false, false},
		{[]string{"src/*.go"}, "a.go", false, true},
		{[]string{"src/*.go"}, "x/src/a.go", false, true}, // anchored to the search root
		{[]string{"!*_test.go"}, "a_test.go", false, true},
		{[]string{"!*_test.go"}, "a.go", false, false},
		{[]string{"*.go", "!*_test.go"}, "a_test.go", false, true},
		{[]string{"*.go", "!*_test.go"}, "a.go", false, false},
		{[]string{"!vendor"}, "vendor", true, true},
		{[]string{"!vendor"}, "src", true, false},
		{[]string{"**/*.js"}, "a/b/c.js", false, false},
	}
	for _, tc := range tests {
		o := compileOverride(tc.globs)
		if got := o.excludes(splitPath(tc.path), tc.isDir); got != tc.want {
			t.Errorf("globs %v: excludes(%q, dir=%v) = %v, want %v", tc.globs, tc.path, tc.isDir, got, tc.want)
		}
	}
	var nilOverride *overrideGlobs
	if nilOverride.excludes([]string{"x"}, false) {
		t.Errorf("nil override excludes nothing")
	}
}

// naiveMatchSegs is the obvious exponential-time definition of matchSegs, used to
// check the optimised prefix/suffix and dynamic-programming versions.
func naiveMatchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if naiveMatchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	return len(segs) > 0 && segMatch(pat[0], segs[0]) && naiveMatchSegs(pat[1:], segs[1:])
}

func TestMatchSegsAgreesWithTheNaiveDefinition(t *testing.T) {
	patSegs := []string{"a", "b", "*", "?", "**", "a*", "[ab]"}
	pathSegs := []string{"a", "b", "ab", "c"}
	var pats, paths [][]string
	var gen func(alphabet []string, max int, cur []string, out *[][]string)
	gen = func(alphabet []string, max int, cur []string, out *[][]string) {
		*out = append(*out, append([]string(nil), cur...))
		if len(cur) == max {
			return
		}
		for _, a := range alphabet {
			gen(alphabet, max, append(cur, a), out)
		}
	}
	gen(patSegs, 4, nil, &pats)
	gen(pathSegs, 4, nil, &paths)
	checked := 0
	for _, p := range pats {
		for _, s := range paths {
			if got, want := matchSegs(p, s), naiveMatchSegs(p, s); got != want {
				t.Fatalf("matchSegs(%q, %q) = %v, want %v", p, s, got, want)
			}
			checked++
		}
	}
	// Longer, random cases: up to 8 pattern segments (several "**") against up to 9 path segments.
	rng := rand.New(rand.NewSource(11))
	pick := func(alphabet []string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return out
	}
	for i := 0; i < 200_000; i++ {
		p, s := pick(patSegs, rng.Intn(9)), pick(pathSegs, rng.Intn(10))
		if got, want := matchSegs(p, s), naiveMatchSegs(p, s); got != want {
			t.Fatalf("matchSegs(%q, %q) = %v, want %v", p, s, got, want)
		}
		checked++
	}
	t.Logf("%d pattern/path pairs agree", checked)
}

func TestMatchSegsCostIsBoundedForHostilePatterns(t *testing.T) {
	// 60 "**" groups against a 2000-segment path: quadratic-in-depth matching
	// would take seconds per call; the DP is one 61x2001 table.
	pat := strings.Split(strings.Repeat("**/a/", 30)+"c", "/")
	segs := strings.Split(strings.Repeat("a/", 2000)+"b", "/")
	start := time.Now()
	for i := 0; i < 20; i++ {
		if matchSegs(pat, segs) {
			t.Fatal("must not match")
		}
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("20 matches took %v", d)
	}
	// Rules with absurdly many segments are dropped at parse time instead.
	if _, ok := parseIgnoreLine(strings.Repeat("a/", 100) + "b"); ok {
		t.Errorf("a %d-segment rule should be ignored", 101)
	}
	if _, err := compileGlob(strings.Repeat("a/", 100) + "b"); err == nil || !strings.Contains(err.Error(), "too many path segments") {
		t.Errorf("compileGlob err = %v", err)
	}
}
