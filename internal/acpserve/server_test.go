package acpserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
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
	if path := os.Getenv("ACPSERVE_FAKE_CWD"); path != "" {
		if here, err := os.Getwd(); err == nil {
			_ = os.WriteFile(path, []byte(here), 0o600)
		}
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
	case "toolonly":
		// A response and a tool call before any text: message counters must
		// still be initialized.
		emit(map[string]any{"type": "tool_start", "agent": "main", "id": "t9", "name": "bash", "input": map[string]any{"command": "echo hi"}})
		emit(map[string]any{"type": "tool_end", "agent": "main", "id": "t9", "name": "bash", "output": "hi"})
		emit(map[string]any{"type": "response", "agent": "main", "usage": map[string]any{"input_tokens": 10}, "stop": "end_turn"})
		emit(map[string]any{"type": "result", "text": "done", "session": "sess-tool", "stop": "end_turn"})
	case "graceful":
		// A cancelled child that still writes its final result: a load must
		// read the record only after that result has been saved. Register the
		// handler before announcing readiness so a fast cancel cannot kill us
		// while the default SIGTERM behaviour is still in force.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		if path := os.Getenv("ACPSERVE_FAKE_READY"); path != "" {
			_ = os.WriteFile(path, []byte("ready"), 0o600)
		}
		<-sig
		emit(map[string]any{"type": "text", "agent": "main", "text": "finishing"})
		emit(map[string]any{"type": "result", "text": "done", "session": "sess-graceful", "stop": "end_turn"})
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

// waitSignal waits for a test hook with a hang guard, not a timing assertion.
func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// waitFile waits for a file a child is expected to write, with a hang guard.
func waitFile(t *testing.T, path, what string) {
	t.Helper()
	limit := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-limit:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(10 * time.Millisecond):
		}
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

func TestTwoPromptsOnOneSession(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, dir)
	for i := 0; i < 2; i++ {
		res := resultOf(t, c.call("session/prompt", map[string]any{
			"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "again"}},
		}))
		if res["stopReason"] != "end_turn" {
			t.Fatalf("prompt %d: stopReason = %v", i+1, res["stopReason"])
		}
	}
}

func TestResponseOnlyAndResetFirstStreamsDoNotPanic(t *testing.T) {
	s, dir := testServer(t, "toolonly", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, dir)
	c.updates = nil
	res := resultOf(t, c.call("session/prompt", map[string]any{
		"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "tools first"}},
	}))
	if res["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %v", res["stopReason"])
	}
	if !has(c.kinds(), "tool_call") || !has(c.kinds(), "tool_call_update") {
		t.Fatalf("tool updates missing: %v", c.kinds())
	}
}

func TestCancelBeforeStartSettlesWithoutLaunching(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	sess := newSession(s, dir, dir, "", "")
	j := &Job{
		sess: sess, srv: s, goal: "never started", done: make(chan struct{}), state: turnRunning,
		gen: map[string]int{}, tools: map[string]string{}, streamed: map[string]bool{},
	}
	j.cancel()
	if err := j.start(); err != nil {
		t.Fatal(err)
	}
	<-j.done
	if j.cmd != nil {
		t.Fatal("a cancelled turn launched a child")
	}
	if got := j.stopReasonValue(); got != "cancelled" {
		t.Fatalf("stopReason = %q, want cancelled", got)
	}
}

func TestFailureUpdateIncludesStderrDiagnostic(t *testing.T) {
	s, dir := testServer(t, "fail", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, dir)
	c.updates = nil
	c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "go"}}})
	var text string
	for _, u := range c.updates {
		content, _ := u["content"].(map[string]any)
		if v, ok := content["text"].(string); ok && strings.Contains(v, "no model configured") {
			text = v
		}
	}
	if text == "" {
		t.Fatalf("stderr diagnostic not reported: %v", c.updates)
	}
}

func TestSystemPromptSurvivesLoad(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	c.call("initialize", map[string]any{"protocolVersion": 1})
	goalPath := filepath.Join(dir, "goal-load.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_GOAL", goalPath)
	res := resultOf(t, c.call("session/new", map[string]any{
		"cwd":          dir,
		"systemPrompt": map[string]any{"mode": "append", "content": []any{map[string]any{"type": "text", "text": "Keep the rules."}}},
	}))
	id := res["sessionId"].(string)
	c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "first"}}})
	c.call("session/load", map[string]any{"sessionId": id, "cwd": dir, "mcpServers": []any{}})
	c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "second"}}})
	goal, _ := os.ReadFile(goalPath)
	if !strings.Contains(string(goal), "Keep the rules.") {
		t.Fatalf("system prompt lost after load: %s", goal)
	}
}

