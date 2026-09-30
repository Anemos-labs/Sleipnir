//go:build unix

package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// harness wires a Manager to the tool registry the way the agent loop does.
type harness struct {
	t     *testing.T
	m     *Manager
	reg   *tools.Registry
	root  string
	blobs *events.MemBlobs
	log   *events.MemLog
}

func newHarness(t *testing.T, opts ...Options) *harness {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(opts...)
	t.Cleanup(m.Shutdown)
	reg := tools.NewRegistry()
	Register(reg, m)
	return &harness{t: t, m: m, reg: reg, root: root, blobs: events.NewMemBlobs(), log: events.NewMemLog()}
}

// outLog collects what an Env.Out callback receives.
type outLog struct {
	mu     sync.Mutex
	stdout strings.Builder
	stderr strings.Builder
	calls  int
}

func (o *outLog) fn(stream, text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	switch stream {
	case "stdout":
		o.stdout.WriteString(text)
	case "stderr":
		o.stderr.WriteString(text)
	}
}

func (o *outLog) get() (stdout, stderr string, calls int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stdout.String(), o.stderr.String(), o.calls
}

// env returns an agent's Env. Like the real one it is created once per agent
// and reused across calls.
func (h *harness) env(agent string) *tools.Env {
	return &tools.Env{Agent: agent, Cwd: h.root, Root: h.root, Blobs: h.blobs, Emit: h.log, Perm: perm.AllowAll{}} // the tests are about the tool, not the policy
}

func (h *harness) call(ctx context.Context, env *tools.Env, name string, input any) *tools.Result {
	h.t.Helper()
	res, err := h.tryCall(ctx, env, name, input)
	if err != nil {
		h.t.Fatalf("%s: harness error: %v", name, err)
	}
	return res
}

// tryCall is call without t.Fatal, for use from other goroutines.
func (h *harness) tryCall(ctx context.Context, env *tools.Env, name string, input any) (*tools.Result, error) {
	tool, ok := h.reg.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool %q is not registered", name)
	}
	var raw json.RawMessage
	switch v := input.(type) {
	case json.RawMessage:
		raw = v
	case string:
		raw = json.RawMessage(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return tool.Run(ctx, &tools.Call{ID: "call_1", Name: name, Input: raw, Env: env})
}

// bash runs a foreground command; extra is merged into the input object.
func (h *harness) bash(env *tools.Env, command string, extra ...map[string]any) *tools.Result {
	h.t.Helper()
	in := map[string]any{"command": command}
	for _, e := range extra {
		for k, v := range e {
			in[k] = v
		}
	}
	return h.call(context.Background(), env, "bash", in)
}

func (h *harness) output(env *tools.Env, id string, extra ...map[string]any) *tools.Result {
	h.t.Helper()
	in := map[string]any{"id": id}
	for _, e := range extra {
		for k, v := range e {
			in[k] = v
		}
	}
	return h.call(context.Background(), env, "bash_output", in)
}

func (h *harness) kill(env *tools.Env, id string) *tools.Result {
	h.t.Helper()
	return h.call(context.Background(), env, "bash_kill", map[string]any{"id": id})
}

// startJob starts a background command and returns its id.
func (h *harness) startJob(env *tools.Env, command string, extra ...map[string]any) string {
	h.t.Helper()
	res := h.bash(env, command, append(extra, map[string]any{"run_in_background": true})...)
	if res.IsError {
		h.t.Fatalf("starting job: %s", res.Text)
	}
	id, ok := strings.CutPrefix(res.Text, "job ")
	if !ok || !strings.HasSuffix(id, " started") {
		h.t.Fatalf("unexpected start text %q", res.Text)
	}
	return strings.TrimSuffix(id, " started")
}

// mustText fails the test if the result is an error and returns its text.
func mustText(t *testing.T, res *tools.Result) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Text)
	}
	return res.Text
}

// pidAlive reports whether pid is a live process (a zombie awaiting its
// parent does not count: it is dead, merely not yet collected).
func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i > 0 && i+2 < len(s) && s[i+2] == 'Z' {
			return false
		}
	}
	return true
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", within, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitDead(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	waitFor(t, fmt.Sprintf("pid %d to die", pid), within, func() bool { return !pidAlive(pid) })
}

// readPid waits for a pid file written by a test command.
func readPid(t *testing.T, path string) int {
	t.Helper()
	var pid int
	waitFor(t, "pid file "+filepath.Base(path), 10*time.Second, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return false
		}
		pid = n
		return true
	})
	return pid
}

func metaInt(t *testing.T, res *tools.Result, key string) int {
	t.Helper()
	v, ok := res.Meta[key].(int)
	if !ok {
		t.Fatalf("Meta[%q] = %#v, want int", key, res.Meta[key])
	}
	return v
}

// denyAll is a Requester that refuses everything and remembers what it saw.
type recordingPerm struct {
	mu    sync.Mutex
	reqs  []perm.Request
	allow bool
	why   string
}

func (r *recordingPerm) Check(_ context.Context, req perm.Request) perm.Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return perm.Decision{Allow: r.allow, Reason: r.why}
}

func (r *recordingPerm) seen() []perm.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]perm.Request(nil), r.reqs...)
}
