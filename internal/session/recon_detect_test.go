package session

// Table tests for the detectors behind the project survey (recon.go): what each one
// finds, and what it must leave alone. The survey runs them over arbitrary repository
// files, so the negative cases are as much the contract as the positive ones.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
)

func TestLanguageOf(t *testing.T) {
	for ext, lang := range extLang {
		for _, name := range []string{"a" + ext, "dir/sub/a" + ext, "A" + strings.ToUpper(ext), "x.y" + ext} {
			if got := languageOf(name); got != lang {
				t.Errorf("languageOf(%q) = %q, want %q", name, got, lang)
			}
		}
	}
	for _, name := range []string{"", "Makefile", ".gitignore", "go", "a.go.bak", "a.gox", "archive.tar.gz", "noext.", "a.md", "dir.go/file", "a.go/"} {
		if got := languageOf(name); got != "" {
			t.Errorf("languageOf(%q) = %q, want no language", name, got)
		}
	}
}

func TestIsNoise(t *testing.T) {
	for _, tc := range []struct {
		path  string
		noise bool
	}{
		{"server_test.go", true}, {"pkg/Server_TEST.go", true}, {"api.pb.go", true}, {"x.generated.go", true},
		{"bundle.min.js", true}, {"types.d.ts", true}, {"test_utils.py", true}, {"utils_test.py", true},
		{"a.test.ts", true}, {"a.spec.js", true},
		{"testdata/x.go", true}, {"a/fixtures/x.go", true}, {"src/__tests__/x.js", true}, {"tests/x.py", true}, {"a/test/x.go", true},
		{"mocks/m.go", true}, {"__snapshots__/s.js", true}, {"third_party/x/y.go", true}, {"a/b/generated/g.go", true}, {"A/TESTS/x.go", true},

		{"main.go", false}, {"latest.go", false}, {"attest/x.go", false}, {"contest.go", false}, {"src/testing/x.go", false},
		{"pkg/generate/x.go", false}, {"a.tests.go", false}, {"spec.go", false}, {"testify.go", false}, {"minify.js", false},
		{"a.min.jsx", false}, {"d.ts", false}, {"attest_test", false},
	} {
		if got := isNoise(tc.path); got != tc.noise {
			t.Errorf("isNoise(%q) = %v, want %v", tc.path, got, tc.noise)
		}
	}
}

func TestSkippedDropsDependencyDirectoriesEvenWhenTracked(t *testing.T) {
	for _, tc := range []struct {
		path string
		skip bool
	}{
		{"vendor/x.go", true}, {"a/node_modules/b.js", true}, {"build/out.go", true}, {"src/dist/x.js", true},
		{"pkg/__pycache__/x.py", true}, {"venv/lib/x.py", true}, {"target/debug/x.rs", true}, {"a/testdata/x.go", true},
		{"a/.git/hooks/x", true},
		// bin and obj are in the walk's list but a tracked file there is part of the project.
		{"bin/tool.sh", false}, {"obj/x.cs", false},
		{"src/distribution/x.js", false}, {"vendor", false}, {"main.go", false}, {"a/vendored/x.go", false}, {"docs/build.md", false},
	} {
		if got := skipped(tc.path); got != tc.skip {
			t.Errorf("skipped(%q) = %v, want %v", tc.path, got, tc.skip)
		}
	}
}

func TestListFilesWalk(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go": "package a\n", "src/b.go": "package b\n", ".github/workflows/ci.yml": "name: ci\n", "docs/readme.md": "x\n",
		"node_modules/x/y.js": "1\n", ".hidden/z.go": "package z\n", "vendor/v.go": "package v\n", "bin/tool": "x\n", "obj/o.cs": "x\n",
		"build/out.go": "package out\n", ".venv/lib/x.py": "x\n", "testdata/t.go": "package t\n", "deep/.dot/q.go": "package q\n",
	})
	if err := os.Symlink("a.go", filepath.Join(root, "link.go")); err != nil {
		t.Logf("no symlinks here: %v", err)
	} else {
		os.Symlink("src", filepath.Join(root, "linkdir"))
		os.Symlink("/etc/hostname", filepath.Join(root, "outside.txt"))
	}
	got, err := listFiles(context.Background(), root, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{".github/workflows/ci.yml", "a.go", "docs/readme.md", "src/b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listed %q, want %q (hidden and build directories, symlinks and dependency trees are not part of the survey)", got, want)
	}
	capped, err := listFiles(context.Background(), root, 2, false)
	if err != nil || len(capped) != 2 {
		t.Errorf("a cap of 2 gave %q (%v)", capped, err)
	}
}

