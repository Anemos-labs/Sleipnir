package mcpserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The test binary is also a fake `sleipnir run --json`: with fakeEnv set it
// prints the stream named by the variable's value and exits.
const fakeEnv = "MCPSERVE_FAKE_RUN"

func TestMain(m *testing.M) {
	if v := os.Getenv(fakeEnv); v != "" {
		fakeRun(v)
		return
	}
	os.Exit(m.Run())
}

// fakeRun plays one scripted run.
func fakeRun(kind string) {
	goal, _ := io.ReadAll(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	emit := func(m map[string]any) { _ = enc.Encode(m) }
	switch kind {
	case "ok":
		emit(map[string]any{"type": "notice", "agent": "main", "level": "info", "message": "starting"})
		emit(map[string]any{"type": "text", "agent": "main", "text": "Reading "})
		emit(map[string]any{"type": "text", "agent": "main", "text": "the code."})
		emit(map[string]any{"type": "tool_start", "agent": "main", "name": "bash", "input": map[string]any{"command": "go test ./...\nmore"}})
		emit(map[string]any{"type": "tool_end", "agent": "main", "name": "bash", "failed": true, "exit_code": 1, "output": "\nFAIL x\n"})
		fmt.Println(strings.Repeat("x", maxEventBytes+10)) // a line too long to parse is skipped
		emit(map[string]any{"type": "result", "text": "did: " + string(goal), "steps": 3, "session": "s1", "error": ""})
	case "fail":
		fmt.Fprintln(os.Stderr, "sleipnir: no model configured")
		os.Exit(2)
	case "slow":
		emit(map[string]any{"type": "notice", "agent": "main", "level": "info", "message": "working"})
		time.Sleep(time.Minute)
	}
}

// newTestServer is a server whose jobs run the fake.
func newTestServer(t *testing.T, kind string, cfg Config) *Server {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Exe = exe
	if len(cfg.Roots) == 0 {
		cfg.Roots = []string{t.TempDir()}
		cfg.Dir = cfg.Roots[0]
	}
	cfg.Env = append(os.Environ(), fakeEnv+"="+kind)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// session runs Serve over pipes and returns a function that sends a request
// and returns the decoded response.
func session(t *testing.T, s *Server) (call func(method string, params any) map[string]any, closeIn func() error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), inR, outW); outW.Close() }()
	t.Cleanup(func() { inW.Close() })
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	id := 0
	call = func(method string, params any) map[string]any {
		t.Helper()
		id++
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatalf("not JSON: %q", sc.Text())
			}
			if m["id"] == float64(id) {
				return m
			}
		}
		t.Fatalf("stdout ended: %v", sc.Err())
		return nil
	}
	closeIn = func() error {
		inW.Close()
		go io.Copy(io.Discard, outR)
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			return fmt.Errorf("Serve did not return")
		}
	}
	return call, closeIn
}

