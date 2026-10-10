package settings

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/mcp"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Outcome lines of the MCP actions (decision D-08: a session's tool list is fixed when it starts, so an approval or a revocation
// takes effect when the team starts again).
const (
	mcpApproved = "approved: it starts when the team starts again (the tool list of a running session is fixed)"
	mcpRevoked  = "revoked: it is not started when the team starts again (the tool list of a running session is fixed)"
)

// mcpPrompt is a prompt an MCP server offers, as a slash command.
type mcpPrompt struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
}

// mcpServer is wire.MCPServer with what the page needs to confirm an approval (the entry's fingerprint and the confirmation scope
// of approving exactly it) and the server's prompts.
type mcpServer struct {
	wire.MCPServer
	Fingerprint  string      `json:"fingerprint,omitempty"`
	ConfirmScope string      `json:"confirmScope,omitempty"`
	Prompts      []mcpPrompt `json:"prompts,omitempty"`
}

// mcpView is wire.MCPView with the project root (the scope of approvals), the problems found reading the entries, and the extended
// servers.
type mcpView struct {
	Servers     []mcpServer `json:"servers"`
	SessionNote string      `json:"sessionNote"`
	FrozenTools []string    `json:"frozenTools,omitempty"`
	Root        string      `json:"root"`
	Issues      []string    `json:"issues,omitempty"`
}

// mcpEntries lists the MCP entries a session of the tab's project would consider, project entries included (a project entry is
// listed even when the session does not use the project's files, so that it can be approved ahead).
func (s *service) mcpEntries(tc *tabCtx) ([]session.MCPEntry, []mcp.Issue, error) {
	return session.MCPEntries(s.homeOf(tc), tc.root, true)
}

