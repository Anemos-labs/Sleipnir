package swarm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
)

// approvalRig is a team whose manager answers its workers' routine questions, with one idle backend worker.
func approvalRig(t *testing.T, timeout time.Duration) (*rvRig, string) {
	t.Helper()
	r := newRVRig(t, Config{MaxWorkers: 4, ManagerApproves: true, ManagerApproveTimeout: timeout}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "serve notes", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	return r, id
}

// ask puts a worker's question to the manager and returns a channel with what came of it.
func ask(r *rvRig, agent, cmd string) <-chan [2]any {
	out := make(chan [2]any, 1)
	go func() {
		d, by := r.sw.DelegateApproval(context.Background(), perm.Request{Agent: agent, Role: "backend", Tool: "bash", Command: cmd, Why: "./bin/app: not on the read-only allowlist"})
		out <- [2]any{d, by}
	}()
	return out
}

// waitQuestion waits until the manager has a question waiting and returns its id.
func waitQuestion(t *testing.T, r *rvRig) string {
	t.Helper()
	var id string
	rvWait(t, "a question for the manager", func() bool {
		r.sw.mu.Lock()
		defer r.sw.mu.Unlock()
		for k := range r.sw.approvals {
			id = k
		}
		return id != ""
	})
	return id
}

// The manager is told the question, and its yes, its no with an instruction, and its escalation come back to the engine as the
// decision of "manager", the instruction included, or as no decision (the person decides).
func TestTheManagerAnswersAWorkersRoutineQuestion(t *testing.T) {
	r, worker := approvalRig(t, time.Minute)
	mgr := r.sw.ManagerID()
	ctx := context.Background()
	for _, c := range []struct {
		decision, text string
		allow          bool
		by, reason     string
	}{
		{decision: "yes", allow: true, by: ByManager, reason: "approved by the manager"},
		{decision: "no", text: "use go test instead", by: ByManager, reason: "The manager says: use go test instead"},
		{decision: "escalate", by: ""},
	} {
		res := ask(r, worker, "PORT=18098 ./bin/app &")
		id := waitQuestion(t, r)
		if !mailSaid(r, mgr, "Approval "+id+": "+worker) {
			t.Errorf("the manager was not told about %s", id)
		}
		if out := r.callTool(ctx, "approve", worker, "backend", map[string]any{"id": id, "decision": "yes"}); !out.IsError {
			t.Errorf("a worker answered a question: %q", out.Text)
		}
		if out := r.callTool(ctx, "approve", mgr, "manager", map[string]any{"id": id, "decision": c.decision, "text": c.text}); out.IsError {
			t.Fatalf("approve: %q", out.Text)
		}
		got := <-res
		d, by := got[0].(perm.Decision), got[1].(string)
		if d.Allow != c.allow || by != c.by || !strings.Contains(d.Reason, c.reason) {
			t.Errorf("%s: %+v by %q", c.decision, d, by)
		}
		if out := r.callTool(ctx, "approve", mgr, "manager", map[string]any{"id": id, "decision": "yes"}); !out.IsError {
			t.Errorf("a second answer to %s was taken", id)
		}
	}
}

// A question the manager does not answer in time goes to the person; the manager's own questions and those of a session without
// manager approvals never reach it.
func TestAnUnansweredQuestionGoesToThePerson(t *testing.T) {
	r, worker := approvalRig(t, 50*time.Millisecond)
	got := <-ask(r, worker, "./bin/app")
	if by := got[1].(string); by != "" {
		t.Errorf("an unanswered question was decided by %q", by)
	}
	if _, by := r.sw.DelegateApproval(context.Background(), perm.Request{Agent: r.sw.ManagerID(), Command: "./bin/app"}); by != "" {
		t.Errorf("the manager's own question went to the manager")
	}
	off := newRVRig(t, Config{MaxWorkers: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	off.sw.StartManager()
	if _, by := off.sw.DelegateApproval(context.Background(), perm.Request{Agent: "be-1", Command: "./bin/app"}); by != "" {
		t.Errorf("manager approvals off, decided by %q", by)
	}
}

// mailSaid reports whether the harness sent the agent mail holding text.
func mailSaid(r *rvRig, agent, text string) bool {
	for _, e := range r.log.OfType(events.TypeMailSend) {
		if strings.Contains(string(e.Data), `"to":"`+agent+`"`) && strings.Contains(string(e.Data), text) {
			return true
		}
	}
	return false
}

// The manager is told what a question reaches: the workspace and this machine, or, for an install into the project's virtual
// environment, the package index as well.
func TestTheApprovalSaysWhatTheRequestReaches(t *testing.T) {
	plain := approvalText("A1", perm.Request{Agent: "be-1", Command: "./bin/app", Why: "x"}, "T1", true, time.Minute)
	if !strings.Contains(plain, "reaches no network but this machine") || strings.Contains(plain, "package index") {
		t.Errorf("an ordinary question: %q", plain)
	}
	install := approvalText("A2", perm.Request{Agent: "be-1", Command: ".venv/bin/pip install pytest", Why: "x", PackageIndex: true}, "T1", true, time.Minute)
	if !strings.Contains(install, "it reaches the package index") || strings.Contains(install, "reaches no network") {
		t.Errorf("an install: %q", install)
	}
}
