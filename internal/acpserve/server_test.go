package acpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary is also a fake `sleipnir run --json`: with fakeEnv set it
// writes one scripted stream and exits.
const fakeEnv = "ACPSERVE_FAKE_RUN"

func TestMain(m *testing.M) {
	if v := os.Getenv(fakeEnv); v != "" {
		fakeRun(v)
		return
	}
	os.Exit(m.Run())
}

// fakeRun plays one scripted run for the ACP server under test.
func fakeRun(kind string) {
	goal, _ := io.ReadAll(os.Stdin)
	if path := os.Getenv("ACPSERVE_FAKE_ARGV"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(os.Args[1:], "\n")), 0o600)
	}
	if path := os.Getenv("ACPSERVE_FAKE_GOAL"); path != "" {
		_ = os.WriteFile(path, goal, 0o600)
	}
	enc := json.NewEncoder(os.Stdout)
	emit := func(m map[string]any) { _ = enc.Encode(m) }
	switch kind {
	case "ok":
		emit(map[string]any{"type": "notice", "agent": "main", "level": "info", "message": "starting"})
		emit(map[string]any{"type": "text", "agent": "main", "text": "Reading "})
		emit(map[string]any{"type": "text", "agent": "main", "text": "the code."})
		emit(map[string]any{"type": "tool_start", "agent": "main", "id": "t1", "name": "bash", "input": map[string]any{"command": "echo hi"}})
		emit(map[string]any{"type": "tool_end", "agent": "main", "id": "t1", "name": "bash", "output": "hi"})
		emit(map[string]any{"type": "response", "agent": "main", "usage": map[string]any{"input_tokens": 100, "output_tokens": 20}, "stop": "end_turn"})
		emit(map[string]any{"type": "result", "text": "did: " + string(goal), "steps": 3, "session": "sess-1", "cost_usd": 0.01, "stop": "end_turn"})
	case "fail":
		fmt.Fprintln(os.Stderr, "sleipnir: no model configured")
		os.Exit(2)
	case "slow":
		emit(map[string]any{"type": "text", "agent": "main", "text": "working"})
		time.Sleep(time.Minute)
	}
}

// testServer builds a server whose turns run the fake, in an isolated home.
func testServer(t *testing.T, kind string, mutate func(*Config)) (*Server, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	var ids int
	cfg := Config{
		Exe: exe, Roots: []string{dir}, Dir: dir, Version: "test",
		Home: home, StateDir: filepath.Join(dir, "state"),
		Env: append(os.Environ(), fakeEnv+"="+kind),
		Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		NewID: func() string {
			ids++
			return fmt.Sprintf("acp_test_%d", ids)
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// setFakeEnv adds an environment variable to the turns of a server. It must
// be called before a prompt starts.
func setFakeEnv(s *Server, key, value string) {
	s.cfg.Env = append(s.cfg.Env, key+"="+value)
}

// acpClient is a JSON-RPC client that keeps the notifications it saw.
type acpClient struct {
	t       *testing.T
	write   func([]byte) error
	scan    *bufio.Scanner
	writeMu sync.Mutex
	id      int
	updates []map[string]any
}

// call sends a request and waits for its response, collecting notifications.
func (c *acpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	c.writeMu.Lock()
	err := c.write(append(b, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		c.t.Fatal(err)
	}
	for c.scan.Scan() {
		var m map[string]any
		if err := json.Unmarshal(c.scan.Bytes(), &m); err != nil {
			c.t.Fatalf("not JSON: %q", c.scan.Text())
		}
		if m["method"] == "session/update" {
			p, _ := m["params"].(map[string]any)
			if u, ok := p["update"].(map[string]any); ok {
				c.updates = append(c.updates, u)
			}
			continue
		}
		if m["id"] == float64(c.id) {
			return m
		}
	}
	c.t.Fatalf("stdout ended: %v", c.scan.Err())
	return nil
}

// notify sends a notification (no response expected).
func (c *acpClient) notify(method string, params any) {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	c.writeMu.Lock()
	err := c.write(append(b, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		c.t.Fatal(err)
	}
}

// resultOf returns a response's result or fails.
func resultOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	if e, ok := m["error"]; ok {
		t.Fatalf("rpc error: %v", e)
	}
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", m)
	}
	return r
}

// connect runs Serve over pipes and returns a client and a close function.
func connect(t *testing.T, s *Server) (*acpClient, func()) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), inR, outW); outW.Close() }()
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	c := &acpClient{t: t, write: func(b []byte) error { _, err := inW.Write(b); return err }, scan: sc}
	closeFn := func() {
		inW.Close()
		go io.Copy(io.Discard, outR)
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("Serve did not return")
		}
	}
	return c, closeFn
}