// handleMCPView is GET /api/sessions/{id}/mcp: every configured server with where it came from, whether it may start, its state in
// the tab's session, its tools and prompts; environment values, header values and secret-shaped arguments are withheld (the names of
// the variables it reads are listed).
func (s *service) handleMCPView(w http.ResponseWriter, r *http.Request) {
	tc, err := s.tabOf(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	entries, issues, err := s.mcpEntries(tc)
	if err != nil {
		reply(w, nil, fail(http.StatusUnprocessableEntity, "rejected", "the configuration does not load: see Config layers"))
		return
	}
	_, rep, _ := config.Load(config.LoadOpts{Root: tc.root, Cwd: tc.cwd, Home: s.homeOf(tc)})
	status := map[string]mcp.ServerStatus{}
	var tools []string
	var prompts []mcp.PromptEntry
	if tc.sess != nil {
		for _, st := range tc.sess.MCPStatus() {
			status[st.Name] = st
		}
		tools = tc.sess.MCPToolNames()
		prompts = tc.sess.MCPPrompts()
	}
	v := mcpView{Servers: []mcpServer{}, Root: s.display(tc.root), FrozenTools: tools}
	for _, is := range issues {
		v.Issues = append(v.Issues, cleanText(is.Error()))
	}
	var started []string
	for _, e := range entries {
		srv := s.mcpServerOf(tc, e, rep, status, tools, prompts)
		if srv.State == "running" {
			started = append(started, e.Name)
		}
		v.Servers = append(v.Servers, srv)
	}
	v.SessionNote = sessionNote(tc, started, len(tools))
	reply(w, v, nil)
}

// mcpServerOf describes one entry for the page.
func (s *service) mcpServerOf(tc *tabCtx, e session.MCPEntry, rep *config.Report, status map[string]mcp.ServerStatus, tools []string, prompts []mcp.PromptEntry) mcpServer {
	c := e.Config
	red := config.MCPEntry(c)
	srv := mcpServer{MCPServer: wire.MCPServer{
		Name: e.Name, Transport: c.EffectiveType(), Tools: []string{}, Approved: e.Approved, EnvRefs: c.EnvRefs(),
	}}
	if cmd, ok := red["command"].(string); ok {
		srv.Command = cmd
	}
	if args, ok := red["args"].([]string); ok {
		srv.Args = args
	}
	if u, ok := red["url"].(string); ok && srv.Command == "" {
		srv.Command = u
	}
	shown := c
	shown.Args = config.RedactArgs(c.Args)
	srv.Describe = cleanText(shown.Describe())
	switch {
	case e.Trusted:
		srv.From = "your config"
		srv.Origin = "user config"
		if rep != nil {
			if src := rep.Origins["mcp."+e.Name]; src != "" {
				srv.Origin = "user config (" + s.display(src) + ")"
			}
		}
	default:
		srv.From = "this project"
		srv.Origin = "project .mcp.json"
		if rep != nil {
			if src := rep.Origins["mcp."+e.Name]; src != "" {
				srv.Origin = rep.LayerKind(src) + " config (" + s.ruleFile(config.RuleOrigin{Layer: rep.LayerKind(src), File: src}, tc) + ")"
			}
		}
		srv.Fingerprint = e.Fingerprint
		srv.ConfirmScope = mcpScope(tc.root, e.Name, e.Fingerprint)
	}
	prefix := "mcp__" + e.Name + "__"
	for _, t := range tools {
		if strings.HasPrefix(t, prefix) {
			srv.Tools = append(srv.Tools, strings.TrimPrefix(t, prefix))
		}
	}
	for _, p := range prompts {
		if p.Server == e.Name {
			srv.Prompts = append(srv.Prompts, mcpPrompt{Command: p.Command, Description: provider.SanitizeText(p.Description, 160)})
		}
	}
	st, running := status[e.Name]
	switch {
	case c.Disabled:
		srv.State = "disabled"
	case !e.Trusted && !e.Approved:
		srv.State = "needs approval"
		if !tc.trusted {
			srv.Note = "this session does not use the project's files; an approval applies once they are trusted"
		}
	case tc.sess == nil:
		srv.State = "off"
		srv.Note = "the session is starting again"
	case tc.info.NoMCP:
		srv.State = "off"
		srv.Note = "this session was started without tool servers (--no-mcp)"
	case !running || st.State == mcp.StateRefused:
		// not in this session, or refused when it started and approved since: the tool list is fixed (D-08)
		srv.State = "not started"
		srv.Note = "it starts when the team starts again"
		if !e.Trusted && !tc.trusted {
			srv.Note = "this session does not use the project's files"
		}
	default:
		srv.State = mcpState(st)
		if st.ServerName != "" {
			srv.Server = provider.SanitizeText(strings.TrimSpace(st.ServerName+" "+st.ServerVersion), 120)
		}
		if st.Error != "" {
			srv.Error = cleanText(st.Error)
		}
	}
	return srv
}

// mcpState is a server's state as the page names it.
func mcpState(st mcp.ServerStatus) string {
	switch st.State {
	case mcp.StateReady:
		return "running"
	case mcp.StateFailed:
		return "failed"
	case mcp.StateRefused:
		return "refused"
	case mcp.StateDisabled:
		return "disabled"
	case mcp.StateClosed:
		return "off"
	}
	return string(st.State) // connecting, restarting
}

// sessionNote says what the tab's session started and that its tool list is fixed.
func sessionNote(tc *tabCtx, started []string, tools int) string {
	switch {
	case tc.sess == nil:
		return "the session is starting again; its servers are listed when it has started"
	case tc.info.NoMCP:
		return "this session was started without tool servers"
	case len(started) == 0:
		return "this session started no tool server; its tool list is fixed until the team starts again"
	}
	return fmt.Sprintf("this session started %d %s (%s) with %d tools; its tool list is fixed until the team starts again, and Reconnect restarts a server that gave up",
		len(started), plural(len(started), "server", "servers"), strings.Join(started, ", "), tools)
}

// plural picks the word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// mcpEntry resolves the {name} of an MCP route among the tab's entries.
func (s *service) mcpEntry(r *http.Request) (*tabCtx, session.MCPEntry, error) {
	tc, err := s.tabOf(r)
	if err != nil {
		return nil, session.MCPEntry{}, err
	}
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		return nil, session.MCPEntry{}, fail(http.StatusBadRequest, "bad_name", "the server name is not valid")
	}
	entries, _, err := s.mcpEntries(tc)
	if err != nil {
		return nil, session.MCPEntry{}, fail(http.StatusUnprocessableEntity, "rejected", "the configuration does not load: see Config layers")
	}
	for _, e := range entries {
		if e.Name == name {
			return tc, e, nil
		}
	}
	return nil, session.MCPEntry{}, fail(http.StatusNotFound, "not_found", "there is no MCP server of that name here")
}

