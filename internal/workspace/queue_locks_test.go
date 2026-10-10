package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// newImpatientQueueEnv is newQueueEnv with a git runner that gives up on a lock
// file after 200 ms instead of retrying for ten seconds, so a lock that is never
// going away fails a test quickly.
func newImpatientQueueEnv(t *testing.T, o QueueOptions) *queueEnv {
	t.Helper()
	skipWithoutUnix(t)
	dir := newRepo(t)
	repo := openRepo(t, dir, gitx.WithLockWait(200*time.Millisecond))
	m := newManager(t, repo)
	log := &eventLog{}
	m.OnEvent = log.fn()
	return &queueEnv{t: t, dir: dir, repo: repo, m: m, q: mustQueue(t, m, o), log: log}
}

// A git command that is stopped after creating its first lock file but before it
// has installed its cleanup (or that has to be killed) leaves the lock behind, and
// nothing but the queue works in the integration tree, so the next submission must
// not be refused for it. A cancelled merge can leave ORIG_HEAD.lock this way, which
// TestQueueSurvivesRandomCancellations can reach by timing alone.
func TestQueueMergesPastLocksAStoppedGitLeftInItsTree(t *testing.T) {
	for _, name := range []string{"ORIG_HEAD.lock", "HEAD.lock", "index.lock", "MERGE_HEAD.lock", "AUTO_MERGE.lock"} {
		t.Run(name, func(t *testing.T) {
			e := newImpatientQueueEnv(t, QueueOptions{})
			admin := e.q.tree.repo.GitDir()
			a, b := e.agent("a"), e.agent("b") // b starts before a lands, so its submission is a real merge
			edit(t, a, "a.txt", "a\n")
			edit(t, b, "b.txt", "b\n")
			lock := filepath.Join(admin, name)

			writeFile(t, lock, "")
			if r := e.submit(a, "a"); !r.Merged() {
				t.Fatalf("a, with %s in the integration tree: %+v", name, r)
			}
			if exists(lock) {
				t.Fatalf("%s is still there after a merge", name)
			}
			e.integrationClean()

			writeFile(t, lock, "")
			if r := e.submit(b, "b"); !r.Merged() {
				t.Fatalf("b, with %s in the integration tree: %+v", name, r)
			}
			if exists(lock) {
				t.Fatalf("%s is still there after a second merge", name)
			}
			e.integrationClean()
			for _, f := range []string{"a.txt", "b.txt"} {
				if !exists(filepath.Join(e.q.tree.Path, f)) {
					t.Errorf("%s did not land", f)
				}
			}
		})
	}
}

// The rollback of a submission has to work with the lock a stopped git left: it
// is what the queue falls back on after a cancelled merge, and it must restore the
// tree in place, not by throwing the tree away.
func TestQueueRollbackRestoresTheTreePastAStaleLock(t *testing.T) {
	var e *queueEnv
	e = newImpatientQueueEnv(t, QueueOptions{
		VerifyCmd: "verify",
		Verify: func(_ context.Context, req VerifyRequest) VerifyResult {
			writeFile(t, filepath.Join(e.q.tree.repo.GitDir(), "index.lock"), "")
			return VerifyResult{Cmd: req.Cmd, ExitCode: 1, Output: "rejected"}
		},
	})
	before := e.q.tree
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	r := e.submit(a, "a")
	if r.Outcome != OutcomeVerifyFailed || !r.RolledBack {
		t.Fatalf("a: %+v", r)
	}
	if e.q.tree != before {
		t.Fatal("the integration tree was rebuilt because a lock stood in the way of the rollback")
	}
	if exists(filepath.Join(e.q.tree.repo.GitDir(), "index.lock")) {
		t.Fatal("the stale index.lock is still there")
	}
	if st := e.q.Status(); !st.Healthy {
		t.Fatalf("status: %+v", st)
	}
	e.integrationClean()
}

// Only the integration tree's own lock files are removed: not the user's
// repository's, not another tree's, and nothing in the tree's administrative
// directory that is not a plain file named *.lock directly inside it.
func TestQueueLeavesEveryOtherLockAlone(t *testing.T) {
	e := newImpatientQueueEnv(t, QueueOptions{})
	a, b := e.agent("a"), e.agent("b")
	edit(t, a, "a.txt", "a\n")
	admin := e.q.tree.repo.GitDir()

	keep := []string{
		filepath.Join(e.repo.GitDir(), "index.lock"),                     // the user's own index
		filepath.Join(e.repo.CommonDir(), "refs", "heads", "other.lock"), // a ref of the user's
		filepath.Join(b.repo.GitDir(), "index.lock"),                     // another tree
		filepath.Join(filepath.Dir(admin), "stray.lock"),                 // beside the administrative directories
		filepath.Join(admin, "logs", "nested.lock"),                      // below the directory, not in it
		filepath.Join(admin, "dir.lock", "inner.lock"),                   // inside a directory named like a lock
		filepath.Join(admin, "lock"),
		filepath.Join(admin, "lock.txt"),
	}
	for _, p := range keep {
		writeFile(t, p, "keep\n")
	}
	outside := filepath.Join(t.TempDir(), "outside.lock")
	writeFile(t, outside, "keep\n")
	link := filepath.Join(admin, "link.lock")
	must(t, os.Symlink(outside, link))
	keep = append(keep, outside)
	stale := filepath.Join(admin, "ORIG_HEAD.lock")
	writeFile(t, stale, "")

	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	if exists(stale) {
		t.Error("the integration tree's own stale lock is still there")
	}
	for _, p := range keep {
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() || readFile(t, p) != "keep\n" {
			t.Errorf("%s was removed or changed (%v)", p, err)
		}
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symbolic link %s was removed or replaced (%v)", link, err)
	}
	if !exists(filepath.Join(admin, markerFile)) {
		t.Error("the ownership marker is gone")
	}
}

// The directory that is cleared has to be the integration tree's own entry under
// the repository's worktrees/ directory. Handles that name anything else are left
// alone, even though they hold lock files: the main repository's git directory,
// and an entry that has been replaced by a link to somewhere else.
func TestQueueClearsLocksOnlyInItsOwnAdministrativeDirectory(t *testing.T) {
	e := newImpatientQueueEnv(t, QueueOptions{})
	admin := e.q.tree.repo.GitDir()

	t.Run("the main git directory", func(t *testing.T) {
		lock := filepath.Join(e.repo.GitDir(), "index.lock")
		writeFile(t, lock, "keep\n")
		(&Queue{m: e.m, tree: &Tree{repo: e.repo}}).clearStaleLocks()
		if !exists(lock) {
			t.Fatal("the lock in the user's git directory was removed")
		}
	})

	t.Run("a link in place of the directory", func(t *testing.T) {
		moved := admin + ".moved"
		must(t, os.Rename(admin, moved))
		must(t, os.Symlink(moved, admin))
		defer func() {
			must(t, os.Remove(admin))
			must(t, os.Rename(moved, admin))
		}()
		lock := filepath.Join(moved, "ORIG_HEAD.lock")
		writeFile(t, lock, "keep\n")
		e.q.clearStaleLocks()
		if !exists(lock) {
			t.Fatal("a lock was removed through a symbolic link")
		}
	})

	t.Run("the directory itself", func(t *testing.T) {
		lock := filepath.Join(admin, "ORIG_HEAD.lock")
		writeFile(t, lock, "")
		e.q.clearStaleLocks()
		if exists(lock) {
			t.Fatal("the stale lock of the integration tree is still there")
		}
	})
}
