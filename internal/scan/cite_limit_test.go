package scan

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// A file of exactly maxCiteFile bytes is read and kept for the next citation; one a byte larger is refused and not kept.
func TestCodeAtReadsUpToTheCitationLimit(t *testing.T) {
	root, commit := gitRepo(t, map[string]string{
		"at.txt":   strings.Repeat("a", maxCiteFile),
		"over.txt": strings.Repeat("b", maxCiteFile+1),
	})
	repo, err := gitx.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	code := CodeAt(repo, commit).(*gitCode)
	if b, err := code.Read(context.Background(), "at.txt"); err != nil || len(b) != maxCiteFile {
		t.Errorf("a file at the limit: %d bytes, %v", len(b), err)
	}
	if _, err := code.Read(context.Background(), "over.txt"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a file over the limit: %v", err)
	}
	if _, kept := code.cache["at.txt"]; !kept {
		t.Error("the file at the limit was not kept")
	}
	if _, kept := code.cache["over.txt"]; kept {
		t.Error("the file over the limit was kept")
	}
}
