package gitx

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryReaders(t *testing.T) {
	dir := newRepo(t)
	first := rawGit(t, dir, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha two\n")
	rawGit(t, dir, "commit", "-qam", "second")
	second := rawGit(t, dir, "rev-parse", "HEAD")
	// unrelated history
	rawGit(t, dir, "checkout", "-q", "--orphan", "other")
	rawGit(t, dir, "rm", "-rfq", ".")
	writeFile(t, filepath.Join(dir, "z.txt"), "zulu\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "unrelated root")
	rawGit(t, dir, "checkout", "-q", "main")

	r := openRepo(t, dir)
	ctx := ctxT(t)

	if n, err := r.CountCommits(ctx, first, second); err != nil || n != 1 {
		t.Fatalf("CountCommits(first, second) = %d, %v", n, err)
	}
	if n, err := r.CountCommits(ctx, second, first); err != nil || n != 0 {
		t.Fatalf("CountCommits(second, first) = %d, %v", n, err)
	}
	if _, err := r.CountCommits(ctx, "-x", second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CountCommits with an option-looking revision: %v", err)
	}

	c, err := r.CommitInfo(ctx, "HEAD")
	if err != nil || c.SHA != second || c.Subject != "second" || len(c.Parents) != 1 || c.Parents[0] != first || c.Author.Name != "Fixture" {
		t.Fatalf("CommitInfo: %+v, %v", c, err)
	}
	if _, err := r.CommitInfo(ctx, "no-such-ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CommitInfo of nothing: %v", err)
	}

	blob := rawGit(t, dir, "rev-parse", "HEAD:a.txt")
	if b, err := r.Blob(ctx, blob, 0); err != nil || string(b) != "alpha two\n" {
		t.Fatalf("Blob: %q, %v", b, err)
	}
	if b, err := r.Blob(ctx, blob, 4); err != nil || len(b) > 4 {
		t.Fatalf("Blob with a cap of 4 bytes: %q, %v", b, err)
	}
	if _, err := r.Blob(ctx, strings.Repeat("0", 40), 0); err == nil {
		t.Fatal("Blob of a missing object succeeded")
	}
	if _, err := r.Blob(ctx, "--help", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Blob of an option: %v", err)
	}

	if ok, err := r.IsAncestor(ctx, first, second); err != nil || !ok {
		t.Fatalf("IsAncestor(first, second) = %v, %v", ok, err)
	}
	if ok, err := r.IsAncestor(ctx, second, first); err != nil || ok {
		t.Fatalf("IsAncestor(second, first) = %v, %v", ok, err)
	}
	if ok, err := r.IsAncestor(ctx, second, second); err != nil || !ok {
		t.Fatalf("a commit is its own ancestor: %v, %v", ok, err)
	}
	if _, err := r.IsAncestor(ctx, "no-such-ref", second); err == nil {
		t.Fatal("IsAncestor of an unknown revision must be an error, not a quiet false")
	}
	if _, err := r.IsAncestor(ctx, "-x", second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("IsAncestor with an option: %v", err)
	}

	if mb, err := r.MergeBase(ctx, "main", first); err != nil || mb != first {
		t.Fatalf("MergeBase(main, first) = %q, %v", mb, err)
	}
	if _, err := r.MergeBase(ctx, "main", "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MergeBase of unrelated histories: %v", err)
	}
}

// Abort abandons whatever multi-step operation is in progress, not just merges
// and rebases: a cherry-pick or revert that stopped for conflicts must not be left
// behind for the next command to trip over.
func TestAbortCoversEveryMultiStepOperation(t *testing.T) {
	dir := newRepo(t)
	rawGit(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha from side\n")
	rawGit(t, dir, "commit", "-qam", "side edits a")
	side := rawGit(t, dir, "rev-parse", "HEAD")
	rawGit(t, dir, "checkout", "-q", "main")
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha from main\n")
	rawGit(t, dir, "commit", "-qam", "main edits a")
	head := rawGit(t, dir, "rev-parse", "HEAD")

	r := openRepo(t, dir)
	ctx := ctxT(t)

	for _, tc := range []struct {
		name  string
		start []string
		state string
	}{
		{"cherry-pick", []string{"cherry-pick", side}, "cherry-pick"},
		{"revert", []string{"revert", "--no-edit", "HEAD~1"}, "revert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rawGitMayFail(dir, tc.start...)
			if got := r.InProgress(); got != tc.state {
				t.Fatalf("InProgress = %q after a conflicting %s, want %q", got, tc.name, tc.state)
			}
			if err := r.Abort(ctx); err != nil {
				t.Fatalf("Abort: %v", err)
			}
			if got := r.InProgress(); got != "" {
				t.Fatalf("still %q after Abort", got)
			}
			if err := r.ResetHard(ctx, head); err != nil {
				t.Fatal(err)
			}
			if clean, err := r.IsClean(ctx); err != nil || !clean {
				t.Fatalf("not clean after Abort and reset: %v, %v", clean, err)
			}
		})
	}
	// nothing in progress: nothing to do, and no error
	if err := r.Abort(ctx); err != nil {
		t.Fatalf("Abort with nothing in progress: %v", err)
	}
}
