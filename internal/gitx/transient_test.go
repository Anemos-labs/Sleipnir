package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// With a new branch too: git's own `worktree add -b` leaves the branch behind when it fails, so
	// running it again meets "a branch named ... already exists" (four processes adding worktrees at
	// once did that in one run in six, once the wait was in).
	for name, add := range map[string]WorktreeAddOptions{
		"detached":   {Detach: true, Commit: "HEAD"},
		"new branch": {Branch: "sleipnir/s1/w", Commit: "HEAD"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			commondir := halfMadeWorktree(t, dir, "other")
			add.Path = filepath.Join(t.TempDir(), "w")
			ctx := ctxT(t)

			// A runner that does not wait reports it as a lock, and nothing was left behind.
			err := openRepo(t, dir, WithLockWait(0)).WorktreeAdd(ctx, add)
			if !errors.Is(err, ErrLocked) {
				t.Fatalf("want ErrLocked from a worktree that is being created, got %v", err)
			}
			if _, err := os.Stat(add.Path); !os.IsNotExist(err) {
				t.Fatalf("the failed add left %s behind: %v", add.Path, err)
			}
			if ents, _ := os.ReadDir(filepath.Join(dir, ".git", "worktrees")); len(ents) != 1 {
				t.Fatalf("the failed add left an administrative directory behind: %v", ents)
			}
			if refs, _ := openRepo(t, dir).Branches(ctx, "sleipnir/"); len(refs) != 0 {
				t.Fatalf("the failed add left its branch behind: %+v", refs)
			}

			// One that does wait gets its worktree once the other one is complete.
			finishHalfMade(t, commondir, 400*time.Millisecond)
			r := openRepo(t, dir, WithLockWait(30*time.Second))
			if err := r.WorktreeAdd(ctx, add); err != nil {
				t.Fatalf("WorktreeAdd should have waited for the other worktree: %v", err)
			}
			if _, err := os.Stat(filepath.Join(add.Path, ".git")); err != nil {
				t.Fatal(err)
			}
			if add.Branch != "" {
				refs, _ := r.Branches(ctx, "sleipnir/")
				if len(refs) != 1 || refs[0].Name != add.Branch {
					t.Fatalf("branches after the add: %+v", refs)
				}
				if br, _ := openRepo(t, add.Path).Branch(ctx); br != add.Branch {
					t.Fatalf("the worktree has %q checked out, want %q", br, add.Branch)
				}
			}
		})
	}
}