func TestLoadUsesTheRequestedDirectory(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	c, closeFn := connect(t, s)
	defer closeFn()
	id := setup(t, c, dir)
	other := filepath.Join(dir, "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	cwdPath := filepath.Join(dir, "cwd.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_CWD", cwdPath)
	c.call("session/load", map[string]any{"sessionId": id, "cwd": other, "mcpServers": []any{}})
	c.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "go"}}})
	got, err := os.ReadFile(cwdPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(observed, want) {
		t.Fatalf("turn ran in %q, want %q", observed, want)
	}
}

func TestRootsAreNormalizedBeforeChecks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		root string
		cwd  string
		skip bool
	}{
		{name: "absolute", root: real, cwd: real},
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(wd, real); err == nil && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..") {
		cases = append(cases, struct {
			name string
			root string
			cwd  string
			skip bool
		}{name: "relative", root: rel, cwd: real})
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err == nil {
		cases = append(cases, struct {
			name string
			root string
			cwd  string
			skip bool
		}{name: "symlink", root: link, cwd: link})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(dir, tc.name, "home")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			srv, err := New(Config{
				Exe: exe, Roots: []string{tc.root}, Dir: tc.cwd, Version: "test",
				Home: home, StateDir: filepath.Join(dir, tc.name, "state"),
				Env: os.Environ(),
			})
			if err != nil {
				t.Fatal(err)
			}
			c, closeFn := connect(t, srv)
			defer closeFn()
			init := resultOf(t, c.call("initialize", map[string]any{"protocolVersion": 1}))
			if init["protocolVersion"] != float64(ProtocolVersion) {
				t.Fatal(init)
			}
			res := resultOf(t, c.call("session/new", map[string]any{"cwd": tc.cwd}))
			if res["sessionId"] == "" {
				t.Fatal(res)
			}
		})
	}
}

func TestMakeRoomReservesASlotForANewSession(t *testing.T) {
	for _, attached := range []int{0, 1} {
		name := "all_closed"
		if attached == 1 {
			name = "one_attached"
		}
		t.Run(name, func(t *testing.T) {
			s, dir := testServer(t, "ok", nil)
			keepID := ""
			var keepSess *Session
			for i := 0; i < maxSessions; i++ {
				sess := newSession(s, dir, dir, "", "")
				sess.ID = fmt.Sprintf("acp_old_%03d", i)
				sess.Title = fmt.Sprintf("old %d", i)
				s.sessions[sess.ID] = sess
				sess.save()
				delete(s.sessions, sess.ID)
				if i == 0 {
					keepID, keepSess = sess.ID, sess
				}
			}
			if attached == 1 {
				s.sessions[keepID] = keepSess
			}
			_ = keepSess
			if err := s.makeRoom(); err != nil {
				t.Fatalf("makeRoom: %v", err)
			}
			recs, err := s.records()
			if err != nil {
				t.Fatal(err)
			}
			if got := len(recs); got > maxSessions-1 {
				t.Fatalf("%d records kept, want at most %d", got, maxSessions-1)
			}
			found := false
			for _, rec := range recs {
				if rec.ID == keepID {
					found = true
				}
			}
			if !found {
				t.Fatalf("the retained session %s was removed", keepID)
			}
			// A new session can now be created on the same connection.
			c, closeFn := connect(t, s)
			defer closeFn()
			c.call("initialize", map[string]any{"protocolVersion": 1})
			res := resultOf(t, c.call("session/new", map[string]any{"cwd": dir}))
			if res["sessionId"] == "" {
				t.Fatalf("session/new refused after %d records: %v", maxSessions, res)
			}
		})
	}
}

func TestLaunchFailureSettlesTheTurn(t *testing.T) {
	s, dir := testServer(t, "ok", func(c *Config) {
		c.Exe = "/definitely/not/here/sleipnir"
	})
	sess := newSession(s, dir, dir, "", "")
	j := &Job{
		sess: sess, srv: s, goal: "x", dir: s.mcpCfg.Dir, done: make(chan struct{}), state: turnRunning,
		gen: map[string]int{}, tools: map[string]string{}, streamed: map[string]bool{},
	}
	if err := j.start(); err == nil {
		t.Fatal("a missing executable started")
	}
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a launch failure left done open")
	}
	if got := j.stopReasonValue(); got != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", got)
	}
	if j.state != turnFailed {
		t.Fatalf("state = %q, want failed", j.state)
	}
}

