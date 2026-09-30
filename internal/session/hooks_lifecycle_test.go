package session_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
)

// readPayloads reads the JSON objects a hook command appended, one per line, to a file.
func readPayloads(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("hook payload is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// A swarm's manager ends with Stop, its workers with SubagentStop (as in Claude Code:
// a Stop hook is for the agent the person talks to); a SubagentStart hook may add to
// a new worker's first task message; and SessionEnd says why the session ended.
func TestSwarmHooksSubagentStartStopAndTheSessionEndReason(t *testing.T) {
	repo := newRepo(t)
	dir := t.TempDir()
	starts, stops, subStops, ends := filepath.Join(dir, "starts"), filepath.Join(dir, "stops"), filepath.Join(dir, "substops"), filepath.Join(dir, "ends")
	var mu sync.Mutex
	workerFirstMessage := ""
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		id, role, tid := whoIs(c)
		n := assistantTurns(c)
		if role == "manager" {
			switch n {
			case 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("m1", "task", map[string]any{"action": "create", "title": "Add notes.txt", "role": "backend", "files": []string{"notes.txt"}}),
					call("m2", "spawn", map[string]any{"role": "backend", "task": "T1"}),
				}}
			case 1:
				return mock.Reply{Text: "waiting", ToolCalls: []mock.ToolCall{call("m3", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20})}}
			case 2:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("m4", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "all done"}
		}
		mu.Lock()
		if n == 0 && workerFirstMessage == "" {
			workerFirstMessage = c.LastUser()
		}
		mu.Unlock()
		switch n {
		case 0:
			return mock.Reply{Text: "on it", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "notes.txt", "content": "by " + id + "\n"})}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("w2", "task", map[string]any{"action": "done", "id": tid, "text": "added notes.txt"})}}
		}
		return mock.Reply{Text: "summary from " + id}
	})
	o := opts(t, repo, client, model)
	o.Swarm = true
	o.Config = hookConfig(t, map[string]string{
		// Only JSON carries context on this event (plain output is not injected into a prompt).
		"SubagentStart": "cat >> " + starts + "; echo >> " + starts + `; echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"WORKER-CONTEXT: read docs/style.md first"}}'`,
		"Stop":          "cat >> " + stops + "; echo >> " + stops,
		"SubagentStop":  "cat >> " + subStops + "; echo >> " + subStops,
		"SessionEnd":    "cat >> " + ends + "; echo >> " + ends,
	})
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(context.Background(), "add a notes.txt file")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "all done" {
		t.Fatalf("manager result %q", res.Text)
	}
	s.SetEndReason(session.EndCompleted)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// SubagentStart: once, for the new worker, before it read its task; its output is in
	// the task message and in no cached layer.
	st := readPayloads(t, starts)
	if len(st) != 1 || st[0]["hook_event_name"] != "SubagentStart" || st[0]["agent_id"] != "be-1" || st[0]["agent_type"] != "backend" || st[0]["task"] != "T1" {
		t.Fatalf("SubagentStart payloads: %v", st)
	}
	mu.Lock()
	first := workerFirstMessage
	mu.Unlock()
	if !strings.Contains(first, "WORKER-CONTEXT: read docs/style.md first") || !strings.Contains(first, "[context from your hooks]") {
		t.Errorf("the hook's context did not reach the worker's first task message:\n%s", first)
	}
	if strings.Contains(s.Shared.Text(), "WORKER-CONTEXT") {
		t.Error("hook output leaked into the shared layer")
	}

	// Stop for the manager only; SubagentStop for the worker, with what it said last.
	for _, p := range readPayloads(t, stops) {
		if p["agent_id"] != "mgr" || p["hook_event_name"] != "Stop" {
			t.Errorf("a Stop hook ran for %v (%v): only the manager's end is a Stop", p["agent_id"], p["hook_event_name"])
		}
	}
	if n := len(readPayloads(t, stops)); n == 0 {
		t.Error("the manager's Stop hook never ran")
	}
	sub := readPayloads(t, subStops)
	if len(sub) == 0 {
		t.Fatal("the worker's SubagentStop hook never ran")
	}
	for _, p := range sub {
		if p["agent_id"] != "be-1" || p["agent_type"] != "backend" || p["hook_event_name"] != "SubagentStop" {
			t.Errorf("SubagentStop payload: %v", p)
		}
	}
	if msg, _ := sub[len(sub)-1]["last_assistant_message"].(string); !strings.Contains(msg, "summary from be-1") {
		t.Errorf("SubagentStop lacks what the worker said last: %v", sub[len(sub)-1])
	}
	// SessionEnd carries the reason the command gave.
	en := readPayloads(t, ends)
	if len(en) != 1 || en[0]["reason"] != "completed" {
		t.Errorf("SessionEnd payloads: %v", en)
	}
	for _, e := range readEvents(t, s.Dir) {
		if e.Type == "session.end" && !strings.Contains(string(e.Data), `"reason":"completed"`) {
			t.Errorf("the session.end event lacks the reason: %s", e.Data)
		}
	}
}