// setup initializes and creates a session, returning its id.
func setup(t *testing.T, c *acpClient, cwd string) string {
	t.Helper()
	init := resultOf(t, c.call("initialize", map[string]any{"protocolVersion": 2, "clientCapabilities": map[string]any{"session": map[string]any{"notices": map[string]any{}}}}))
	if init["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatalf("protocolVersion = %v, want %d", init["protocolVersion"], ProtocolVersion)
	}
	res := resultOf(t, c.call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}))
	id, _ := res["sessionId"].(string)
	if id == "" {
		t.Fatalf("no sessionId: %v", res)
	}
	return id
}

// kinds returns the sessionUpdate names the client has seen.
func (c *acpClient) kinds() []string {
	kinds := make([]string, 0, len(c.updates))
	for _, u := range c.updates {
		k, _ := u["sessionUpdate"].(string)
		kinds = append(kinds, k)
	}
	return kinds
}

func has(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestInitializeAdvertisesBaselineAndSessionMethods(t *testing.T) {
	s, _ := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	res := resultOf(t, c.call("initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]any{"name": "test", "version": "1"}}))
	caps, _ := res["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != true {
		t.Fatalf("loadSession not advertised: %v", caps)
	}
	sc, _ := caps["sessionCapabilities"].(map[string]any)
	for _, k := range []string{"list", "resume", "close"} {
		if _, ok := sc[k]; !ok {
			t.Errorf("sessionCapabilities missing %s: %v", k, sc)
		}
	}
	info, _ := res["agentInfo"].(map[string]any)
	if info["name"] != "sleipnir" {
		t.Fatalf("agentInfo = %v", info)
	}
}

func TestRequestBeforeInitializeIsRefused(t *testing.T) {
	s, _ := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	m := c.call("session/list", map[string]any{})
	e, _ := m["error"].(map[string]any)
	if e == nil || e["code"] != float64(codeInvalidRequest) {
		t.Fatalf("want initialize-required error, got %v", m)
	}
}

func TestSessionNewExposesModelModeAndTeamOptions(t *testing.T) {
	s, _ := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, s.cfg.Dir)
	res := resultOf(t, c.call("session/new", map[string]any{"cwd": s.cfg.Dir}))
	if res["sessionId"] == id {
		t.Fatal("new session reused an id")
	}
	opts, _ := res["configOptions"].([]any)
	byID := map[string]map[string]any{}
	for _, o := range opts {
		m, _ := o.(map[string]any)
		byID[m["id"].(string)] = m
	}
	for _, id := range []string{"model", "mode", "swarm", "effort"} {
		if byID[id] == nil {
			t.Errorf("missing config option %s in %v", id, opts)
		}
	}
	modes, _ := res["modes"].(map[string]any)
	if modes["currentModeId"] == "" {
		t.Fatalf("no current mode: %v", modes)
	}
}

func TestPromptStreamsUpdatesAndReturnsStopReason(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, dir)
	goalPath := filepath.Join(dir, "goal.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_GOAL", goalPath)
	setFakeEnv(s, "ACPSERVE_FAKE_ARGV", filepath.Join(dir, "argv.txt"))
	c.updates = nil
	res := resultOf(t, c.call("session/prompt", map[string]any{
		"sessionId": id,
		"prompt":    []any{map[string]any{"type": "text", "text": "do the thing"}},
	}))
	if res["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %v, want end_turn", res["stopReason"])
	}
	for _, k := range []string{"agent_message_chunk", "tool_call", "tool_call_update", "usage_update"} {
		if !has(c.kinds(), k) {
			t.Errorf("missing %s update in %v", k, c.kinds())
		}
	}
	goal, err := os.ReadFile(goalPath)
	if err != nil || !strings.Contains(string(goal), "do the thing") {
		t.Fatalf("goal = %q, err %v", goal, err)
	}
	argv, err := os.ReadFile(filepath.Join(dir, "argv.txt"))
	if err != nil || !strings.Contains(string(argv), "--json") || !strings.Contains(string(argv), "--delegated") {
		t.Fatalf("argv = %q, err %v", argv, err)
	}
}

