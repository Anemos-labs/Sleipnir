package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// gitRepo makes a project under git with one commit, and points the config at the mock.
func isoGitRepo(t *testing.T, providerURL string) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"providers":{"mock":{"base_url":%q}},"models":{"default":"mock/mock-1"}}`, providerURL)
	if err := os.WriteFile(filepath.Join(repo, ".sleipnir", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	run("add", "-A")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	// The session keeps its state and its trees under these, whatever the developer has set.
	t.Setenv("SLEIPNIR_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return repo
}

var (
	cliWho  = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	cliTask = regexp.MustCompile(`task (T\d+)`)
)

// A one-worker swarm through the command: the flags reach the session, the worker gets a
// tree, its work is merged and applied to the checkout, and the result line says so (in
// JSON, under "integration").
func TestRunSwarmWithWorktreeIsolationAppliesAndReportsTheResult(t *testing.T) {
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		var role, tid string
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Role != "user" {
				continue
			}
			if m := cliWho.FindStringSubmatch(c.Messages[i].Content); m != nil && role == "" {
				role = m[2]
			}
			if m := cliTask.FindStringSubmatch(c.Messages[i].Content); m != nil && tid == "" {
				tid = m[1]
			}
		}
		n := 0
		for _, m := range c.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		call := func(id, name string, args any) mock.ToolCall {
			b, _ := json.Marshal(args)
			return mock.ToolCall{ID: id, Name: name, Args: string(b)}
		}
		if role == "manager" {
			switch n {
			case 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("m1", "task", map[string]any{"action": "create", "title": "Add notes.txt", "role": "backend", "files": []string{"notes.txt"}}),
					call("m2", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("m3", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 60}),
				}}
			case 1:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("m4", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "notes.txt added"}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "on it", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "notes.txt", "content": "hello\n"})}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("w2", "task", map[string]any{"action": "done", "id": tid, "text": "added notes.txt"})}}
		}
		return mock.Reply{Text: "summary"}
	})
	ts := srv.Start()
	defer ts.Close()
	repo := isoGitRepo(t, ts.URL)
	head, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()

	var err error
	out := capture(t, func() {
		err = cmdRun(context.Background(), []string{"--cwd", repo, "--mode", "bypass", "--no-web", "--trust-project", "--json",
			"--swarm", "2", "--isolation", "worktree", "add notes.txt"})
	})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var result map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && ev["type"] == "result" {
			result = ev
		}
	}
	if result == nil {
		t.Fatalf("no result line:\n%s", out)
	}
	integ, _ := result["integration"].(map[string]any)
	if integ == nil || integ["applied"] != true || fmt.Sprint(integ["files"]) != "[notes.txt]" || !strings.Contains(fmt.Sprint(integ["message"]), "uncommitted") {
		t.Fatalf("the result line's integration: %v", result["integration"])
	}
	if b, err := os.ReadFile(filepath.Join(repo, "notes.txt")); err != nil || string(b) != "hello\n" {
		t.Fatalf("notes.txt: %v %q", err, b)
	}
	if now, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output(); string(now) != string(head) {
		t.Error("HEAD moved without --commit")
	}
	if out, err := exec.Command("git", "-C", repo, "branch", "--list", "sleipnir/*").Output(); err != nil || !bytes.Contains(out, []byte("/_resume")) || !bytes.Contains(out, []byte("/_integration")) {
		t.Errorf("recovery references are missing: %s (%v)", out, err)
	}
}

// The flags say what they mean: isolation applies to swarms, and a commit needs it.
func TestIsolationFlagsAreChecked(t *testing.T) {
	repo := isoGitRepo(t, "http://127.0.0.1:1")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--isolation", "worktree", "hello"}, "swarm sessions"},
		{[]string{"--swarm", "2", "--isolation", "bogus", "hello"}, `isolation "bogus"`},
		{[]string{"--swarm", "2", "--commit", "hello"}, "commit needs worktree isolation"},
		{[]string{"--swarm", "2", "--isolation", "none", "--commit", "hello"}, "commit needs worktree isolation"},
	} {
		args := append([]string{"--cwd", repo, "--mode", "bypass", "--no-web", "--trust-project", "--quiet"}, tc.args...)
		err := cmdRun(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error %v, want one mentioning %q", tc.args, err, tc.want)
		}
	}
}

// --mailman is a tri-state: given, it turns the mailman on; --mailman=false turns it off for
// one run whatever the configuration says; left out, it leaves the configuration in charge.
func TestMailmanFlagIsATriState(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "unset"},
		{[]string{"--mailman"}, "true"},
		{[]string{"--mailman=true"}, "true"},
		{[]string{"--mailman=false"}, "false"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		get := mailmanFlag(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		got := "unset"
		if v := get(); v != nil {
			got = fmt.Sprint(*v)
		}
		if got != tc.want {
			t.Errorf("%v: %s, want %s", tc.args, got, tc.want)
		}
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	mailmanFlag(fs)
	fs.SetOutput(io.Discard)
	if err := fs.Parse([]string{"--mailman=maybe"}); err == nil {
		t.Error("a value that is not a boolean must be rejected")
	}
}

// What the person is told at the end of an isolated run: what reached the checkout may
// be silenced with --quiet; a result that did not reach it never is, and names the one
// command that gets it.
func TestPrintIntegration(t *testing.T) {
	applied := &swarm.IntegrationReport{Applied: true, Files: []string{"a.go"}, Message: "Applied 1 file(s) to your working tree"}
	stuck := &swarm.IntegrationReport{Applied: false, BranchKept: true, Message: "The result was NOT applied to your checkout (x). To get it: git merge sleipnir/s/_integration"}
	kept := &swarm.IntegrationReport{Applied: true, Kept: []string{"be-2 (branch sleipnir/s/be-2)"}, Message: "Applied 1 file(s). 1 worker tree(s) still hold work"}
	for _, tc := range []struct {
		name  string
		rep   *swarm.IntegrationReport
		quiet bool
		want  string
	}{
		{"nothing to say", nil, false, ""},
		{"applied", applied, false, "integration: Applied 1 file(s) to your working tree\n"},
		{"applied and quiet", applied, true, ""},
		{"not applied and quiet", stuck, true, "integration: The result was NOT applied to your checkout (x). To get it: git merge sleipnir/s/_integration\n"},
		{"work was left in a tree, quiet", kept, true, "integration: Applied 1 file(s). 1 worker tree(s) still hold work\n"},
	} {
		var b bytes.Buffer
		printIntegration(&b, tc.rep, tc.quiet)
		if b.String() != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, b.String(), tc.want)
		}
	}
}

// `sleipnir config` says what the swarm's isolation is and which layer set it.
func TestConfigShowsTheSwarmIsolationAndWhereItCameFrom(t *testing.T) {
	cfg := config.Defaults()
	if got := swarmSettings(cfg, nil); len(got) != 2 || !strings.HasPrefix(got[0], "swarm.isolation: none (default)") || !strings.HasPrefix(got[1], "swarm.mailman: off (default)") {
		t.Fatalf("defaults: %q", got)
	}
	cfg.Swarm.Isolation = config.IsolationWorktree
	rep := &config.Report{Origins: map[string]string{"swarm.isolation": "/home/u/.sleipnir/config.json"}}
	cfg.Swarm.Mailman = true
	rep.Origins["swarm.mailman"] = "env:SLEIPNIR_SWARM_MAILMAN"
	got := swarmSettings(cfg, rep)
	if len(got) != 2 || !strings.HasPrefix(got[0], "swarm.isolation: worktree (/home/u/.sleipnir/config.json)") || !strings.Contains(got[0], "own git worktree") ||
		!strings.HasPrefix(got[1], "swarm.mailman: on (env:SLEIPNIR_SWARM_MAILMAN)") || !strings.Contains(got[1], "--role-model mailman=") {
		t.Fatalf("worktree: %q", got)
	}
	// The real thing, through the loader: a project file that sets it.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sleipnir", "config.json"), []byte(`{"swarm":{"isolation":"worktree"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, lrep, err := config.Load(config.LoadOpts{Cwd: repo, Root: repo, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	line := swarmSettings(loaded, lrep)[0]
	if !strings.HasPrefix(line, "swarm.isolation: worktree (") || strings.Contains(line, "(default)") || !strings.Contains(line, "config.json") {
		t.Fatalf("loaded: %q", line)
	}
}
