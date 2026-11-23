package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// acpClient speaks ACP's newline-delimited JSON-RPC to an `sleipnir acp serve`
// child and keeps the session updates it sees.
type acpClient struct {
	t       *testing.T
	in      io.WriteCloser
	out     *bufio.Scanner
	stderr  *strings.Builder
	id      int
	updates []map[string]any
}

// call sends a request and waits for its response.
func (c *acpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if _, err := c.in.Write(append(msg, '\n')); err != nil {
		c.t.Fatal(err)
	}
	for c.out.Scan() {
		var resp map[string]any
		if err := json.Unmarshal(c.out.Bytes(), &resp); err != nil {
			c.t.Fatalf("not JSON-RPC on stdout: %q", c.out.Text())
		}
		if resp["method"] == "session/update" {
			p, _ := resp["params"].(map[string]any)
			if u, ok := p["update"].(map[string]any); ok {
				c.updates = append(c.updates, u)
			}
			continue
		}
		if resp["id"] == float64(c.id) {
			return resp
		}
	}
	c.t.Fatalf("%s: the server closed stdout: %v\nserver stderr:\n%s", method, c.out.Err(), c.stderr.String())
	return nil
}

// result returns a successful response's result.
func (c *acpClient) result(method string, params any) map[string]any {
	c.t.Helper()
	m := c.call(method, params)
	if e, ok := m["error"]; ok {
		c.t.Fatalf("%s: %v", method, e)
	}
	r, _ := m["result"].(map[string]any)
	return r
}

// Another harness drives a real Sleipnir agent through `sleipnir acp serve`:
// the client sets up a session, sends a prompt and follows the streamed answer.
func TestACPServeRunsAGoalForAnotherHarness(t *testing.T) {
	m := startCapturingModel(t)
	w := newWorld(t, m.url())
	ctx, cancel := context.WithTimeout(context.Background(), e2eGuard)
	defer cancel()
	cmd := w.cmdContext(ctx, "acp", "serve", "--mode", "accept-edits", "--cwd", w.project)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	c := &acpClient{t: t, in: in, out: sc, stderr: &stderr}

	init := c.result("initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if init["protocolVersion"] != float64(1) {
		t.Fatalf("initialize = %v", init)
	}
	setup := c.result("session/new", map[string]any{"cwd": w.project, "mcpServers": []any{}})
	id, _ := setup["sessionId"].(string)
	if id == "" {
		t.Fatalf("session/new = %v", setup)
	}
	opts, _ := setup["configOptions"].([]any)
	if len(opts) == 0 {
		t.Fatalf("no config options: %v", setup)
	}
	// The server's ceiling holds: a client cannot widen a mode or leave its roots.
	bad := c.call("session/set_config_option", map[string]any{"sessionId": id, "configId": "mode", "value": "yolo"})
	if bad["error"] == nil {
		t.Fatalf("yolo was accepted under an accept-edits ceiling: %v", bad)
	}
	bad = c.call("session/new", map[string]any{"cwd": w.home})
	if bad["error"] == nil {
		t.Fatalf("a directory outside the root was accepted: %v", bad)
	}

	goal := "--help is not a flag here: write the greeting"
	turn := c.result("session/prompt", map[string]any{
		"sessionId": id,
		"prompt":    []any{map[string]any{"type": "text", "text": goal}},
	})
	if turn["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %v", turn["stopReason"])
	}
	var chunks int
	for _, u := range c.updates {
		if u["sessionUpdate"] == "agent_message_chunk" {
			chunks++
		}
	}
	if chunks == 0 {
		t.Fatalf("no streamed message chunks in %v", c.updates)
	}
	if got := m.got(); !strings.Contains(got, goal) {
		t.Errorf("the model was sent %q, not the goal", got)
	}

	listed := c.result("session/list", map[string]any{"cwd": w.project})
	if rows, _ := listed["sessions"].([]any); len(rows) == 0 {
		t.Errorf("session/list is empty: %v", listed)
	}
	c.result("session/close", map[string]any{"sessionId": id})

	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the server did not exit cleanly when its client left: %v\n%s", err, stderr.String())
	}
}

func TestACPServeHelpAndUnknownCommand(t *testing.T) {
	if err := cmdACP(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdACP(context.Background(), []string{"nope"}); err == nil {
		t.Fatal("an unknown acp command was accepted")
	}
}
