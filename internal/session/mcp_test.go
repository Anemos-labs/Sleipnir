package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/mcp/mcptest"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/testutil"
)

// The test binary re-executes itself as an MCP server (see mcptest.HelperMain):
// real child processes, real pipes, no fixture binary. Otherwise it runs the tests and
// checks that no goroutine of the module is left behind: a closed session has stopped
// its agents, its servers and everything they started.
func TestMain(m *testing.M) {
	if mcptest.IsHelper() {
		mcptest.HelperMain()
		return
	}
	os.Exit(testutil.CheckLeaks(m))
}

// refServer is an MCP config entry that runs the reference server as a child of
// this test binary. extra distinguishes two otherwise identical entries.
func refServer(extra string) map[string]any {
	env := map[string]string{mcptest.EnvHelper: "1", "GORACE": "atexit_sleep_ms=0"}
	args := []string{"-test.run=^$"}
	if extra != "" {
		args = append(args, "-test.v="+extra) // a harmless flag that changes the entry, not the server
	}
	return map[string]any{"command": os.Args[0], "args": args, "env": env}
}

func writeConfig(t *testing.T, path string, cfg map[string]any) {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b))
}

type noticeSink struct {
	agent.NopSink
	mu   sync.Mutex
	msgs []string
}

func (n *noticeSink) Notice(_, _, msg string) {
	n.mu.Lock()
	n.msgs = append(n.msgs, msg)
	n.mu.Unlock()
}

func (n *noticeSink) all() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.msgs, "\n")
}

func hasSpec(s *session.Session, name string) bool {
	for _, sp := range s.Specs {
		if sp.Name == name {
			return true
		}
	}
	return false
}

func TestMCPToolsOfTheUsersOwnServerAreOfferedAndWork(t *testing.T) {
	repo := newRepo(t)
	var offered []string
	var toolResult string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		offered = c.Tools
		for _, m := range c.Messages {
			if m.Role == "tool" {
				toolResult = m.Content
				return mock.Reply{Text: "done"}
			}
		}
		return mock.Reply{Text: "calling", ToolCalls: []mock.ToolCall{call("c1", "mcp__ref__echo", map[string]any{"message": "hello from mcp"})}}
	})
	o := opts(t, repo, client, model)
	writeConfig(t, config.UserConfigPath(o.Home), map[string]any{"mcp": map[string]any{"ref": refServer("")}})
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !hasSpec(s, "mcp__ref__echo") {
		t.Fatalf("the frozen tool list lacks the server's tools: %v", s.MCPToolNames())
	}
	if st := s.MCPStatus(); len(st) != 1 || st[0].State != "ready" {
		t.Fatalf("status: %+v", st)
	}
	if _, err := s.Run(context.Background(), "use the echo tool"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range offered {
		found = found || n == "mcp__ref__echo"
	}
	if !found {
		t.Errorf("the model was not offered the MCP tool: %v", offered)
	}
	if !strings.Contains(toolResult, "hello from mcp") {
		t.Errorf("the tool result did not come back: %q", toolResult)
	}
	// session.start says what MCP contributed.
	s.Close()
	var info map[string]any
	for _, e := range readEvents(t, s.Dir) {
		if e.Type == "session.start" {
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			info, _ = d["mcp"].(map[string]any)
		}
	}
	if info == nil || info["tools"] == nil || info["hash"] == "" {
		t.Errorf("session.start does not record the MCP tool list: %v", info)
	}
}

func TestMCPNoMCPLeavesTheBuiltInToolList(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	writeConfig(t, config.UserConfigPath(o.Home), map[string]any{"mcp": map[string]any{"ref": refServer("")}})
	o.NoMCP = true
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if hasSpec(s, "mcp__ref__echo") || s.MCPStatus() != nil {
		t.Error("NoMCP must start no servers")
	}
}

// A repository's server can name any command; its tools land in every agent's
// prompt. It starts only after the user approves that exact entry.
func TestMCPProjectServersNeedApprovalThatIsRememberedPerEntry(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, "state"))
	writeConfig(t, config.ProjectConfigPath(repo), map[string]any{"mcp": map[string]any{"proj": refServer("")}})

	start := func(prompter perm.Prompter) (*session.Session, *noticeSink) {
		t.Helper()
		o := opts(t, repo, client, model)
		o.Home = home
		o.Prompter = prompter
		sink := &noticeSink{}
		o.Sink = sink
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s, sink
	}

	// Headless: no way to ask, so the server is left out and the person is told how to approve it.
	s, sink := start(nil)
	if hasSpec(s, "mcp__proj__echo") {
		t.Fatal("an unapproved project server started")
	}
	if !strings.Contains(sink.all(), "needs your approval") {
		t.Errorf("no notice about the missing approval: %q", sink.all())
	}
	s.Close()

	// Asked once, answered "no": still not started, and the question named the command.
	var asked []perm.Request
	s, _ = start(func(_ context.Context, r perm.Request) perm.Decision {
		asked = append(asked, r)
		return perm.Decision{Allow: false}
	})
	if hasSpec(s, "mcp__proj__echo") || len(asked) != 1 || asked[0].Tool != "mcp-server" || !strings.Contains(asked[0].Summary, os.Args[0]) {
		t.Fatalf("declined: started=%v asked=%+v", hasSpec(s, "mcp__proj__echo"), asked)
	}
	s.Close()

	// "Yes this time": starts, but is asked again next session.
	asked = nil
	s, _ = start(func(_ context.Context, r perm.Request) perm.Decision {
		asked = append(asked, r)
		return perm.Decision{Allow: true}
	})
	if !hasSpec(s, "mcp__proj__echo") {
		t.Fatal("an approved server did not start")
	}
	s.Close()
	s, _ = start(nil)
	if hasSpec(s, "mcp__proj__echo") {
		t.Fatal("a one-time approval was remembered")
	}
	s.Close()

	// "Project": remembered for that exact entry, so a headless session starts it...
	s, _ = start(func(_ context.Context, r perm.Request) perm.Decision {
		return perm.Decision{Allow: true, Remember: perm.ScopeProject}
	})
	s.Close()
	s, _ = start(nil)
	if !hasSpec(s, "mcp__proj__echo") {
		t.Fatal("a remembered approval was not honoured")
	}
	s.Close()

	// ...until a commit changes the entry: it asks again, and the headless session leaves it out.
	writeConfig(t, config.ProjectConfigPath(repo), map[string]any{"mcp": map[string]any{"proj": refServer("changed")}})
	s, _ = start(nil)
	if hasSpec(s, "mcp__proj__echo") {
		t.Fatal("an edited entry rode on the old approval")
	}
	s.Close()

	// The approvals live in the user's state directory, not in the repository.
	if _, err := os.Stat(filepath.Join(home, "state", "mcp-approvals.json")); err != nil {
		t.Errorf("approvals file: %v", err)
	}
}

