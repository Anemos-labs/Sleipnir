package reward

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

func TestCleanRel(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		escapes bool
	}{
		{"a/b.go", "a/b.go", false},
		{"./a/b.go", "a/b.go", false},
		{"a//b///c.go", "a/b/c.go", false},
		{`a\b\c.go`, "a/b/c.go", false},
		{"a/../b.go", "b.go", false},
		{"a/./b.go", "a/b.go", false},
		{"../b.go", "b.go", true},
		{"../../etc/passwd", "etc/passwd", true},
		{"a/../../b.go", "b.go", true},
		{"/etc/passwd", "etc/passwd", true},
		{`C:\Windows\system.ini`, "Windows/system.ini", true},
		{"d:/x/../y", "y", true},
		{"..", "", true},
		{".", "", false},
		{"", "", false},
		{"  spaced.go  ", "spaced.go", false},
		{"dir/", "dir", false},
		{"ünï/cödé.go", "ünï/cödé.go", false},
	}
	for _, tc := range tests {
		got, esc := cleanRel(tc.in)
		if got != tc.want || esc != tc.escapes {
			t.Errorf("cleanRel(%q) = %q,%v want %q,%v", tc.in, got, esc, tc.want, tc.escapes)
		}
	}
}

func TestUnquoteGit(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		quoted bool
	}{
		{`plain.go`, "plain.go", false},
		{`"a b.go"`, "a b.go", true},
		{`"tab\there"`, "tab\there", true},
		{`"q\"uote"`, `q"uote`, true},
		{`"back\\slash"`, `back\slash`, true},
		{`"caf\303\251.go"`, "café.go", true},
		{`"nl\nx"`, "nl\nx", true},
		{`"trailing\"`, `trailing\`, true}, // malformed: a dangling backslash stays literal
		{`"`, `"`, false},
		{`""`, "", true},
		{`"\9"`, `\9`, true},
	}
	for _, tc := range tests {
		got, q := unquoteGit(tc.in)
		if got != tc.want || q != tc.quoted {
			t.Errorf("unquoteGit(%q) = %q,%v want %q,%v", tc.in, got, q, tc.want, tc.quoted)
		}
	}
}

func TestGlobMatching(t *testing.T) {
	tests := []struct {
		patterns []string
		path     string
		want     bool
	}{
		// from the doc's example
		{[]string{"*_test.go", "go.mod", ".github/**"}, "x_test.go", true},
		{[]string{"*_test.go"}, "pkg/deep/x_test.go", true},
		{[]string{"*_test.go"}, "x_test.go.bak", false},
		{[]string{"*_test.go"}, "x_test.gox", false},
		{[]string{"*_test.go"}, "x.go", false},
		{[]string{"go.mod"}, "go.mod", true},
		{[]string{"go.mod"}, "sub/go.mod", true},
		{[]string{"go.mod"}, "go.mod.orig", false},
		{[]string{"go.mod"}, "mygo.mod", false},
		{[]string{".github/**"}, ".github/workflows/ci.yml", true},
		{[]string{".github/**"}, ".github", true},
		{[]string{".github/**"}, "docs/.github/x", false},
		{[]string{".github/**"}, ".githubx/y", false},
		// case folding: the verifier checkout may be case-insensitive
		{[]string{"go.mod"}, "GO.MOD", true},
		{[]string{".github/**"}, ".GitHub/Workflows/CI.yml", true},
		{[]string{"*_TEST.GO"}, "a_test.go", true},
		// directories
		{[]string{"testdata/"}, "pkg/testdata/a.golden", true},
		{[]string{"testdata/"}, "testdata", false},
		{[]string{"testdata"}, "pkg/testdata/a.golden", true},
		{[]string{"internal/foo"}, "internal/foo/x.go", true},
		{[]string{"internal/foo"}, "internal/foobar/x.go", false},
		{[]string{"internal/foo"}, "other/internal/foo/x.go", false},
		{[]string{"/README.md"}, "README.md", true},
		{[]string{"/README.md"}, "docs/README.md", false},
		{[]string{"**/*.golden"}, "a/b/c.golden", true},
		{[]string{"**/*.golden"}, "c.golden", true},
		{[]string{"a/**/z.txt"}, "a/z.txt", true},
		{[]string{"a/**/z.txt"}, "a/b/c/z.txt", true},
		{[]string{"a/**/z.txt"}, "b/a/z.txt", false},
		{[]string{"src/*/gen.go"}, "src/x/gen.go", true},
		{[]string{"src/*/gen.go"}, "src/x/y/gen.go", false},
		{[]string{"?.txt"}, "a.txt", true},
		{[]string{"?.txt"}, "ab.txt", false},
		{[]string{"[ab].txt"}, "a.txt", true},
		{[]string{"[ab].txt"}, "c.txt", false},
		// braces
		{[]string{"*.{yml,yaml}"}, "ci.yaml", true},
		{[]string{"*.{yml,yaml}"}, "ci.json", false},
		// oddities
		{[]string{"["}, "[", true}, // malformed: literal compare, never silently off
		{[]string{"["}, "a", false},
		{[]string{"!go.mod"}, "go.mod", false}, // negation unsupported: ignored
		{[]string{""}, "go.mod", false},
		{[]string{"  "}, "go.mod", false},
		{nil, "go.mod", false},
		{[]string{"go.mod"}, "", false},
		{[]string{"./go.mod"}, "go.mod", true},
		{[]string{`.github\**`}, ".github/x", true},
		{[]string{"ünï/*.go"}, "ünï/cödé.go", true},
		{[]string{"**"}, "anything/at/all", true},
		{[]string{"*"}, "x", true},
	}
	for _, tc := range tests {
		set := compileGlobs(tc.patterns)
		p, _ := cleanRel(tc.path)
		_, got := set.match(p)
		if got != tc.want {
			t.Errorf("patterns %q vs %q = %v, want %v", tc.patterns, tc.path, got, tc.want)
		}
	}
}

func TestGlobReturnsTheUsersPattern(t *testing.T) {
	set := compileGlobs([]string{"*.{yml,yaml}", "go.mod"})
	if pat, ok := set.match("ci.yaml"); !ok || pat != "*.{yml,yaml}" {
		t.Errorf("match reported %q,%v", pat, ok)
	}
}

func TestGlobIsBoundedOnAdversarialPatterns(t *testing.T) {
	// A pattern of many ** against a long path must not go exponential.
	pat := strings.Repeat("**/", 40) + "x.txt"
	path := strings.Repeat("d/", 200) + "y.txt"
	set := compileGlobs([]string{pat})
	start := time.Now()
	if _, ok := set.match(path); ok {
		t.Error("should not match")
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("matching took %v", d)
	}
	// Brace explosion is capped.
	if n := len(expandBraces(strings.Repeat("{a,b}", 30), maxBraceAlternatives)); n > maxBraceAlternatives {
		t.Errorf("brace expansion produced %d alternatives", n)
	}
}

func TestCleanAbs(t *testing.T) {
	tests := map[string]string{
		"/a/b/../c": "/a/c", "/a//b": "/a/b", "rel/path": "", "": "", "/": "/", `\a\b`: "/a/b",
		`d:\work\..\x`: "D:/x", "C:/..": "C:/", "C:/a//b/": "C:/a/b", "C:rel": "",
		// Windows paths ignore case; POSIX paths do not.
		`D:\Work\Repo\Pkg`: "D:/work/repo/pkg", `d:/WORK/repo`: "D:/work/repo", "/Work/Repo": "/Work/Repo",
		// A UNC root stays apart from a path on the current drive; slashes alone never make one, so a doubled
		// slash cannot hide a system location.
		`\\srv\share\x`: "//srv/share/x", `\\SRV\Share\X\..\y`: "//srv/share/y", "//etc/passwd": "/etc/passwd",
		`\\\etc\passwd`: "/etc/passwd",
	}
	for in, want := range tests {
		if got := cleanAbs(in); got != want {
			t.Errorf("cleanAbs(%q) = %q, want %q", in, got, want)
		}
	}
}

// refMatchSegs is the specification of anchored glob matching, written the
// obvious way: a dynamic program over one pattern and one path, tried against
// every non-empty prefix of the path. matchPrefix must agree with it on every
// input; it exists only to be faster.
func refMatchSegs(pat, ps []string) bool {
	prev := make([]bool, len(ps)+1)
	cur := make([]bool, len(ps)+1)
	prev[0] = true
	for i := 1; i <= len(pat); i++ {
		p := pat[i-1]
		cur[0] = p == "**" && prev[0]
		for j := 1; j <= len(ps); j++ {
			if p == "**" {
				cur[j] = prev[j] || cur[j-1] || prev[j-1]
			} else {
				cur[j] = prev[j-1] && segMatch(p, ps[j-1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(ps)]
}

func refMatchPrefix(pat, ps []string) bool {
	for j := 1; j <= len(ps); j++ {
		if refMatchSegs(pat, ps[:j]) {
			return true
		}
	}
	return false
}

func TestMatchPrefixAgreesWithTheSpecification(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	patSegs := []string{"a", "b", "c", "*", "**", "**", "?", "a*", "*b", "[ab]", "x"}
	names := []string{"a", "b", "c", "ab", "ba", "x", "aab"}
	pick := func(from []string, max int) []string {
		out := make([]string, rng.Intn(max+1))
		for i := range out {
			out[i] = from[rng.Intn(len(from))]
		}
		return out
	}
	for i := 0; i < 30000; i++ {
		pat, path := pick(patSegs, 6), pick(names, 8)
		if len(pat) == 0 {
			continue
		}
		if got, want := matchPrefix(pat, path, false), refMatchPrefix(pat, path); got != want {
			t.Fatalf("matchPrefix(%q, %q) = %v, specification says %v", pat, path, got, want)
		}
		// Reseeded: the pattern may begin at any segment, i.e. some suffix matches.
		want := false
		for k := 0; k < len(path) && !want; k++ {
			want = refMatchPrefix(pat, path[k:])
		}
		if got := matchPrefix(pat, path, true); got != want {
			t.Fatalf("matchPrefix(%q, %q, anywhere) = %v, specification says %v", pat, path, got, want)
		}
	}
}

func TestGlobMatchIsLinearInPathLength(t *testing.T) {
	// A path of tens of thousands of segments can be written into a diff. Matching
	// every prefix separately made this cubic in the worst case (minutes for a 20 KB
	// path); one pass is linear.
	set := compileGlobs([]string{".github/**", "**/*_test.go", "a/**/b/**/c", "src/*/gen.go", "vendor/", "*.lock", "**/**/x/**"})
	for _, unit := range []string{"a/", "a/b/", ".github/", "**/", "src/x/", "b/"} {
		requireLinear(t, fmt.Sprintf("glob match of %q*n", unit), 10000, func(n int) {
			p, _ := cleanRel(strings.Repeat(unit, n) + "z.go")
			set.match(p)
		})
	}
}

func TestMatchAnywhereFindsProtectedPathsUnderAnyRoot(t *testing.T) {
	set := compileGlobs([]string{"tests/**", "go.mod", "src/*/gen.go", "vendor/", "**/*.golden", ".github/**"})
	tests := []struct {
		path string
		want bool
	}{
		{"/work/repo/tests/a.py", true},
		{"/work/repo/go.mod", true},
		{"/a/b/c/d/e/src/x/gen.go", true},
		{"/a/src/x/y/gen.go", false},
		{"/work/vendor/x.go", true}, // anything under a matched directory
		{"/work/vendor", false},     // a directory-only pattern needs the directory to have content
		{"/w/x/testdata/a.golden", true},
		{"/work/.github/workflows/ci.yml", true},
		{"/work/repo/pkg/a.go", false},
	}
	for _, tc := range tests {
		if _, got := set.matchAnywhere(tc.path); got != tc.want {
			t.Errorf("matchAnywhere(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestProtectedTargetIsLinearInDepth(t *testing.T) {
	// The write target "/x/x/x..." is one segment per two bytes. With no workspace
	// roots known every suffix used to be matched on its own, which took seconds
	// for a 20 KB path; growth is now linear.
	set := compileGlobs([]string{"tests/**", "go.mod", "**/*_test.go", "a/**/b"})
	requireLinear(t, "protectedTarget", 20000, func(n int) {
		protectedTarget(set, strings.Repeat("/x", n), nil)
		protectedTarget(set, strings.Repeat("/a", n)+"/b", nil)
	})
}

func TestOverlongProtectedPatternsAreRejectedNotIgnored(t *testing.T) {
	long := strings.Repeat("a/", maxPatternBytes) // twice the limit
	if got := compileGlobs([]string{long}); len(got) != 0 {
		t.Errorf("an over-long pattern compiled: %d globs", len(got))
	}
	ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""))))
	before := epJSON(t, ep)
	err := Score(ep, &rl.Task{Verifier: rl.Verifier{Protected: []string{"tests/**", long}}}, DefaultConfig(), nil)
	if err == nil || !strings.Contains(err.Error(), "task.verifier.protected[1]") {
		t.Fatalf("want an error naming task.verifier.protected[1], got %v", err)
	}
	if epJSON(t, ep) != before {
		t.Error("a rejected task changed the episode")
	}
	// At the limit is fine.
	ok := strings.Repeat("a", maxPatternBytes-2) + "/x"
	if err := Score(ep, &rl.Task{Verifier: rl.Verifier{Protected: []string{ok}}}, DefaultConfig(), nil); err != nil {
		t.Errorf("a pattern of exactly %d bytes was rejected: %v", len(ok), err)
	}
}

func TestBraceExplosionInAProtectedPatternIsRefused(t *testing.T) {
	ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(100, ""))))
	explosive := strings.Repeat("{a,b}", 7) + "/x" // 128 alternatives
	err := Score(ep, &rl.Task{Verifier: rl.Verifier{Protected: []string{explosive}}}, DefaultConfig(), nil)
	if err == nil || !strings.Contains(err.Error(), "task.verifier.protected[0]") || !strings.Contains(err.Error(), "brace") {
		t.Fatalf("want a refusal naming the pattern and the braces, got %v", err)
	}
	fine := strings.Repeat("{a,b}", 6) + "/x" // exactly 64
	if err := Score(ep, &rl.Task{Verifier: rl.Verifier{Protected: []string{fine}}}, DefaultConfig(), nil); err != nil {
		t.Errorf("64 alternatives must be accepted: %v", err)
	}
	if n := len(compileGlobs([]string{fine})); n != 64 {
		t.Errorf("compiled %d patterns, want 64", n)
	}
}

func TestConsecutiveDoubleStarsCollapse(t *testing.T) {
	g, ok := compileGlob("x", "a/**/**/**/b")
	if !ok || len(g.segs) != 3 {
		t.Fatalf("segs = %q", g.segs)
	}
	set := compileGlobs([]string{strings.Repeat("**/", 300) + "x.txt"}) // 905 bytes: under the limit
	if _, hit := set.match(strings.Repeat("d/", 300) + "x.txt"); !hit {
		t.Error("collapsed ** pattern no longer matches")
	}
}
