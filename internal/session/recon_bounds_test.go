package session

// The survey runs over whatever repository the harness is started in, at the start of
// every session. These tests make a repository that is as large and as unfriendly as a
// test can afford (most of its bytes are sparse, which costs no disk and is read as
// zeros) and check what the survey documents: it considers at most MaxFiles files, its
// text fits its token budget, and it neither fails nor takes minutes. The time limit is
// a guard against a complexity bomb, not a timing: the work is linear, and a
// race-enabled run on a loaded machine is seconds.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// reconGuard is how long one survey may take before the test calls it a hang.
const reconGuard = 3 * time.Minute

// within runs fn and fails the test if it has not finished within reconGuard.
func within(t testing.TB, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(reconGuard):
		t.Fatalf("%s did not finish within %v (a complexity bomb, or a loop that never ends)", what, reconGuard)
	}
}

// reconSlack is what a survey may exceed its budget by. The closing note that says source
// files were left out is written after the entries are counted, and in practice the
// entries' rounding up leaves room for it (no budget of the tests is exceeded); the slack is
// the size of that note, as a guard against the budget ever being missed by more.
const reconSlack = 16

// hostileTree makes a tree of about 200 MB (logical) of which a few megabytes are real.
// It has more files than the cap the test sets, which are the last ones in walk order.
func hostileTree(t *testing.T) (root string, files int) {
	t.Helper()
	root = t.TempDir()
	count := 0
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		count++
	}
	sparse := func(name string, size int64) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := f.Truncate(size); err != nil {
			t.Skipf("no sparse files here: %v", err)
		}
		count++
	}

	// Manifests that are as long as the survey reads them and made of what hurts most:
	// a scripts object of twenty thousand keys, a Makefile of thirty thousand rules, a
	// go.mod that is all white space, a pyproject with a name that never ends.
	var pj strings.Builder
	pj.WriteString(`{"scripts":{`)
	for i := 0; pj.Len() < 250<<10; i++ {
		fmt.Fprintf(&pj, "\"k%d\":\"v\",", i)
	}
	write("package.json", pj.String())
	write("Makefile", strings.Repeat("a:\n", 40000)+strings.Repeat("x", 100000))
	write("go.mod", "module "+strings.Repeat(" ", 64<<10))
	write("pyproject.toml", "name = \""+strings.Repeat("n", 64<<10))
	write("Cargo.toml", "name = \""+strings.Repeat("{", 64<<10))

	// Source files whose content is aimed at the regular expressions and the line reader.
	hostile := map[string]string{
		"real/funcs.go":    strings.Repeat("func ", 12000),
		"real/parens.go":   "func F" + strings.Repeat("(", 60000),
		"real/imports.go":  "package x\n\nimport (\n" + strings.Repeat("\t\"a/b\"\n", 8000),
		"real/quotes.go":   "package x\n\nimport (\n" + strings.Repeat("\"", 60000),
		"real/newlines.go": strings.Repeat("\n", 60000) + "func Late() {}\n",
		"real/long.go":     strings.Repeat("a", 100000) + "\nfunc After() {}\n",
		"real/pkgdoc.go":   strings.Repeat("// Package x y\n", 4000) + "package x\n",
		"real/imp.ts":      strings.Repeat("import x from './a'\n", 3000),
		"real/from.ts":     strings.Repeat("from ", 12000),
		"real/imp.py":      strings.Repeat("import a.b.c\n", 3000),
		"real/nul.go":      strings.Repeat("\x00", 50000),
		"real/mixed.rs":    strings.Repeat("pub fn a() {}\n", 4000),
	}
	for name, body := range hostile {
		write(name, body)
	}

	// Most of the bytes: eight source files of eight megabytes, sparse (the survey reads the
	// first 256 KiB of each), and one of 128 MiB.
	for i := 0; i < 8; i++ {
		sparse(fmt.Sprintf("sparse/d%02d/f%03d.go", i%10, i), 8<<20)
	}
	sparse("sparse/huge.go", 128<<20)

	// Wide and deep: five hundred small files in one directory, a path a hundred and
	// twenty directories deep, and names as long as a name may be.
	for i := 0; i < 520; i++ {
		write(fmt.Sprintf("wide/w%04d.go", i), "package w\n\nfunc W() {}\n")
	}
	deep := strings.Repeat("d/", 120)
	write(deep+"deep.go", "package d\n\nfunc Deep() {}\n")
	write(strings.Repeat("n", 250)+".go", "package n\n\nfunc Long() {}\n")
	return root, count
}