// handleMCPApprove is POST /api/sessions/{id}/mcp/{name}/approve: remember a project entry for the project, with the confirmation
// mcp.approve:<d16 of {root, name, fingerprint}> (the entry as it is now). It applies when the team starts again (D-08).
func (s *service) handleMCPApprove(w http.ResponseWriter, r *http.Request) {
	tc, e, err := s.mcpEntry(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if e.Trusted {
		reply(w, nil, fail(http.StatusConflict, "nothing", "it is your own entry: it needs no approval"))
		return
	}
	if !s.srv.RequireConfirm(w, r, mcpScope(tc.root, e.Name, e.Fingerprint)) {
		return
	}
	if err := session.OpenMCPApprovals(s.homeOf(tc)).Approve(tc.root, e.Fingerprint, e.Name); err != nil {
		web.Logf(r, "mcp approval not written")
		reply(w, nil, fail(http.StatusInternalServerError, "internal", "the approval could not be written"))
		return
	}
	reply(w, wire.MCPResult{T: mcpApproved, Cls: "ok"}, nil)
}

// handleMCPRevoke is POST /api/sessions/{id}/mcp/{name}/revoke: forget a project entry's approval.
func (s *service) handleMCPRevoke(w http.ResponseWriter, r *http.Request) {
	tc, e, err := s.mcpEntry(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if e.Trusted {
		reply(w, nil, fail(http.StatusConflict, "nothing", "it is your own entry: remove it from your configuration to stop it"))
		return
	}
	if !e.Approved {
		reply(w, wire.MCPResult{T: "it was not approved", Cls: "warm"}, nil)
		return
	}
	if err := session.OpenMCPApprovals(s.homeOf(tc)).Revoke(tc.root, e.Fingerprint); err != nil {
		web.Logf(r, "mcp approval not written")
		reply(w, nil, fail(http.StatusInternalServerError, "internal", "the approval could not be written"))
		return
	}
	reply(w, wire.MCPResult{T: mcpRevoked, Cls: "warm"}, nil)
}

// handleMCPTest is POST /api/sessions/{id}/mcp/{name}/test: start the server alone (as `sleipnir mcp test NAME`, without a session
// and without writing anything), list its tools and stop it. A project entry that is not approved is refused (409 needs_approval).
func (s *service) handleMCPTest(w http.ResponseWriter, r *http.Request) {
	tc, e, err := s.mcpEntry(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if !e.Trusted && !e.Approved {
		reply(w, nil, fail(http.StatusConflict, "needs_approval", "approve it first: a project entry starts only once you approved it"))
		return
	}
	if e.Config.Disabled {
		reply(w, wire.MCPResult{T: e.Name + ": nothing to test: it is disabled in the configuration", Cls: "warm"}, nil)
		return
	}
	select {
	case s.mcpSem <- struct{}{}:
		defer func() { <-s.mcpSem }()
	default:
		reply(w, nil, fail(http.StatusConflict, "busy", "other servers are being tested; try again when they are done"))
		return
	}
	res, err := s.mcpTest(r.Context(), s.homeOf(tc), tc.cwd, []string{e.Name}, !e.Trusted)
	if err != nil {
		reply(w, wire.MCPResult{T: e.Name + ": " + cleanError(err), Cls: "err"}, nil)
		return
	}
	reply(w, testResult(e.Name, res), nil)
}

// testResult turns an MCP test into the card's outcome line.
func testResult(name string, res *session.MCPTestResult) wire.MCPResult {
	for _, rn := range res.Refused {
		if rn == name {
			return wire.MCPResult{T: name + ": not started: approve it first", Cls: "warm"}
		}
	}
	for _, st := range res.Servers {
		if st.Name != name {
			continue
		}
		if st.State != mcp.StateReady {
			msg := name + ": " + mcpState(st)
			if st.Error != "" {
				msg += ": " + cleanText(st.Error)
			}
			return wire.MCPResult{T: msg, Cls: "err"}
		}
		n := 0
		for _, t := range res.Tools {
			if strings.HasPrefix(t, "mcp__"+name+"__") {
				n++
			}
		}
		return wire.MCPResult{T: fmt.Sprintf("%s: %d %s answered ✓ (%d ms)", name, n, plural(n, "tool", "tools"), res.Elapsed.Milliseconds()), Cls: "ok"}
	}
	return wire.MCPResult{T: name + ": nothing to test", Cls: "warm"}
}

// handleMCPReconnect is POST /api/sessions/{id}/mcp/{name}/reconnect: ask a server of the tab's running session that gave up to start
// again. The session's tool list does not change.
func (s *service) handleMCPReconnect(w http.ResponseWriter, r *http.Request) {
	tc, e, err := s.mcpEntry(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if tc.sess == nil {
		reply(w, nil, fail(http.StatusConflict, "idle", "the session is starting again; reconnect when it has started"))
		return
	}
	if err := tc.sess.MCPReconnect(e.Name); err != nil {
		reply(w, wire.MCPResult{T: e.Name + ": reconnect refused: " + cleanError(err), Cls: "err"}, nil)
		return
	}
	reply(w, wire.MCPResult{T: "reconnecting " + e.Name + ": its state shows here when it answers", Cls: "ok"}, nil)
}

// sortedNames returns the keys of m in order.
func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
