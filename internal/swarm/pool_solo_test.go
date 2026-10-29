package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// newSoloRig is a team of three backend members whose model calls stay in their turn until released, so a member given work keeps
// it while the test looks.
func newSoloRig(t *testing.T) (*rvRig, *heldModel) {
	t.Helper()
	h := &heldModel{held: map[string]bool{"be-1": true, "be-2": true, "be-3": true}}
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}}, h.call)
	return r, h
}

// poolWoken reports whether any member was mailed the start of a pool.
func poolWoken(t *testing.T, r *rvRig) bool {
	t.Helper()
	for _, e := range r.log.OfType(events.TypeMailSend) {
		var m Message
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(m.Text, "Plan together") {
			return true
		}
	}
	return false
}

// A small top-level task of a role with several members, spawned solo, goes to one member and starts at once: no pool, no votes.
func TestSoloTaskSkipsThePool(t *testing.T) {
	r, _ := newSoloRig(t)
	res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": "scaffold the project", "files": []string{"go.mod", "cmd/**"}, "solo": true})
	if res.IsError {
		t.Fatal(res.Text)
	}
	task := r.sw.Board.Snapshot().Tasks[0]
	if task.Pool != nil || !task.Solo || task.Owner != "be-1" || task.Status != StatusDoing {
		t.Fatalf("solo task: %+v", task)
	}
	if poolWoken(t, r) {
		t.Fatal("a solo task woke a pool")
	}
	if n := len(r.sw.poolMembers("backend")); n != 3 {
		t.Fatalf("the role's members were not all made: %d", n)
	}
	rvWait(t, "be-1 in a turn", func() bool { return r.running("be-1") })

	// The next solo task goes to the next free member, and agent= names one.
	if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": "write the config", "files": []string{"config/**"}, "solo": true, "agent": "be-3"}); res.IsError {
		t.Fatal(res.Text)
	}
	if got, _ := r.sw.Board.Snapshot().Task("T2"); got.Owner != "be-3" || got.Pool != nil {
		t.Fatalf("agent= on a solo task: %+v", got)
	}

	// A parent of the role goes to the one member that holds no solo task, and as a pool of one would plan with nobody, it takes
	// the parent alone: no pool, no votes, no child repeating its parent.
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent, _ := r.sw.Board.Snapshot().Task("T3")
	if parent.Pool != nil || !parent.Solo || parent.Owner != "be-2" || parent.Status != StatusDoing {
		t.Fatalf("a parent with one free member: %+v", parent)
	}
	if poolWoken(t, r) {
		t.Fatal("a parent with one free member woke a pool")
	}

	// Nobody is free for a third solo task: an existing task is queued, not given to a busy member.
	tk, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "add a makefile", Role: "backend", Files: []string{"Makefile"}})
	if err != nil {
		t.Fatal(err)
	}
	if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"task": tk.ID, "solo": true}); res.IsError || !strings.Contains(res.Text, "queued") {
		t.Fatalf("solo spawn with every member busy: %q", res.Text)
	}
	if got, _ := r.sw.Board.Snapshot().Task(tk.ID); got.Owner != "" || got.Pool != nil {
		t.Fatalf("queued solo task: %+v", got)
	}
}

// A solo task stays solo: when it comes back to the board, its member is started on it again (the repair pass) without a pool, and
// a member may claim it; a parent that is not solo still cannot be claimed past its pool.
func TestSoloTaskStaysSoloWhenStartedAgain(t *testing.T) {
	r, h := newSoloRig(t)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "scaffold", Files: []string{"go.mod"}, Solo: true, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	task := r.sw.Board.Snapshot().Tasks[0]
	rvWait(t, "be-1 in a turn", func() bool { return r.running("be-1") })
	h.hold("be-1")
	m := r.sw.get("be-1")
	m.mu.Lock()
	rs := m.run
	m.mu.Unlock()
	rs.cancel()
	rvWait(t, "be-1 idle", func() bool { return r.idle("be-1") })
	if cur, _ := r.sw.Board.Snapshot().Task(task.ID); cur.Status == StatusDoing && cur.Owner == "be-1" {
		r.sw.Board.Requeue("be-1", cur.ID, cur.Rev, "stopped", true, 0)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Agent: "be-1", TaskID: task.ID, By: "harness"}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.sw.Board.Snapshot().Task(task.ID)
	if got.Pool != nil || got.Owner != "be-1" || got.Status != StatusDoing {
		t.Fatalf("a solo task started again: %+v", got)
	}
	if poolWoken(t, r) {
		t.Fatal("a solo task started again woke a pool")
	}

	// claimCheck: a solo task may be claimed; an ordinary parent of the role may not.
	check := r.sw.claimCheck("be-2", false)
	if err := check(r.sw.Board.Snapshot(), Task{ID: "T9", Role: "backend", Solo: true}); err != nil {
		t.Fatalf("claim of a solo task: %v", err)
	}
	if err := check(r.sw.Board.Snapshot(), Task{ID: "T9", Role: "backend"}); err == nil || !strings.Contains(err.Error(), "solo=true") {
		t.Fatalf("claim of a pooled parent: %v", err)
	}
}

// A worker that claims a top-level task of its pooled role is told that the manager starts it and what to do instead, not to start
// it with spawn, which only the manager may do.
func TestAWorkersClaimOfAPoolParentSaysTheManagerStartsIt(t *testing.T) {
	r, _ := newSoloRig(t)
	task, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "build the API", Role: "backend", Files: []string{"api/**"}})
	if err != nil {
		t.Fatal(err)
	}
	res := secRevCall(t, secRevTool(t, r.sw, "task"), "be-2", "backend", map[string]any{"action": "claim", "id": task.ID})
	text := res.Text
	if !res.IsError || !strings.Contains(text, task.ID+" is a top-level backend task: the manager starts it") || strings.Contains(text, "assign this parent") {
		t.Fatalf("a worker's claim of a pool parent: %q", text)
	}
	if got, _ := r.sw.Board.Snapshot().Task(task.ID); got.Owner != "" {
		t.Fatalf("the claim took the task: %+v", got)
	}
}

// solo is for a top-level task: a child of a pool, or a parent that already has a pool, is refused.
func TestSoloRefusesChildrenAndPooledParents(t *testing.T) {
	r, _ := newSoloRig(t)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: parent.ID, Agent: "be-2", Solo: true, By: "mgr"}); err == nil {
		t.Fatal("a pooled parent was given solo to one member")
	}
	if _, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "child", Parent: parent.ID, Role: "backend", Files: []string{"api/x.go"}, Solo: true}); err == nil || !strings.Contains(err.Error(), "top-level") {
		t.Fatalf("a solo child was created: %v", err)
	}
}

// A pool agreeing on its plan holds every member of the role. While other tasks of the role are ready, a parent goes to one member
// instead, and the next ready task starts on the next member: the tasks run side by side, not one after another.
func TestReadyTasksOfARoleRunSideBySideInsteadOfPooling(t *testing.T) {
	r, _ := newSoloRig(t)
	var ids []string
	for _, title := range []string{"cross-compile the agent", "mock totals test", "notifier receivers"} {
		tk, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: title, Role: "backend", Files: []string{strings.ReplaceAll(title, " ", "-") + "/**"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tk.ID)
	}
	for i, id := range ids[:2] {
		if res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"task": id}); res.IsError {
			t.Fatalf("spawn %s: %s", id, res.Text)
		}
		got, _ := r.sw.Board.Snapshot().Task(id)
		if want := []string{"be-1", "be-2"}[i]; got.Pool != nil || got.Owner != want || got.Status != StatusDoing {
			t.Fatalf("%s: want it started on %s alone, got %+v", id, want, got)
		}
	}
	if poolWoken(t, r) {
		t.Fatal("a pool was started while other tasks of the role were ready")
	}
}
