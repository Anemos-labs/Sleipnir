package repocheck

import (
	"path"
	"strings"
	"testing"
)

// docs/README.md is the index of the documentation: a reader who browses docs/ on GitHub sees it first. A document added to
// docs/ without a line there can only be found by a reader who already knows its name, so every Markdown file directly in
// docs/ (other than the index itself) must be a link target of the index.
func TestDocumentationIndexListsEveryDocument(t *testing.T) {
	const index = "docs/README.md"
	linked := map[string]bool{}
	for _, target := range markdownTargets(read(t, index)) {
		if rel, ok := localTarget(path.Dir(index), target); ok {
			linked[rel] = true
		}
	}
	n := 0
	for _, f := range treeFiles(t) {
		if path.Dir(f) != "docs" || !strings.HasSuffix(f, ".md") || f == index {
			continue
		}
		n++
		if !linked[f] {
			t.Errorf("%s is not linked from %s: add a line for it to the index", f, index)
		}
	}
	if n == 0 {
		t.Fatal("no documents found in docs/")
	}
}