func TestRetiredSessionRefusesANewTurn(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	sess := newSession(s, dir, dir, "", "")
	sess.retire()
	j := &Job{sess: sess, srv: s, done: make(chan struct{}), state: turnRunning}
	if sess.setActive(j) {
		t.Fatal("a retired session admitted a turn")
	}
	if sess.isActive() {
		t.Fatal("a retired session reports an active turn")
	}
}

// lockedBuffer collects replies written concurrently by request handlers.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// lines returns a snapshot of the JSON-RPC replies written so far.
func (w *lockedBuffer) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Split(strings.TrimSpace(w.b.String()), "\n")
}

func TestConcurrentSessionCreationReservesOneSlot(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	// 255 attached sessions; one slot is free.
	for i := 0; i < maxSessions-1; i++ {
		sess := newSession(s, dir, dir, "", "")
		sess.ID = fmt.Sprintf("acp_seed_%03d", i)
		s.sessions[sess.ID] = sess
		sess.save()
	}
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	params, _ := json.Marshal(map[string]any{"cwd": dir})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start // both creators cross the capacity check together
			s.newSession(&message{
				ID:     json.RawMessage(fmt.Sprintf(`%d`, id)),
				Method: "session/new", Params: params,
			})
		}(i)
	}
	close(start)
	wg.Wait()
	ok, refused := 0, 0
	for _, line := range out.lines() {
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("not a reply: %q", line)
		}
		if reply["error"] != nil {
			refused++
		} else if reply["result"] != nil {
			ok++
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("replies: %d success, %d refused; want one of each: %v", ok, refused, out.lines())
	}
	s.mu.Lock()
	count := len(s.sessions)
	s.mu.Unlock()
	if count != maxSessions {
		t.Fatalf("%d sessions attached, want %d", count, maxSessions)
	}
	recs, err := s.records()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) > maxSessions {
		t.Fatalf("%d persisted records, want at most %d", len(recs), maxSessions)
	}
}

func TestStaleSaveCannotOverwriteReplacement(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	old := newSession(s, dir, dir, "", "")
	old.Title = "stale"
	replacement := newSession(s, dir, dir, "", "")
	replacement.ID = old.ID
	replacement.Title = "current"
	s.sessions[replacement.ID] = replacement
	replacement.save()
	old.save() // a retired object must not commit its older snapshot
	rec, err := s.readRecord(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != "current" {
		t.Fatalf("record title = %q, want current", rec.Title)
	}
}

func TestLoadReadsTheFinalStateOfACancelledTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot deliver SIGTERM to the child; the harness falls back to a kill, so there is no final result to observe")
	}
	s, dir := testServer(t, "graceful", nil)
	ready := filepath.Join(dir, "ready.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_READY", ready)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	newParams, _ := json.Marshal(map[string]any{"cwd": dir, "mcpServers": []any{}})
	s.newSession(&message{ID: json.RawMessage(`1`), Method: "session/new", Params: newParams})
	var created map[string]any
	if err := json.Unmarshal([]byte(out.lines()[0]), &created); err != nil {
		t.Fatal(err)
	}
	result, _ := created["result"].(map[string]any)
	id, _ := result["sessionId"].(string)
	if id == "" {
		t.Fatalf("no session id: %v", created)
	}
	promptParams, _ := json.Marshal(map[string]any{
		"sessionId": id,
		"prompt":    []any{map[string]any{"type": "text", "text": "work"}},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.prompt(context.Background(), &message{ID: json.RawMessage(`2`), Method: "session/prompt", Params: promptParams})
	}()
	limit := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-limit:
			t.Fatal("the run never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	params, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": dir, "mcpServers": []any{}})
	s.openSession(&message{ID: json.RawMessage(`99`), Method: "session/resume", Params: params}, false)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled prompt did not end")
	}
	rec, err := s.readRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ResumeID != "sess-graceful" {
		t.Fatalf("resume id = %q, want sess-graceful: the load read before the turn's final save", rec.ResumeID)
	}
	s.mu.Lock()
	replacement := s.sessions[id]
	s.mu.Unlock()
	if replacement == nil || replacement.resumeID() != "sess-graceful" {
		t.Fatalf("the attached replacement did not carry the final resume id: %v", replacement)
	}
}

