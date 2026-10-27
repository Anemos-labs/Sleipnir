package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appendConfig adds raw configuration text to the repository's own config file.
func appendConfig(t *testing.T, dir, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// A rebase.instructionFormat whose expansion holds a newline turns each todo line
// into two, the second an exec; Rebase replays with the subject alone.
func TestRebaseIgnoresTheRepositorysInstructionFormat(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "exec-ran")
	rawGit(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, filepath.Join(dir, "side.txt"), "side\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "side")
	rawGit(t, dir, "checkout", "-q", "main")
	writeFile(t, filepath.Join(dir, "main.txt"), "main\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "main")
	appendConfig(t, dir, "[rebase]\n\tinstructionFormat = "+quoteCfg("%s%nexec touch "+marker)+"\n")

	r := openRepo(t, dir)
	ctx := ctxT(t)
	side, err := r.ResolveRef(ctx, "side")
	if err != nil {
		t.Fatal(err)
	}
	mb, err := r.MergeBase(ctx, "main", "side")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rebase(ctx, RebaseOptions{Onto: "main", Upstream: mb, Branch: side}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the exec line of the repository's instruction format ran")
	}
}

// branch.<current>.mergeOptions = -F <file> would read that file into the merge
// message; Merge uses only the options it was given.
func TestMergeIgnoresTheBranchsMergeOptions(t *testing.T) {
	skipWithoutUnix(t)
	// A branch name may hold "=", where a -c key=value override would be split.
	for _, branch := range []string{"main", "a=b"} {
		t.Run(branch, func(t *testing.T) {
			dir := newRepo(t)
			secret := filepath.Join(t.TempDir(), "secret")
			writeFile(t, secret, "private-token-text\n")
			rawGit(t, dir, "checkout", "-q", "-B", branch)
			rawGit(t, dir, "checkout", "-q", "-b", "side")
			writeFile(t, filepath.Join(dir, "side.txt"), "side\n")
			rawGit(t, dir, "add", "-A")
			rawGit(t, dir, "commit", "-qm", "side")
			rawGit(t, dir, "checkout", "-q", branch)
			appendConfig(t, dir, "[branch \""+branch+"\"]\n\tmergeOptions = -F "+secret+"\n")

			r := openRepo(t, dir)
			ctx := ctxT(t)
			if _, err := r.Merge(ctx, MergeOptions{Ref: "side", NoFF: true, Message: "merge side"}); err != nil {
				t.Fatal(err)
			}
			if msg := rawGit(t, dir, "log", "-1", "--format=%B"); strings.Contains(msg, "private-token-text") || !strings.Contains(msg, "merge side") {
				t.Fatalf("merge message = %q", msg)
			}
		})
	}
}

// core.alternateRefsCommand runs while git walks an alternate's references.
func TestAlternateRefsCommandDoesNotRun(t *testing.T) {
	skipWithoutUnix(t)
	other := newRepo(t)
	dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "alternate-ran")
	writeFile(t, filepath.Join(dir, ".git", "objects", "info", "alternates"), filepath.Join(other, ".git", "objects")+"\n")
	appendConfig(t, dir, "[core]\n\talternateRefsCommand = "+quoteCfg("touch "+marker+"; true")+"\n")

	r := openRepo(t, dir)
	if _, err := r.Git(ctxT(t), "rev-list", "--alternate-refs", "--count", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's alternateRefsCommand ran")
	}
}

// A relative git path is pinned when the runner is built, not looked up again in
// the repository each command runs in.
func TestRelativeGitPathIsPinned(t *testing.T) {
	skipWithoutUnix(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := newRepo(t)
	tools := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(tools, "git")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "planted-ran")
	// A planted ./bin/git inside the repository must never be the one that runs.
	writeFile(t, filepath.Join(dir, "bin", "git"), "#!/bin/sh\ntouch "+marker+"\nexec "+real+" \"$@\"\n")
	if err := os.Chmod(filepath.Join(dir, "bin", "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(tools))
	r, err := Open(dir, WithHermeticConfig(), WithGitPath(filepath.Join("bin", "git")))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(r.s.git) {
		t.Fatalf("git path %q is not absolute", r.s.git)
	}
	if _, err := r.Status(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's own bin/git ran")
	}
}

// worktreeOnlyFilter commits attributes that select a filter defined only by an
// include whose condition matches the git directory of a linked worktree named
// name (or name with a suffix git adds to keep it unique), so the repository's own configuration never shows it.
func worktreeOnlyFilter(t *testing.T, dir, name, marker string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".gitattributes"), "*.txt filter=sneaky\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "attributes")
	extra := filepath.Join(dir, ".git", "worktree-only")
	writeFile(t, extra, "[filter \"sneaky\"]\n\tsmudge = "+quoteCfg("touch "+marker+"; cat")+"\n\tclean = "+quoteCfg("touch "+marker+"; cat")+"\n")
	appendConfig(t, dir, "[includeIf \"gitdir:**/worktrees/"+name+"*\"]\n\tpath = "+extra+"\n")
}

// A filter that only the new worktree's configuration defines does not run when
// WorktreeAdd checks the files out.
func TestWorktreeAddNeutralizesTheNewWorktreesOwnDrivers(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "smudge-ran")
	worktreeOnlyFilter(t, dir, "wtx", marker)

	// canary: plain git runs it
	rawGit(t, dir, "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "wtx"), "HEAD")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("canary: plain git worktree add did not run the worktree-only filter")
	}
	os.Remove(marker)

	r := openRepo(t, dir)
	ctx := ctxT(t)
	for _, o := range []WorktreeAddOptions{
		{Path: filepath.Join(t.TempDir(), "wtx"), Detach: true, Commit: "HEAD"},
		{Path: filepath.Join(t.TempDir(), "wtx"), Branch: "named", Commit: "HEAD"},
	} {
		if err := r.WorktreeAdd(ctx, o); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(o.Path, "a.txt")); got != "alpha\n" {
			t.Fatalf("a.txt = %q", got)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("WorktreeAdd(%+v) ran the worktree-only filter", o)
		}
	}
}

// Without force, git checks a worktree is clean with a status inside it; the
// drivers of that worktree's own configuration do not run then either.
func TestWorktreeRemoveNeutralizesTheWorktreesOwnDrivers(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "clean-ran")
	worktreeOnlyFilter(t, dir, "wty", marker)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	wt := filepath.Join(t.TempDir(), "wty")
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt, Detach: true, Commit: "HEAD"}); err != nil {
		t.Fatal(err)
	}
	// a stat change makes status read the content through the clean filter
	now := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(wt, "a.txt"), now, now); err != nil {
		t.Fatal(err)
	}
	if err := r.WorktreeRemove(ctx, wt, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("WorktreeRemove ran the worktree-only filter")
	}
}
