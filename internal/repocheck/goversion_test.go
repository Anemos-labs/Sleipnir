package repocheck

import (
	"regexp"
	"testing"
)

// The Go version of the project is written in go.mod and in four documents. The workflows read it from go.mod
// (go-version-file), so they cannot drift; the documents can, and a contributor who reads "Go 1.24" in the README while
// go.mod says 1.25 builds with the wrong toolchain or doubts the README.
func TestDocumentsStateTheGoVersionOfGoMod(t *testing.T) {
	m := regexp.MustCompile(`(?m)^go (1\.\d+)(?:\.\d+)?\s*$`).FindStringSubmatch(read(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no `go 1.N` line")
	}
	want := m[1]
	// "Go 1.24," "(1.24, stdlib-first)" "it must stay `1.24`"
	statement := regexp.MustCompile("(?:\\bGo |\\()(1\\.\\d+)[ ,)]|must stay `(1\\.\\d+)`")
	n := 0
	for _, f := range []string{"README.md", "AGENTS.md", "CONTRIBUTING.md", "docs/BUILDING.md"} {
		for _, s := range statement.FindAllStringSubmatch(read(t, f), -1) {
			got := s[1] + s[2]
			n++
			if got != want {
				t.Errorf("%s says Go %s, but go.mod says go %s: change the documents with go.mod (docs/BUILDING.md also says the directive must stay at the version it names: that sentence is the decision being revisited)", f, got, want)
			}
		}
	}
	if n == 0 {
		t.Fatal("none of README.md, AGENTS.md, CONTRIBUTING.md and docs/BUILDING.md states the Go version: the test is not reading them")
	}
}
