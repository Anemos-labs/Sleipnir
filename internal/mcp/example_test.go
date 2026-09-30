package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/reee344/sleipnir/internal/mcp"
	"github.com/reee344/sleipnir/internal/mcp/mcptest"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// onlyEcho allows exactly one MCP tool: what a permission engine with a single
// allow rule ("mcp__demo__echo") would do.
type onlyEcho struct{}

func (onlyEcho) Check(_ context.Context, r perm.Request) perm.Decision {
	if r.Tool == "mcp__demo__echo" {
		return perm.Decision{Allow: true, Reason: "allowed by rule mcp__demo__echo"}
	}
	return perm.Decision{Reason: "ask the user"}
}

// Example connects to a server, freezes its tools and runs one of them.
func Example() {
	// A reference server stands in for a real one.
	srv := httptest.NewServer(mcptest.New().HTTP(mcptest.HTTPOptions{JSONOnly: true}))
	defer srv.Close()

	// User configuration is trusted; loopback needs an explicit allow_private.
	servers, err := mcp.ParseWith(map[string]json.RawMessage{
		"demo": json.RawMessage(`{"url": "` + srv.URL + `/mcp", "allow_private": true}`),
	}, mcp.ParseOptions{Scope: mcp.ScopeUser})
	if err != nil {
		fmt.Println(err)
	}

	mgr := mcp.NewManager(mcp.Options{Servers: servers})
	defer mgr.Close()
	if err := mgr.Start(context.Background()); err != nil {
		fmt.Println("start:", err)
	}

	// Freeze the tool list: sorted, sanitised, budgeted, hashed.
	snap := mgr.Snapshot()
	reg := tools.NewRegistry()
	snap.Register(reg)
	specs, _ := reg.Specs()
	names := snap.Names()
	fmt.Println("tools:", len(specs) == len(names), names[0] < names[1], strings.HasPrefix(names[0], "mcp__demo__"))

	// Run one. Errors of every kind come back as results the model can read.
	echo, _ := reg.Get("mcp__demo__echo")
	env := (&tools.Env{Agent: "main", Perm: onlyEcho{}}).Defaults()
	res, _ := echo.Run(context.Background(), &tools.Call{Input: json.RawMessage(`{"message":"hello"}`), Env: env})
	fmt.Println("echo:", res.Text, res.IsError)

	res, _ = mustGet(reg, "mcp__demo__big").Run(context.Background(), &tools.Call{Input: json.RawMessage(`{}`), Env: env})
	fmt.Println("big:", res.IsError, res.Text)
	// Output:
	// tools: true true true
	// echo: hello false
	// big: true permission denied for mcp__demo__big: ask the user
}

func mustGet(r *tools.Registry, name string) tools.Tool {
	t, _ := r.Get(name)
	return t
}

// ExampleParseWith reads a project's .mcp.json: it works, but a repository
// cannot make its own servers trusted.
func ExampleParseWith() {
	doc := map[string]json.RawMessage{
		"mcpServers": json.RawMessage(`{
			"docs":   {"url": "https://docs.example.com/mcp", "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"}},
			"github": {"command": "gh-mcp", "args": ["--stdio"], "trust": true}
		}`),
	}
	servers, issues := mcp.ParseWith(doc, mcp.ParseOptions{Scope: mcp.ScopeProject})
	for _, name := range []string{"docs", "github"} {
		s := servers[name]
		fmt.Printf("%s: %s trusted=%v refs=%v\n", name, s.EffectiveType(), s.Trust, s.EnvRefs())
	}
	for _, is := range issues {
		fmt.Println(is)
	}
	// Output:
	// docs: http trusted=false refs=[DOCS_TOKEN]
	// github: stdio trusted=false refs=[]
	// mcp config: server "github", field "trust": cleared: a project-scoped file cannot vouch for its own servers; approve the server instead
}

// ExampleOptions_approve shows the approval hook: it sees the entry as written,
// and can ask the user about exactly what will run and which variables it wants.
func ExampleOptions_approve() {
	servers, _ := mcp.ParseWith(map[string]json.RawMessage{
		"tools": json.RawMessage(`{"command": "npx", "args": ["-y", "some-mcp-server"], "env": {"API_KEY": "${SERVICE_KEY}"}}`),
	}, mcp.ParseOptions{Scope: mcp.ScopeProject})
	mgr := mcp.NewManager(mcp.Options{
		Servers: servers,
		Approve: func(c mcp.ServerConfig) bool {
			fmt.Printf("run %v? wants %v\n", c, c.EnvRefs())
			return false // the user said no
		},
	})
	defer mgr.Close()
	err := mgr.Start(context.Background())
	fmt.Println("refused:", strings.Contains(fmt.Sprint(err), "not approved"))
	// Output:
	// run stdio(command="npx" args=2 env=[API_KEY] scope=project)? wants [SERVICE_KEY]
	// refused: true
}
