package session_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// fakeHost records what the sleipnir tool asks of the program the session runs in.
type fakeHost struct {
	mu       sync.Mutex
	commands []string
	queued   []string
	reloads  []string
}

func (h *fakeHost) Command(_ context.Context, line string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands = append(h.commands, line)
	return "answer of " + line, nil
}

func (h *fakeHost) Queue(line string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queued = append(h.queued, line)
	return "queued " + line, nil
}

func (h *fakeHost) Reload(why string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reloads = append(h.reloads, why)
	return "reloading", nil
}

// harnessSession starts a single-agent session (bypass mode, as opts makes it) and returns it with a caller of its sleipnir tool
// as the agent the person talks to.
func harnessSession(t *testing.T) (*session.Session, func(args map[string]any) *tools.Result) {
	t.Helper()
	t.Setenv("SLEIPNIR_HOME", "")
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, harnessCaller(t, s)
}

// harnessCaller calls the sleipnir tool of s as the agent the person talks to.
func harnessCaller(t *testing.T, s *session.Session) func(args map[string]any) *tools.Result {
	t.Helper()
	tool, ok := s.Registry.Get(session.HarnessToolName)
	if !ok {
		t.Fatalf("the standard profile has no %s tool: %v", session.HarnessToolName, specNames(s))
	}
	return func(args map[string]any) *tools.Result {
		t.Helper()
		b, _ := json.Marshal(args)
		res, err := tool.Run(context.Background(), &tools.Call{Name: session.HarnessToolName, Input: b, Env: &tools.Env{Agent: "main", Role: "worker", Perm: s.Perm, Cwd: s.Cwd()}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
}

func TestHarnessToolSchedulesGoals(t *testing.T) {
	s, call := harnessSession(t)
	res := call(map[string]any{"action": "schedule", "op": "add", "cron": "0 9 * * 1-5", "goal": "summarise yesterday's commits"})
	if res.IsError || !strings.Contains(res.Text, "scheduled j1") {
		t.Fatalf("add: %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(s.Home(), ".sleipnir", "schedule.json"))
	if err != nil || !strings.Contains(string(b), "summarise yesterday") || !strings.Contains(string(b), `"mode": "default"`) || !strings.Contains(string(b), s.Root()) {
		t.Fatalf("the job is not in the person's schedule file, with the default mode and this project: %s %v", b, err)
	}
	if res := call(map[string]any{"action": "schedule", "op": "add", "cron": "@daily", "goal": "x", "mode": "bypass"}); !res.IsError {
		t.Error("a run nobody watches is not given bypass")
	}
	if res := call(map[string]any{"action": "schedule", "op": "add", "cron": "every day", "goal": "x"}); !res.IsError {
		t.Error("a cron that does not parse is refused")
	}
	if res := call(map[string]any{"action": "schedule"}); !strings.Contains(res.Text, "j1") {
		t.Errorf("list: %s", res.Text)
	}
	if res := call(map[string]any{"action": "schedule", "op": "pause", "id": "j1"}); res.IsError {
		t.Errorf("pause: %s", res.Text)
	}
	if res := call(map[string]any{"action": "schedule", "op": "remove", "id": "j1"}); res.IsError {
		t.Errorf("remove: %s", res.Text)
	}
	if res := call(map[string]any{"action": "schedule"}); !strings.Contains(res.Text, "no scheduled goals") {
		t.Errorf("after remove: %s", res.Text)
	}
}

func TestHarnessToolChangesAskUnderThePermissionMode(t *testing.T) {
	s, call := harnessSession(t)
	s.Perm.SetMode(perm.ModePlan)
	res := call(map[string]any{"action": "schedule", "op": "add", "cron": "@daily", "goal": "x"})
	if !res.IsError || !strings.Contains(res.Text, "permission denied") {
		t.Fatalf("plan mode changes nothing: %+v", res)
	}
	if res := call(map[string]any{"action": "help"}); res.IsError {
		t.Errorf("help only looks: %s", res.Text)
	}
}

func TestHarnessToolIsTheManagersOnly(t *testing.T) {
	s, _ := harnessSession(t)
	tool, _ := s.Registry.Get(session.HarnessToolName)
	res, _ := tool.Run(context.Background(), &tools.Call{Input: json.RawMessage(`{"action":"help"}`), Env: &tools.Env{Agent: "w3", Role: "backend", Perm: perm.AllowAll{}}})
	if !res.IsError || !strings.Contains(res.Text, "manager") {
		t.Errorf("a worker = %+v", res)
	}
}

func TestHarnessToolRunsLinesThroughTheHost(t *testing.T) {
	s, call := harnessSession(t)
	if res := call(map[string]any{"action": "run", "line": "/goal ship the release"}); !res.IsError || !strings.Contains(res.Text, "interactive chat") {
		t.Errorf("without a host: %+v", res)
	}
	h := &fakeHost{}
	s.SetHost(h)
	if res := call(map[string]any{"action": "run", "line": "/cost"}); res.Text != "answer of /cost" {
		t.Errorf("a look line is answered at once: %+v", res)
	}
	if res := call(map[string]any{"action": "run", "line": "/goal ship the release"}); res.IsError || len(h.queued) != 1 {
		t.Errorf("a line that starts work waits for the turn: %+v %v", res, h.queued)
	}
	if res := call(map[string]any{"action": "run", "line": "/scan standard"}); !res.IsError || !strings.Contains(res.Text, "op enable") {
		t.Errorf("a line of a plugin that is off says how to turn it on: %+v", res)
	}
	for _, line := range []string{"/mode yolo", "/allow Bash(*)", "/budget off", "/model x/y", "/restart --trust-project"} {
		if res := call(map[string]any{"action": "run", "line": line}); !res.IsError || !strings.Contains(res.Text, "the person's") {
			t.Errorf("%s is the person's: %+v", line, res)
		}
	}
	if res := call(map[string]any{"action": "reload", "why": "the standup plugin"}); res.IsError || !slices.Equal(h.reloads, []string{"the standup plugin"}) {
		t.Errorf("reload: %+v %v", res, h.reloads)
	}
	if len(h.commands) != 1 || len(h.queued) != 1 {
		t.Errorf("host saw %v and %v", h.commands, h.queued)
	}
}

// TestHarnessToolInstallsAPluginOfItsOwn is the self-improvement loop: the agent writes a plugin, checks it, installs it (on for
// the person), the next start of the session has it, and installing a new version over it replaces it.
func TestHarnessToolInstallsAPluginOfItsOwn(t *testing.T) {
	s, call := harnessSession(t)
	src := filepath.Join(s.Cwd(), "plugins", "standup")
	write := func(rel, body string) {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugin.json", `{"id":"standup","api":1,"version":"1.0.0","description":"daily standup","requires":["skills","commands"]}`)
	write("skills/standup-notes/SKILL.md", "---\nname: standup-notes\ndescription: write the standup notes from yesterday's commits\n---\nRead git log.\n")
	write("commands/today.md", "Summarise today's plan. $ARGUMENTS\n")

	res := call(map[string]any{"action": "plugins", "op": "check", "source": "plugins/standup"})
	if res.IsError || !strings.Contains(res.Text, "no problems found") {
		t.Fatalf("check: %+v", res)
	}
	res = call(map[string]any{"action": "plugins", "op": "install", "source": "plugins/standup"})
	if res.IsError || !strings.Contains(res.Text, "installed standup 1.0.0") {
		t.Fatalf("install: %+v", res)
	}
	if res := call(map[string]any{"action": "help"}); !strings.Contains(res.Text, "standup") {
		t.Errorf("help says what reload would apply: %s", res.Text)
	}
	if res := call(map[string]any{"action": "plugins", "op": "list"}); !strings.Contains(res.Text, "standup restart") {
		t.Errorf("list: %s", res.Text)
	}

	write("plugin.json", `{"id":"standup","api":1,"version":"1.1.0","description":"daily standup","requires":["skills","commands"]}`)
	if res := call(map[string]any{"action": "plugins", "op": "install", "source": "plugins/standup"}); !res.IsError || !strings.Contains(res.Text, "already installed") || !strings.Contains(res.Text, "replace: true") {
		t.Errorf("a second install without replace is refused: %+v", res)
	}
	if res := call(map[string]any{"action": "plugins", "op": "install", "source": "plugins/standup", "replace": true}); res.IsError || !strings.Contains(res.Text, "1.1.0") {
		t.Fatalf("replace: %+v", res)
	}

	write("plugin.json", `{"id":"standup","api":1,"requires":["no-such-thing"]}`)
	if res := call(map[string]any{"action": "plugins", "op": "install", "source": "plugins/standup", "replace": true}); !res.IsError || !strings.Contains(res.Text, "no-such-thing") {
		t.Errorf("a plugin with an error is not installed: %+v", res)
	}

	// The next start of the session (what reload does) has it.
	o := s.Options()
	o.Dir = t.TempDir()
	s.Close()
	s2, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if st, ok := s2.Plugins().Get("standup"); !ok || st.State != "on" || st.Version != "1.1.0" {
		t.Fatalf("after the restart: %+v", st)
	}
	if !strings.Contains(s2.Shared.Text(), "write the standup notes") {
		t.Error("the plugin's skill is listed")
	}
	if _, ok := s2.Commands().Get("standup:today"); !ok {
		t.Error("the plugin's command is there")
	}
	if c, ok := s2.Commands().Get("today"); !ok || c.Name != "standup:today" {
		t.Errorf("the plugin's command answers to its own name too: %+v", c)
	}

	// Removing it takes the person's choice for it too, so the next start has no choice for a plugin that is not there.
	call2 := harnessCaller(t, s2)
	if res := call2(map[string]any{"action": "plugins", "op": "remove", "id": "standup"}); res.IsError {
		t.Fatalf("remove: %+v", res)
	}
	if b, _ := os.ReadFile(config.UserConfigPath(s2.Home())); strings.Contains(string(b), "standup") {
		t.Errorf("the person's configuration still names the removed plugin:\n%s", b)
	}
}

// A plugin installed from a git repository is checked as it was fetched: one written for a newer plugin API is not installed.
func TestHarnessToolChecksAPluginFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	s, call := harnessSession(t)
	repo := filepath.Join(t.TempDir(), "later.git")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "plugin.json"), []byte(`{"id":"later","api":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	res := call(map[string]any{"action": "plugins", "op": "install", "source": repo})
	if !res.IsError || !strings.Contains(res.Text, "not ready") || !strings.Contains(res.Text, "99") {
		t.Fatalf("install of a newer API's plugin: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(plugin.UserDir(s.Home()), "later")); err == nil {
		t.Error("the refused plugin was installed")
	}
}