// The default reason is "other", and the mailman (a service of the harness, not a
// worker) never fires a SubagentStop.
func TestSessionEndReasonDefaultsToOtherAndTheMailmanFiresNoSubagentHooks(t *testing.T) {
	repo := newRepo(t)
	dir := t.TempDir()
	ends, subStops := filepath.Join(dir, "ends"), filepath.Join(dir, "substops")
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "nothing to do"} })
	yes := true
	o := opts(t, repo, client, model)
	o.Swarm, o.MaxAgents, o.Mailman = true, 4, &yes
	o.Config = hookConfig(t, map[string]string{
		"SessionEnd":   "cat >> " + ends + "; echo >> " + ends,
		"SubagentStop": "cat >> " + subStops + "; echo >> " + subStops,
	})
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "say when there is nothing to do"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if en := readPayloads(t, ends); len(en) != 1 || en[0]["reason"] != "other" {
		t.Errorf("SessionEnd payloads: %v", en)
	}
	for _, p := range readPayloads(t, subStops) {
		if p["agent_type"] == swarm.MailmanRoleName {
			t.Errorf("the mailman fired a SubagentStop hook: %v", p)
		}
	}
}

// A compaction the agent decides on itself fires PreCompact and PostCompact with the
// trigger "auto" (a person's /compact says "manual"); a hook cannot stop it.
func TestAutomaticCompactionFiresPreAndPostCompactHooks(t *testing.T) {
	repo := newRepo(t)
	big := strings.Repeat("// a long comment that only takes up room in the prompt\n", 700) // ~40 KB
	if err := os.WriteFile(filepath.Join(repo, "big.go"), []byte("package main\n\n"+big), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pre, post := filepath.Join(dir, "pre"), filepath.Join(dir, "post")
	var mu sync.Mutex
	requests := 0
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock() // a count of requests: compaction folds the thread, so its length is no clock
		requests++
		n := requests
		mu.Unlock()
		if n < 9 {
			return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{call("r"+itoa(n), "read", map[string]any{"path": "big.go", "offset": 1 + n, "limit": 1500})}}
		}
		return mock.Reply{Text: "done"}
	})
	o := opts(t, repo, client, model)
	o.ContextWindow = 24000 // small: the reads push the prompt over 85% of it, which compacts without a model
	o.Config = hookConfig(t, map[string]string{
		"PreCompact":  "cat >> " + pre + "; echo >> " + pre + "; echo 'refuse' >&2; exit 2",
		"PostCompact": "cat >> " + post + "; echo >> " + post,
	})
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "read big.go"); err != nil {
		t.Fatal(err)
	}
	pres, posts := readPayloads(t, pre), readPayloads(t, post)
	if len(pres) == 0 {
		t.Fatalf("no automatic compaction ran (or its PreCompact hook was never asked): %d PostCompact", len(posts))
	}
	for _, p := range append(append([]map[string]any{}, pres...), posts...) {
		if p["trigger"] != "auto" || p["agent_id"] != "main" || p["reason"] == "" {
			t.Errorf("compaction hook payload: %v", p)
		}
	}
	if len(posts) != len(pres) {
		t.Errorf("%d PreCompact but %d PostCompact: the hook that exited 2 must not have stopped the compaction", len(pres), len(posts))
	}
}