func TestLayoutText(t *testing.T) {
	files := []string{
		"README.md", "go.mod", "cmd/app/main.go", "cmd/app/util.go", "cmd/tool/main.go",
		"internal/a/x.go", "internal/a/y.go", "internal/b/z.go", "docs/guide.md",
	}
	want := "Layout (file counts):\n" +
		"  cmd/ 3: app(2) tool(1)\n" +
		"  docs/ 1\n" +
		"  internal/ 3: a(2) b(1)\n" +
		"  root files: README.md go.mod\n"
	if got := layoutText(files); got != want {
		t.Errorf("layout:\n%s\nwant:\n%s", got, want)
	}

	// Sub-directories are listed most files first, then by name, and at most 14.
	var many []string
	for i := 0; i < 20; i++ {
		for j := 0; j <= i%3; j++ {
			many = append(many, fmt.Sprintf("big/d%02d/f%d.go", i, j))
		}
	}
	got := layoutText(many)
	if !strings.Contains(got, " +6 more\n") || len(regexp.MustCompile(`d\d\d\(\d\)`).FindAllString(got, -1)) != 14 {
		t.Errorf("a directory of 20 sub-directories must show 14 and say '+6 more':\n%s", got)
	}
	if !strings.HasPrefix(got, "Layout (file counts):\n  big/ 39: d02(3) d05(3) d08(3) d11(3) d14(3) d17(3) d01(2) d04(2)") {
		t.Errorf("sub-directories are not ordered by size, then name:\n%s", got)
	}

	// Root files are listed in order, at most 24.
	var root []string
	for i := 0; i < 30; i++ {
		root = append(root, fmt.Sprintf("f%02d.txt", 29-i))
	}
	got = layoutText(root)
	if !strings.Contains(got, "root files: f00.txt f01.txt") || !strings.HasSuffix(got, "f23.txt +6 more\n") {
		t.Errorf("root files:\n%s", got)
	}
	if got := layoutText(nil); got != "Layout (file counts):\n" {
		t.Errorf("an empty tree: %q", got)
	}
}

// detect runs detectManifests over a directory made of files (path -> content). The
// listing is sorted, as BuildRecon gives it; extra are names that are in the listing and
// not on disk.
func detect(t *testing.T, files map[string]string, extra ...string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, files)
	names := append([]string(nil), extra...)
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return detectManifests(root, names)
}

