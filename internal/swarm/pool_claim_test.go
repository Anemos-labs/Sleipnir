package swarm

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPoolChildClaimReceivesMergedFoundation(t *testing.T) {
	r := newIsoRig(t, isoOpts{cfg: Config{PersistentPools: true, Team: []string{"backend", "backend"}, MaxWorkers: 2}, verify: true}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "idle"} })
	members, err := r.sw.ensurePool("backend")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "Build feature", Role: "backend", Files: []string{"a.txt", "b.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.beginPool("mgr", parent.ID, []string{members[0].id, members[1].id}); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.poolUpdate(members[0].id, parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	foundation, err := r.sw.Board.CreateTask(members[0].id, TaskSpec{Parent: parent.ID, Title: "Foundation", Role: "backend", Files: []string{"a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.Claim(members[0].id, foundation.ID); err != nil {
		t.Fatal(err)
	}
	foundation, _ = r.sw.Board.Snapshot().Task(foundation.ID)
	if err := writeFileString(filepath.Join(members[0].dir, "a.txt"), "accepted foundation\n"); err != nil {
		t.Fatal(err)
	}
	if result := r.sw.integrate(context.Background(), members[0], foundation, taskCommitMessage(foundation, "")); !result.ok {
		t.Fatalf("foundation merge: %+v", result)
	}
	if err := r.sw.Board.Submit(members[0].id, foundation.ID, "foundation ready", "verified"); err != nil {
		t.Fatal(err)
	}
	if res := r.accept(foundation.ID); res.IsError {
		t.Fatal(res.Text)
	}
	peerPath := filepath.Join(members[1].dir, "a.txt")
	if readText(t, peerPath) != "alpha\n" {
		t.Fatal("peer unexpectedly synchronized before claiming")
	}
	child, err := r.sw.Board.CreateTask(members[0].id, TaskSpec{Parent: parent.ID, Title: "Use foundation", Role: "backend", Files: []string{"b.txt"}, Deps: []string{foundation.ID}})
	if err != nil {
		t.Fatal(err)
	}
	result := r.callTool(context.Background(), "task", members[1].id, "backend", map[string]any{"action": "claim", "id": child.ID})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if got := readText(t, peerPath); got != "accepted foundation\n" {
		t.Fatalf("claimed child on stale checkout: %q", got)
	}
	got, _ := r.sw.Board.Snapshot().Task(child.ID)
	if got.Owner != members[1].id || got.Status != StatusDoing {
		t.Fatalf("claim changed worker identity: %+v", got)
	}
}

func TestPoolChildClaimRequiresMembership(t *testing.T) {
	s := secRevSwarm()
	defer s.Shutdown()
	parent, err := s.Board.CreateTask("mgr", TaskSpec{Title: "Backend pool", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Board.beginPool("mgr", parent.ID, []string{"be-1", "be-2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Board.poolUpdate("be-1", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := s.Board.CreateTask("be-1", TaskSpec{Title: "Backend child", Role: "backend", Parent: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	before := s.Board.Snapshot()
	result := secRevCall(t, secRevTool(t, s, "task"), "fe-1", "frontend", map[string]any{"action": "claim", "id": child.ID})
	if !result.IsError || s.Board.Snapshot() != before {
		t.Fatalf("outsider claimed pool child: %+v", result)
	}
}