func TestLoadIsSerializedWithCapacityReservation(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	// 254 attached sessions and one detached record B leave a single free slot.
	for i := 0; i < maxSessions-2; i++ {
		sess := newSession(s, dir, dir, "", "")
		sess.ID = fmt.Sprintf("acp_seed_%03d", i)
		s.sessions[sess.ID] = sess
		sess.save()
	}
	detached := newSession(s, dir, dir, "", "")
	detached.ID = "acp_detached"
	s.sessions[detached.ID] = detached
	detached.save()
	delete(s.sessions, detached.ID)

	release := make(chan struct{})
	entered := make(chan struct{})
	s.afterRead = func() {
		close(entered)
		<-release
	}
	loadParams, _ := json.Marshal(map[string]any{
		"sessionId": detached.ID, "cwd": dir, "mcpServers": []any{},
	})
	loadDone := make(chan struct{})
	go func() {
		defer close(loadDone)
		s.openSession(&message{ID: json.RawMessage(`1`), Method: "session/load", Params: loadParams}, false)
	}()
	waitSignal(t, entered, "entered") // the load has read B but has not attached it yet
	newDone := make(chan struct{})
	go func() {
		defer close(newDone)
		params, _ := json.Marshal(map[string]any{"cwd": dir})
		s.newSession(&message{ID: json.RawMessage(`2`), Method: "session/new", Params: params})
	}()
	close(release)
	select {
	case <-loadDone:
	case <-time.After(10 * time.Second):
		t.Fatal("load did not finish")
	}
	select {
	case <-newDone:
	case <-time.After(10 * time.Second):
		t.Fatal("session/new did not finish")
	}
	s.mu.Lock()
	count := len(s.sessions)
	_, attachedB := s.sessions[detached.ID]
	s.mu.Unlock()
	if count != maxSessions || !attachedB {
		t.Fatalf("attached %d sessions, loaded record attached = %v; want %d and true", count, attachedB, maxSessions)
	}
	if _, err := s.readRecord(detached.ID); err != nil {
		t.Fatalf("the loaded record was evicted: %v", err)
	}
}

func TestCloseRefusesTurnsDuringCleanup(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	sess := newSession(s, dir, dir, "", "")
	s.sessions[sess.ID] = sess
	params, _ := json.Marshal(map[string]any{"sessionId": sess.ID})
	s.closeSession(&message{ID: json.RawMessage(`1`), Method: "session/close", Params: params})
	if sess.setActive(&Job{sess: sess, srv: s, done: make(chan struct{})}) {
		t.Fatal("a closed session admitted a turn")
	}
}

func TestPromptRejectsASessionReplacedDuringAdmission(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	newParams, _ := json.Marshal(map[string]any{"cwd": dir})
	s.newSession(&message{ID: json.RawMessage(`1`), Method: "session/new", Params: newParams})
	var created map[string]any
	if err := json.Unmarshal([]byte(out.lines()[0]), &created); err != nil {
		t.Fatal(err)
	}
	id := created["result"].(map[string]any)["sessionId"].(string)
	argv := filepath.Join(dir, "argv.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_ARGV", argv)
	entered, release := make(chan struct{}), make(chan struct{})
	s.afterLookup = func() {
		close(entered)
		<-release
	}
	promptParams, _ := json.Marshal(map[string]any{
		"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "work"}},
	})
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		s.prompt(context.Background(), &message{ID: json.RawMessage(`3`), Method: "session/prompt", Params: promptParams})
	}()
	waitSignal(t, entered, "entered")
	loadParams, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": dir, "mcpServers": []any{}})
	s.openSession(&message{ID: json.RawMessage(`2`), Method: "session/resume", Params: loadParams}, false)
	close(release)
	select {
	case <-promptDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the rejected prompt did not return")
	}
	var reply map[string]any
	for _, line := range out.lines() {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["id"] == float64(3) {
			reply = m
		}
	}
	if reply == nil || reply["error"] == nil {
		t.Fatalf("the stale prompt was not refused: %v", out.lines())
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatalf("the stale prompt launched a child (argv file %v): %v", argv, err)
	}
}

