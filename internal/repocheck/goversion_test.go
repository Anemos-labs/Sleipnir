package repocheck

import (
	"regexp"
	"testing"
)

// The Go version of the project is written in go.mod and in four documents. The workflows read it from go.mod
// (go-version-file), so they cannot drift; the documents can, and a contributor who reads "Go 1.25" in the README while go.mod
// says 1.26 builds with the wrong toolchain or doubts the README.
//
// Only the forms in which the documents state the version are read: "Go 1.25, standard library", "a Go 1.25 coding-agent
// harness", "Go 1.25 or newer", "(1.25, stdlib-first)" and the directive quoted as `go 1.25.0`. A sentence about another
// version ("Go 1.24 is out of support") is history and is left alone.
func TestDocumentsStateTheGoVersionOfGoMod(t *testing.T) {
	m := regexp.MustCompile(`(?m)^go (1\.\d+)(?:\.\d+)?\s*$`).FindStringSubmatch(read(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no `go 1.N` line")
	}
	want := m[1]
	statement := regexp.MustCompile("\\bGo (1\\.\\d+)(?:,| or newer| coding-agent)|\\((1\\.\\d+), stdlib-first\\)|`go (1\\.\\d+)(?:\\.\\d+)?`")
	n := 0
	for _, f := range []string{"README.md", "AGENTS.md", "CONTRIBUTING.md", "docs/BUILDING.md"} {
		for _, s := range statement.FindAllStringSubmatch(read(t, f), -1) {
			got := s[1] + s[2] + s[3]
			n++
			if got != want {
				t.Errorf("%s says Go %s, but go.mod says go %s: change the documents with go.mod (README.md, AGENTS.md, CONTRIBUTING.md and docs/BUILDING.md state it)", f, got, want)
			}
		}
	}
	if n == 0 {
		t.Fatal("none of README.md, AGENTS.md, CONTRIBUTING.md and docs/BUILDING.md states the Go version: the test is not reading them")
	}
}
