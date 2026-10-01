package harness_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/harness"
)

// A request the endpoint did not answer is one the agent repeats, and it is not the policy's doing. It must
// not spend the request budget: on a marketplace that answers 429 now and then, runs ended as "budget" for requests
// that produced nothing, and a benchmark would have measured the endpoint's moods instead of the model.
func TestRequestsTheEndpointRefusedDoNotSpendTheRequestBudget(t *testing.T) {
	repo := newRepo(t)
	inner := startPolicy(t, func(c *mock.Call) mock.Reply {
		if turns(c) == 0 {
			return mock.Reply{Text: "look", ToolCalls: []mock.ToolCall{call("c1", "bash", map[string]any{"command": "echo hi"})}}
		}
		return mock.Reply{Text: "done"}
	})
	var refused atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refused.Add(1) <= 4 { // four 429s before the first request is answered
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit"}}`))
			return
		}
		proxyTo(t, inner.url, w, r)
	}))
	defer front.Close()
	h := &harness.Harness{NewProvider: injected(front.URL)}
	sp := spec(t, repo, "look, then say done")
	sp.Budget.Requests = 2 // two answered requests are all this run needs
	res, err := h.Run(context.Background(), sp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Claimed != "done" || res.Err != nil {
		t.Fatalf("four refused requests spent a budget of two that the run did not exceed: %+v", res)
	}
	if n := refused.Load(); n < 6 {
		t.Fatalf("the endpoint saw only %d requests: the refusals were never retried", n)
	}
}

// What the budget is for still holds: a policy that keeps asking is stopped at the budget.
func TestAnsweredRequestsStillSpendTheRequestBudget(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		return mock.Reply{Text: "again", ToolCalls: []mock.ToolCall{call("c"+string(rune('a'+turns(c))), "bash", map[string]any{"command": "echo hi"})}}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "loop forever")
	sp.Budget.Requests = 3
	res, err := h.Run(context.Background(), sp)
	if err != nil || res.Claimed != "budget" {
		t.Fatalf("%+v %v", res, err)
	}
	if n := len(pol.bodies()); n != 3 {
		t.Errorf("the endpoint answered %d requests, the budget was 3", n)
	}
}

// A rollout's spend is capped too: the budget of requests says nothing about dollars, and a single agent had no
// limit at all (only swarms had the session's default).
func TestSpendBudgetEndsTheRunAsABudgetOutcome(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		return mock.Reply{Text: "again", ToolCalls: []mock.ToolCall{call("c"+string(rune('a'+turns(c))), "bash", map[string]any{"command": "echo hi"})}}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "loop forever")
	sp.Budget.USD = 0.000001 // one request of the mock's prices costs more than this
	res, err := h.Run(context.Background(), sp)
	if err != nil {
		t.Fatalf("running out of money is the policy's doing, not an infrastructure error: %v", err)
	}
	if res.Claimed != "budget" {
		t.Fatalf("result: %+v", res)
	}
	if n := len(pol.bodies()); n < 1 || n > 3 {
		t.Errorf("a budget of a millionth of a dollar let %d requests through", n)
	}
}

// proxyTo forwards a request to another server with its body and answers with what came back.
func proxyTo(t *testing.T, base string, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base+r.URL.RequestURI(), r.Body)
	if err != nil {
		t.Error(err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// --mode single runs a swarm task as one agent: the baseline a swarm is compared with.
func TestASwarmTaskRunsAsOneAgentWhenAskedTo(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "done on my own"} })
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "fix it")
	sp.Task.Kind = rl.TaskSwarm
	sp.Task.Team = rl.Team{Mode: "swarm", Agents: 3}
	sp.Single = true
	res, err := h.Run(context.Background(), sp)
	if err != nil || res.Claimed != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	for _, e := range readEvents(t, sp.RunDir) {
		if e.Agent == "mgr" {
			t.Fatalf("a manager ran in a single-agent rollout: %s %s", e.Agent, e.Type)
		}
	}
}
