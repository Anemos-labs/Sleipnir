package gitx

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBlameAttributesLinesToAgentCommits(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nfrom be-1\n")
	if _, err := r.CommitAll(ctx, "T1: first", Author{Name: "be-1", Email: "be-1@sleipnir.invalid"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nfrom be-1\nfrom fe-1\n")
	if _, err := r.CommitAll(ctx, "T2: second", Author{Name: "fe-1", Email: "fe-1@sleipnir.invalid"}); err != nil {
		t.Fatal(err)
	}
	lines, err := r.Blame(ctx, "HEAD", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range lines {
		got = append(got, l.Author+"|"+l.Email+"|"+l.Summary)
	}
	want := []string{"Fixture|fixture@example.com|initial", "be-1|be-1@sleipnir.invalid|T1: first", "fe-1|fe-1@sleipnir.invalid|T2: second"}
	if !reflect.DeepEqual(got, want) || lines[2].Line != 3 {
		t.Fatalf("blame = %v (%+v)", got, lines)
	}
	// Working-tree content through stdin: the new line is no commit's.
	lines, err = r.BlameContents(ctx, "HEAD", "a.txt", []byte("alpha\nfrom be-1\nfrom fe-1\nuncommitted\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 || lines[3].Commit != ZeroCommit || lines[1].Author != "be-1" {
		t.Fatalf("contents blame = %+v", lines)
	}
	// Refusals: a path outside the repository, an option as a revision.
	for _, bad := range [][2]string{{"HEAD", "../x"}, {"HEAD", "/etc/passwd"}, {"--output=/tmp/x", "a.txt"}, {"HEAD", ""}} {
		if _, err := r.Blame(ctx, bad[0], bad[1]); KindOf(err) != KindInvalid {
			t.Errorf("Blame(%q, %q) = %v", bad[0], bad[1], err)
		}
	}
}

func TestGitAllowsBlameButNotItsFileReadingOptions(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	if res, err := r.Git(ctx, "blame", "--porcelain", "HEAD", "--", "a.txt"); err != nil || !strings.Contains(res.Stdout, "\talpha") {
		t.Fatalf("blame through Git: %v %+v", err, res)
	}
	for _, args := range [][]string{
		{"blame", "--contents", "/etc/passwd", "a.txt"},
		{"blame", "--contents=/etc/passwd", "a.txt"},
		{"blame", "--cont=/etc/passwd", "a.txt"},
		{"blame", "--ignore-revs-file", "/etc/passwd", "a.txt"},
		{"blame", "-S", "/etc/passwd", "a.txt"},
		{"blame", "-wS/etc/passwd", "a.txt"},
	} {
		if _, err := r.Git(ctx, args...); KindOf(err) != KindInvalid {
			t.Errorf("Git(%v) = %v, want refused", args, err)
		}
	}
}

func TestCommitPathsCommitsOnlyThosePaths(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha changed\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "bravo changed by the person\n")
	writeFile(t, filepath.Join(dir, "new dir", "n.txt"), "new\n")
	if err := os.Remove(filepath.Join(dir, "sub", "c.txt")); err != nil {
		t.Fatal(err)
	}
	// the person has something staged of their own, too
	writeFile(t, filepath.Join(dir, "staged.txt"), "mine\n")
	rawGit(t, dir, "add", "staged.txt")

	sha, err := r.CommitPaths(ctx, "accept verified work", DefaultAuthor, []string{"a.txt", "new dir/n.txt", "sub/c.txt", "never-existed.txt", ":(glob)*"})
	if err != nil || sha == "" {
		t.Fatalf("CommitPaths: %q %v", sha, err)
	}
	files := rawGit(t, dir, "show", "--name-status", "--format=", "HEAD")
	if want := "M\ta.txt\nA\tnew dir/n.txt\nD\tsub/c.txt"; files != want {
		t.Fatalf("committed:\n%s\nwant:\n%s", files, want)
	}
	st := rawGit(t, dir, "status", "--porcelain")
	if !strings.Contains(st, " M b.txt") || !strings.Contains(st, "A  staged.txt") {
		t.Fatalf("other changes must stay as they were:\n%s", st)
	}
	// Nothing left in those paths: no commit.
	if sha2, err := r.CommitPaths(ctx, "again", DefaultAuthor, []string{"a.txt"}); err != nil || sha2 != "" {
		t.Fatalf("second commit: %q %v", sha2, err)
	}
	// Refusals.
	if _, err := r.CommitPaths(ctx, "x", DefaultAuthor, []string{"../outside"}); KindOf(err) != KindInvalid {
		t.Fatalf("outside: %v", err)
	}
	if _, err := r.CommitPaths(ctx, " ", DefaultAuthor, []string{"b.txt"}); KindOf(err) != KindInvalid {
		t.Fatalf("empty message: %v", err)
	}
	writeFile(t, filepath.Join(dir, "b.txt"), "<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n")
	if sha, err := r.CommitPaths(ctx, "markers are fine outside a merge", DefaultAuthor, []string{"b.txt"}); err != nil || sha == "" {
		t.Fatalf("plain content with markers outside a merge commits: %v", err)
	}
}

func TestApplyHunkReversesOneHunkOnly(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	var orig strings.Builder
	for i := range 30 {
		orig.WriteString("line " + string(rune('a'+i%26)) + "\n")
	}
	writeFile(t, filepath.Join(dir, "f.txt"), orig.String())
	rawGit(t, dir, "add", "f.txt")
	rawGit(t, dir, "commit", "-q", "-m", "f")
	lines := strings.Split(strings.TrimSuffix(orig.String(), "\n"), "\n")
	lines[1] = "CHANGED early"
	lines[25] = "CHANGED late"
	writeFile(t, filepath.Join(dir, "f.txt"), strings.Join(lines, "\n")+"\n")
	d, err := r.Diff(ctx, "HEAD", DiffOptions{Paths: []string{"f.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	// The second hunk of the patch, on its own.
	parts := strings.Split(d.Patch, "\n@@ ")
	if len(parts) != 3 {
		t.Fatalf("expected two hunks:\n%s", d.Patch)
	}
	second := "@@ " + parts[2]
	if err := r.ApplyHunk(ctx, "f.txt", second, true); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(dir, "f.txt"))
	if !strings.Contains(got, "CHANGED early") || strings.Contains(got, "CHANGED late") {
		t.Fatalf("only the second hunk is reverted:\n%s", got)
	}
	// Applying it again in reverse no longer applies: nothing changes.
	if err := r.ApplyHunk(ctx, "f.txt", second, true); KindOf(err) != KindConflict {
		t.Fatalf("second reverse: %v", err)
	}
	if readFile(t, filepath.Join(dir, "f.txt")) != got {
		t.Fatal("a failed check must change nothing")
	}
	// Forward again restores the change: reverse then forward is the identity.
	if err := r.ApplyHunk(ctx, "f.txt", second, false); err != nil || !strings.Contains(readFile(t, filepath.Join(dir, "f.txt")), "CHANGED late") {
		t.Fatalf("forward: %v", err)
	}
	// A hunk that smuggles another file's header is refused.
	if err := r.ApplyHunk(ctx, "f.txt", "@@ -1 +1 @@\n-a\n+b\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-bravo\n+evil\n", false); KindOf(err) != KindInvalid {
		t.Fatalf("smuggled header: %v", err)
	}
	if readFile(t, filepath.Join(dir, "b.txt")) != "bravo\n" {
		t.Fatal("b.txt must be untouched")
	}
}