func TestReconOfAHostileTreeStaysWithinItsCaps(t *testing.T) {
	root, files := hostileTree(t)
	const maxFiles, budget = 500, 3000

	var r, again *Recon
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	within(t, "BuildRecon", func() {
		var err error
		r, err = BuildRecon(context.Background(), ReconOptions{Root: root, BudgetTokens: budget, MaxFiles: maxFiles, NoGit: true})
		if err != nil {
			t.Error(err)
		}
	})
	runtime.ReadMemStats(&after)
	if r == nil {
		t.FailNow()
	}
	t.Logf("%d files in the tree, %d considered, %d tokens; %d MiB allocated", files, r.Files, r.Tokens, (after.TotalAlloc-before.TotalAlloc)>>20)

	if r.Files != maxFiles {
		t.Errorf("considered %d files, want the cap of %d (the tree has %d)", r.Files, maxFiles, files)
	}
	if r.Tokens > budget+reconSlack {
		t.Errorf("the survey is %d tokens for a budget of %d", r.Tokens, budget)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 4<<30 { // 46 MiB quiet, about a gigabyte under the race detector
		t.Errorf("the survey allocated %d MiB", got>>20)
	}
	within(t, "the second BuildRecon", func() {
		var err error
		again, err = BuildRecon(context.Background(), ReconOptions{Root: root, BudgetTokens: budget, MaxFiles: maxFiles, NoGit: true})
		if err != nil {
			t.Error(err)
		}
	})
	if again == nil || joinSegments(again) != joinSegments(r) {
		t.Errorf("the survey of an unchanged tree differs between two runs")
	}
}

// The cap on files is the option's, and the default is the documented 30000.
func TestReconCapsTheFilesItConsiders(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		writeTree(t, root, map[string]string{fmt.Sprintf("p%02d/f%d.go", i%6, i): "package p\n"})
	}
	for _, tc := range []struct{ max, want int }{{10, 10}, {59, 59}, {60, 60}, {1000, 60}, {0, 60}, {-5, 60}} {
		r, err := BuildRecon(context.Background(), ReconOptions{Root: root, MaxFiles: tc.max, NoGit: true})
		if err != nil {
			t.Fatal(err)
		}
		if r.Files != tc.want {
			t.Errorf("MaxFiles %d: considered %d files, want %d", tc.max, r.Files, tc.want)
		}
	}
}

// Whatever the budget, the text fits it (with the closing note's slack): the layout takes at
// most two fifths, and the map what is left.
func TestReconTextFitsEveryBudget(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"go.mod": "module example.com/big\n\ngo 1.24\n", "Makefile": "test:\n\ttrue\nbuild:\n\ttrue\n"}
	for i := 0; i < 400; i++ {
		files[fmt.Sprintf("pkg%02d/file%03d.go", i%25, i)] = fmt.Sprintf("// Package pkg%02d is number %d of many.\npackage pkg\n\nfunc Exported%d() {}\n\ntype Thing%d struct{}\n", i%25, i, i, i)
	}
	writeTree(t, root, files)
	est := core.NewBytesEstimator()
	for _, budget := range []int{1, 8, 50, 100, 200, 201, 300, 500, 1000, 2000, 5000, 20000} {
		r, err := BuildRecon(context.Background(), ReconOptions{Root: root, BudgetTokens: budget, Est: est, NoGit: true})
		if err != nil {
			t.Fatal(err)
		}
		limit := budget + reconSlack
		if budget < 40 { // the note that says the layout was cut is all that fits
			limit = 40
		}
		if r.Tokens > limit {
			t.Errorf("budget %d: the survey is %d tokens:\n%s", budget, r.Tokens, joinSegments(r))
		}
		if budget >= 2000 && len(r.Segments) != 2 {
			t.Errorf("budget %d: %d segments, want the layout and the code map", budget, len(r.Segments))
		}
	}
}

