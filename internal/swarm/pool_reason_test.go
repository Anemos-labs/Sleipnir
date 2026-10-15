package swarm

import (
	"context"
	"encoding/json"
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

// A pool plans before it builds, and planning is research: a member reads an API's documentation or a package registry and runs the
// project's tests, as a reviewer may, while it still writes nothing. The session decides what that research needs: in a session that
// asks before reaching the network, a planner is not let through silently. (In a trial an architect pool could not look up a library's
// current version while choosing it: web_fetch was refused as "plan mode (read-only)".)
func TestAPlanningPoolMemberResearchesAsAReviewerMay(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	member := parent.Pool.Members[1]
	root := t.TempDir()
	engine := func(mode perm.Mode) *perm.Engine {
		e, err := perm.NewEngine(perm.Config{Root: root, Home: t.TempDir(), Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	fetch := perm.Request{Agent: member, Role: "backend", Tool: "web_fetch", Network: true, Input: json.RawMessage(`{"url":"https://proxy.golang.org/modernc.org/sqlite/@v/list"}`)}
	tests := perm.Request{Agent: member, Role: "backend", Tool: "bash", Command: "go test ./..."}
	write := perm.Request{Agent: member, Role: "backend", Tool: "write", Paths: []string{filepath.Join(root, "api/server.go")}, Writes: true}
	bypass := engine(perm.ModeBypass)
	for _, req := range []perm.Request{fetch, tests} {
		if d, handled := r.sw.poolPermission(context.Background(), member, bypass, req); !handled || !d.Allow {
			t.Errorf("%s %s: a planning member may research and check: %+v %v", req.Tool, req.Command, d, handled)
		}
	}
	if d, handled := r.sw.poolPermission(context.Background(), member, bypass, write); !handled || d.Allow || !strings.Contains(d.Reason, "pool task "+parent.ID) {
		t.Errorf("a planning member wrote: %+v %v", d, handled)
	}
	if d, _ := r.sw.poolPermission(context.Background(), member, engine(perm.ModeDefault), fetch); d.Allow {
		t.Errorf("a session that asks before the network let a planner through without asking: %+v", d)
	}
}

// A member's memory note is no change to the project its pool coordinates: while the pool plans, the note goes to the session, which
// puts it to the person with the exact text. (A long run lost twelve such notes: each was refused by the pool's read-only probe, with a
// "no one to ask" that the person never saw.)
func TestAPoolMemberMayKeepANote(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	member := parent.Pool.Members[1]
	home := t.TempDir()
	var asked []perm.Request
	e, err := perm.NewEngine(perm.Config{Root: t.TempDir(), Home: home, Mode: perm.ModeBypass, Ask: []string{"Edit(~/.sleipnir/**)"},
		Prompter: func(_ context.Context, r perm.Request) perm.Decision {
			asked = append(asked, r)
			return perm.Decision{Allow: true}
		}})
	if err != nil {
		t.Fatal(err)
	}
	rr := roleRequester{inner: e, role: Role{Name: "backend"}, swarm: r.sw, agent: member}
	note := perm.Request{Agent: member, Tool: perm.ToolMemory, Summary: "remember: vote the instant a revision lands",
		Paths: []string{filepath.Join(home, ".sleipnir", "MEMORY.md")}, Writes: true, Risk: perm.RiskMedium}
	if d := rr.Check(context.Background(), note); !d.Allow || len(asked) != 1 || !strings.Contains(asked[0].Summary, "vote the instant a revision lands") {
		t.Fatalf("the note was not put to the person: %+v, asked %d", d, len(asked))
	}
	// a change to the project is still the pool's to refuse while it plans
	write := perm.Request{Agent: member, Tool: "write", Paths: []string{filepath.Join(home, "api", "x.go")}, Writes: true}
	if d := rr.Check(context.Background(), write); d.Allow || !strings.Contains(d.Reason, "pool task "+parent.ID) {
		t.Errorf("a planning member wrote: %+v", d)
	}
}