func TestDetectManifests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		text  string
		cmds  []string
	}{
		{"nothing", map[string]string{"README.md": "hello"}, "", nil},

		{"a Go module with binaries", map[string]string{
			"go.mod":          "module example.com/m\n\ngo 1.22.3\n",
			"cmd/app/main.go": "package main", "cmd/app/sub/x.go": "package x", "cmd/tool/main.go": "package main",
			"cmd/tool/util.go": "package main", "cmd/x.go": "package x", "cmd/readme.txt": "t",
		}, "Go module example.com/m (go 1.22.3)\n  binaries: cmd/app cmd/tool\n", []string{"go build ./...", "go vet ./...", "go test ./..."}},
		{"a go.mod with CRLF line ends", map[string]string{"go.mod": "module example.com/crlf\r\n\r\ngo 1.21\r\n"},
			"Go module example.com/crlf (go 1.21)\n", []string{"go build ./...", "go vet ./...", "go test ./..."}},
		{"a commented module line is not the module", map[string]string{"go.mod": "// module fake.com/x\nmodule real.com/x\n// go 1.1\ngo 1.21\nrequire go.uber.org/zap v1.0.0\n"},
			"Go module real.com/x (go 1.21)\n", []string{"go build ./...", "go vet ./...", "go test ./..."}},
		{"a go.mod with no module", map[string]string{"go.mod": "go 1.20\n"},
			"Go module  (go 1.20)\n", []string{"go build ./...", "go vet ./...", "go test ./..."}},

		{"a Node package: scripts sorted, the standard ones become commands", map[string]string{
			"package.json": `{"name":"x","scripts":{"test":"jest","build":"tsc","start":"node .","lint":"eslint .","a b":"c"},"dependencies":{"scripts":"1"}}`,
		}, "Node package (package.json); scripts: a b, build, lint, start, test\n", []string{"npm run build", "npm run lint", "npm run test"}},
		{"a Node package without scripts", map[string]string{"package.json": `{"name":"x"}`}, "Node package (package.json); scripts: \n", nil},
		{"a Node package whose scripts hold braces and quotes in strings", map[string]string{
			"package.json": `{"scripts":{"test":"echo \"}\" && jest","build":"{ tsc; }","check":"x"},"other":{"lint":"no"}}`,
		}, "Node package (package.json); scripts: build, check, test\n", []string{"npm run build", "npm run check", "npm run test"}},

		{"a Python project with pytest, ruff and mypy configured", map[string]string{
			"pyproject.toml": "[project]\nname = \"my-pkg\"\n[tool.pytest.ini_options]\n[tool.ruff]\n[tool.mypy]\n",
		}, "Python project my-pkg\n", []string{"pytest", "ruff check .", "mypy ."}},
		{"a Python project with single quotes and a tests directory", map[string]string{
			"pyproject.toml": "[tool.poetry]\nname = 'quoted'\n", "tests/test_a.py": "x",
		}, "Python project quoted\n", []string{"pytest"}},
		{"a Python project with only requirements", map[string]string{"requirements.txt": "requests\n"}, "Python project\n", nil},
		{"setup.py and a test directory", map[string]string{"setup.py": "x", "test/a.py": "x"}, "Python project\n", []string{"pytest"}},
		{"an indented name is not the project's", map[string]string{"pyproject.toml": "[tool.x]\n  name = \"nested\"\n"}, "Python project\n", nil},

		{"a Rust crate", map[string]string{"Cargo.toml": "[package]\nname = \"crate-x\"\nversion = \"0.1.0\"\n"},
			"Rust crate crate-x\n", []string{"cargo build", "cargo test", "cargo clippy"}},
		{"a Rust workspace", map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n"},
			"Rust workspace\n", []string{"cargo build", "cargo test", "cargo clippy"}},

		{"Maven", map[string]string{"pom.xml": "<project/>"}, "Maven project\n", []string{"mvn test"}},
		{"Gradle", map[string]string{"build.gradle": "plugins {}"}, "Gradle project\n", []string{"./gradlew test"}},
		{"Gradle with Kotlin", map[string]string{"build.gradle.kts": "plugins {}"}, "Gradle project\n", []string{"./gradlew test"}},

		{"a Makefile: rules, not assignments, patterns, recipes or special targets", map[string]string{"Makefile": "" +
			".PHONY: all test\nVAR := 1\nVAR2 = 2\nFOO:=bar\nCC ?= gcc\nall: test lint\ntest:\n\tgo test ./...\nlint: ; go vet ./...\n%.o: %.c\nbuild::\ndeploy : x\nall: again\n",
		}, "Makefile targets: all test lint build deploy\n", []string{"make test", "make lint", "make build"}},
		{"a Makefile with no useful target", map[string]string{"Makefile": "run:\n\t./run.sh\n"}, "Makefile targets: run\n", nil},
		{"GNUmakefile is found when Makefile is not", map[string]string{"GNUmakefile": "check:\n\ttrue\n"}, "Makefile targets: check\n", []string{"make check"}},
		{"an empty Makefile", map[string]string{"Makefile": ""}, "", nil},

		{"Docker and CI", map[string]string{"Dockerfile": "FROM x", ".github/workflows/ci.yml": "x", ".github/workflows/release.yml": "x"},
			"Dockerfile present\nCI: ci.yml release.yml\n", nil},
		{"CI with many workflows", map[string]string{
			".github/workflows/a.yml": "x", ".github/workflows/b.yml": "x", ".github/workflows/c.yml": "x", ".github/workflows/d.yml": "x",
			".github/workflows/e.yml": "x", ".github/workflows/f.yml": "x", ".github/workflows/g.yml": "x", ".github/workflows/h.yml": "x",
		}, "CI: a.yml b.yml c.yml d.yml e.yml f.yml +2 more\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, cmds := detect(t, tc.files)
			if text != tc.text {
				t.Errorf("text = %q, want %q", text, tc.text)
			}
			if !reflect.DeepEqual(cmds, tc.cmds) {
				t.Errorf("commands = %q, want %q", cmds, tc.cmds)
			}
		})
	}
}