// The detectors take what a file holds, however much of it and however it is arranged;
// 256 KiB (the most the survey reads of a file) of any of these is a few milliseconds of
// linear work, and a quadratic one (sixty billion steps) would not finish. Each detector gets the
// inputs it is most exposed to.
func TestDetectorsAreLinearOnHostileContent(t *testing.T) {
	mb := func(unit string) string { return strings.Repeat(unit, (256<<10)/len(unit)+1) }
	in := map[string]string{
		"func keyword":          mb("func "),
		"open parens":           "func F" + mb("("),
		"newlines":              mb("\n"),
		"quotes":                mb("\""),
		"quoted pairs":          mb("\"a\" "),
		"import lines":          mb("import a.b.c\n"),
		"from lines":            mb("from './a' "),
		"braces":                "{\"scripts\":" + mb("{"),
		"scripts keys":          "{\"scripts\":{" + mb("\"k\":1,"),
		"escaped quotes":        "{\"scripts\":{\"a\":\"" + mb("\\\""),
		"rule names":            mb("a:\n"),
		"one long identifier":   mb("a"),
		"rule with white space": "a" + mb(" ") + ":",
		"package comments":      mb("// Package x y\n"),
		"comment lines":         "// Package x y\n" + mb("// more\n"),
		"NUL bytes":             mb("\x00"),
		"invalid UTF-8":         mb("\xff\xfe"),
	}
	est := core.NewBytesEstimator()
	each := func(names []string, what string, fn func(text string)) {
		for _, n := range names {
			text := in[n]
			within(t, what+" on "+n, func() { fn(text) })
		}
	}
	each([]string{"func keyword", "open parens", "newlines", "one long identifier", "NUL bytes", "invalid UTF-8"}, "extractSymbols", func(text string) {
		for _, lang := range []string{"go", "python", "ruby", "ts"} {
			extractSymbols(lang, text)
		}
	})
	each([]string{"braces", "scripts keys", "escaped quotes", "quotes", "NUL bytes"}, "jsonKeys", func(text string) { jsonKeys(text, "scripts") })
	each([]string{"quotes", "quoted pairs", "import lines", "from lines", "newlines"}, "countImports", func(text string) {
		for _, f := range []string{"a.go", "a.ts", "a.py"} {
			countImports(f, text, "example.com/m", map[string]int{})
		}
	})
	each([]string{"package comments", "comment lines", "newlines", "one long identifier"}, "packageDocs", func(text string) {
		packageDocs(".", map[string]string{"a/a.go": text}, nil, 500, est)
	})
	each([]string{"newlines", "one long identifier", "invalid UTF-8"}, "fitTokens", func(text string) { fitTokens(text, 100, est) })
	each([]string{"invalid UTF-8", "NUL bytes", "newlines"}, "safeLine", func(text string) { safeLine(text) })
	each([]string{"rule names", "rule with white space", "one long identifier", "braces", "scripts keys"}, "detectManifests", func(text string) {
		root, err := os.MkdirTemp("", "recon-manifests-")
		if err != nil {
			t.Error(err)
			return
		}
		defer os.RemoveAll(root)
		for _, m := range []string{"go.mod", "package.json", "Makefile", "pyproject.toml", "Cargo.toml"} {
			if err := os.WriteFile(filepath.Join(root, m), []byte(text), 0o644); err != nil {
				t.Error(err)
				return
			}
		}
		detectManifests(root, []string{"Cargo.toml", "Makefile", "go.mod", "package.json", "pyproject.toml"})
	})
}

// Twenty thousand files in twenty thousand directories, every path a hundred characters
// longer than it needs to be (the survey is given the names, not the disk): the layout and
// the ranking are sorts and maps over the names, nothing more.
func TestLayoutAndDocsOfAHugeListing(t *testing.T) {
	var files []string
	texts := map[string]string{}
	for i := 0; i < 20000; i++ {
		f := fmt.Sprintf("top%05d/sub%d/%s/file%d.go", i, i%7, strings.Repeat("x", 100), i)
		files = append(files, f)
		texts[f] = fmt.Sprintf("// Package p%d does thing %d.\npackage p\n", i, i)
	}
	est := core.NewBytesEstimator()
	within(t, "layoutText", func() {
		if got := layoutText(files); len(got) < 1000 {
			t.Errorf("layout of %d files is %d bytes", len(files), len(got))
		}
	})
	within(t, "packageDocs", func() { packageDocs(".", texts, nil, 1000, est) })
}
