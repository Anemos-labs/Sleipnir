package swarm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLeaseSnapshotAndCovering(t *testing.T) {
	s := New(Config{SessionID: "t"}, Deps{Root: "/repo"}, nil)
	s.Leases.SetRoots("/repo")
	task, _ := s.Board.CreateTask("mgr", TaskSpec{Title: "docs", Files: []string{"docs/**", "README.md"}})
	if err := s.Board.Assign("mgr", "dc-1", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Leases.BeforeWrite("dc-1", "/repo/docs/a.md"); err != nil {
		t.Fatal(err)
	}
	snap := s.Leases.Snapshot()
	if len(snap) != 1 || snap[0].Path != "/repo/docs/a.md" || snap[0].Holder != "dc-1" || snap[0].Isolated || !snap[0].Expires.After(snap[0].Last) {
		t.Fatalf("snapshot = %+v", snap)
	}
	if c, ok := s.Leases.Covering("docs/deep/b.md"); !ok || c.Agent != "dc-1" || c.Task != task.ID || c.Glob != "docs/**" {
		t.Fatalf("covering docs/deep/b.md = %+v %v", c, ok)
	}
	if c, ok := s.Leases.Covering("README.md"); !ok || c.Glob != "README.md" {
		t.Fatalf("covering README.md = %+v %v", c, ok)
	}
	if _, ok := s.Leases.Covering("src/x.go"); ok {
		t.Fatal("src/x.go is in no scope")
	}
	if r := s.Leases.Roots(); len(r) != 1 || r[0] != "/repo" {
		t.Fatalf("roots = %v", r)
	}
	// A task out of progress covers nothing; released leases are gone.
	if err := s.Board.Submit("dc-1", task.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Leases.Covering("docs/a.md"); ok {
		t.Fatal("a task in review covers nothing")
	}
	s.Leases.ReleaseAll("dc-1")
	if snap := s.Leases.Snapshot(); len(snap) != 0 {
		t.Fatalf("after release: %+v", snap)
	}
}

func TestIsolatedLeaseSnapshotListsTheWritersOfAPath(t *testing.T) {
	b := NewBoard(nil)
	l := NewLeases(time.Minute, b)
	l.Isolate()
	l.BindTree("be-1", "/trees/be-1")
	l.BindTree("be-2", "/trees/be-2")
	for _, a := range []string{"be-1", "be-2"} {
		if err := l.BeforeWrite(a, "/trees/"+a+"/src/x.go"); err != nil {
			t.Fatal(err)
		}
	}
	snap := l.Snapshot()
	if len(snap) != 1 || snap[0].Path != "src/x.go" || !snap[0].Isolated || len(snap[0].Agents) != 2 || snap[0].Holder == "" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestAcceptNeedsAnIsolatedTeam(t *testing.T) {
	s := New(Config{SessionID: "t"}, Deps{Root: "/repo"}, nil)
	if _, err := s.Accept(context.Background(), AcceptOptions{DryRun: true}); !errors.Is(err, ErrNotIsolated) {
		t.Fatalf("Accept: %v", err)
	}
	if m, q := s.Workspace(); m != nil || q != nil || s.Isolated() || s.AppliedTip() != "" {
		t.Fatal("a shared-tree team has no workspace")
	}
}