func TestDetectManifestsCombinesEverythingItFinds(t *testing.T) {
	text, cmds := detect(t, map[string]string{
		"go.mod": "module example.com/m\ngo 1.24\n", "package.json": `{"scripts":{"test":"x"}}`, "Makefile": "test:\n\ttrue\n",
		"Dockerfile": "FROM x", ".github/workflows/ci.yml": "x",
	})
	want := "Go module example.com/m (go 1.24)\nNode package (package.json); scripts: test\nMakefile targets: test\nDockerfile present\nCI: ci.yml\n"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if want := []string{"go build ./...", "go vet ./...", "go test ./...", "npm run test", "make test"}; !reflect.DeepEqual(cmds, want) {
		t.Errorf("commands = %q, want %q (each once, in the order found)", cmds, want)
	}
	// Duplicates are dropped: a Makefile with the same target twice, and a go.mod next to a Cargo.toml.
	_, cmds = detect(t, map[string]string{"Makefile": "test:\ntest:\n", "go.mod": "module m\n"})
	if n := strings.Count(strings.Join(cmds, "\n"), "make test"); n != 1 {
		t.Errorf("make test listed %d times", n)
	}
}

func TestJSONKeys(t *testing.T) {
	for _, tc := range []struct {
		name, doc, key string
		want           []string
	}{
		{"the keys of an object, sorted", `{"scripts":{"b":1,"a":2,"c":3}}`, "scripts", []string{"a", "b", "c"}},
		{"nested objects and arrays do not add keys", `{"scripts":{"a":{"x":1,"y":[{"z":1}]},"b":[1,2]}}`, "scripts", []string{"a", "b"}},
		{"braces and escaped quotes inside strings", `{"scripts":{"a":"}{\"","b":"\\","c":"x"}}`, "scripts", []string{"a", "b", "c"}},
		{"the first member of that name", `{"x":{"scripts":{"first":1}},"scripts":{"second":1}}`, "scripts", []string{"first"}},
		{"another key", `{"scripts":{"a":1},"bin":{"tool":"x"}}`, "bin", []string{"tool"}},
		{"a key that is missing", `{"name":"x"}`, "scripts", nil},
		{"no object after the name", `{"scripts":"none"}`, "scripts", nil},
		{"an empty object", `{"scripts":{}}`, "scripts", nil},
		{"an object that never ends: the keys so far", `{"scripts":{"a":1,"b":`, "scripts", []string{"a", "b"}},
		{"not JSON at all", `this is "scripts" {oops`, "scripts", nil},
		{"empty", ``, "scripts", nil},
		{"hand-edited: comments, trailing commas", "{\"scripts\":{\n// c\n\"a\":1,\n\"b\":2,\n}}", "scripts", []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonKeys(tc.doc, tc.key); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("jsonKeys(%q, %q) = %q, want %q", tc.doc, tc.key, got, tc.want)
			}
		})
	}
}

