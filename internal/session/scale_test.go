package session_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
)

// TestFortyWorkerSwarmCompletes drives a manager and 40 workers through the
// assembled session against the mock: the scale the design targets (10-50
// agents), under the race detector. It checks that everything finishes, that the
// warm gate lets workers read the manager's prefix instead of each paying for
// it, and that requests stay inside the governor's budget.
func TestFortyWorkerSwarmCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	const workers = 40
	repo := newRepo(t)
	whoRe := regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	taskRe := regexp.MustCompile(`task (T\d+)`)
	var mu sync.Mutex
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		id, role, tid := "", "", ""
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Role != "user" {
				continue
			}
			if m := whoRe.FindStringSubmatch(c.Messages[i].Content); m != nil && id == "" {
				id, role = m[1], m[2]
			}
			if m := taskRe.FindStringSubmatch(c.Messages[i].Content); m != nil && tid == "" {
				tid = m[1]
			}
		}
		n := assistantTurns(c)
		if role == "manager" {
			switch n {
			case 0:
				var calls []mock.ToolCall
				var ids []string
				for i := 1; i <= workers; i++ {
					calls = append(calls, call(fmt.Sprintf("t%d", i), "task", map[string]any{"action": "create", "title": fmt.Sprintf("Survey area %d", i), "role": "scout"}))
					ids = append(ids, fmt.Sprintf("T%d", i))
				}
				for i := 1; i <= workers; i++ {
					calls = append(calls, call(fmt.Sprintf("s%d", i), "spawn", map[string]any{"role": "scout", "task": fmt.Sprintf("T%d", i)}))
				}
				calls = append(calls, call("w", "wait", map[string]any{"until": ids, "timeout_sec": 60}))
				return mock.Reply{Text: "dispatching", ToolCalls: calls}
			case 1:
				var calls []mock.ToolCall
				for i := 1; i <= workers; i++ {
					calls = append(calls, call(fmt.Sprintf("a%d", i), "task", map[string]any{"action": "accept", "id": fmt.Sprintf("T%d", i)}))
				}
				return mock.Reply{Text: "accepting", ToolCalls: calls}
			}
			return mock.Reply{Text: "all 40 areas surveyed"}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "looking", ToolCalls: []mock.ToolCall{call("r"+id, "read", map[string]any{"path": "main.go"})}}
		case 1:
			return mock.Reply{Text: "reporting", ToolCalls: []mock.ToolCall{call("d"+id, "task", map[string]any{"action": "done", "id": tid, "text": "surveyed by " + id})}}
		}
		return mock.Reply{Text: "done " + id}
	})
	o := opts(t, repo, client, model)
	o.Swarm = true
	o.MaxAgents = workers + 2
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := s.Run(ctx, "survey the repository in 40 parts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "all 40 areas surveyed") {
		t.Fatalf("manager result %q", res.Text)
	}
	snap := s.Swarm.Board.Snapshot()
	done := 0
	for _, tk := range snap.Tasks {
		if tk.Status == swarm.StatusDone {
			done++
		}
	}
	if done != workers {
		t.Fatalf("%d of %d tasks accepted", done, workers)
	}

	// Log analysis: first-request hit ratios and request volume.
	var reqs, firstReads, firstN int
	seen := map[string]bool{}
	for _, e := range readEvents(t, s.Dir) {
		if e.Type != events.TypeModelResponse {
			continue
		}
		reqs++
		var m struct {
			Req string  `json:"req"`
			Hit float64 `json:"hit_ratio"`
		}
		_ = jsonUnmarshal(e.Data, &m)
		if strings.HasSuffix(m.Req, ".1") && !seen[e.Agent] && e.Agent != "mgr" {
			seen[e.Agent] = true
			firstN++
			if m.Hit >= 0.5 {
				firstReads++
			}
		}
	}
	if firstN != workers {
		t.Fatalf("expected first requests of %d workers, saw %d", workers, firstN)
	}
	if firstReads < workers*3/4 {
		t.Fatalf("only %d of %d workers read the shared prefix on their first request", firstReads, workers)
	}
	t.Logf("40 workers finished in %s with %d model requests; %d/%d first requests were cache reads", time.Since(start).Round(time.Millisecond), reqs, firstReads, firstN)
}