func TestPromptWithResourcesAndSystemPromptReachesTheGoal(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	goalPath := filepath.Join(dir, "goal.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_GOAL", goalPath)
	c.call("initialize", map[string]any{"protocolVersion": 1})
	res := resultOf(t, c.call("session/new", map[string]any{
		"cwd": dir,
		"systemPrompt": map[string]any{
			"mode": "append", "content": []any{map[string]any{"type": "text", "text": "Be brief."}},
		},
	}))
	id := res["sessionId"].(string)
	resultOf(t, c.call("session/prompt", map[string]any{
		"sessionId": id,
		"prompt": []any{
			map[string]any{"type": "text", "text": "check this"},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///tmp/a.txt", "text": "hello"}},
		},
	}))
	goal, _ := os.ReadFile(goalPath)
	text := string(goal)
	for _, want := range []string{"Be brief.", "check this", "file:///tmp/a.txt", "hello"} {
		if !strings.Contains(text, want) {
			t.Errorf("goal missing %q: %s", want, text)
		}
	}
}

func TestCancelEndsATurnWithCancelledStopReason(t *testing.T) {
	s, dir := testServer(t, "slow", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	init := resultOf(t, c.call("initialize", map[string]any{"protocolVersion": 1}))
	if init["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatalf("init = %v", init)
	}
	id := resultOf(t, c.call("session/new", map[string]any{"cwd": dir}))["sessionId"].(string)
	done := make(chan map[string]any, 1)
	go func() {
		done <- c.call("session/prompt", map[string]any{
			"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "sleep"}},
		})
	}()
	time.Sleep(250 * time.Millisecond)
	c.notify("session/cancel", map[string]any{"sessionId": id})
	select {
	case res := <-done:
		got, _ := res["result"].(map[string]any)
		if got["stopReason"] != "cancelled" {
			t.Fatalf("stopReason = %v, want cancelled (%v)", got["stopReason"], res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("prompt did not end after cancel")
	}
}

func TestSessionLoadReplaysAndResumesTheSleipnirSession(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, dir)
	argvPath := filepath.Join(dir, "argv2.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_ARGV", argvPath)
	c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "first"}}})
	listed := resultOf(t, c.call("session/list", map[string]any{"cwd": dir}))
	rows, _ := listed["sessions"].([]any)
	if len(rows) == 0 {
		t.Fatalf("session/list empty: %v", listed)
	}
	row, _ := rows[0].(map[string]any)
	if row["sessionId"] != id {
		t.Fatalf("listed %v, want %s", row, id)
	}
	c.updates = nil
	loaded := resultOf(t, c.call("session/load", map[string]any{"sessionId": id, "cwd": dir, "mcpServers": []any{}}))
	if loaded["sessionId"] != id {
		t.Fatalf("load result = %v", loaded)
	}
	if !has(c.kinds(), "agent_message_chunk") {
		t.Fatalf("load did not replay messages: %v", c.kinds())
	}
	c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "second"}}})
	argv, _ := os.ReadFile(argvPath)
	if !strings.Contains(string(argv), "--resume") || !strings.Contains(string(argv), "sess-1") {
		t.Fatalf("second turn did not resume: %s", argv)
	}
}