func TestExtractSymbols(t *testing.T) {
	for _, tc := range []struct {
		lang string
		text string
		want []string
	}{
		{"go", "" +
			"func Serve() {}\n" +
			"func (s *Server) Start() error {\n" +
			"func (Server) Name() string\n" +
			"func (s Server[T]) Get(k string) T\n" +
			"func Map[T, U any](xs []T) []U\n" +
			"type Config struct {\n" +
			"type Reader interface {\n" +
			"func Serve() {}\n" + // a repeat
			"func (c *Client) Start() {}\n",
			[]string{"Serve", "Server.Start", "Server.Name", "Server.Get", "Map", "Config", "Reader", "Client.Start"}},
		{"go", "" +
			"func helper() {}\nfunc (s *server) Start() {}\nfunc (s *Server) stop() {}\ntype config struct {\ntype Alias = Other\n" +
			"func(x int) {}\n\tfunc Nested()\n// func Commented()\nvar f = func() {}\ntype (\n\tA int\n)\nfunc init() {}\nfunc _() {}\n", nil},

		{"python", "def serve():\nasync def fetch(x):\nclass Server:\nclass Sub(Base):\ndef serve():\n", []string{"serve", "fetch", "Server", "Sub"}},
		{"python", "def _private():\nclass _Hidden:\n    def method(self):\n# def commented():\ndefine()\nclassy = 1\n", nil},

		{"js", "" +
			"export function foo() {}\nexport default function bar() {}\nexport async function baz() {}\nexport class K {}\n" +
			"export const x = 1\nexport let y\nexport var z\nfunction plain() {}\nasync function af() {}\nexport function* gen() {}\nexport const $el = 1\n",
			[]string{"foo", "bar", "baz", "K", "x", "y", "z", "plain", "af", "gen", "$el"}},
		{"js", "export { a, b }\nexport default {}\nconst local = 1\n  export function indented() {}\n// export function c() {}\nexports.foo = 1\nmodule.exports = {}\nfunction* anon() {}\n", nil},

		{"ts", "" +
			"export interface Foo {\nexport type Bar = {\nexport enum E {\nexport abstract class A {\nexport declare function f(): void\n" +
			"export default class D {\nexport const c = 1\nexport async function g() {}\n",
			[]string{"Foo", "Bar", "E", "A", "f", "D", "c", "g"}},
		{"ts", "interface Local {\ntype T = 1\nexport * from './x'\nexport default 5\nfunction plain() {}\n  export class Indented {}\n", nil},

		{"rust", "pub fn run() {}\nfn private() {}\npub(crate) struct S;\n    pub async fn go() {}\npub enum E {}\ntrait T {}\n",
			[]string{"run", "private", "S", "go", "E", "T"}},
		{"rust", "let fn_ptr = 1;\n// pub fn commented() {}\npub mod m;\nimpl S {}\nfunction f() {}\npub const C: u8 = 1;\n", nil},

		{"java", "public class A {\npublic abstract class B {\nprotected static final class C {\npublic interface I {\npublic enum E {\npublic record R(int x) {}\n    public class Nested {\n",
			[]string{"A", "B", "C", "I", "E", "R", "Nested"}},
		{"java", "class Package {\nprivate class P {\npublic void method() {}\n// public class C {\n", nil},

		{"kotlin", "class A\ndata class D(val x: Int)\nsealed interface S\nobject O\nfun main() {}\npublic fun pub() {}\ninternal class I\nopen class Base\nabstract class AB\n",
			[]string{"A", "D", "S", "O", "main", "pub", "I", "Base", "AB"}},
		{"kotlin", "private fun hidden() {}\nval x = 1\n// class C\nprotected class P\n", nil},

		{"csharp", "public class A\ninternal static class B\npublic sealed class C\npublic partial class P\npublic interface I\npublic enum E\npublic struct S\npublic record R\n",
			[]string{"A", "B", "C", "P", "I", "E", "S", "R"}},
		{"csharp", "class NoVis\nprivate class X\npublic void M() {}\npublic static void Main()\n[Attribute]\n", nil},

		{"ruby", "class Foo\nmodule Bar\ndef baz\n  def indented\nclass Foo::Bar\ndef self.make\n", []string{"Foo", "Bar", "baz", "indented", "Foo::Bar", "self.make"}},
		{"ruby", "# class Commented\nclassify(x)\ndefine_method :x\nend\n", nil},

		{"php", "class A\nabstract class B\nfinal class C\ninterface I\ntrait T\nfunction f()\n  function indented()\n", []string{"A", "B", "C", "I", "T", "f", "indented"}},
		{"php", "$class = 1;\n// class X\npublic function pm() {}\nprivate function q() {}\n", nil},

		{"haskell", "module Main where\nmain = return ()\n", nil},
		{"", "func A() {}\n", nil},
	} {
		got := extractSymbols(tc.lang, tc.text)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: extractSymbols of\n%s= %q, want %q", tc.lang, tc.text, got, tc.want)
		}
	}
}

// A line is read up to 240 bytes: a declaration that starts with them is found, a name
// that would run past them is cut, and a huge line costs no more than that.
func TestExtractSymbolsReadsOnlyTheStartOfALine(t *testing.T) {
	long := "func Name" + strings.Repeat("x", 500) + "() {}\n"
	got := extractSymbols("go", long)
	if len(got) != 1 || len(got[0]) != 240-len("func ") {
		t.Errorf("a name running past the 240 bytes: %q", got)
	}
	if got := extractSymbols("go", strings.Repeat("a", 1<<20)+"\nfunc After() {}\n"); !reflect.DeepEqual(got, []string{"After"}) {
		t.Errorf("a megabyte line before a declaration: %q", got)
	}
}