func TestLoadBlocksAdmissionWhileReplacingASession(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	newParams, _ := json.Marshal(map[string]any{"cwd": dir})
	s.newSession(&message{ID: json.RawMessage(`1`), Method: "session/new", Params: newParams})
	var created map[string]any
	if err := json.Unmarshal([]byte(out.lines()[0]), &created); err != nil {
		t.Fatal(err)
	}
	id := created["result"].(map[string]any)["sessionId"].(string)
	argv := filepath.Join(dir, "argv-during-load.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_ARGV", argv)
	var refused map[string]any
	entered := make(chan struct{})
	release := make(chan struct{})
	s.afterRead = func() {
		close(entered)
		<-release
		promptParams, _ := json.Marshal(map[string]any{
			"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "work"}},
		})
		s.prompt(context.Background(), &message{ID: json.RawMessage(`3`), Method: "session/prompt", Params: promptParams})
		for _, line := range out.lines() {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["id"] == float64(3) {
				refused = m
			}
		}
	}
	loadParams, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": dir, "mcpServers": []any{}})
	loadDone := make(chan struct{})
	go func() {
		defer close(loadDone)
		s.openSession(&message{ID: json.RawMessage(`2`), Method: "session/resume", Params: loadParams}, false)
	}()
	waitSignal(t, entered, "entered")
	close(release)
	select {
	case <-loadDone:
	case <-time.After(10 * time.Second):
		t.Fatal("load did not finish")
	}
	if refused == nil || refused["error"] == nil {
		t.Fatalf("a prompt was admitted while the session was being replaced: %v", refused)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatalf("the prompt launched a child during replacement: %v", err)
	}
}

