package swarm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
)

func TestManagerWaitReturnsActionableErrorWhenNoWorkerCanProgress(t *testing.T) {
	for _, state := range []string{"todo", "doing", "blocked", "review"} {
		t.Run(state, func(t *testing.T) {
			r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
			if _, err := r.sw.StartManager(); err != nil {
				t.Fatal(err)
			}
			task := agreementTask(t, r.sw.Board, "Shared contract")
			if state != "todo" {
				if err := r.sw.Board.Assign("mgr", "sc-1", task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if state == "blocked" {
				if err := r.sw.Board.Block("sc-1", task.ID, "needs manager decision"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "review" {
				if err := r.sw.Board.submitAgreementAt("sc-1", task.ID, 0, "proposal", "", "Use the shared interface."); err != nil {
					t.Fatal(err)
				}
			}
			before := r.sw.Board.Snapshot()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			in := map[string]any{"timeout_sec": 600}
			if state != "review" {
				in["until"] = []string{task.ID}
			}
			res := r.callTool(ctx, "wait", "mgr", "manager", in)
			if !res.IsError || !strings.Contains(res.Text, "no workers are running") || !strings.Contains(res.Text, task.ID) || !strings.Contains(res.Text, "spawn") || !strings.Contains(res.Text, "accept or reject") {
				t.Fatalf("expected manager actions instead of sleeping: %+v", res)
			}
			if r.sw.Board.Snapshot() != before {
				t.Fatal("waiting changed task state")
			}
		})
	}
}

func TestManagerWaitPreservesSettledTargetsAndWorkerWaits(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "ok"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	if why := r.sw.stalledWaitReason("mgr"); why != "" {
		t.Fatalf("empty board must allow waiting for future work: %s", why)
	}
	plan := agreementTask(t, r.sw.Board, "Plan")
	acceptAgreement(t, r.sw.Board, plan, "sc-1", "Use the existing interface.")
	agreementTask(t, r.sw.Board, "Unstarted implementation", plan.ID)
	res := r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"until": []string{plan.ID}})
	if res.IsError || !strings.Contains(res.Text, "all awaited tasks settled") {
		t.Fatalf("settled targets were hidden by other unfinished work: %+v", res)
	}
	if why := r.sw.stalledWaitReason("sc-1"); why != "" {
		t.Fatalf("worker waits must remain available for manager mail: %s", why)
	}
}

func TestManagerWaitAllowsAnActiveWorker(t *testing.T) {
	gate := make(chan struct{})
	r := newRVRig(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
		rvBlock(ctx, gate)
		return rvReply{Text: "ok"}
	})
	t.Cleanup(func() { close(gate) })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: "scout", Title: "Plan", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	if why := r.sw.stalledWaitReason("mgr"); why != "" {
		t.Fatalf("running worker %s was mistaken for a stall: %s", id, why)
	}
}

func TestRepeatedImpossibleManagerWaitsStop(t *testing.T) {
	r := newRVRig(t, Config{}, func(context.Context, *rvCall) rvReply {
		return rvReply{Tools: []rvToolCall{{"wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 600}}}}
	})
	agreementTask(t, r.sw.Board, "Unstarted plan")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.sw.RunManager(ctx, "Start the planning task")
	if !errors.Is(err, agent.ErrStuck) {
		t.Fatalf("repeated impossible waits were not bounded: %v", err)
	}
	if !r.prov.sawEver("Use spawn") {
		t.Fatal("the model never received the way out")
	}
}
