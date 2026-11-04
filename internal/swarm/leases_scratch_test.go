package swarm

import (
	"strings"
	"testing"
	"time"
)

// The runtime context tells every agent that scratch files go in $TMPDIR and that the file tools take $TMPDIR/name, and models
// write /tmp/name too. Such a write is no edit of the project: neither a task's scope nor an isolated writer's tree may refuse
// it (the permission engine has judged it by the session's mode), or the file tools contradict the prompt, a bypass session
// refuses what its mode allows, and the manager is alerted about a scope breach that is not one. Every other path keeps its
// checks, a project checked out below /tmp included.
func TestScratchWritesPassTheLeaseGuard(t *testing.T) {
	const tmp = "/state/sessions/s1/tmp"
	t.Run("shared tree with a scoped task", func(t *testing.T) {
		s := New(Config{SessionID: "t"}, Deps{Root: "/repo", Tmp: tmp}, nil)
		s.Leases.SetRoots("/repo")
		s.Leases.sysTemp = []string{"/tmp"}
		task, _ := s.Board.CreateTask("mgr", TaskSpec{Title: "docs", Files: []string{"docs/**"}})
		if err := s.Board.Assign("mgr", "dc-1", task.ID); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{tmp + "/notes.md", "/tmp/build.log", "/tmp/probe/out.json"} {
			if err := s.Leases.BeforeWrite("dc-1", p); err != nil {
				t.Fatalf("scratch write to %s refused: %v", p, err)
			}
		}
		if alerts := s.Board.Snapshot().Alerts; len(alerts) != 0 {
			t.Fatalf("scratch writes raised an alert: %+v", alerts)
		}
		if snap := s.Leases.Snapshot(); len(snap) != 0 {
			t.Fatalf("a scratch file holds no lease: %+v", snap)
		}
		for _, p := range []string{"/repo/src/x.go", "/home/u/.config/x.toml"} {
			if err := s.Leases.BeforeWrite("dc-1", p); err == nil || !strings.Contains(err.Error(), "outside the scope") {
				t.Fatalf("%s must still be refused, got %v", p, err)
			}
		}
	})
	t.Run("a project below /tmp keeps its scope", func(t *testing.T) {
		s := New(Config{SessionID: "t"}, Deps{Root: "/tmp/proj"}, nil)
		s.Leases.SetRoots("/tmp/proj")
		s.Leases.sysTemp = []string{"/tmp"}
		task, _ := s.Board.CreateTask("mgr", TaskSpec{Title: "docs", Files: []string{"docs/**"}})
		if err := s.Board.Assign("mgr", "dc-1", task.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Leases.BeforeWrite("dc-1", "/tmp/proj/src/x.go"); err == nil {
			t.Fatal("a project file below /tmp outside the task's scope was allowed")
		}
		if err := s.Leases.BeforeWrite("dc-1", "/tmp/other.txt"); err != nil {
			t.Fatalf("/tmp outside the project is scratch space: %v", err)
		}
	})
	t.Run("isolated trees", func(t *testing.T) {
		l := NewLeases(time.Minute, NewBoard(nil))
		l.Isolate()
		l.SetScratch(tmp)
		l.SetRoots("/repo")
		l.SetTreeParent("/trees")
		l.sysTemp = []string{"/tmp"}
		l.BindTree("be-1", "/trees/be-1")
		for _, p := range []string{tmp + "/probe.json", "/tmp/out.txt"} {
			if err := l.BeforeWrite("be-1", p); err != nil {
				t.Fatalf("scratch write to %s refused: %v", p, err)
			}
		}
		for _, p := range []string{"/repo/src/x.go", "/trees/be-2/src/x.go", "/trees/old-3/src/x.go", "/home/u/.config/x.toml"} {
			err := l.BeforeWrite("be-1", p)
			if err == nil || !strings.Contains(err.Error(), "outside your working tree") || !strings.Contains(err.Error(), "$TMPDIR") {
				t.Fatalf("%s is not be-1's tree: want a refusal that points at $TMPDIR, got %v", p, err)
			}
		}
		if err := l.BeforeWrite("rv-1", tmp+"/probe.json"); err == nil {
			t.Fatal("an agent without a tree (a read-only role) writes no file, $TMPDIR included")
		}
	})
}
