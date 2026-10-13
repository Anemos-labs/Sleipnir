package swarm

import "testing"

// TestPlanningPoolSkipsCodeVerification exercises the complete agreement path in
// an empty repository, where the implementation verifier must not run yet.
func TestPlanningPoolSkipsCodeVerification(t *testing.T) {
	s := New(Config{SessionID: "planning", VerifyCmd: "exit 79"}, Deps{Workdir: t.TempDir()}, nil)
	defer s.Shutdown()
	taskTool, poolTool := secRevTool(t, s, "task"), secRevTool(t, s, "pool")
	parent, err := s.Board.CreateTask("mgr", TaskSpec{Title: "Agree before coding", Role: "scout", Kind: TaskKindPlan})
	if err != nil {
		t.Fatal(err)
	}
	members := []string{"sc-1", "sc-2"}
	if err := s.Board.beginPool("mgr", parent.ID, members); err != nil {
		t.Fatal(err)
	}
	agreement := "Agree on the ABI, split toolchain and browser research, reconcile findings."
	pool := func(by, action string) {
		t.Helper()
		current, _ := s.Board.Snapshot().Task(parent.ID)
		result := secRevCall(t, poolTool, by, "scout", map[string]any{"action": action, "id": parent.ID, "revision": current.Pool.Revision, "text": agreement})
		if result.IsError {
			t.Fatal(result.Text)
		}
	}
	pool(members[0], "propose")
	for _, id := range members {
		pool(id, "agree")
	}
	pool(members[0], "execute")
	child, err := s.Board.CreateTask(members[0], TaskSpec{Parent: parent.ID, Title: "Inspect toolchain", Role: "scout"})
	if err != nil {
		t.Fatal(err)
	}
	if child.Kind != TaskKindPlan {
		t.Fatalf("child lost planning contract: %+v", child)
	}
	if err := s.Board.Claim(members[1], child.ID); err != nil {
		t.Fatal(err)
	}
	submit := func(id, by string) {
		t.Helper()
		result := secRevCall(t, taskTool, by, "scout", map[string]any{"action": "done", "id": id, "text": "research reconciled", "agreement": "Use a pinned toolchain and a deterministic simulation."})
		if result.IsError {
			t.Fatal(result.Text)
		}
	}
	accept := func(id, by, role string) {
		t.Helper()
		result := secRevCall(t, taskTool, by, role, map[string]any{"action": "accept", "id": id})
		if result.IsError {
			t.Fatal(result.Text)
		}
	}
	submit(child.ID, members[1])
	accept(child.ID, members[0], "scout")
	agreement = "Final reconciled contract: use the pinned toolchain, versioned saves and a fixed-step WASM simulation."
	pool(members[0], "reconcile")
	for _, id := range members {
		pool(id, "confirm")
	}
	submit(parent.ID, members[0])
	accept(parent.ID, "mgr", "manager")
	got, _ := s.Board.Snapshot().Task(parent.ID)
	if got.Status != StatusDone || got.Agreement != agreement {
		t.Fatalf("agreement not accepted: %+v", got)
	}
	if s.verifyRan.Load() != 0 {
		t.Fatal("planning ran the code verifier")
	}
	// An explicitly requested implementation task still receives code verification.
	work, err := s.Board.CreateTask("mgr", TaskSpec{Title: "Verify implementation", Role: "scout", Kind: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Board.Assign("mgr", members[0], work.ID); err != nil {
		t.Fatal(err)
	}
	if result := secRevCall(t, taskTool, members[0], "scout", map[string]any{"action": "done", "id": work.ID, "text": "done"}); !result.IsError {
		t.Fatal("work bypassed verifier")
	}
}

func TestTaskKindRepairPreservesIDAndInvalidatesSubmission(t *testing.T) {
	s := secRevSwarm()
	defer s.Shutdown()
	task, err := s.Board.CreateTask("mgr", TaskSpec{Title: "Inspect environment", Role: "scout"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Board.Assign("mgr", "sc-1", task.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Board.Snapshot().Task(task.ID)
	result := secRevCall(t, secRevTool(t, s, "task"), "mgr", "manager", map[string]any{"action": "revise", "id": task.ID, "kind": "plan", "text": "Correct the research verifier contract"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	got, _ := s.Board.Snapshot().Task(task.ID)
	if len(s.Board.Snapshot().Tasks) != 1 || got.Kind != TaskKindPlan || got.Rev <= before.Rev || got.LastOwner != "sc-1" || got.Status != StatusTodo {
		t.Fatalf("repair lost task identity: %+v", got)
	}
	if err := s.Board.revise("mgr", task.ID, TaskSpec{Kind: "work"}, "Implementation now required"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Board.Snapshot().Task(task.ID)
	if got.Kind != "" {
		t.Fatal("explicit work kind was not restored")
	}
	snapshot := s.Board.Snapshot()
	if err := s.Board.revise("mgr", task.ID, TaskSpec{Kind: "invalid"}, "Invalid"); err == nil || s.Board.Snapshot() != snapshot {
		t.Fatal("invalid kind mutated board")
	}
}
