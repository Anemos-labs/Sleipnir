package workspace

import (
	"errors"
	"os"
	"path/filepath"
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
	return readFile(t, filepath.Join(admin, markerFile))
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
