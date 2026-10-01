package repocheck

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docs/TESTING.md opens with what the repository has: how many tests, fuzz targets, benchmarks, golden and seed files and packages. A
// figure that was true when it was written and has not been touched since is how a document comes to say something false, so the figures
// are counted from the tree and held to within a tenth of what the page says: the page is "about" right, and when the suite has grown
// past that, this says by how much and which line to change.
func TestTestingDocSaysAboutWhatTheTreeHas(t *testing.T) {
	text := strings.Join(strings.Fields(read(t, "docs/TESTING.md")), " ")
	m := regexp.MustCompile(`About ([0-9,]+) test functions, ([0-9,]+) fuzz targets, ([0-9,]+) benchmarks and ([0-9,]+) golden and seed files, in ([0-9,]+) packages`).FindStringSubmatch(text)
	if m == nil {
		t.Fatal("docs/TESTING.md no longer opens with \"About N test functions, N fuzz targets, N benchmarks and N golden and seed files, in N packages\": say it in those words, with digits, so that it can be counted")
	}
	said := make([]int, 5)
	for i := range said {
		n, err := strconv.Atoi(strings.ReplaceAll(m[i+1], ",", ""))
		if err != nil {
			t.Fatalf("docs/TESTING.md: %q is not a number", m[i+1])
		}
		said[i] = n
	}

	var tests, fuzz, bench, fixtures int
	pkgs := map[string]bool{}
	fn := regexp.MustCompile(`(?m)^func (Test|Fuzz|Benchmark)[A-Z_0-9]`)
	// The fixtures of the benchmark suite are small projects with a go.mod of their own: not packages of this module, and their tests
	// are what a model is asked to make pass, not what the repository runs.
	var nested []string
	for _, f := range treeFiles(t) {
		if path.Base(f) == "go.mod" && f != "go.mod" {
			nested = append(nested, path.Dir(f)+"/")
		}
	}
	for _, f := range treeFiles(t) {
		other := false
		for _, n := range nested {
			other = other || strings.HasPrefix(f, n)
		}
		if other {
			continue
		}
		inTestdata := strings.Contains("/"+f, "/testdata/")
		switch {
		case inTestdata:
			fixtures++
		case strings.HasSuffix(f, ".golden"):
			fixtures++
		case strings.HasSuffix(f, ".go"):
			pkgs[path.Dir(f)] = true
			if !strings.HasSuffix(f, "_test.go") {
				continue
			}
			for _, kind := range fn.FindAllStringSubmatch(read(t, f), -1) {
				switch kind[1] {
				case "Test":
					tests++
				case "Fuzz":
					fuzz++
				case "Benchmark":
					bench++
				}
			}
		}
	}
	for i, c := range []struct {
		what string
		got  int
	}{{"test functions", tests}, {"fuzz targets", fuzz}, {"benchmarks", bench}, {"golden and seed files", fixtures}, {"packages", len(pkgs)}} {
		if lo, hi := c.got-c.got/10, c.got+c.got/10; said[i] < lo || said[i] > hi {
			t.Errorf("docs/TESTING.md says about %d %s and the tree has %d: change the figure on its first lines (it is held to a tenth)", said[i], c.what, c.got)
		}
	}
}
