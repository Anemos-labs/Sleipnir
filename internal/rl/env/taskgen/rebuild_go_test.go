package taskgen

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

const stubCalcA = `// Package calc does arithmetic.
package calc

import (
	"errors"
	"fmt"
	"strings"
	str "strconv"
)

// ErrNeg is returned for negative input.
var ErrNeg = errors.New("negative")

var table = func() []int { return []int{1, 2, 3} }()

// Pi is pi.
const Pi = 3.14

// Point is a point.
type Point struct {
	X, Y  int // the coordinates
	label string
}

// Add adds.
func Add(a, b int) int {
	// a comment inside
	return helper(a) + b
}

func helper(x int) int { return x }

func usedByTests(x int) string { return fmt.Sprint(x) }

func scaleFactor() int { return 2 }

func init() { table = append(table, 4) }

// String formats p.
func (p Point) String() string {
	return strings.Join([]string{str.Itoa(p.X), str.Itoa(p.Y)}, ",")
}

func (p *Point) scale(k int) { p.X *= k }

// Sum adds numbers of any kind.
func Sum[T int | float64](xs ...T) T {
	var s T
	for _, x := range xs {
		s += x
	}
	return s
}
`

const stubCalcB = `package calc

import "strings"

var defaultScale = scaleFactor()

// Upper is strings.ToUpper.
func Upper(s string) string { return strings.ToUpper(s) }
`

func TestGoStubPackage(t *testing.T) {
	files := map[string][]byte{"calc/a.go": []byte(stubCalcA), "calc/b.go": []byte(stubCalcB)}
	stubs, err := goStubPackage(files, map[string]bool{"usedByTests": true})
	if err != nil {
		t.Fatal(err)
	}
	a, b := string(stubs["calc/a.go"]), string(stubs["calc/b.go"])
	for _, want := range []string{
		"// Package calc does arithmetic.\npackage calc",
		"\t\"errors\"\n)",
		"// ErrNeg is returned for negative input.\nvar ErrNeg = errors.New(\"negative\")",
		"var table = func() []int { panic(\"not implemented\") }()",
		"const Pi = 3.14",
		"X, Y  int // the coordinates",
		"// Add adds.\nfunc Add(a, b int) int {\n\tpanic(\"not implemented\")\n}",
		"func usedByTests(x int) string {\n\tpanic(\"not implemented\")\n}",                  // the tests use it
		"func scaleFactor() int {\n\tpanic(\"not implemented\")\n}",                          // b.go's variable uses it
		"func (p *Point) scale(k int) {\n\tpanic(\"not implemented\")\n}",                    // a method stays
		"// Sum adds numbers of any kind.\nfunc Sum[T int | float64](xs ...T) T {\n\tpanic(", // generics
	} {
		if !strings.Contains(a, want) {
			t.Errorf("a.go's stub lacks %q:\n%s", want, a)
		}
	}
	for _, gone := range []string{"func helper", "func init", "a comment inside", "\"fmt\"", "\"strings\"", "strconv", "\treturn", "{ return"} {
		if strings.Contains(a, gone) {
			t.Errorf("a.go's stub still has %q:\n%s", gone, a)
		}
	}
	if !strings.Contains(b, "var defaultScale = scaleFactor()") || strings.Contains(b, "\"strings\"") || !strings.Contains(b, "func Upper(s string) string {\n\tpanic(") {
		t.Errorf("b.go's stub:\n%s", b)
	}

	// The stubs build and vet clean.
	have(t, "go")
	dir := t.TempDir()
	for p, body := range map[string]string{"go.mod": "module example.com/stubs\n\ngo 1.21\n", "calc/a.go": a, "calc/b.go": b} {
		if err := writeFile(filepath.Join(dir, p), body); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOCACHE="+sharedGoCache, "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet of the stubs: %v\n%s", err, out)
	}
}

