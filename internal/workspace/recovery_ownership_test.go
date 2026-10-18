package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setUnknownOwner(t *testing.T, tree *Tree, identity string) string {
	t.Helper()
	admin := tree.repo.GitDir()
	mk, err := readMarker(admin)
	if err != nil {
		t.Fatal(err)
	}
	switch identity {
	case "boot":
		mk.BootID = bootID() + "-other"
	case "namespace":
		mk.PIDNS = pidNamespace() + "-other"
	default:
		t.Fatalf("unknown test identity %q", identity)
	}
	if mk.owner() != ownerUnknown {
		t.Fatal("fixture must have an unknown owner")
	}
	if err := writeMarker(admin, *mk); err != nil {
		t.Fatal(err)
	}
	// An owner from a version without owner locks: nothing settles the question.
	dropOwnerLock(admin)
	must(t, os.Remove(filepath.Join(admin, ownerLockFile)))
	return readFile(t, filepath.Join(admin, markerFile))
}

// rebootedOwner makes tree's marker look written in another boot by an owner
// whose lock is free, as after the machine or container restarted.
func rebootedOwner(t *testing.T, tree *Tree) string {
	t.Helper()
	admin := tree.repo.GitDir()
	mk, err := readMarker(admin)
	if err != nil {
		t.Fatal(err)
	}
	mk.BootID = bootID() + "-before-restart"
	if err := writeMarker(admin, *mk); err != nil {
		t.Fatal(err)
	}
	dropOwnerLock(admin)
	if !exists(filepath.Join(admin, ownerLockFile)) {
		t.Fatal("fixture must keep the owner lock file")
	}
	return admin
}

func TestOwnerLockSettlesAnotherBoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner locks are not used on this platform")
	}
	repo := openRepo(t, newRepo(t))
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	admin := rebootedOwner(t, a)
	mk, err := readMarker(admin)
	if err != nil {
		t.Fatal(err)
	}
	if got := mk.owner(); got != ownerUnknown {
		t.Fatalf("marker alone = %v, want unknown", got)
	}
	if got := mk.ownerIn(admin); got != ownerDead {
		t.Errorf("free lock = %v, want dead", got)
	}
	held, err := os.OpenFile(filepath.Join(admin, ownerLockFile), os.O_RDWR, 0)
	must(t, err)
	defer held.Close()
	must(t, tryLock(held))
	if got := mk.ownerIn(admin); got != ownerAlive {
		t.Errorf("lock held elsewhere = %v, want alive", got)
	}
}

func TestRecoveryAfterRestartAdoptsReleasedOwners(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner locks are not used on this platform")
	}
	t.Run("worker", func(t *testing.T) {
		repo := openRepo(t, newRepo(t))
		m := newManager(t, repo)
		a := mustCreate(t, m, "a")
		edit(t, a, "README.md", "work in progress\n")
		rebootedOwner(t, a)
		livePaths.Delete(a.Path)

		fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: m.Prefix, Clock: testClock()}
		got, err := fresh.Create(tctx(t), "a", CreateOptions{Reuse: true})
		if err != nil {
			t.Fatalf("recovery after a restart must adopt the tree: %v", err)
		}
		if content := readFile(t, filepath.Join(got.Path, "README.md")); content != "work in progress\n" {
			t.Error("adoption lost the worker's unfinished work")
		}
		mk, err := readMarker(got.repo.GitDir())
		must(t, err)
		if mk.BootID != bootID() || mk.ownerIn(got.repo.GitDir()) != ownerAlive {
			t.Error("the adopting process must own the marker and its lock")
		}
	})
	t.Run("queue", func(t *testing.T) {
		e := newQueueEnv(t, QueueOptions{})
		tip := e.q.Tip()
		rebootedOwner(t, e.q.tree)
		livePaths.Delete(e.q.tree.Path)

		fresh := &Manager{Repo: e.repo, Dir: e.m.Dir, Prefix: e.m.Prefix, Clock: testClock()}
		q, err := NewQueue(tctx(t), fresh, QueueOptions{Resume: true})
		if err != nil {
			t.Fatalf("queue recovery after a restart must replace the integration tree: %v", err)
		}
		if q.Tip() != tip {
			t.Errorf("resumed tip = %s, want %s", q.Tip(), tip)
		}
	})
}

