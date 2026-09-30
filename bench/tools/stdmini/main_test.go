package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadList(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "list.txt")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	got, err := readList(write("# comment\nnet/url\n\ngo/ast !example_test.go !filter_test.go  # why\ncmp\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].path != "cmp" || got[1].path != "go/ast" || got[2].path != "net/url" {
		t.Fatalf("entries not sorted by path: %+v", got)
	}
	if !got[1].exclude["example_test.go"] || !got[1].exclude["filter_test.go"] || len(got[0].exclude) != 0 {
		t.Errorf("exclusions: %+v", got)
	}
	for name, body := range map[string]string{
		"duplicate":        "cmp\ncmp\n",
		"bare word":        "cmp example_test.go\n",
		"exclude with dir": "cmp !sub/file.go\n",
		"empty":            "# nothing\n",
	} {
		if _, err := readList(write(body)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestRewriteImports(t *testing.T) {
	selected := map[string]bool{"go/token": true, "go/scanner": true}
	cases := []struct{ name, in, want string }{
		{
			name: "selected import is rewritten and the block re-sorted",
			in:   "package p\n\nimport (\n\t\"fmt\"\n\t\"go/token\"\n\t\"strings\"\n)\n\nvar _ = token.NoPos\n",
			want: "package p\n\nimport (\n\t\"fmt\"\n\t\"stdmini/go/token\"\n\t\"strings\"\n)\n\nvar _ = token.NoPos\n",
		},
		{
			name: "alias, dot and blank imports keep their form",
			in:   "package p\n\nimport (\n\ttok \"go/token\"\n\t. \"go/scanner\"\n\t_ \"go/token\"\n)\n",
			want: "package p\n\nimport (\n\t. \"stdmini/go/scanner\"\n\t_ \"stdmini/go/token\"\n\ttok \"stdmini/go/token\"\n)\n", // gofmt orders by path, then by name
		},
		{
			name: "neighbours of a selected path are not selected",
			in:   "package p\n\nimport (\n\t\"go/tokenx\"\n\t\"go/token/sub\"\n\t\"go/ast\"\n)\n",
			want: "package p\n\nimport (\n\t\"go/tokenx\"\n\t\"go/token/sub\"\n\t\"go/ast\"\n)\n",
		},
		{
			name: "a file with nothing to rewrite comes back byte for byte, even when it is not gofmt'd",
			in:   "package p\nimport \"fmt\"\nfunc   f() {  fmt.Println() }\n",
			want: "package p\nimport \"fmt\"\nfunc   f() {  fmt.Println() }\n",
		},
		{
			name: "single import",
			in:   "package p\n\nimport \"go/scanner\"\n\nvar _ scanner.Mode\n",
			want: "package p\n\nimport \"stdmini/go/scanner\"\n\nvar _ scanner.Mode\n",
		},
	}
	for _, c := range cases {
		got, err := rewriteImports("x.go", []byte(c.in), "stdmini", selected)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	if _, err := rewriteImports("bad.go", []byte("package p\nimport (\n"), "stdmini", selected); err == nil {
		t.Error("a file that does not parse must be an error")
	}
}

func TestCopyPackage(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"a.go":                    "package p\n\nimport \"go/token\"\n\nvar _ = token.NoPos\n",
		"a_test.go":               "package p\n",
		"skip_test.go":            "package p\n",
		"data.txt":                "plain\n",
		"sub/other.go":            "package sub\n",
		"testdata/broken.go":      "this is not go {{{\n",
		"testdata/deep/x.go":      "package x\n\nimport \"go/token\"\n",
		"testdata/deep/list.json": "[1]\n",
	}
	for rel, body := range files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a.go", filepath.Join(src, "link.go")); err != nil {
		t.Logf("no symlinks here: %v", err) // the copy must simply not see one
	}
	dst := filepath.Join(t.TempDir(), "out", "p")
	if err := copyPackage(src, dst, "stdmini", map[string]bool{"go/token": true}, map[string]bool{"skip_test.go": true}); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		return string(b), err == nil
	}
	if got, _ := read("a.go"); !strings.Contains(got, `"stdmini/go/token"`) {
		t.Errorf("a.go was not rewritten:\n%s", got)
	}
	for _, rel := range []string{"a_test.go", "data.txt", "testdata/broken.go", "testdata/deep/list.json"} {
		if _, ok := read(rel); !ok {
			t.Errorf("%s was not copied", rel)
		}
	}
	// testdata is data, not code: its Go files are copied untouched.
	if got, _ := read("testdata/deep/x.go"); !strings.Contains(got, `"go/token"`) || strings.Contains(got, "stdmini") {
		t.Errorf("testdata/deep/x.go was rewritten:\n%s", got)
	}
	for _, rel := range []string{"skip_test.go", "sub/other.go", "link.go"} {
		if _, ok := read(rel); ok {
			t.Errorf("%s must not be copied", rel)
		}
	}
}

// TestBuildIsDeterministicAndStandsAlone builds a corpus from two real standard library packages, one importing the other,
// twice: the result compiles and passes its tests with the copies' imports pointing at each other, and the same Go version
// always gives the same tree and the same commit.
func TestBuildIsDeterministicAndStandsAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	list := filepath.Join(t.TempDir(), "list.txt")
	if err := os.WriteFile(list, []byte("go/scanner\ngo/token !example_test.go\ncontainer/list\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sums, revs [2]string
	for i := range sums {
		out := filepath.Join(t.TempDir(), "corpus")
		var stdout, stderr strings.Builder
		args := []string{"--out", out, "--packages", list, "--verify=" + map[bool]string{true: "true", false: "false"}[i == 0]}
		if err := run(args, &stdout, &stderr); err != nil {
			t.Fatalf("build %d: %v\n%s", i, err, stderr.String())
		}
		sum, n, err := treeHash(out)
		if err != nil || n == 0 {
			t.Fatalf("treeHash: %d files, %v", n, err)
		}
		rev, err := gitOut(out, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		sums[i], revs[i] = sum, rev
		if i == 0 {
			b, err := os.ReadFile(filepath.Join(out, "go", "scanner", "scanner.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), `"stdmini/go/token"`) || strings.Contains(string(b), "\t\"go/token\"") {
				t.Errorf("go/scanner still imports the standard go/token")
			}
			if st, err := gitOut(out, "status", "--porcelain"); err != nil || strings.TrimSpace(st) != "" {
				t.Errorf("the corpus must be one clean commit: %q %v", st, err)
			}
		}
	}
	if sums[0] != sums[1] || revs[0] != revs[1] {
		t.Errorf("two builds differ:\n%s %s\n%s %s", sums[0], revs[0], sums[1], revs[1])
	}
}

func TestBuildRefusesANonEmptyDirectory(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := build(options{out: out, module: "stdmini"}, []entry{{path: "cmp"}}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("got %v, want a refusal", err)
	}
}