// A project that is not trusted contributes no MCP servers at all (the config
// keys are ignored), whatever its .mcp.json says.
func TestMCPUntrustedProjectContributesNothing(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	writeConfig(t, config.ProjectConfigPath(repo), map[string]any{"mcp": map[string]any{"proj": refServer("")}})
	writeFile(t, filepath.Join(repo, ".mcp.json"), fmt.Sprintf(`{"mcpServers":{"shared":{"command":%q,"args":["-test.run=^$"],"env":{%q:"1"}}}}`, os.Args[0], mcptest.EnvHelper))
	o := opts(t, repo, client, model)
	o.TrustProject = false
	o.Prompter = func(context.Context, perm.Request) perm.Decision {
		t.Error("nothing should be asked")
		return perm.Decision{}
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if hasSpec(s, "mcp__proj__echo") || hasSpec(s, "mcp__shared__echo") {
		t.Error("an untrusted project started MCP servers")
	}
}

func TestMCPDotMcpJSONIsReadForTrustedProjectsAndNeedsApproval(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	writeFile(t, filepath.Join(repo, ".mcp.json"), fmt.Sprintf(`{"mcpServers":{"shared":{"command":%q,"args":["-test.run=^$"],"env":{%q:"1","GORACE":"atexit_sleep_ms=0"}}}}`, os.Args[0], mcptest.EnvHelper))
	var asked int
	o := opts(t, repo, client, model)
	o.Prompter = func(_ context.Context, r perm.Request) perm.Decision {
		asked++
		return perm.Decision{Allow: true}
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if asked != 1 || !hasSpec(s, "mcp__shared__echo") {
		t.Errorf(".mcp.json entry: asked %d times, tool offered %v", asked, hasSpec(s, "mcp__shared__echo"))
	}
}

// Every agent of a swarm sends the same tool list, MCP tools included.
func TestMCPSwarmAgentsShareOneToolList(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	lists := map[string]bool{}
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		lists[strings.Join(c.Tools, ",")] = true
		mu.Unlock()
		return mock.Reply{Text: "ok"}
	})
	o := opts(t, repo, client, model)
	o.Swarm = true
	writeConfig(t, config.UserConfigPath(o.Home), map[string]any{"mcp": map[string]any{"ref": refServer("")}})
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !hasSpec(s, "mcp__ref__echo") {
		t.Fatal("swarm session lacks the MCP tools")
	}
	if _, err := s.Run(context.Background(), "say hi"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lists) != 1 {
		t.Errorf("agents sent %d different tool lists", len(lists))
	}
}

// An MCP tool call is an action like any other: it asks first, and a rule in the
// user's configuration can allow a server's tools by name.
func TestMCPCallsAskUnlessARuleAllowsThem(t *testing.T) {
	run := func(rules []string) (asked []perm.Request, result string) {
		repo := newRepo(t)
		client, model := startMock(t, func(c *mock.Call) mock.Reply {
			for _, m := range c.Messages {
				if m.Role == "tool" {
					result = m.Content
					return mock.Reply{Text: "done"}
				}
			}
			return mock.Reply{Text: "calling", ToolCalls: []mock.ToolCall{call("c1", "mcp__ref__echo", map[string]any{"message": "hi"})}}
		})
		o := opts(t, repo, client, model)
		o.Mode = perm.ModeDefault
		o.Prompter = func(_ context.Context, r perm.Request) perm.Decision {
			asked = append(asked, r)
			return perm.Decision{Allow: false, Reason: "declined in the test"}
		}
		cfg := map[string]any{"mcp": map[string]any{"ref": refServer("")}}
		if len(rules) > 0 {
			cfg["permissions"] = map[string]any{"allow": rules}
		}
		writeConfig(t, config.UserConfigPath(o.Home), cfg)
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.Run(context.Background(), "echo hi"); err != nil {
			t.Fatal(err)
		}
		return asked, result
	}

	asked, result := run(nil)
	if len(asked) != 1 || asked[0].Tool != "mcp__ref__echo" || !strings.Contains(result, "declined") {
		t.Errorf("no rule: asked=%+v result=%q", asked, result)
	}
	for _, rule := range []string{"mcp__ref__echo", "mcp__ref__*"} {
		asked, result = run([]string{rule})
		if len(asked) != 0 || !strings.Contains(result, "hi") {
			t.Errorf("rule %q: asked=%+v result=%q", rule, asked, result)
		}
	}
}