func TestCloseBlocksAdmissionBeforeCancelling(t *testing.T) {
	s, dir := testServer(t, "slow", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	newParams, _ := json.Marshal(map[string]any{"cwd": dir})
	s.newSession(&message{ID: json.RawMessage(`1`), Method: "session/new", Params: newParams})
	var created map[string]any
	if err := json.Unmarshal([]byte(out.lines()[0]), &created); err != nil {
		t.Fatal(err)
	}
	id := created["result"].(map[string]any)["sessionId"].(string)
	argv := filepath.Join(dir, "argv-during-close.txt")
	setFakeEnv(s, "ACPSERVE_FAKE_ARGV", argv)
	firstPrompt, _ := json.Marshal(map[string]any{
		"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "work"}},
	})
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		s.prompt(context.Background(), &message{ID: json.RawMessage(`3`), Method: "session/prompt", Params: firstPrompt})
	}()
	limit := time.After(10 * time.Second)
	var activeJob *Job
	for {
		s.mu.Lock()
		sess := s.sessions[id]
		s.mu.Unlock()
		if sess != nil {
			sess.mu.Lock()
			activeJob = sess.active
			sess.mu.Unlock()
			if activeJob != nil {
				break
			}
		}
		select {
		case <-limit:
			t.Fatal("the first prompt never became active")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var refused map[string]any
	s.afterCloseRetire = func() {
		if activeJob.isCancelled() {
			t.Error("cancellation happened before the admission check")
		}
		promptParams, _ := json.Marshal(map[string]any{
			"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "second"}},
		})
		s.prompt(context.Background(), &message{ID: json.RawMessage(`4`), Method: "session/prompt", Params: promptParams})
		for _, line := range out.lines() {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["id"] == float64(4) {
				refused = m
			}
		}
	}
	waitFile(t, argv, "the first child's argv file")
	_ = os.Remove(argv) // only a new child should recreate it
	params, _ := json.Marshal(map[string]any{"sessionId": id})
	s.closeSession(&message{ID: json.RawMessage(`2`), Method: "session/close", Params: params})
	waitSignal(t, promptDone, "first prompt completion")
	if refused == nil || refused["error"] == nil {
		t.Fatalf("a prompt was admitted after close retired the session: %v", refused)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatalf("the prompt launched a child during close: %v", err)
	}
}

func TestLoadKeepsDetachedRecordAtCapacityBoundary(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	// 255 attached sessions plus one detached record fill the directory.
	for i := 0; i < maxSessions-1; i++ {
		sess := newSession(s, dir, dir, "", "")
		sess.ID = fmt.Sprintf("acp_seed_%03d", i)
		s.sessions[sess.ID] = sess
		sess.save()
	}
	detached := newSession(s, dir, dir, "", "")
	detached.ID = "acp_detached_boundary"
	s.sessions[detached.ID] = detached
	detached.save()
	delete(s.sessions, detached.ID)

	entered, release := make(chan struct{}), make(chan struct{})
	creatorEntered := make(chan struct{})
	s.beforeCreate = func() { close(creatorEntered) }
	s.afterRead = func() {
		close(entered)
		<-release
	}
	loadParams, _ := json.Marshal(map[string]any{"sessionId": detached.ID, "cwd": dir, "mcpServers": []any{}})
	loadDone := make(chan struct{})
	go func() {
		defer close(loadDone)
		s.openSession(&message{ID: json.RawMessage(`1`), Method: "session/load", Params: loadParams}, false)
	}()
	waitSignal(t, entered, "entered")
	newDone := make(chan struct{})
	go func() {
		defer close(newDone)
		params, _ := json.Marshal(map[string]any{"cwd": dir})
		s.newSession(&message{ID: json.RawMessage(`2`), Method: "session/new", Params: params})
	}()
	waitSignal(t, creatorEntered, "creator entering newSession") // the creator has reached newSession while the load is paused
	if s.createMu.TryLock() {
		s.createMu.Unlock()
		t.Fatal("the load did not hold createMu across its record read")
	}
	close(release)
	select {
	case <-loadDone:
	case <-time.After(10 * time.Second):
		t.Fatal("load did not finish")
	}
	select {
	case <-newDone:
	case <-time.After(10 * time.Second):
		t.Fatal("session/new did not finish")
	}
	refused := false
	for _, line := range out.lines() {
		var reply map[string]any
		if json.Unmarshal([]byte(line), &reply) == nil && reply["id"] == float64(2) {
			refused = reply["error"] != nil
		}
	}
	if !refused {
		t.Fatal("session/new was admitted at the capacity boundary")
	}
	s.mu.Lock()
	count := len(s.sessions)
	_, attachedB := s.sessions[detached.ID]
	s.mu.Unlock()
	if count != maxSessions || !attachedB {
		t.Fatalf("attached %d sessions, loaded record attached = %v; want %d and true", count, attachedB, maxSessions)
	}
	if _, err := s.readRecord(detached.ID); err != nil {
		t.Fatalf("the loaded record was evicted at the boundary: %v", err)
	}
}

func TestInvalidLoadSetupDoesNotCancelTheActiveTurn(t *testing.T) {
	s, dir := testServer(t, "slow", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	newParams, _ := json.Marshal(map[string]any{"cwd": dir})
	s.newSession(&message{ID: json.RawMessage(`1`), Method: "session/new", Params: newParams})
	var created map[string]any
	if err := json.Unmarshal([]byte(out.lines()[0]), &created); err != nil {
		t.Fatal(err)
	}
	id := created["result"].(map[string]any)["sessionId"].(string)
	promptParams, _ := json.Marshal(map[string]any{
		"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "work"}},
	})
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		s.prompt(context.Background(), &message{ID: json.RawMessage(`2`), Method: "session/prompt", Params: promptParams})
	}()
	limit := time.After(10 * time.Second)
	for {
		s.mu.Lock()
		sess := s.sessions[id]
		s.mu.Unlock()
		if sess != nil && sess.isActive() {
			break
		}
		select {
		case <-limit:
			t.Fatal("the prompt never became active")
		case <-time.After(10 * time.Millisecond):
		}
	}
	outside := t.TempDir()
	params, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": outside, "mcpServers": []any{}})
	s.openSession(&message{ID: json.RawMessage(`3`), Method: "session/resume", Params: params}, false)
	var refused map[string]any
	for _, line := range out.lines() {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["id"] == float64(3) {
			refused = m
		}
	}
	if refused == nil || refused["error"] == nil {
		t.Fatalf("an out-of-roots cwd was accepted: %v", refused)
	}
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil || !sess.isActive() {
		t.Fatal("a rejected load cancelled the active turn")
	}
	if j := sess.cancelJob(); j != nil {
		<-j.done
	}
	waitSignal(t, promptDone, "prompt completion")
}

func TestCloseRemovesTheMCPConfigAfterDetaching(t *testing.T) {
	s, dir := testServer(t, "ok", nil)
	out := &lockedBuffer{}
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	params, _ := json.Marshal(map[string]any{
		"cwd": dir,
		"mcpServers": []any{
			map[string]any{"name": "tools", "command": "/bin/echo", "args": []any{"stdio"}},
		},
	})
	s.newSession(&message{ID: json.RawMessage(`1`), Method: "session/new", Params: params})
	var created map[string]any
	if err := json.Unmarshal([]byte(out.lines()[0]), &created); err != nil {
		t.Fatal(err)
	}
	id := created["result"].(map[string]any)["sessionId"].(string)
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	path := sess.mcpFile()
	if path == "" {
		t.Fatal("no MCP config was written")
	}
	closeParams, _ := json.Marshal(map[string]any{"sessionId": id})
	s.closeSession(&message{ID: json.RawMessage(`2`), Method: "session/close", Params: closeParams})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the MCP config survived close: %v", err)
	}
}