func TestGoImportNames(t *testing.T) {
	src := `package p

import (
	"example.com/go-thing"
	"example.com/foo/v2"
	"example.com/lib"
	x "example.com/renamed"
	_ "embed"
)

var decl = 1

func F() { thing.A(); foo.B(); library.C(); x.D(); decl.E() }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := goImportNames(f, map[string]bool{"decl": true, "F": true})
	want := map[string]string{
		"example.com/go-thing": "thing", "example.com/foo/v2": "foo", "example.com/lib": "library", "example.com/renamed": "x", "embed": "_",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("import names: %v, want %v", got, want)
	}
}

func TestGoAPISection(t *testing.T) {
	files := map[string][]byte{"calc/a.go": []byte(stubCalcA), "calc/b.go": []byte(stubCalcB)}
	sec, err := goAPISection(&rebuildPkg{Name: "example.com/m/calc", Dir: "calc"}, "calc", files, map[string]bool{"usedByTests": true, "label": true, "t": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Package `example.com/m/calc`", "Directory `calc/`, package `calc`.", "Package calc does arithmetic.",
		"```go\nfunc Add(a, b int) int\n```", "Add adds.",
		"```go\nfunc Sum[T int | float64](xs ...T) T\n```",
		"var ErrNeg = errors.New(\"negative\")", "const Pi = 3.14", "func Upper(s string) string",
		"X, Y int // the coordinates", "// contains filtered or unexported fields",
		"func (p Point) String() string", "String formats p.",
		"### Unexported declarations the package's tests use", "func usedByTests(x int) string",
		"label string", // the field the tests use: Point in full
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("the API lacks %q:\n%s", want, sec)
		}
	}
	for _, gone := range []string{"helper", "scaleFactor", "return []int", "a comment inside", "func (p *Point) scale"} {
		if strings.Contains(sec, gone) {
			t.Errorf("the API shows %q:\n%s", gone, sec)
		}
	}
}

func TestDependencyOrder(t *testing.T) {
	pkgs := []*rebuildPkg{
		{Name: "a", Imports: []string{"b"}}, {Name: "b", Imports: []string{"c"}}, {Name: "c"}, {Name: "d"},
		{Name: "x", Imports: []string{"y"}}, {Name: "y", Imports: []string{"x", "c"}},
	}
	var got []string
	for _, p := range dependencyOrder(pkgs) {
		got = append(got, p.Name)
	}
	// c and d are free at once (c first by name); a cycle is broken at the package with the fewest imports left.
	if want := []string{"c", "b", "a", "d", "x", "y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestRebuildSizing(t *testing.T) {
	for _, c := range []struct{ loc, agents, wall int }{
		{1000, 3, 6 * 3600}, {2000, 3, 6 * 3600}, {6000, 5, 9 * 3600}, {10000, 6, 12 * 3600}, {20000, 6, 12 * 3600},
	} {
		if a := rebuildAgents(c.loc); a != c.agents {
			t.Errorf("%d lines: %d agents, want %d", c.loc, a, c.agents)
		}
		if w := rebuildBudget(c.loc, rl.Budget{}).WallS; w != c.wall {
			t.Errorf("%d lines: wall %d s, want %d", c.loc, w, c.wall)
		}
	}
	if w := rebuildBudget(4000, rl.Budget{WallS: 60}).WallS; w != 60 {
		t.Errorf("an explicit wall clock was overridden: %d", w)
	}
}

func TestEscapeModulePath(t *testing.T) {
	if got := escapeModulePath("github.com/BurntSushi/toml"); got != "github.com/!burnt!sushi/toml" {
		t.Fatal(got)
	}
	if got := goAssumedName("gopkg.in/yaml.v3"); got != "yaml" {
		t.Fatal(got)
	}
	if got := goModulePath([]byte("// comment\nmodule \"example.com/x/v2\" // trailing\n\ngo 1.21\n")); got != "example.com/x/v2" {
		t.Fatal(got)
	}
}
