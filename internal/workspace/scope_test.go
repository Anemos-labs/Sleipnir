package workspace

import (
	"context"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScopeMatch(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		// exact files and the subtree rule
		{"README.md", "README.md", true},
		{"README.md", "docs/README.md", false},
		{"internal/swarm", "internal/swarm/leases.go", true},
		{"internal/swarm/", "internal/swarm/leases.go", true},
		{"internal/swarm/**", "internal/swarm/leases.go", true},
		{"internal/swarm/**", "internal/swarm/deep/er/x.go", true},
		{"internal/swarm", "internal/swarmish/x.go", false},
		{"internal/swarm", "internal/swarm", true},
		{"./internal/swarm/", "internal/swarm/x.go", true},
		{"/internal/swarm", "internal/swarm/x.go", true},
		// * stays inside a segment, but what it matches is a directory's subtree too
		{"internal/*", "internal/swarm/leases.go", true},
		{"internal/*", "cmd/x.go", false},
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", false}, // no slash: top level only, like the glob tool
		{"**/*.go", "cmd/main.go", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "cmd/app/deep/main.go", true},
		{"**/*.go", "cmd/app/main.rs", false},
		{"cmd/*/main.go", "cmd/app/main.go", true},
		{"cmd/*/main.go", "cmd/app/sub/main.go", false},
		{"cmd/**/main.go", "cmd/app/sub/main.go", true},
		{"cmd/**/main.go", "cmd/main.go", true},
		// ? and classes
		{"file?.txt", "file1.txt", true},
		{"file?.txt", "file10.txt", false},
		{"file[0-9].txt", "file7.txt", true},
		{"file[0-9].txt", "filex.txt", false},
		{"file[!0-9].txt", "filex.txt", true},
		{"file[^0-9].txt", "file7.txt", false},
		{"[[:upper:]]*.md", "README.md", true},
		{"[[:upper:]]*.md", "readme.md", false},
		// braces
		{"{cmd,internal}/**", "cmd/app/main.go", true},
		{"{cmd,internal}/**", "docs/guide.md", false},
		{"src/*.{go,rs}", "src/lib.rs", true},
		{"src/*.{go,rs}", "src/lib.py", false},
		{"a{b,c{d,e}}f", "acef", true},
		{"a{b,c{d,e}}f", "acf", false},
		// escapes and literal specials
		{`file\*.txt`, "file*.txt", true},
		{`file\*.txt`, "fileX.txt", false},
		{"data/table with space.csv", "data/table with space.csv", true},
		{"docs/日本語/*", "docs/日本語/ガイド.md", true},
		{"docs/?本語", "docs/日本語", true},
		// whitespace around a pattern is a typo, unless a backslash protects it
		{"  docs/guide.md\n", "docs/guide.md", true},
		{"docs/guide.md ", "docs/guide.md ", false}, // trimmed: it names guide.md, not "guide.md "
		{`docs/guide.md\ `, "docs/guide.md ", true},
		{`docs/guide.md\ `, "docs/guide.md", false},
		{`docs/guide.md\   `, "docs/guide.md ", true}, // one escaped space kept, the typo trimmed
		{`docs/guide.md\\ `, `docs/guide.md\`, true},  // an escaped backslash does not protect the space
		{`\ lead`, " lead", true},
		{" ", " ", false},
		{`\ `, " ", true},
		// hostile or malformed input covers nothing
		{"", "a", false},
		{"../etc/passwd", "etc/passwd", false},
		{"a/../b", "b", false},
		{"a\x00b", "a\x00b", false},
		{"*", "../x", false},
		{"*", "/etc/passwd", false},
		{"**", "", false},
		{strings.Repeat("a/", 100) + "b", strings.Repeat("a/", 100) + "b", false}, // too deep
		// the whole repository
		{"**", "any/thing/at/all.txt", true},
		{".", "x.txt", true},
		{"/", "x.txt", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.file); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

func TestScopeCoveredAndOutOfScope(t *testing.T) {
	scope := []string{"internal/core/**", "docs/guide.md", "cmd/*/main.go"}
	files := []string{"internal/core/core.go", "docs/guide.md", "docs/other.md", "cmd/app/main.go", "cmd/app/util.go", "README.md",
		"internal/core/core.go", "../evil", "/etc/passwd", "internal/util/util.go"}
	got := OutOfScope(files, scope)
	want := []string{"../evil", "/etc/passwd", "README.md", "cmd/app/util.go", "docs/other.md", "internal/util/util.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OutOfScope = %v\nwant %v", got, want)
	}
	if OutOfScope(files, nil) != nil || OutOfScope(files, []string{}) != nil {
		t.Fatal("an empty scope means unrestricted")
	}
	if !Covered(scope, "docs/guide.md") || Covered(scope, "docs/other.md") || Covered(nil, "x") {
		t.Fatal("Covered")
	}
	// an unusable pattern in the scope covers nothing, it does not widen the scope
	if got := OutOfScope([]string{"a/b.go"}, []string{"../*", "x/**"}); len(got) != 1 {
		t.Fatalf("bad pattern widened the scope: %v", got)
	}
	if err := ValidateScope(scope); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScope([]string{"ok", "../nope"}); err == nil {
		t.Fatal("ValidateScope accepted ..")
	}
}

func TestScopeOverlap(t *testing.T) {
	type pair struct{ a, b string }
	overlapping := []pair{
		{"internal/swarm/**", "internal/swarm/leases.go"},
		{"internal/swarm/**", "internal/**"},
		{"internal/swarm", "internal/swarm/sub/x.go"},
		{"internal/swarm/", "internal/*/leases.go"},
		{"**/*.go", "cmd/app/main.go"},
		{"**/*.go", "internal/**/x_?.go"},
		{"**/*_test.go", "internal/swarm/*"},
		{"*.go", "main.go"},
		{"{cmd,internal}/**", "internal/x"},
		{"src/*.{go,rs}", "src/lib.*"},
		{"a*b", "*b"},
		{"a*b", "a*"},
		{"a*c", "*b*"},
		{"file[0-9].txt", "file?.txt"},
		{"file[a-f].txt", "file[d-z].txt"},
		{"file[!a].txt", "file[!b].txt"},
		{"file[!a-y].txt", "file[y-z].txt"},
		{"**", "anything/goes"},
		{"data/*.csv", "data/table with space.csv"},
		{"docs/日本語/**", "docs/?本語/x.md"},
		{"a/**/b/**/c", "a/x/b/y/c/z"},
		{"a/**/b", "a/b"},
		{"**/b", "a/**"},
	}
	disjoint := []pair{
		{"internal/swarm/**", "internal/kv/**"},
		{"internal/swarm", "internal/swarmish"},
		{"cmd/app/main.go", "cmd/app/main.rs"},
		{"*.go", "cmd/*.go"},
		{"*.go", "*.rs"},
		{"*.go", "sub/x.go"}, // top-level only versus a nested file
		{"a*b", "c*"},
		{"a*b", "*c"},
		{"file[0-9].txt", "file[a-z].txt"},
		{"file[!a-z].txt", "file[b-y].txt"},
		{"file?.txt", "file.txt"},
		{"docs/guide.md", "docs/guide.md.bak"},
		{"a/b", "a/c/**"},
		{"{cmd,docs}/**", "internal/**"},
		{`file\*.txt`, "fileX.txt"},
		{"src/*.{go,rs}", "src/*.py"},
		{"README.md", "docs/README.md"},
	}
	for _, p := range overlapping {
		if got := Overlap([]string{p.a}, []string{p.b}); len(got) != 1 {
			t.Errorf("Overlap(%q, %q) = %v, want an overlap", p.a, p.b, got)
		}
		if got := Overlap([]string{p.b}, []string{p.a}); len(got) != 1 {
			t.Errorf("Overlap(%q, %q) = %v, want an overlap (symmetric)", p.b, p.a, got)
		}
	}
	for _, p := range disjoint {
		if got := Overlap([]string{p.a}, []string{p.b}); len(got) != 0 {
			t.Errorf("Overlap(%q, %q) = %v, want none", p.a, p.b, got)
		}
		if got := Overlap([]string{p.b}, []string{p.a}); len(got) != 0 {
			t.Errorf("Overlap(%q, %q) = %v, want none (symmetric)", p.b, p.a, got)
		}
	}
	// list semantics: entries of a that overlap something in b, in a's order, deduped
	a := []string{"cmd/**", "docs/**", "internal/swarm/**", "cmd/**", "web/**"}
	b := []string{"internal/**", "cmd/app/*.go", "assets/**"}
	if got, want := Overlap(a, b), []string{"cmd/**", "internal/swarm/**"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Overlap = %v, want %v", got, want)
	}
	pairs := OverlapPairs(a[:3], b)
	if len(pairs) != 2 || pairs[0] != (OverlapPair{"cmd/**", "cmd/app/*.go"}) || pairs[1] != (OverlapPair{"internal/swarm/**", "internal/**"}) {
		t.Fatalf("OverlapPairs = %v", pairs)
	}
	if Overlap(nil, b) != nil || Overlap(a, nil) != nil {
		t.Fatal("empty side")
	}
	// an unreadable scope is granted to nobody: it overlaps everything
	if got := Overlap([]string{"../x"}, []string{"docs/**"}); len(got) != 1 {
		t.Fatalf("unparsable pattern must overlap: %v", got)
	}
}

// TestTokensIntersectExhaustive checks the character-level product search against
// brute force: every string of up to six letters over {a,b,c,A} (A satisfies the
// upper-case class and every negated class). Patterns have at most three tokens,
// so a common string, if one exists, is at most six characters long, which makes
// the check exact in both directions.
func TestTokensIntersectExhaustive(t *testing.T) {
	var universe []string
	var gen func(prefix string)
	gen = func(prefix string) {
		universe = append(universe, prefix)
		if len(prefix) == 6 {
			return
		}
		for _, c := range "abcA" {
			gen(prefix + string(c))
		}
	}
	gen("")
	rng := rand.New(rand.NewSource(7))
	atoms := []string{"a", "b", "c", "?", "*", "[ab]", "[!a]", "[b-c]", "[!b-c]", "[[:upper:]]", "[a]"}
	rand3 := func() []token {
		n := 1 + rng.Intn(3)
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString(atoms[rng.Intn(len(atoms))])
		}
		return parseSegment(sb.String()).toks
	}
	overlaps, disjoint := 0, 0
	for i := 0; i < 700; i++ {
		a, b := rand3(), rand3()
		want := false
		for _, s := range universe {
			if matchTokens(a, s) && matchTokens(b, s) {
				want = true
				break
			}
		}
		if got := tokensIntersect(a, b); got != want {
			t.Fatalf("tokensIntersect = %v, brute force says %v (a=%+v b=%+v)", got, want, a, b)
		}
		if want {
			overlaps++
		} else {
			disjoint++
		}
	}
	if overlaps < 50 || disjoint < 50 {
		t.Fatalf("degenerate sample: %d overlapping, %d disjoint", overlaps, disjoint)
	}
}

// TestScopeOverlapPathLevelExhaustive checks the "**" dynamic programme: patterns
// of up to three segments over {a, b, ?, *, **} against every path of up to six
// segments over {a, b}.
func TestScopeOverlapPathLevelExhaustive(t *testing.T) {
	var universe []string
	var gen func(prefix []string)
	gen = func(prefix []string) {
		if len(prefix) > 0 {
			universe = append(universe, strings.Join(prefix, "/"))
		}
		if len(prefix) == 6 {
			return
		}
		for _, c := range []string{"a", "b"} {
			gen(append(prefix[:len(prefix):len(prefix)], c))
		}
	}
	gen(nil)
	rng := rand.New(rand.NewSource(11))
	atoms := []string{"a", "b", "?", "*", "**", "**"}
	randPat := func() string {
		n := 1 + rng.Intn(3)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = atoms[rng.Intn(len(atoms))]
		}
		return strings.Join(parts, "/")
	}
	overlaps, disjoint := 0, 0
	for i := 0; i < 1500; i++ {
		pa, pb := randPat(), randPat()
		ca, err1 := compileScope(pa)
		cb, err2 := compileScope(pb)
		if err1 != nil || err2 != nil {
			t.Fatalf("compile %q / %q: %v %v", pa, pb, err1, err2)
		}
		want := false
		for _, p := range universe {
			segs := strings.Split(p, "/")
			if ca.match(segs) && cb.match(segs) {
				want = true
				break
			}
		}
		got := len(Overlap([]string{pa}, []string{pb})) == 1
		if got != want {
			t.Fatalf("Overlap(%q, %q) = %v, brute force says %v", pa, pb, got, want)
		}
		if want {
			overlaps++
		} else {
			disjoint++
		}
	}
	if overlaps < 100 || disjoint < 30 {
		t.Fatalf("degenerate sample: %d overlapping, %d disjoint", overlaps, disjoint)
	}
}

func TestScopeGuardsAgainstPathologicalPatterns(t *testing.T) {
	// Many ** segments must not blow up (memoized), and huge brace groups are refused.
	pat := strings.Repeat("**/a/", 20) + "**"
	path := strings.Repeat("a/", 40)
	if Match(pat, path+"x") != true {
		t.Fatal("deep ** pattern should match")
	}
	braces := strings.Repeat("{a,b}", 10) // 1024 alternatives
	if Match(braces, "aaaaaaaaaa") {
		t.Fatal("brace explosion must be refused, matching nothing")
	}
	if err := ValidateScope([]string{braces}); err == nil {
		t.Fatal("ValidateScope accepted a brace bomb")
	}
	long := strings.Repeat("a", maxScopeBytes+1)
	if Match(long, long) {
		t.Fatal("over-long pattern accepted")
	}
	if got := Overlap([]string{pat}, []string{strings.Repeat("**/a/", 20) + "**"}); len(got) != 1 {
		t.Fatal("pathological but valid patterns must still be answered")
	}
}

func TestAssertReportsChangesOutsideScope(t *testing.T) {
	dir := newRepo(t)
	m := newManager(t, openRepo(t, dir))
	tree := mustCreate(t, m, "be-1")
	edit(t, tree, "internal/core/core.go", "package core\n\nfunc Name() string { return \"changed\" }\n")
	edit(t, tree, "docs/new.md", "new\n")
	if err := renameInTree(tree, "internal/util/util.go", "internal/util/renamed.go"); err != nil {
		t.Fatal(err)
	}
	out, err := Assert(context.Background(), tree, []string{"internal/core/**"})
	if err != nil {
		t.Fatal(err)
	}
	// a rename counts at both ends: the deleted old path and the added new path
	want := []string{"docs/new.md", "internal/util/renamed.go", "internal/util/util.go"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("Assert = %v, want %v", out, want)
	}
	out, err = Assert(context.Background(), tree, []string{"internal/**", "docs/*.md"})
	if err != nil || len(out) != 0 {
		t.Fatalf("Assert inside scope = %v, %v", out, err)
	}
	// committed changes count too
	if _, err := tree.Commit(context.Background(), "commit it"); err != nil {
		t.Fatal(err)
	}
	out, _ = Assert(context.Background(), tree, []string{"internal/core/**"})
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("after commit: %v", out)
	}
	if out, err := Assert(context.Background(), tree, nil); err != nil || out != nil {
		t.Fatalf("no scope: %v, %v", out, err)
	}
}

func renameInTree(tree *Tree, from, to string) error {
	return osRename(filepath.Join(tree.Path, filepath.FromSlash(from)), filepath.Join(tree.Path, filepath.FromSlash(to)))
}

// A pattern longer than the cap matches nothing, whatever it says (a scope is read from a configuration file and from what an agent writes:
// its size is bounded so that matching it is). The escaped form of a literal is longer than the literal, a backslash before each space and
// special character, so a name that fits the cap can have an escape that does not: FuzzScope found it with a name of 2,500 newlines, and its
// invariant ("the escaped literal matches itself") now holds for patterns inside the cap only.
func TestAPatternOverTheCapMatchesNothingEvenItsOwnLiteral(t *testing.T) {
	lit := "a" + strings.Repeat("\n", maxScopeBytes/2+10)
	if len(lit) > maxScopeBytes {
		t.Fatalf("the test's name is %d bytes, over the cap", len(lit))
	}
	esc := escapeGlob(lit)
	if len(esc) <= maxScopeBytes {
		t.Fatalf("the escape is %d bytes: not over the cap, the test proves nothing", len(esc))
	}
	if Match(esc, lit) {
		t.Fatal("a pattern over the cap matched")
	}
	if !Match(escapeGlob("a b"), "a b") {
		t.Fatal("a pattern inside the cap with a space in it does not match itself")
	}
}
