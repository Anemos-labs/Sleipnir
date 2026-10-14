package swarm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// A pool member's refused write says why in the pool's terms (its task, phase, leader) and what lets it write, never the plan-mode
// advice to present a plan or the note that nobody can be asked: a member told only that kept retrying writes for minutes in a trial.
func TestAPoolMembersRefusalSaysWhatLetsItWrite(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"frontend", "frontend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "build the UI", Files: []string{"web/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	leader, member := parent.Pool.Members[0], parent.Pool.Members[1]
	root := t.TempDir()
	eng, err := perm.NewEngine(perm.Config{Root: root, Home: t.TempDir(), Mode: perm.ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	write := perm.Request{Agent: member, Role: "frontend", Tool: "write", Paths: []string{filepath.Join(root, "web/js/api.mjs")}, Writes: true}
	check := func(who string, req perm.Request, wants ...string) {
		t.Helper()
		d, handled := r.sw.poolPermission(context.Background(), who, eng, req)
		if !handled || d.Allow {
			t.Fatalf("%s: not refused: %+v %v", who, d, handled)
		}
		for _, w := range wants {
			if !strings.Contains(d.Reason, w) {
				t.Errorf("%s: the reason does not say %q: %s", who, w, d.Reason)
			}
		}
		for _, bad := range []string{"Present a plan", "no one to ask", "approval required"} {
			if strings.Contains(d.Reason, bad) {
				t.Errorf("%s: the reason says %q: %s", who, bad, d.Reason)
			}
		}
	}
	check(member, write, "pool task "+parent.ID, "phase planning", "pool propose or agree", "web/js/api.mjs")
	call := func(as, action string, rev int, text string) {
		if res := r.callTool(context.Background(), "pool", as, "frontend", map[string]any{"id": parent.ID, "action": action, "revision": rev, "text": text}); res.IsError {
			t.Fatalf("%s %s: %s", as, action, res.Text)
		}
	}
	call(leader, "propose", 0, "Split views and charts.")
	p, _ := r.sw.Board.Snapshot().Task(parent.ID)
	call(leader, "agree", p.Pool.Revision, "")
	call(member, "agree", p.Pool.Revision, "")
	call(leader, "execute", 0, "")
	check(member, write, "phase executing", "Ask the leader "+leader, "child task assigned")
	write.Agent = leader
	check(leader, write, "As the leader", "task create parent="+parent.ID)
	read := perm.Request{Agent: member, Role: "frontend", Tool: "bash", Command: "ls -la /tmp"}
	check(member, read, "pool task "+parent.ID)
	// While the pool reconciles, a member is told to inspect the combined result and confirm it.
	call(leader, "reconcile", 0, "")
	write.Agent = member
	check(member, write, "phase reconciling", "pool inspect", "confirm", "mail the leader "+leader)
}