// A worktree that cannot be made does not leave its branch behind (git's own -b does).
func TestFailedWorktreeAddTakesItsNewBranchBack(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	taken := t.TempDir()
	writeFile(t, filepath.Join(taken, "in the way"), "x") // a directory that is not empty: git refuses it
	err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: taken, Branch: "sleipnir/s1/w", Commit: "HEAD"})
	if err == nil {
		t.Fatal("a worktree was added over a directory that has files in it")
	}
	if refs, _ := r.Branches(ctx, "sleipnir/"); len(refs) != 0 {
		t.Fatalf("the branch of a worktree that was not made is still there: %+v", refs)
	}
	// A branch that was there before is not this call's to take back.
	rawGit(t, dir, "branch", "sleipnir/s1/mine")
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: filepath.Join(t.TempDir(), "w"), Branch: "sleipnir/s1/mine", Commit: "HEAD"}); err == nil {
		t.Fatal("a branch that exists was created again")
	}
	if refs, _ := r.Branches(ctx, "sleipnir/"); len(refs) != 1 || refs[0].Name != "sleipnir/s1/mine" {
		t.Fatalf("branches: %+v", refs)
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

func TestWorktreeAddRetriesMissingRefLockParent(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(fmt.Sprint(wait), func(t *testing.T) {
			dir := newRepo(t)
			gitShim, evidence := shim(t, `
case " $* " in
  *" branch --no-track -- sleipnir/retry/worker "*)
    echo attempt >> "$D/attempts"
    if [ ! -f "$D/failed-once" ]; then
      touch "$D/failed-once"
      echo "fatal: cannot lock ref 'refs/heads/sleipnir/retry/worker': Unable to create '/repo/.git/refs/heads/sleipnir/retry/worker.lock': No such file or directory" >&2
      exit 128
    fi
    ;;
esac
exec "$REAL" "$@"`)
			lockWait := time.Duration(0)
			if wait {
				lockWait = 5 * time.Second
			}
			r := openRepo(t, dir, WithGitPath(gitShim), WithLockWait(lockWait))
			path := filepath.Join(t.TempDir(), "worker")
			err := r.WorktreeAdd(ctxT(t), WorktreeAddOptions{Path: path, Branch: "sleipnir/retry/worker", Commit: "HEAD"})
			attempts := strings.Count(readFile(t, filepath.Join(evidence, "attempts")), "attempt\n")
			if !wait {
				if !errors.Is(err, ErrLocked) || attempts != 1 {
					t.Fatalf("no-wait add: attempts=%d error=%v", attempts, err)
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("ref lock failure left a worktree: %v", statErr)
				}
				return
			}
			if err != nil || attempts != 2 {
				t.Fatalf("retrying add: attempts=%d error=%v", attempts, err)
			}
			if branch, err := openRepo(t, path).Branch(ctxT(t)); err != nil || branch != "sleipnir/retry/worker" {
				t.Fatalf("worktree branch=%q error=%v", branch, err)
			}
		})
	}
}

// The real thing, not a planted entry: handles that add, remove and list the worktrees of one repository at
// once (a swarm's managers, and a person's own git). Before the runner waited these moments out, three
// seconds of it gave dozens of failures: an empty commondir being read, a commondir that vanished, the
// worktrees directory removed under a listing, and (with a new branch) the branch a failed add leaves behind.
func TestConcurrentWorktreeCommandsDoNotFail(t *testing.T) {
	if testing.Short() {
		t.Skip("runs git for a few seconds")
	}
	dir := newRepo(t)
	trees := t.TempDir()
	ctx := ctxT(t)
	stop := time.Now().Add(3 * time.Second)
	var mu sync.Mutex
	var failures []string
	fail := func(what string, err error) {
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, what+": "+err.Error())
	}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := openRepo(t, dir, WithLockWait(time.Minute))
			for n := 0; time.Now().Before(stop); n++ {
				add := WorktreeAddOptions{Path: filepath.Join(trees, fmt.Sprintf("w%d-%d", w, n)), Detach: true, Commit: "HEAD", NoCheckout: n%2 == 0}
				if n%3 == 0 {
					add.Detach, add.Branch = false, fmt.Sprintf("sleipnir/s/w%d-%d", w, n)
				}
				if err := r.WorktreeAdd(ctx, add); err != nil {
					fail("add", err)
					continue
				}
				if err := r.WorktreeRemove(ctx, add.Path, true); err != nil {
					fail("remove", err)
				}
				if add.Branch != "" {
					if err := r.DeleteBranch(ctx, add.Branch); err != nil {
						fail("delete branch", err)
					}
				}
			}
		}(w)
	}
	for l := 0; l < 3; l++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := openRepo(t, dir, WithLockWait(time.Minute))
			for time.Now().Before(stop) {
				if _, err := r.Worktrees(ctx); err != nil {
					fail("list", err)
				}
			}
		}()
	}
	wg.Wait()
	if len(failures) > 0 {
		shown := failures
		if len(shown) > 5 {
			shown = shown[:5]
		}
		t.Fatalf("%d command(s) failed, for example:\n%s", len(failures), strings.Join(shown, "\n"))
	}
	if refs, _ := openRepo(t, dir).Branches(ctx, "sleipnir/"); len(refs) != 0 {
		t.Fatalf("branches left behind: %+v", refs)
	}
}
