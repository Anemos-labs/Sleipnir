package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func init() { extraCommands["mcp"] = cmdMCP }

const mcpUsage = `usage: sleipnir mcp <command> [flags]

Tool servers (the Model Context Protocol). Servers are configured under "mcp" in
your user configuration (trusted) or a project's (.sleipnir/config.json or
.mcp.json; only read with --trust-project, and each entry needs your approval).

commands:
  list [--trust-project]              the servers a session here would consider, where each came from and whether it may start
  approve NAME [--yes]                remember a project entry for this project (shows what it would run and asks first)
  revoke NAME                         forget an approval
  test [NAME...] [--trust-project]    start the servers and list the tools they offer (project entries still need approval)
`

// cmdMCP inspects and manages MCP servers without starting a session.
func cmdMCP(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, mcpUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("mcp "+sub, flag.ContinueOnError)
	trust := fs.Bool("trust-project", false, "read the project's configuration (.sleipnir/config.json, .mcp.json)")
	yes := fs.Bool("yes", false, "approve without asking (scripts: you have read `sleipnir mcp list`)")
	cwd := fs.String("cwd", "", "working directory (default: current)")
	names, err := parseInterspersed(fs, rest)
	if err != nil {
		return err
	}
	dir := *cwd
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	root, _ := config.FindRoot(dir)
	if root == "" {
		root = dir
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	home, _ := os.UserHomeDir()
	switch sub {
	case "list":
		return mcpList(os.Stdout, home, root, *trust)
	case "approve", "revoke":
		if len(names) != 1 {
			return fmt.Errorf("mcp %s: exactly one server name is required", sub)
		}
		return mcpApproval(os.Stdin, os.Stderr, home, root, sub == "approve", names[0], *yes)
	case "test":
		return mcpTest(ctx, os.Stdout, os.Stderr, dir, names, *trust)
	}
	fmt.Fprint(os.Stderr, mcpUsage)
	return fmt.Errorf("mcp: unknown command %q", sub)
}

func mcpList(w io.Writer, home, root string, trust bool) error {
	entries, issues, err := session.MCPEntries(home, root, trust)
	if err != nil {
		return err
	}
	for _, is := range issues {
		fmt.Fprintln(os.Stderr, "warning:", is.Error())
	}
	if len(entries) == 0 {
		fmt.Fprintln(w, `no MCP servers configured (add them under "mcp" in ~/.sleipnir/config.json)`)
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tFROM\tSTATE\tWHAT IT DOES")
	for _, e := range entries {
		from, state := "your config", "trusted"
		if !e.Trusted {
			from = "this project"
			state = "needs approval"
			if e.Approved {
				state = "approved"
			}
		}
		if e.Config.Disabled {
			state = "disabled"
		}
		what := e.Config.Describe()
		if e.Trusted {
			what = e.Config.String() // your own entry: no arguments in listings
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, from, state, what)
	}
	return tw.Flush()
}

func mcpApproval(in io.Reader, out io.Writer, home, root string, approve bool, name string, yes bool) error {
	entries, _, err := session.MCPEntries(home, root, true)
	if err != nil {
		return err
	}
	var e *session.MCPEntry
	for i := range entries {
		if entries[i].Name == name {
			e = &entries[i]
		}
	}
	if e == nil {
		return fmt.Errorf("no project MCP server %q (see: sleipnir mcp list --trust-project)", name)
	}
	if e.Trusted {
		return fmt.Errorf("%q is your own entry: it needs no approval", name)
	}
	ap := session.OpenMCPApprovals(home)
	if !approve {
		if err := ap.Revoke(root, e.Fingerprint); err != nil {
			return err
		}
		fmt.Fprintf(out, "approval for %q forgotten\n", name)
		return nil
	}
	fmt.Fprintf(out, "%s from %s\n  it %s\n", name, root, e.Config.Describe())
	if !yes {
		fmt.Fprint(out, "approve this exact entry for this project? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not approved")
		}
	}
	if err := ap.Approve(root, e.Fingerprint, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "approved %q for this project (the approval ends when its entry changes)\n", name)
	return nil
}

// mcpTest starts a throw-away session (no model calls) and reports what the
// servers offered.
func mcpTest(ctx context.Context, w, errw io.Writer, dir string, names []string, trust bool) error {
	// No model is involved: a stand-in provider satisfies the assembly and is never called.
	m := cost.Model{ID: "mcp-test", ContextTokens: 100_000, MaxOutput: 4096}
	o := session.Options{Cwd: dir, TrustProject: trust, Offline: true, NoRecon: true, NoWeb: true, Provider: noModel{}, ModelInfo: &m, Model: m.ID,
		Sink: session.NewTextSink(io.Discard, errw, "main", false)}
	o.Prompter = session.TerminalPrompter(bufio.NewReader(os.Stdin), errw)
	s, err := session.New(ctx, o)
	if err != nil {
		return err
	}
	defer s.Close()
	status := s.MCPStatus()
	if len(status) == 0 {
		fmt.Fprintln(w, "no MCP servers started")
		return nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	bad := 0
	for _, st := range status {
		if len(want) > 0 && !want[st.Name] {
			continue
		}
		line := fmt.Sprintf("%s: %s", st.Name, st.State)
		if st.ServerName != "" {
			line += fmt.Sprintf(" (%s %s)", st.ServerName, st.ServerVersion)
		}
		if st.Error != "" {
			line += " — " + st.Error
			bad++
		}
		fmt.Fprintln(w, line)
	}
	tools := s.MCPToolNames()
	sort.Strings(tools)
	for _, t := range tools {
		fmt.Fprintln(w, "  "+t)
	}
	fmt.Fprintf(w, "%d tools in the frozen list\n", len(tools))
	if bad > 0 {
		return fmt.Errorf("%d server(s) not ready", bad)
	}
	return nil
}

// noModel is the provider of a session that never talks to a model.
type noModel struct{}

func (noModel) Profile() provider.Profile {
	return provider.Profile{Name: "none", Dialect: "openai-chat"}
}
func (noModel) Do(context.Context, *provider.Request, func(provider.Event)) (*provider.Response, error) {
	return nil, errors.New("no model in this session")
}

// printMCP is /mcp in chat: the servers, their state and how many tools each
// contributed to this session's frozen list.
func printMCP(s *session.Session, w io.Writer) {
	status := s.MCPStatus()
	if len(status) == 0 {
		fmt.Fprintln(w, `no MCP servers in this session (configure them under "mcp"; see: sleipnir mcp list)`)
		return
	}
	names := s.MCPToolNames()
	for _, st := range status {
		n := 0
		for _, t := range names {
			if strings.HasPrefix(t, "mcp__"+st.Name+"__") {
				n++
			}
		}
		line := fmt.Sprintf("  %-16s %-10s %d tools in this session", st.Name, st.State, n)
		if st.Error != "" {
			line += " — " + st.Error
		}
		fmt.Fprintln(w, line)
	}
	if ps := s.MCPPrompts(); len(ps) > 0 {
		fmt.Fprintln(w, "prompts you can run:")
		for _, p := range ps {
			fmt.Fprintf(w, "  %-32s %s\n", p.Command, firstText(p.Description, 70))
		}
	}
	fmt.Fprintln(w, "the tool list is fixed for the session; /mcp reconnect NAME restarts a server that gave up")
}
