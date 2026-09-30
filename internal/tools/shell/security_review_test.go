//go:build unix

package shell

// Security review repros for docs/reviews/security-robustness.md.
//
// TestSecReview_* are gated behind SLEIPNIR_REVIEW=1 and assert the SECURE behaviour, so they
// FAIL while the finding is open:
//
//	HEIMDALL_API_KEY=sk-review-canary SLEIPNIR_REVIEW=1 go test -count=1 -run TestSecReview ./internal/tools/shell
//
// TestSecSound_* are ungated regression checks for behaviour the review found sound.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

func secRevGate(t *testing.T) {
	t.Helper()
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1 (asserts the secure behaviour, fails while the finding is open)")
	}
}

// secRevPerm allows only the listed agents (a stand-in for a role gate or plan mode).
type secRevPerm struct{ allow map[string]bool }

func (p secRevPerm) Check(_ context.Context, r perm.Request) perm.Decision {
	if p.allow[r.Agent] {
		return perm.Decision{Allow: true}
	}
	return perm.Decision{Allow: false, Reason: "read-only role"}
}

type secRevShell struct {
	t    *testing.T
	m    *Manager
	reg  *tools.Registry
	root string
}

func secRevNew(t *testing.T) *secRevShell {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	t.Cleanup(m.Shutdown)
	reg := tools.NewRegistry()
	Register(reg, m)
	return &secRevShell{t: t, m: m, reg: reg, root: root}
}