func TestWorkerRecoveryPreservesUnknownOwners(t *testing.T) {
	for _, identity := range []string{"boot", "namespace"} {
		for _, missing := range []bool{false, true} {
			name := identity + "/present"
			if missing {
				name = identity + "/missing"
			}
			t.Run(name, func(t *testing.T) {
				repo := openRepo(t, newRepo(t))
				m := newManager(t, repo)
				a := mustCreate(t, m, "a")
				edit(t, a, "README.md", "work in progress\n")
				markerBefore := setUnknownOwner(t, a, identity)
				markerPath := filepath.Join(a.repo.GitDir(), markerFile)
				indexLock := filepath.Join(a.repo.GitDir(), "index.lock")
				writeFile(t, indexLock, "owner's lock\n")
				if missing {
					must(t, os.RemoveAll(a.Path))
				}

				fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: m.Prefix, Clock: testClock()}
				if _, err := fresh.Create(tctx(t), "a", CreateOptions{Reuse: true}); !errors.Is(err, ErrExists) {
					t.Errorf("recovery must refuse an unknown owner: %v", err)
				}
				if got := readFile(t, markerPath); got != markerBefore {
					t.Error("recovery changed the ownership marker")
				}
				if got := readFile(t, indexLock); got != "owner's lock\n" {
					t.Error("recovery changed the owner's index lock")
				}
				if head, err := repo.BranchSHA(tctx(t), a.Branch); err != nil || head != a.Base {
					t.Errorf("recovery changed the worker branch: %s, %v", head, err)
				}
				if missing {
					if exists(a.Path) {
						t.Error("recovery recreated an unknown owner's missing directory")
					}
				} else if got := readFile(t, filepath.Join(a.Path, "README.md")); got != "work in progress\n" {
					t.Error("recovery changed the worker's unfinished work")
				}
			})
		}
	}
}

func TestQueueRecoveryPreservesUnknownOwners(t *testing.T) {
	for _, identity := range []string{"boot", "namespace"} {
		for _, missing := range []bool{false, true} {
			name := identity + "/present"
			if missing {
				name = identity + "/missing"
			}
			t.Run(name, func(t *testing.T) {
				e := newQueueEnv(t, QueueOptions{})
				tree := e.q.tree
				edit(t, tree, "README.md", "verification in progress\n")
				markerBefore := setUnknownOwner(t, tree, identity)
				markerPath := filepath.Join(tree.repo.GitDir(), markerFile)
				refLock := filepath.Join(e.repo.CommonDir(), "refs", "heads", filepath.FromSlash(e.q.Branch())+".lock")
				writeFile(t, refLock, "owner's publication lock\n")
				if missing {
					must(t, os.RemoveAll(tree.Path))
				}

				fresh := &Manager{Repo: e.repo, Dir: e.m.Dir, Prefix: e.m.Prefix, Clock: testClock()}
				if _, err := NewQueue(tctx(t), fresh, QueueOptions{Resume: true}); !errors.Is(err, ErrExists) {
					t.Errorf("queue recovery must refuse an unknown owner: %v", err)
				}
				if got := readFile(t, markerPath); got != markerBefore {
					t.Error("queue recovery changed the ownership marker")
				}
				if got := readFile(t, refLock); got != "owner's publication lock\n" {
					t.Error("queue recovery changed the owner's publication lock")
				}
				if head, err := e.repo.BranchSHA(tctx(t), e.q.Branch()); err != nil || head != e.q.Tip() {
					t.Errorf("queue recovery changed the integration branch: %s, %v", head, err)
				}
				if missing {
					if exists(tree.Path) {
						t.Error("queue recovery recreated an unknown owner's missing directory")
					}
				} else if got := readFile(t, filepath.Join(tree.Path, "README.md")); got != "verification in progress\n" {
					t.Error("queue recovery changed the owner's verification tree")
				}
			})
		}
	}
}

func TestExclusiveRecoveryAdoptsUnknownOwnersOfItsPrefix(t *testing.T) {
	for _, identity := range []string{"boot", "namespace"} {
		t.Run(identity, func(t *testing.T) {
			e := newQueueEnv(t, QueueOptions{})
			setUnknownOwner(t, e.q.tree, identity)
			a := mustCreate(t, e.m, "a")
			edit(t, a, "README.md", "work in progress\n")
			setUnknownOwner(t, a, identity)

			fresh := &Manager{Repo: e.repo, Dir: e.m.Dir, Prefix: e.m.Prefix, Clock: testClock(), Exclusive: true}
			q, err := NewQueue(tctx(t), fresh, QueueOptions{Resume: true})
			if err != nil {
				t.Fatalf("an exclusive resume must clear its own stale integration tree: %v", err)
			}
			if q.Tip() != e.q.Tip() {
				t.Errorf("tip %s, want %s", q.Tip(), e.q.Tip())
			}
			got, err := fresh.Create(tctx(t), "a", CreateOptions{Reuse: true})
			if err != nil {
				t.Fatalf("an exclusive resume must adopt its own worker tree: %v", err)
			}
			if body := readFile(t, filepath.Join(got.Path, "README.md")); body != "work in progress\n" {
				t.Errorf("adoption lost the worker's unfinished work: %q", body)
			}
		})
	}
}