func TestCountImports(t *testing.T) {
	for _, tc := range []struct {
		name, file, text, goMod string
		want                    map[string]int
	}{
		{"go: a block, aliases, the root package, other modules and strings outside the block", "x/x.go", "" +
			"package x\n\nimport (\n\t\"fmt\"\n\t\"example.com/demo/server\"\n\t\"example.com/demo/internal/db\"\n\talias \"example.com/demo/server\"\n" +
			"\t_ \"example.com/demo\"\n\t\"example.com/other/thing\"\n\t\"example.com/demoish/x\"\n)\n\nfunc f() { _ = \"example.com/demo/notanimport\" }\n",
			"example.com/demo", map[string]int{"server": 2, "internal/db": 1, ".": 1}},
		{"go: a single import line", "x/x.go", "package x\n\nimport \"example.com/demo/server\"\n\nvar s = \"example.com/demo/lit\"\n", "example.com/demo", map[string]int{"server": 1}},
		{"go: no module, no counting", "x/x.go", "package x\nimport \"example.com/demo/server\"\n", "", map[string]int{}},

		{"ts: relative imports in every spelling", "src/app/main.ts", "" +
			"import { a } from './util'\nimport b from \"../lib/b\"\nconst c = require('./c.js')\nimport './side'\nimport d from 'react'\nexport * from \"./reexp\"\n", "",
			map[string]int{
				"src/app/util": 1, "src/app/util/index": 1, "src/lib/b": 1, "src/lib/b/index": 1, "src/app/c": 1, "src/app/c.js/index": 1,
				"src/app/side": 1, "src/app/side/index": 1, "src/app/reexp": 1, "src/app/reexp/index": 1,
			}},
		{"js: nothing relative", "a.js", "import x from 'lodash'\nconst y = require('fs')\n", "", map[string]int{}},

		{"python: import and from, indented, not in strings", "pkg/a.py", "import os\nimport pkg.util\nfrom pkg.helpers import thing\n  import indented.mod\nx = \"import fake\"\n", "",
			map[string]int{"os": 1, "util": 1, "helpers": 1, "mod": 1}},

		{"rust has no import counting", "a.rs", "use crate::x;\n", "", map[string]int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]int{}
			countImports(tc.file, tc.text, tc.goMod, got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("counted %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImportKey(t *testing.T) {
	for _, tc := range []struct{ file, lang, want string }{
		{"a/b/c.go", "go", "a/b"}, {"c.go", "go", "."}, {"src/app/main.ts", "ts", "src/app/main"}, {"x.tsx", "ts", "x"},
		{"src/util.js", "js", "src/util"}, {"pkg/mod/a.py", "python", "a"}, {"lib.rs", "rust", "lib.rs"},
	} {
		if got := importKey(tc.file, tc.lang); got != tc.want {
			t.Errorf("importKey(%q, %q) = %q, want %q", tc.file, tc.lang, got, tc.want)
		}
	}
}

func TestPackageDocs(t *testing.T) {
	est := core.NewBytesEstimator()
	texts := map[string]string{
		"a/a.go":  "// Package a does the first thing. It also does more.\n// Second line of a.\npackage a\n",
		"a/a2.go": "// Package a again\npackage a\n",
		"b/b.go":  "// Package b is the second.\npackage b\n",
		"c/c.go":  "package c\n",
		"d/d.go":  "// Package d\npackage d\n",
		"e/e.go":  "//Package e spans\n// two lines and\n// stops at the blank one\n//\n// not here\npackage e\n",
		"f/f.py":  "# Package f is not Go\n",
	}
	indeg := map[string]int{"b": 5, "a": 1, "e": 1}
	want := "Packages (most depended-on first):\n" +
		"b: is the second.\n" +
		"a: does the first thing.\n" +
		"e: spans two lines and stops at the blank one\n"
	if got := packageDocs(".", texts, indeg, 1000, est); got != want {
		t.Errorf("packageDocs =\n%s\nwant\n%s", got, want)
	}
	if got := packageDocs(".", map[string]string{"c/c.go": "package c\n"}, nil, 1000, est); got != "" {
		t.Errorf("no documented package: %q", got)
	}
	// The doc is cut at 170 bytes, and the whole list at its budget.
	long := "// Package long " + strings.Repeat("word ", 60) + "\npackage long\n"
	got := packageDocs(".", map[string]string{"long/l.go": long}, nil, 1000, est)
	if line := strings.Split(got, "\n")[1]; !strings.HasSuffix(line, "…") || len(line) > len("long: ")+170+len("…") {
		t.Errorf("a long doc: %q", line)
	}
	if got := packageDocs(".", texts, indeg, 12, est); strings.Count(got, "\n") > 2 {
		t.Errorf("a budget of 12 tokens holds %q", got)
	}
}

func TestFitTokens(t *testing.T) {
	est := core.NewBytesEstimator()
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("line %02d %s\n", i, strings.Repeat("x", 26))) // 36 bytes: 10 tokens
	}
	text := strings.Join(lines, "")
	if got := fitTokens(text, 1000, est); got != text {
		t.Errorf("text that fits was changed")
	}
	got := fitTokens(text, 50, est)
	if est.Tokens(got) > 50 || !strings.HasSuffix(got, "(survey truncated)\n") || !strings.HasPrefix(text, strings.TrimSuffix(got, "(survey truncated)\n")) {
		t.Errorf("a cut survey: %q (%d tokens)", got, est.Tokens(got))
	}
	if body := strings.TrimSuffix(got, "(survey truncated)\n"); !strings.HasSuffix(body, "\n") || strings.Count(body, "\n") != 4 {
		t.Errorf("the cut is on a line boundary and keeps what fits: %q", body)
	}
	if got := fitTokensNote(text, 50, est, "[more]\n"); !strings.HasSuffix(got, "[more]\n") {
		t.Errorf("the note is the caller's: %q", got)
	}
	if got := fitTokens("", 10, est); got != "" {
		t.Errorf("empty text: %q", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := firstN([]string{"a", "b", "c", "d"}, 2); !reflect.DeepEqual(got, []string{"a", "b", "+2 more"}) {
		t.Errorf("firstN: %q", got)
	}
	if got := firstN([]string{"a"}, 2); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("firstN under the cap: %q", got)
	}
	if got := limitSyms([]string{"a", "b", "c"}, 1); !reflect.DeepEqual(got, []string{"a", "+2"}) {
		t.Errorf("limitSyms: %q", got)
	}
	if got := dedupe([]string{"a", "b", "a", "c", "b"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("dedupe: %q", got)
	}
	for score, want := range map[float64]int{0: 7, 5.9: 7, 6: 12, 11.9: 12, 12: 20, 100: 20} {
		if got := symbolCap(score); got != want {
			t.Errorf("symbolCap(%v) = %d, want %d", score, got, want)
		}
	}
	for name, want := range map[string]bool{"A": true, "Type.Method": true, "Type.method": false, "a": false, "": false, "_X": false, "1": false} {
		if got := isExportedGo(name); got != want {
			t.Errorf("isExportedGo(%q) = %v, want %v", name, got, want)
		}
	}
	files := []string{"a", "b/c", "b/d", "e"}
	if !has(files, "b/c") || has(files, "b") || has(files, "f") || !dirHas(files, "b/") || dirHas(files, "c/") {
		t.Errorf("has/dirHas on a sorted listing")
	}
	if got := readSmall(t.TempDir(), "missing", 10); got != "" {
		t.Errorf("a missing file: %q", got)
	}
}

// What the survey reads of a file is bounded by the cap it is given, however large the file
// is (a sparse file costs no disk, only what is read), and only regular files are read.
func TestReadSmallIsBounded(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.go")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil { // sixty-four megabytes, sparse
		f.Close()
		t.Skipf("no sparse files here: %v", err)
	}
	f.Close()
	for _, max := range []int64{0, 1, 4096, 256 << 10} {
		if got := readSmall(root, "big.go", max); int64(len(got)) > max {
			t.Errorf("read %d bytes with a cap of %d", len(got), max)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "small.go"), []byte("package s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSmall(root, "small.go", 4); got != "pack" {
		t.Errorf("a cap below the size: %q", got)
	}
	if got := readSmall(root, "small.go", 100); got != "package s\n" {
		t.Errorf("a cap above the size: %q", got)
	}
	if err := os.Mkdir(filepath.Join(root, "dir.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := readSmall(root, "dir.go", 100); got != "" {
		t.Errorf("a directory: %q", got)
	}
}
