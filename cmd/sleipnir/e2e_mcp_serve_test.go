package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// mcpClient speaks newline-delimited JSON-RPC to a `sleipnir mcp serve` child.
type mcpClient struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
}

// call sends one request and returns its result (or fails on an error answer).
func (c *mcpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if _, err := c.in.Write(append(msg, '\n')); err != nil {
		c.t.Fatal(err)
	}
	for c.out.Scan() {
		var resp struct {
			ID     int             `json:"id"`
			Result map[string]any  `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(c.out.Bytes(), &resp); err != nil {
			c.t.Fatalf("not JSON-RPC on stdout: %q", c.out.Text())
		}
		if resp.ID != c.id {
			continue
		}
		if len(resp.Error) > 0 {
			c.t.Fatalf("%s: error %s", method, resp.Error)
		}
		return resp.Result
	}
	c.t.Fatalf("%s: the server closed stdout: %v", method, c.out.Err())
	return nil
}

// tool calls a tool and returns its structured content; isError fails the test unless wantErr.
func (c *mcpClient) tool(name string, args map[string]any, wantErr bool) (map[string]any, string) {
	c.t.Helper()
	res := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	isErr, _ := res["isError"].(bool)
	text := ""
	if content, ok := res["content"].([]any); ok && len(content) > 0 {
		text, _ = content[0].(map[string]any)["text"].(string)
	}
	if isErr != wantErr {
		c.t.Fatalf("%s: isError %v, want %v: %s", name, isErr, wantErr, text)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	return sc, text
}

// Another harness drives Sleipnir through `sleipnir mcp serve`: it starts a goal, waits for it, and reads the result. The goal reaches
// the model through the child run, and the server refuses what its own flags do not allow.
func TestMCPServeRunsAGoalForAnotherHarness(t *testing.T) {
	m := startCapturingModel(t)
	w := newWorld(t, m.url())
	ctx, cancel := context.WithTimeout(context.Background(), e2eGuard)
	defer cancel()
	cmd := w.cmdContext(ctx, "mcp", "serve", "--mode", "accept-edits", "--cwd", w.project)
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
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	c := &mcpClient{t: t, in: in, out: sc}

	init := c.call("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if init["protocolVersion"] != "2025-03-26" {
		t.Errorf("initialize = %v", init)
	}
	tools := c.call("tools/list", map[string]any{})["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if got := strings.Join(names, ","); got != "sleipnir_start,sleipnir_status,sleipnir_wait,sleipnir_cancel,sleipnir_jobs" {
		t.Fatalf("tools = %s", got)
	}

	// The server's ceiling holds: a caller cannot ask for a wider mode, or a directory outside the server's.
	if _, text := c.tool("sleipnir_start", map[string]any{"goal": "x", "mode": "bypass"}, true); !strings.Contains(text, "more permissive") {
		t.Errorf("a wider mode was not refused: %s", text)
	}
	if _, text := c.tool("sleipnir_start", map[string]any{"goal": "x", "cwd": w.home}, true); !strings.Contains(text, "outside") {
		t.Errorf("a directory outside the root was not refused: %s", text)
	}
	if _, text := c.tool("sleipnir_start", map[string]any{"goal": "x", "modle": "typo"}, true); !strings.Contains(text, "unknown field") {
		t.Errorf("a misspelt argument was not refused: %s", text)
	}

	goal := "--help is not a flag here: write the greeting"
	started, _ := c.tool("sleipnir_start", map[string]any{"goal": goal}, false)
	job, _ := started["job"].(string)
	if job != "j1" || started["state"] != "running" {
		t.Fatalf("start = %v", started)
	}
	st, text := c.tool("sleipnir_wait", map[string]any{"job": job, "timeout_seconds": 120}, false)
	if st["state"] != "done" {
		t.Fatalf("the job did not end done: %s\nserver stderr:\n%s", text, stderr.String())
	}
	result, _ := st["result"].(map[string]any)
	if result["text"] != "ok" || result["session"] == "" {
		t.Errorf("result = %v", result)
	}
	if got := m.got(); !strings.Contains(got, goal) {
		t.Errorf("the model was sent %q, not the goal", got)
	}

	list, _ := c.tool("sleipnir_jobs", map[string]any{}, false)
	if jobs, _ := list["jobs"].([]any); len(jobs) != 1 || list["mode_ceiling"] != "accept-edits" {
		t.Errorf("jobs = %v", list)
	}
	if _, text := c.tool("sleipnir_cancel", map[string]any{"job": job}, true); !strings.Contains(text, "not running") {
		t.Errorf("cancelling an ended job: %s", text)
	}
	if _, text := c.tool("sleipnir_status", map[string]any{"job": "j9"}, true); !strings.Contains(text, "no job") {
		t.Errorf("an unknown job: %s", text)
	}

	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the server did not exit cleanly when its client left: %v\n%s", err, stderr.String())
	}
}
