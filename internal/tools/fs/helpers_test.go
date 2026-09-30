package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// realTemp returns a temp dir whose path contains no symlinks, so tests can
// compare canonical paths textually.
func realTemp(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// testEnv builds an Env for agent "a1" rooted at a fresh temp dir.
func testEnv(t testing.TB) *tools.Env {
	t.Helper()
	dir := realTemp(t)
	return &tools.Env{
		Agent: "a1",
		Cwd:   dir,
		Root:  dir,
		Files: tools.NewFileState(),
		Blobs: events.NewMemBlobs(),
	}
}

// agentEnv returns an Env for another agent that shares everything but the id.
func agentEnv(base *tools.Env, agent string) *tools.Env {
	e := *base
	e.Agent = agent
	return &e
}

// call runs a tool with a JSON-able input.
func run(t testing.TB, tool tools.Tool, env *tools.Env, input any) *tools.Result {
	t.Helper()
	var raw json.RawMessage
	switch v := input.(type) {
	case string:
		raw = json.RawMessage(v)
	case json.RawMessage:
		raw = v
	case []byte:
		raw = json.RawMessage(v)
	default:
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	res, err := tool.Run(context.Background(), &tools.Call{ID: "t1", Name: tool.Spec().Name, Input: raw, Env: env})
	if err != nil {
		t.Fatalf("%s: harness error: %v", tool.Spec().Name, err)
	}
	if res == nil {
		t.Fatalf("%s: nil result", tool.Spec().Name)
	}
	return res
}

func mustOK(t testing.TB, res *tools.Result) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Text)
	}
	return res.Text
}

func mustErr(t testing.TB, res *tools.Result) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected an error result, got: %s", res.Text)
	}
	return res.Text
}

func contains(t testing.TB, text string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(text, s) {
			t.Errorf("result should contain %q, got:\n%s", s, text)
		}
	}
}

func notContains(t testing.TB, text string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(text, s) {
			t.Errorf("result should not contain %q, got:\n%s", s, text)
		}
	}
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFileT(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// setMtime pins a file's modification time.
func setMtime(t testing.TB, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// hooks records the order of permission, guard and snapshot calls and can veto.
type hooks struct {
	mu   sync.Mutex
	log  []string
	reqs []perm.Request

	denyPerm   func(perm.Request) string // non-empty return denies with that reason
	denyGuard  func(path string) error
	failSnap   func(path string) error
	onBeforeFn func(path string) // runs inside Guard.BeforeWrite (fault injection)
}

func (h *hooks) record(s string) {
	h.mu.Lock()
	h.log = append(h.log, s)
	h.mu.Unlock()
}

func (h *hooks) Log() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.log...)
}

func (h *hooks) Requests() []perm.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]perm.Request(nil), h.reqs...)
}

// Check implements perm.Requester.
func (h *hooks) Check(_ context.Context, r perm.Request) perm.Decision {
	h.mu.Lock()
	h.reqs = append(h.reqs, r)
	h.mu.Unlock()
	h.record(fmt.Sprintf("perm:%s", r.Tool))
	if h.denyPerm != nil {
		if reason := h.denyPerm(r); reason != "" {
			return perm.Decision{Allow: false, Reason: reason}
		}
	}
	return perm.Decision{Allow: true}
}

// BeforeWrite implements tools.Guard.
func (h *hooks) BeforeWrite(agent, path string) error {
	h.record("guard.before:" + filepath.Base(path))
	if h.onBeforeFn != nil {
		h.onBeforeFn(path)
	}
	if h.denyGuard != nil {
		return h.denyGuard(path)
	}
	return nil
}

// AfterWrite implements tools.Guard.
func (h *hooks) AfterWrite(agent, path string) { h.record("guard.after:" + filepath.Base(path)) }

// Before implements tools.Snapshotter.
func (h *hooks) Before(agent, path string) error {
	h.record("snap:" + filepath.Base(path))
	if h.failSnap != nil {
		return h.failSnap(path)
	}
	return nil
}

func withHooks(env *tools.Env, h *hooks) {
	env.Perm = h
	env.Guard = h
	env.Snap = h
}

// lines builds n numbered lines "line 1\nline 2\n...".
func lines(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	return sb.String()
}
