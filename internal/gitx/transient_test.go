package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Failures that are another git process in the way and pass by themselves are waited out, like a
// lock file is: a worktree being created, and a configuration file being written.
//
// While one process creates a linked worktree, its administrative directory is there and its
// commondir file exists but is still empty (git opens the file, then writes it). Git lists the
// worktrees before it adds, removes or prunes one, reads that file and dies: "failed to read
// .git/worktrees/w-01/commondir: Success". A swarm has several managers and the user's own git
// working in one repository, so the harness waits that moment out like any other lock (CI found it:
// four processes adding worktrees at once, one of them failed).

// halfMadeWorktree plants what the middle of `git worktree add` leaves for a moment, the
// administrative directory of a worktree called name with an empty commondir, and returns the
// path of that file. A test completes it with finishHalfMade.
func halfMadeWorktree(t *testing.T, dir, name string) (commondir string) {
	t.Helper()
	admin := filepath.Join(dir, ".git", "worktrees", name)
	writeFile(t, filepath.Join(admin, "HEAD"), strings.Repeat("0", 40)+"\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(t.TempDir(), name, ".git")+"\n")
	commondir = filepath.Join(admin, "commondir")
	writeFile(t, commondir, "")
	if out := rawGitMayFail(dir, "worktree", "list"); !strings.Contains(out, "failed to read") {
		t.Skipf("this git lists worktrees although one has an empty commondir: %q", out)
	}
	return commondir
}

// finishHalfMade completes the file after delay, from another goroutine, the way the creating
// process does.
func finishHalfMade(t *testing.T, commondir string, delay time.Duration) {
	t.Helper()
	go func() {
		time.Sleep(delay)
		if err := os.WriteFile(commondir, []byte("../..\n"), 0o644); err != nil {
			t.Error(err)
		}
	}()
}

func TestWorktreeAddWaitsOutAWorktreeThatIsBeingCreated(t *testing.T) {
	dir := newRepo(t)
	commondir := halfMadeWorktree(t, dir, "other")
	target := filepath.Join(t.TempDir(), "w")
	add := WorktreeAddOptions{Path: target, Detach: true, Commit: "HEAD"}
	ctx := ctxT(t)

	// A runner that does not wait reports it as a lock, and git changed nothing.
	err := openRepo(t, dir, WithLockWait(0)).WorktreeAdd(ctx, add)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked from a worktree that is being created, got %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the failed add left %s behind: %v", target, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, ".git", "worktrees")); len(ents) != 1 {
		t.Fatalf("the failed add left an administrative directory behind: %v", ents)
	}

	// One that does wait gets its worktree once the other one is complete.
	finishHalfMade(t, commondir, 400*time.Millisecond)
	r := openRepo(t, dir, WithLockWait(30*time.Second))
	if err := r.WorktreeAdd(ctx, add); err != nil {
		t.Fatalf("WorktreeAdd should have waited for the other worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestListingWorktreesWaitsOutAWorktreeThatIsBeingCreated(t *testing.T) {
	dir := newRepo(t)
	commondir := halfMadeWorktree(t, dir, "other")
	ctx := ctxT(t)

	if _, err := openRepo(t, dir, WithLockWait(0)).Worktrees(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked from a worktree that is being created, got %v", err)
	}
	finishHalfMade(t, commondir, 400*time.Millisecond)
	wts, err := openRepo(t, dir, WithLockWait(30*time.Second)).Worktrees(ctx)
	if err != nil {
		t.Fatalf("Worktrees should have waited: %v", err)
	}
	if len(wts) < 1 || wts[0].Path != dir {
		t.Fatalf("worktrees: %+v", wts)
	}
}

// A commondir that stays empty is not waited for forever: the runner gives up after its wait and
// says why, as it does for a lock file that never goes away.
func TestAnEmptyCommondirThatStaysEmptyIsReported(t *testing.T) {
	dir := newRepo(t)
	halfMadeWorktree(t, dir, "other")
	start := time.Now()
	_, err := openRepo(t, dir, WithLockWait(300*time.Millisecond)).Worktrees(ctxT(t))
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "commondir") {
		t.Fatalf("want ErrLocked naming the file, got %v", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("gave up after %v against a wait of 300ms", d)
	}
}

// git words a held config.lock without the lock's name ("could not lock config file .git/config:
// File exists"), so the runner has to know that sentence too.
func TestAConfigFileBeingWrittenIsWaitedFor(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir, WithLockWait(30*time.Second))
	lock := filepath.Join(dir, ".git", "config.lock")
	writeFile(t, lock, "")
	set := call{args: []string{"config", "sleipnir.test", "value"}, mutating: true}
	if _, err := openRepo(t, dir, WithLockWait(0)).run(ctxT(t), set); !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked while config.lock is held, got %v", err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		if err := os.Remove(lock); err != nil {
			t.Error(err)
		}
	}()
	if _, err := r.run(ctxT(t), set); err != nil {
		t.Fatalf("the write should have waited for config.lock: %v", err)
	}
	if got := rawGit(t, dir, "config", "sleipnir.test"); got != "value" {
		t.Fatalf("config value = %q", got)
	}
}
