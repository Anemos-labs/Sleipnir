//go:build unix

package shell

// Security review repros for docs/SECURITY.md (S38-S40, tranche 2).
//
// They asserted the SECURE behaviour and were gated behind SLEIPNIR_REVIEW=1 while the findings
// were open; all three are fixed, so they are ordinary regression tests now (S38's and S39's fixes
// landed earlier, in jobs.go and env.go, S40's is internal/harden). TestSecSound_* are regression
// checks for behaviour the review found sound.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

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

// S38: bash_output and bash_kill never consulted the permission engine, and jobs are session-wide
// with sequential ids (job_1, job_2, ...). A read-only reviewer, or any agent in plan mode, could
// read every other agent's background output and kill their servers and test runs.
func TestSec_S38_JobToolsAreGatedByPermissionsAcrossAgents(t *testing.T) {
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
	// The owner is not asked again for its own job.
	if own := h.run("be-1", writer, "bash_output", map[string]any{"id": "job_1"}); own.IsError {
		t.Errorf("S38: the owner could not read its own job: %q", own.Text)
	}
}

// S39: the model-visible environment was scrubbed by NAME only (api_key|secret|token|password|
// passwd|credential); plenty of credential-bearing variables did not match. Names and values are
// now both checked (harden.LooksSecret).
func TestSec_S39_EnvScrubCoversCredentialNamesAndValues(t *testing.T) {
	base := []string{
		"DATABASE_URL=postgres://app:hunter2@db/prod", "REDIS_URL=redis://:hunter2@cache", "MYSQL_PWD=hunter2",
		"SENTRY_DSN=https://abc@o1.ingest.sentry.io/1", "SSH_AUTH_SOCK=/tmp/ssh-XXXX/agent.1", "STRIPE_KEY=sk_live_abc",
		"HEIMDALL_KEY=abc", "PRIVATE_KEY=-----BEGIN", "AUTHORIZATION=Bearer abc", "COOKIE=session=abc", "GH_PAT=ghp_abc",
		// controls that ARE scrubbed by name
		"OPENAI_API_KEY=sk-x", "GITHUB_TOKEN=ghp_x", "AWS_SECRET_ACCESS_KEY=x",
		// and variables that must keep reaching commands
		"PATH=/usr/bin", "HOME=/home/u", "GOPATH=/go", "KEYBOARD=us", "PWD=/stale",
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
	for _, name := range []string{"PATH", "HOME", "GOPATH", "KEYBOARD", "PWD"} {
		if !have[name] {
			t.Errorf("S39: the scrub also removed %s, which commands need", name)
		}
	}
	// The operator can still hand a credential to commands on purpose.
	env = commandEnv(base, "be-1", "/work", []string{"database_url", "GH_*"})
	have = map[string]bool{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		have[name] = true
	}
	for _, name := range []string{"DATABASE_URL", "GH_PAT"} {
		if !have[name] {
			t.Errorf("S39: PassEnv did not let %s through", name)
		}
	}
	if have["REDIS_URL"] {
		t.Error("S39: PassEnv let an unlisted credential through")
	}
}

// S40: scrubbing the child's environment does not protect the harness's own key: on Linux any
// same-uid process can read /proc/<pid>/environ of its parent, and os.Unsetenv does not change
// it. The shell tool needs no exploit: `tr '\0' '\n' </proc/$PPID/environ`.
//
// The fix is in internal/harden (harden.Process, the first call of main: the values of credential
// variables are erased from the kernel's copy of the environment and the process is made
// non-dumpable). The harness of the repro is a child copy of this test binary, so that the key is
// part of its initial environment, as when a user exports it before starting sleipnir. The
// control run is a harness that never called harden.Process; it shows the repro can see the leak.
// (The flag itself is tested, as an unprivileged user, in internal/harden.)
func TestSec_S40_ProviderKeyIsNotReadableFromProcEnviron(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
	key := s40Key
	control := runS40Harness(t, "control")
	if !strings.Contains(control.Leak, key) {
		t.Skipf("the control run (no harden.Process) could not read its parent's environment either (%q): nothing to show here", control.Leak)
	}
	h := runS40Harness(t, "hardened")
	t.Logf("env | grep -c HEIMDALL_API_KEY -> %q (scrubbed as designed)", strings.TrimSpace(strings.Split(h.Direct, "\n")[0]))
	t.Logf("tr < /proc/$PPID/environ printed the canary: %v", strings.Contains(h.Leak, key))
	if strings.Contains(h.Leak, key) {
		t.Errorf("S40: the harness's provider key is readable by a model-run command through /proc/$PPID/environ")
	}
	if h.Direct == "" || !strings.Contains(h.Direct, "0") {
		t.Errorf("S40: the shell tool no longer scrubs the key from the command's own environment: %q", h.Direct)
	}
	// The key is still in the harness's own environment: hardening hides it from others, it does not take it from the harness.
	if !h.OwnEnv {
		t.Error("S40: the harness lost its own key")
	}
}

// s40Key is the canary of the review: it must start with "sk-review-" so that it can never be a real key.
const s40Key = "sk-review-canary"

const s40Helper = "SLEIPNIR_S40_HELPER"

type s40Report struct {
	Direct string `json:"direct"`
	Leak   string `json:"leak"`
	OwnEnv bool   `json:"own_env"`
	// Secret is whether harden.Secret still returns the key; Inherited what a child started with
	// the harness's own environment (git, a hook, a verifier) sees of it.
	Secret    bool   `json:"secret"`
	Inherited string `json:"inherited"`
}

// TestSecS40Harness is the harness process of the repro; it does nothing unless it is started as one.
func TestSecS40Harness(t *testing.T) {
	mode := os.Getenv(s40Helper)
	if mode == "" {
		t.Skip("helper process for TestSec_S40_ProviderKeyIsNotReadableFromProcEnviron")
	}
	switch mode {
	case "hardened":
		harden.Process() // the environment erasure and the non-dumpable flag
	case "moved":
		harden.Process(harden.MoveKeys()) // what main does before anything else
	}
	h := secRevNew(t)
	all := secRevPerm{allow: map[string]bool{"be-1": true}}
	direct := h.run("be-1", all, "bash", map[string]any{"command": "env | grep -c HEIMDALL_API_KEY || true"})
	leak := h.run("be-1", all, "bash", map[string]any{"command": `tr '\0' '\n' < /proc/$PPID/environ | grep HEIMDALL_API_KEY`})
	inherited, _ := exec.Command("printenv", "HEIMDALL_API_KEY").Output() // nil Env: inherits ours, like git or a hook
	b, err := json.Marshal(s40Report{
		Direct: direct.Text, Leak: leak.Text, OwnEnv: os.Getenv("HEIMDALL_API_KEY") == s40Key,
		Secret: harden.Secret("HEIMDALL_API_KEY") == s40Key, Inherited: strings.TrimSpace(string(inherited)),
	})
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("\nS40-REPORT " + string(b) + "\n")
}

// With MoveKeys, which is what main does, the key is in no environment at all: not the kernel's copy, not the
// harness's own, so no command it starts by inheriting (git, hooks, verifiers, MCP servers) receives it. The harness
// still has it, through harden.Secret.
func TestSec_S40_AHeldKeyIsInNoEnvironmentAndStillReadable(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
	if _, err := exec.LookPath("printenv"); err != nil {
		t.Skip("no printenv")
	}
	h := runS40Harness(t, "moved")
	if strings.Contains(h.Leak, s40Key) {
		t.Errorf("S40: the key is readable through /proc/$PPID/environ of a harness that holds it")
	}
	if h.OwnEnv || h.Inherited != "" {
		t.Errorf("S40: the key is still in the environment (os.Getenv: %v, inherited by a child: %q)", h.OwnEnv, h.Inherited)
	}
	if !h.Secret {
		t.Error("S40: the harness lost its own key")
	}
	if h.Direct == "" || !strings.Contains(h.Direct, "0") {
		t.Errorf("S40: a command's own environment holds the key: %q", h.Direct)
	}
}

func runS40Harness(t *testing.T, mode string) s40Report {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecS40Harness$", "-test.v")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), s40Helper + "=" + mode, "HEIMDALL_API_KEY=" + s40Key}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness (%s) failed: %v\n%s", mode, err, out)
	}
	_, line, ok := strings.Cut(string(out), "\nS40-REPORT ")
	if !ok {
		t.Fatalf("harness (%s) printed no report (bash missing?):\n%s", mode, out)
	}
	var r s40Report
	if err := json.Unmarshal([]byte(strings.SplitN(line, "\n", 2)[0]), &r); err != nil {
		t.Fatalf("bad report: %v\n%s", err, line)
	}
	return r
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
	// The whole path, not its last element: temp directories are named with a random number
	// ("...Escape1204129002/002"), and the base name of one ("002") can be a substring of another's.
	resolved, _ := filepath.EvalSymlinks(outside)
	for _, where := range []string{outside, resolved} {
		if where != "" && strings.Contains(res.Text, where) {
			t.Fatalf("agent escaped the project through a symlink: %q", res.Text)
		}
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