// toolCall calls a tool and returns its structured content and text.
func toolCall(t *testing.T, call func(string, any) map[string]any, name string, args any) (map[string]any, string, bool) {
	t.Helper()
	m := call("tools/call", map[string]any{"name": name, "arguments": args})
	res, _ := m["result"].(map[string]any)
	if res == nil {
		t.Fatalf("%s: no result: %v", name, m)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	sc, _ := res["structuredContent"].(map[string]any)
	isErr, _ := res["isError"].(bool)
	return sc, text, isErr
}

func TestProtocolBasics(t *testing.T) {
	s := newTestServer(t, "ok", Config{})
	call, closeIn := session(t, s)
	init := call("initialize", map[string]any{"protocolVersion": "1999-01-01"})["result"].(map[string]any)
	if init["protocolVersion"] != ProtocolVersions[0] || init["instructions"] == "" {
		t.Errorf("initialize = %v", init)
	}
	if r := call("ping", nil); r["result"] == nil {
		t.Errorf("ping = %v", r)
	}
	if r := call("resources/list", nil); r["error"].(map[string]any)["code"] != float64(-32601) {
		t.Errorf("unknown method = %v", r)
	}
	if r := call("tools/call", map[string]any{"name": "nope"}); r["error"] == nil {
		t.Errorf("unknown tool = %v", r)
	}
	tools := call("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	for _, tl := range tools {
		m := tl.(map[string]any)
		schema := m["inputSchema"].(map[string]any)
		if schema["type"] != "object" || m["description"] == "" {
			t.Errorf("tool %v", m["name"])
		}
	}
	if err := closeIn(); err != nil {
		t.Fatal(err)
	}
}

func TestStartWaitReportsTheDigestAndResult(t *testing.T) {
	s := newTestServer(t, "ok", Config{})
	call, closeIn := session(t, s)
	st, text, isErr := toolCall(t, call, "sleipnir_start", map[string]any{"goal": "-x add a flag"})
	if isErr || st["job"] != "j1" {
		t.Fatalf("start: %s", text)
	}
	st, text, _ = toolCall(t, call, "sleipnir_wait", map[string]any{"job": "j1", "timeout_seconds": 60})
	if st["state"] != StateDone {
		t.Fatalf("wait: %s", text)
	}
	if r := st["result"].(map[string]any); r["text"] != "did: -x add a flag" || r["session"] != "s1" {
		t.Errorf("result = %v (the goal goes to stdin whole)", r)
	}
	var lines []string
	for _, a := range st["activity"].([]any) {
		lines = append(lines, a.(map[string]any)["line"].(string))
	}
	want := []string{"main [info]: starting", "main says: Reading the code.", "main: bash go test ./...", "main: bash exit 1: FAIL x"}
	if !slices.Equal(lines, want) {
		t.Errorf("activity =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if st["tool_calls"] != float64(1) || st["last_seq"] != float64(4) {
		t.Errorf("counts = %v %v", st["tool_calls"], st["last_seq"])
	}
	// after= shows only newer lines; events= keeps the newest.
	st, _, _ = toolCall(t, call, "sleipnir_status", map[string]any{"job": "j1", "after": 2})
	if a := st["activity"].([]any); len(a) != 2 || a[0].(map[string]any)["seq"] != float64(3) {
		t.Errorf("after=2: %v", a)
	}
	st, _, _ = toolCall(t, call, "sleipnir_status", map[string]any{"job": "j1", "events": 1})
	if a := st["activity"].([]any); len(a) != 1 || st["omitted"] != float64(3) {
		t.Errorf("events=1: %v omitted %v", a, st["omitted"])
	}
	if err := closeIn(); err != nil {
		t.Fatal(err)
	}
}

func TestAFailedRunKeepsItsStderr(t *testing.T) {
	s := newTestServer(t, "fail", Config{})
	call, closeIn := session(t, s)
	toolCall(t, call, "sleipnir_start", map[string]any{"goal": "g"})
	st, text, _ := toolCall(t, call, "sleipnir_wait", map[string]any{"job": "j1", "timeout_seconds": 60})
	if st["state"] != StateFailed || st["exit"] != "exit status 2" || !strings.Contains(st["stderr"].(string), "no model configured") {
		t.Errorf("failed run: %s", text)
	}
	_ = closeIn()
}

func TestCancelAndShutdownEndRunningJobs(t *testing.T) {
	s := newTestServer(t, "slow", Config{MaxJobs: 2})
	call, closeIn := session(t, s)
	toolCall(t, call, "sleipnir_start", map[string]any{"goal": "a"})
	toolCall(t, call, "sleipnir_start", map[string]any{"goal": "b"})
	if _, text, isErr := toolCall(t, call, "sleipnir_start", map[string]any{"goal": "c"}); !isErr || !strings.Contains(text, "most this server runs") {
		t.Errorf("a third job past max-jobs: %s", text)
	}
	if st, text, _ := toolCall(t, call, "sleipnir_wait", map[string]any{"job": "j1", "timeout_seconds": 1}); st["state"] != StateRunning {
		t.Errorf("a wait that times out reports the job running: %s", text)
	}
	if _, text, isErr := toolCall(t, call, "sleipnir_cancel", map[string]any{"job": "j1"}); isErr {
		t.Fatalf("cancel: %s", text)
	}
	if st, text, _ := toolCall(t, call, "sleipnir_wait", map[string]any{"job": "j1", "timeout_seconds": 30}); st["state"] != StateCancelled {
		t.Errorf("after cancel: %s", text)
	}
	// Closing the connection ends j2 too.
	if err := closeIn(); err != nil {
		t.Fatal(err)
	}
	if s.jobs["j2"].running() {
		t.Error("j2 still runs after the client left")
	}
}

func TestArgsKeepTheServersLimits(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	real, _ := filepath.EvalSymlinks(root)
	cfg := Config{Roots: []string{real}, Dir: real, Mode: "accept-edits", Allow: []string{"tests"}, TrustProject: true}

	args, dir, err := Args(cfg, StartArgs{Goal: "g", Cwd: "sub", Workers: 8, Model: "heimdallm/x", Mode: "plan", Verify: "go test {dirs}", Isolation: "worktree", BudgetUSD: 2.5, RoleModel: []string{"manager=heimdallm/y"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--json", "--cwd", filepath.Join(real, "sub"), "--mode", "plan", "--allow", "tests", "--trust-project",
		"--model", "heimdallm/x", "--swarm", "8", "--role-model", "manager=heimdallm/y", "--verify", "go test {dirs}",
		"--isolation", "worktree", "--budget-usd", "2.5", "-"}
	if !slices.Equal(args, want) || dir != filepath.Join(real, "sub") {
		t.Errorf("args =\n%q\nwant\n%q", args, want)
	}
	if args, _, _ := Args(cfg, StartArgs{Goal: "g"}); !slices.Contains(args, "accept-edits") {
		t.Errorf("no mode asked: the server's is used: %q", args)
	}
	for _, tc := range []struct {
		a    StartArgs
		want string
	}{
		{StartArgs{Goal: " "}, "empty"},
		{StartArgs{Goal: "g", Mode: "bypass"}, "more permissive"},
		{StartArgs{Goal: "g", Mode: "root"}, "unknown mode"},
		{StartArgs{Goal: "g", Cwd: outside}, "outside"},
		{StartArgs{Goal: "g", Cwd: "../"}, "outside"},
		{StartArgs{Goal: "g", Workers: 3}, "8 or more"},
		{StartArgs{Goal: "g", Isolation: "chroot"}, "isolation"},
		{StartArgs{Goal: "g", Model: "a\nb"}, "control character"},
		{StartArgs{Goal: "g", BudgetUSD: -1}, "negative"},
	} {
		if _, _, err := Args(cfg, tc.a); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.a, err, tc.want)
		}
	}
	if _, err := New(Config{Exe: "x", Roots: []string{root}, Mode: "everything"}); err == nil {
		t.Error("an unknown ceiling is refused")
	}
}

// A server with several roots and no default directory runs each call in the
// folder it names, makes a new one when asked, and refuses one outside.
func TestResolveDirPicksAFolderPerRun(t *testing.T) {
	a, b, outside := t.TempDir(), t.TempDir(), t.TempDir()
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	if err := os.Mkdir(filepath.Join(ra, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ra, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ra, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Exe: "x", Roots: []string{a, b, a}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.cfg
	if !slices.Equal(cfg.Roots, []string{ra, rb}) || cfg.Dir != "" {
		t.Fatalf("roots %q dir %q", cfg.Roots, cfg.Dir)
	}
	for _, tc := range []struct {
		cwd    string
		create bool
		want   string
	}{
		{filepath.Join(ra, "app"), false, filepath.Join(ra, "app")},
		{rb, false, rb},
		{filepath.Join(rb, "new", "tool"), true, filepath.Join(rb, "new", "tool")},
	} {
		got, err := resolveDir(cfg, tc.cwd, tc.create)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.cwd, got, err, tc.want)
		}
	}
	if fi, err := os.Stat(filepath.Join(rb, "new", "tool")); err != nil || !fi.IsDir() {
		t.Errorf("create did not make the folder: %v", err)
	}
	for _, tc := range []struct {
		cwd    string
		create bool
		want   string
	}{
		{"", false, "cwd is required"},
		{"app", false, "relative"},
		{outside, false, "outside"},
		{filepath.Join(outside, "new"), true, "outside"},
		{filepath.Join(ra, "escape"), false, "outside"},
		{filepath.Join(ra, "escape", "new"), true, "outside"},
		{filepath.Join(ra, "missing"), false, "create"},
		{filepath.Join(ra, "file"), false, "not a directory"},
	} {
		if _, err := resolveDir(cfg, tc.cwd, tc.create); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.cwd, err, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Error("a folder was made outside the roots")
	}
	t.Setenv("HOME", ra)
	if got, err := resolveDir(cfg, "~/app", false); err != nil || got != filepath.Join(ra, "app") {
		t.Errorf("~/app: %q, %v", got, err)
	}
	if _, err := New(Config{Exe: "x", Roots: []string{a}, Dir: b}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("a default directory outside the roots: %v", err)
	}
	if _, err := New(Config{Exe: "x"}); err == nil {
		t.Error("a server with no root is refused")
	}
}

func TestReadLineSkipsOverlongLines(t *testing.T) {
	in := "short\n" + strings.Repeat("y", 300) + "\nlast"
	br := bufio.NewReaderSize(strings.NewReader(in), 16)
	var got []string
	for {
		line, err := readLine(br, 100)
		got = append(got, string(line))
		if err != nil {
			break
		}
	}
	if !slices.Equal(got, []string{"short", "", "last"}) {
		t.Errorf("lines = %q", got)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	var tb tailBuffer
	tb.Write(bytes.Repeat([]byte("a"), maxStderrBytes))
	tb.Write([]byte("END"))
	if s := tb.String(); len(s) != maxStderrBytes || !strings.HasSuffix(s, "END") {
		t.Errorf("tail len %d", len(s))
	}
}