func TestMCPServersBecomeAPrivateConfigFile(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	init := resultOf(t, c.call("initialize", map[string]any{"protocolVersion": 1}))
	_ = init
	argvPath := filepath.Join(dir, "argv3.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_ARGV", argvPath)
	res := resultOf(t, c.call("session/new", map[string]any{
		"cwd": dir,
		"mcpServers": []any{
			map[string]any{"name": "tools", "command": "/bin/echo", "args": []any{"stdio"}, "env": []any{map[string]any{"name": "TOKEN", "value": "secret"}}},
			map[string]any{"name": "remote", "type": "http", "url": "http://127.0.0.1:8080/mcp", "headers": []any{map[string]any{"name": "Authorization", "value": "Bearer x"}}},
		},
	}))
	id := res["sessionId"].(string)
	c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "go"}}})
	argv, _ := os.ReadFile(argvPath)
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	idx := -1
	for i, line := range lines {
		if line == "--mcp-config" && i+1 < len(lines) {
			idx = i + 1
		}
	}
	if idx < 0 {
		t.Fatalf("no --mcp-config in argv: %s", argv)
	}
	body, err := os.ReadFile(lines[idx])
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers["tools"] == nil || servers["remote"] == nil {
		t.Fatalf("translated config = %s", body)
	}
	remote, _ := servers["remote"].(map[string]any)
	if remote["allow_private"] != true {
		t.Fatalf("loopback server not marked private: %v", remote)
	}
}

func TestModeCeilingCannotBeWidened(t *testing.T) {
	s, _ := testServer(t, "ok", func(c *Config) { c.Mode = "default" })
	c, closeFn := connect(t, s)
	defer closeFn()
	init := resultOf(t, c.call("initialize", map[string]any{"protocolVersion": 1}))
	if init["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatal(init)
	}
	res := resultOf(t, c.call("session/new", map[string]any{"cwd": s.cfg.Dir}))
	id := res["sessionId"].(string)
	bad := c.call("session/set_config_option", map[string]any{"sessionId": id, "configId": "mode", "value": "yolo"})
	if e, _ := bad["error"].(map[string]any); e == nil {
		t.Fatalf("yolo was accepted under a default ceiling: %v", bad)
	}
	good := resultOf(t, c.call("session/set_config_option", map[string]any{"sessionId": id, "configId": "mode", "value": "plan"}))
	opts, _ := good["configOptions"].([]any)
	for _, o := range opts {
		m, _ := o.(map[string]any)
		if m["id"] == "mode" && m["currentValue"] != "plan" {
			t.Fatalf("mode not changed: %v", m)
		}
	}
}

func TestUnknownSessionAndInvalidPromptAreErrors(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	setup(t, c, dir)
	unknown := c.call("session/prompt", map[string]any{"sessionId": "acp_nope", "prompt": []any{map[string]any{"type": "text", "text": "hi"}}})
	if e, _ := unknown["error"].(map[string]any); e == nil || e["code"] != float64(codeNotFound) {
		t.Fatalf("want not-found error, got %v", unknown)
	}
	bad := c.call("session/prompt", map[string]any{"sessionId": "acp_test_1", "prompt": []any{}})
	if e, _ := bad["error"].(map[string]any); e == nil || e["code"] != float64(codeInvalidParams) {
		t.Fatalf("want invalid-params error, got %v", bad)
	}
}

func TestFailedRunReportsRefusalAndStderr(t *testing.T) {
	s, dir := testServer(t, "fail", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, dir)
	c.updates = nil
	res := resultOf(t, c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "go"}}}))
	if res["stopReason"] != "end_turn" && res["stopReason"] != "refusal" {
		t.Fatalf("stopReason = %v", res["stopReason"])
	}
	if !has(c.kinds(), "agent_message_chunk") {
		t.Fatalf("failure not reported: %v", c.kinds())
	}
}
