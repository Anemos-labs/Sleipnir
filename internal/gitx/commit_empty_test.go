package gitx

import (
	"path/filepath"
	"strings"
	"testing"
)

// The first commit of a new repository changes nothing and carries the identity given, whatever the user's git configuration says
// (none here), so that a work tree has a commit to start from.
func TestCommitEmptyMakesTheFirstCommitOfANewRepository(t *testing.T) {
	isolateHome(t)
	ctx := ctxT(t)
	dir := filepath.Join(t.TempDir(), "fresh")
	r, err := Init(ctx, dir, false, WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Head(ctx); err == nil {
		t.Fatal("a new repository has a commit")
	}
	sha, err := r.CommitEmpty(ctx, "Start of the project", DefaultAuthor)
	if err != nil || sha == "" {
		t.Fatalf("CommitEmpty: %q, %v", sha, err)
	}
	if head, err := r.Head(ctx); err != nil || head != sha {
		t.Fatalf("head %q, %v; want %q", head, err, sha)
	}
	if out := rawGit(t, dir, "log", "--format=%an <%ae>|%s", "-n", "2"); out != "Sleipnir <sleipnir@localhost>|Start of the project" {
		t.Errorf("log: %q", out)
	}
	if out := rawGit(t, dir, "ls-tree", "-r", "HEAD"); out != "" {
		t.Errorf("the first commit holds files: %q", out)
	}
	if _, err := r.CommitEmpty(ctx, "  ", DefaultAuthor); err == nil || !strings.Contains(err.Error(), "empty commit message") {
		t.Errorf("an empty message: %v", err)
	}
	if _, err := r.CommitEmpty(ctx, "x", Author{Name: "a<b"}); err == nil {
		t.Error("an identity with a forbidden character was taken")
	}
}