func (h *secRevShell) run(agent string, p perm.Requester, tool string, in any) *tools.Result {
	h.t.Helper()
	tl, ok := h.reg.Get(tool)
	if !ok {
		h.t.Fatalf("no tool %s", tool)
	}
	raw, _ := json.Marshal(in)
	res, err := tl.Run(context.Background(), &tools.Call{ID: "c", Name: tool, Input: raw,
		Env: &tools.Env{Agent: agent, Role: "x", Cwd: h.root, Root: h.root, Perm: p}})
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

// S38: bash_output and bash_kill never consult the permission engine, and jobs are session-wide
// with sequential ids (job_1, job_2, ...). A read-only reviewer, or any agent in plan mode, can
// read every other agent's background output and kill their servers and test runs.
func TestSecReview_S38_JobToolsBypassPermissionsAndAreCrossAgent(t *testing.T) {
	secRevGate(t)
	h := secRevNew(t)
	writer := secRevPerm{allow: map[string]bool{"be-1": true}}
	reviewer := secRevPerm{allow: map[string]bool{}} // denies everything, like the read-only role gate
	res := h.run("be-1", writer, "bash", map[string]any{"command": "echo secret-from-be-1; sleep 30", "run_in_background": true})
	if res.IsError {
		t.Fatalf("start: %s", res.Text)
	}
	time.Sleep(200 * time.Millisecond)
	out := h.run("rv-1", reviewer, "bash_output", map[string]any{"id": "job_1"})
	if strings.Contains(out.Text, "secret-from-be-1") {
		t.Errorf("S38a: rv-1 (all permissions denied) read be-1's job output: %q", out.Text)
	}
	kill := h.run("rv-1", reviewer, "bash_kill", map[string]any{"id": "job_1"})
	if !kill.IsError {
		t.Errorf("S38b: rv-1 (all permissions denied) killed be-1's background job: %q", kill.Text)
	}
}

// S39: the model-visible environment is scrubbed by NAME (api_key|secret|token|password|
// passwd|credential). Plenty of credential-bearing variables do not match.
func TestSecReview_S39_EnvScrubIsANameHeuristic(t *testing.T) {
	secRevGate(t)
	base := []string{
		"DATABASE_URL=postgres://app:hunter2@db/prod", "REDIS_URL=redis://:hunter2@cache", "MYSQL_PWD=hunter2",
		"SENTRY_DSN=https://abc@o1.ingest.sentry.io/1", "SSH_AUTH_SOCK=/tmp/ssh-XXXX/agent.1", "STRIPE_KEY=sk_live_abc",
		"HEIMDALL_KEY=abc", "PRIVATE_KEY=-----BEGIN", "AUTHORIZATION=Bearer abc", "COOKIE=session=abc", "GH_PAT=ghp_abc",
		// controls that ARE scrubbed today
		"OPENAI_API_KEY=sk-x", "GITHUB_TOKEN=ghp_x", "AWS_SECRET_ACCESS_KEY=x",
	}
	env := commandEnv(base, "be-1", "/work", nil)
	have := map[string]bool{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		have[name] = true
	}
	for _, name := range []string{"DATABASE_URL", "REDIS_URL", "MYSQL_PWD", "SENTRY_DSN", "SSH_AUTH_SOCK", "STRIPE_KEY", "HEIMDALL_KEY", "PRIVATE_KEY", "AUTHORIZATION", "COOKIE", "GH_PAT"} {
		if have[name] {
			t.Errorf("S39: %s reaches every model-run command", name)
		}
	}
	for _, name := range []string{"OPENAI_API_KEY", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if have[name] {
			t.Errorf("control: %s should be scrubbed", name)
		}
	}
}

// S40: scrubbing the child's environment does not protect the harness's own key: on Linux any
// same-uid process can read /proc/<pid>/environ of its parent, and os.Unsetenv does not change
// it. The shell tool needs no exploit: `tr '\0' '\n' </proc/$PPID/environ`.
func TestSecReview_S40_ProviderKeyReadableFromProcEnviron(t *testing.T) {
	secRevGate(t)
	key := os.Getenv("HEIMDALL_API_KEY")
	if !strings.HasPrefix(key, "sk-review-") { // never run this against (or log) a real key
		t.Skip("start the test binary with HEIMDALL_API_KEY=sk-review-canary so the variable is part of the initial /proc/self/environ")
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
	h := secRevNew(t)
	all := secRevPerm{allow: map[string]bool{"be-1": true}}
	direct := h.run("be-1", all, "bash", map[string]any{"command": "env | grep -c HEIMDALL_API_KEY || true"})
	t.Logf("env | grep -c HEIMDALL_API_KEY -> %q (scrubbed as designed)", strings.TrimSpace(strings.Split(direct.Text, "\n")[0]))
	leak := h.run("be-1", all, "bash", map[string]any{"command": `tr '\0' '\n' < /proc/$PPID/environ | grep HEIMDALL_API_KEY`})
	t.Logf("tr < /proc/$PPID/environ printed the canary: %v", strings.Contains(leak.Text, key))
	if strings.Contains(leak.Text, key) {
		t.Errorf("S40: the harness's provider key is readable by a model-run command through /proc/$PPID/environ")
	}
}

// ---- sound behaviour ----------------------------------------------------------------

// A command cannot move the agent outside the project through cd or a symlink.
func TestSecSound_CwdCannotEscapeTheRoot(t *testing.T) {
	h := secRevNew(t)
	all := secRevPerm{allow: map[string]bool{"be-1": true}}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.root, "link")); err != nil {
		t.Fatal(err)
	}
	h.run("be-1", all, "bash", map[string]any{"command": "cd link"})
	res := h.run("be-1", all, "bash", map[string]any{"command": "pwd -P"})
	if strings.Contains(res.Text, filepath.Base(outside)) {
		t.Fatalf("agent escaped the project through a symlink: %q", res.Text)
	}
}

// Output is bounded in memory and control sequences are removed before reaching the model.
func TestSecSound_OutputIsSanitisedAndBounded(t *testing.T) {
	h := secRevNew(t)
	all := secRevPerm{allow: map[string]bool{"be-1": true}}
	res := h.run("be-1", all, "bash", map[string]any{"command": `printf '\033]52;c;ZXZpbA==\007\033[2Jhello\n'; head -c 3000000 /dev/zero | tr '\0' 'a'`})
	if strings.ContainsRune(res.Text, 0x1b) || strings.Contains(res.Text, "52;c") {
		t.Fatalf("escape sequences reached the model: %q", res.Text[:min(len(res.Text), 80)])
	}
	if len(res.Text) > 30_000 {
		t.Fatalf("result not truncated: %d bytes", len(res.Text))
	}
}
