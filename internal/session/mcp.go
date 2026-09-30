package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/harden"
	"github.com/reee344/sleipnir/internal/mcp"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// mcpStartup bounds how long a session waits for its MCP servers before it
// freezes the tool list. A server that is not ready by then is left out of this
// session (its supervisor keeps trying, and /mcp says what it is doing): the
// list cannot change afterwards without rewriting every agent's cached prefix.
const mcpStartup = 20 * time.Second

// mcpState is the session's MCP connection: the manager, the snapshot whose
// tools were registered (frozen for the session), and the remembered approvals.
type mcpState struct {
	mgr       *mcp.Manager
	snap      *mcp.Snapshot
	approvals *approvalStore
	names     map[string]string // entry fingerprint -> server name, for approval prompts
	refused   []string          // servers left out for want of approval (headless)
	prompter  perm.Prompter

	mu      sync.Mutex
	warned  map[string]bool // change notices already shown, by server
	started time.Time
}

// startMCP connects the configured servers and registers their tools. Nothing
// here is fatal: a server that fails to start, is refused or is misconfigured is
// reported and left out, and the session goes on with the tools it has.
//
// Where an entry came from decides whether it may start unasked. The user's own
// configuration is trusted. An entry that arrives with a repository (the
// project's .sleipnir/config.json or .mcp.json, read only when the project is
// trusted at all) can name any command or URL and its tool descriptions land in
// every agent's prompt, so it starts only after the user approves that exact
// entry (approvals are remembered per project by the entry's fingerprint, so a
// commit that changes the command asks again). A headless session cannot ask and
// leaves such servers out.
func (s *Session) startMCP(ctx context.Context, reg *tools.Registry) {
	o := s.opts
	if o.NoMCP {
		return
	}
	servers, issues := s.mcpServers()
	for _, is := range issues {
		s.notice("", is.Error())
	}
	if len(servers) == 0 {
		return
	}
	st := &mcpState{
		approvals: openApprovals(filepath.Join(stateRoot(o.Home), "mcp-approvals.json")),
		names:     map[string]string{},
		warned:    map[string]bool{},
		prompter:  s.hookPrompter(o.Prompter),
		started:   o.Now(),
	}
	for name, c := range servers {
		st.names[c.Fingerprint()] = name
	}
	s.mcp = st
	env := mcp.EnvMap(os.Environ())
	// Keys the harness holds itself (harden.MoveKeys) are no longer in the environment, and
	// an entry that names ${SOME_API_KEY} must resolve as it always did: what a
	// configuration may see is the same set as before, and approving a project entry still
	// shows which variables it asks for.
	for _, name := range harden.Held() {
		if v, ok := harden.LookupSecret(name); ok {
			env[name] = v
		}
	}
	st.mgr = mcp.NewManager(mcp.Options{
		Servers: servers, Env: env, Cwd: o.Cwd,
		Roots:    []mcp.Root{{Path: o.Root, Name: filepath.Base(o.Root)}},
		Approve:  s.mcpApprove,
		OnChange: s.mcpChanged,
		Logf: func(format string, args ...any) {
			s.Log.Emit("", "notice", map[string]any{"level": "info", "msg": "mcp: " + fmt.Sprintf(format, args...)})
		},
		ClientName: "sleipnir", ClientVersion: Version,
	})
	sctx, cancel := context.WithTimeout(ctx, mcpStartup)
	defer cancel()
	if err := st.mgr.Start(sctx); err != nil {
		var se *mcp.StartError
		if errors.As(err, &se) {
			names := make([]string, 0, len(se.Failed))
			for n := range se.Failed {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				s.notice("", fmt.Sprintf("mcp: server %q did not start: %v", n, se.Failed[n]))
			}
		} else {
			s.notice("", "mcp: "+err.Error())
		}
	}
	st.snap = st.mgr.Snapshot() // adopt: this is the tool list the session freezes
	st.snap.Register(reg)
	for _, w := range st.snap.Warnings() {
		s.notice("", "mcp: "+w)
	}
}

// mcpServers reads the configured entries, split by where they came from.
func (s *Session) mcpServers() (map[string]mcp.ServerConfig, []mcp.Issue) {
	user, project := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for name, raw := range s.cfg.MCP {
		if s.cfgRep != nil && s.cfgRep.KindOf("mcp."+name) != "" && s.cfgRep.KindOf("mcp."+name) != "user" {
			project[name] = raw // project, local: it arrived with the repository
		} else {
			user[name] = raw
		}
	}
	// A .mcp.json at the project root is the shared convention of other clients.
	// It is a repository file like any other: read only for a trusted project,
	// and its entries need approval like the project's own.
	var issues []mcp.Issue
	if s.opts.TrustProject {
		if b, err := os.ReadFile(filepath.Join(s.opts.Root, ".mcp.json")); err == nil {
			var doc map[string]json.RawMessage
			if json.Unmarshal(b, &doc) == nil {
				if inner, ok := doc["mcpServers"]; ok {
					var m map[string]json.RawMessage
					if json.Unmarshal(inner, &m) == nil {
						for name, raw := range m {
							if _, dup := s.cfg.MCP[name]; dup {
								issues = append(issues, mcp.Issue{Server: name, Message: ".mcp.json entry ignored: the configuration already defines a server of that name"})
								continue
							}
							project[name] = raw
						}
					}
				}
			}
		}
	}
	servers := map[string]mcp.ServerConfig{}
	merge := func(raw map[string]json.RawMessage, scope mcp.Scope) {
		if len(raw) == 0 {
			return
		}
		got, is := mcp.ParseWith(raw, mcp.ParseOptions{Scope: scope})
		issues = append(issues, is...)
		for name, c := range got {
			servers[name] = c
		}
	}
	merge(user, mcp.ScopeUser)
	merge(project, mcp.ScopeProject)
	return servers, issues
}

// mcpApprove is asked, one server at a time, whether an untrusted entry may
// start. The user sees the whole command line (or the host), the names of the
// variables and headers it uses and the variables it wants from their
// environment; "p" remembers the exact entry for this project.
func (s *Session) mcpApprove(c mcp.ServerConfig) bool {
	st := s.mcp
	fp := c.Fingerprint()
	name := st.names[fp]
	if st.approvals.has(s.opts.Root, fp) {
		return true
	}
	if st.prompter == nil {
		st.mu.Lock()
		st.refused = append(st.refused, name)
		st.mu.Unlock()
		s.notice("", fmt.Sprintf("mcp: server %q from this project is not started: it needs your approval. Run `sleipnir mcp approve %s` after reading `sleipnir mcp list`, or define it in your user configuration", name, name))
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d := st.prompter(ctx, perm.Request{
		Tool:    "mcp-server",
		Summary: fmt.Sprintf("start the MCP server %q from this project's configuration. It %s", name, c.Describe()),
		Network: c.Remote(), Risk: perm.RiskHigh,
	})
	if !d.Allow {
		return false
	}
	if d.Remember == perm.ScopeProject {
		if err := st.approvals.add(s.opts.Root, fp, name); err != nil {
			s.notice("", "mcp: approval not saved: "+err.Error())
		}
	}
	return true
}

// mcpChanged is told when a server's tools would now differ from the frozen
// list. The session keeps what it started with (a change would rewrite every
// agent's cached prefix), so the person is told once per server.
func (s *Session) mcpChanged(c mcp.Change) {
	if c.Kind != mcp.ListTools {
		return
	}
	st := s.mcp
	st.mu.Lock()
	seen := st.warned[c.Server]
	st.warned[c.Server] = true
	st.mu.Unlock()
	if seen {
		return
	}
	s.notice("", fmt.Sprintf("mcp: server %q changed its tools (%s). This session keeps the tool list it started with, so its cache stays intact; start a new session to pick the change up", c.Server, c.Reason))
}

func (s *Session) closeMCP() {
	if s.mcp != nil && s.mcp.mgr != nil {
		_ = s.mcp.mgr.Close()
	}
}

// mcpInfo is what session.start records about MCP: which servers, how many
// tools are in the frozen list and its hash.
func (s *Session) mcpInfo() map[string]any {
	if s.mcp == nil || s.mcp.snap == nil {
		return nil
	}
	st := s.mcp.mgr.Status()
	ready := 0
	for _, x := range st {
		if x.State == mcp.StateReady {
			ready++
		}
	}
	return map[string]any{"servers": len(st), "ready": ready, "tools": len(s.mcp.snap.Names()), "hash": core.Hash(s.mcp.snap.Hash()).Short()}
}

// MCPStatus reports every configured server (for /mcp); nil when MCP is off.
func (s *Session) MCPStatus() []mcp.ServerStatus {
	if s.mcp == nil || s.mcp.mgr == nil {
		return nil
	}
	return s.mcp.mgr.Status()
}

// MCPToolNames lists the frozen tool names the model was offered.
func (s *Session) MCPToolNames() []string {
	if s.mcp == nil || s.mcp.snap == nil {
		return nil
	}
	return s.mcp.snap.Names()
}

// MCPReconnect asks a server that gave up (or is misbehaving) to start again. It
// does not change the tool list of the running session.
func (s *Session) MCPReconnect(name string) error {
	if s.mcp == nil || s.mcp.mgr == nil {
		return errors.New("no MCP servers are configured")
	}
	return s.mcp.mgr.Reconnect(name)
}

// MCPPrompts lists the prompts the servers offer, as slash-command entries.
func (s *Session) MCPPrompts() []mcp.PromptEntry {
	if s.mcp == nil || s.mcp.mgr == nil {
		return nil
	}
	return s.mcp.mgr.Prompts()
}

// MCPPrompt renders a server prompt with the given arguments into text the user
// is about to send (they picked it; it is not something a server can push).
func (s *Session) MCPPrompt(ctx context.Context, command string, args map[string]string) (string, error) {
	if s.mcp == nil || s.mcp.mgr == nil {
		return "", errors.New("no MCP servers are configured")
	}
	res, err := s.mcp.mgr.GetPrompt(ctx, command, args)
	if err != nil {
		return "", err
	}
	return res.Text(), nil
}

// ---- remembered approvals ----------------------------------------------------

// approvalStore remembers which project MCP entries the user approved, keyed by
// project root and the entry's fingerprint (so editing an entry asks again). It
// lives in the user's state directory, never in the repository: a repository
// must not be able to pre-approve itself.
type approvalStore struct {
	path string
	mu   sync.Mutex
	// root -> fingerprint -> server name (the name is only for humans)
	m map[string]map[string]string
}

const approvalsVersion = 1

func openApprovals(path string) *approvalStore {
	a := &approvalStore{path: path, m: map[string]map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return a
	}
	var doc struct {
		Version  int                          `json:"version"`
		Approved map[string]map[string]string `json:"approved"`
	}
	if json.Unmarshal(b, &doc) == nil && doc.Version == approvalsVersion && doc.Approved != nil {
		a.m = doc.Approved
	}
	return a
}

func (a *approvalStore) has(root, fp string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.m[root][fp]
	return ok
}

func (a *approvalStore) add(root, fp, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m[root] == nil {
		a.m[root] = map[string]string{}
	}
	a.m[root][fp] = name
	return a.saveLocked()
}

func (a *approvalStore) remove(root, fp string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m[root], fp)
	if len(a.m[root]) == 0 {
		delete(a.m, root)
	}
	return a.saveLocked()
}

func (a *approvalStore) saveLocked() error {
	b, err := json.MarshalIndent(struct {
		Version  int                          `json:"version"`
		Approved map[string]map[string]string `json:"approved"`
	}{approvalsVersion, a.m}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".mcp-approvals-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.path)
}

// MCPApprovals lets the CLI list, grant and revoke remembered approvals without
// a session. Root is the project root.
type MCPApprovals struct{ store *approvalStore }

// OpenMCPApprovals opens the approvals kept under the user's state directory.
func OpenMCPApprovals(home string) *MCPApprovals {
	return &MCPApprovals{store: openApprovals(filepath.Join(stateRoot(home), "mcp-approvals.json"))}
}

// Has reports whether the entry with this fingerprint is approved for root.
func (a *MCPApprovals) Has(root, fingerprint string) bool { return a.store.has(root, fingerprint) }

// Approve remembers an entry for a project.
func (a *MCPApprovals) Approve(root, fingerprint, name string) error {
	return a.store.add(root, fingerprint, name)
}

// Revoke forgets an entry.
func (a *MCPApprovals) Revoke(root, fingerprint string) error {
	return a.store.remove(root, fingerprint)
}

// MCPEntry is one configured server as the CLI shows it.
type MCPEntry struct {
	Name        string
	Config      mcp.ServerConfig
	Fingerprint string
	Trusted     bool // the user's own entry
	Approved    bool // a project entry the user approved for this project
}

// MCPEntries lists the servers a session started in root would consider, with
// their approval state, without starting anything. trustProject says whether the
// project's own configuration would be read at all.
func MCPEntries(home, root string, trustProject bool) ([]MCPEntry, []mcp.Issue, error) {
	cfg, rep, err := config.Load(config.LoadOpts{Cwd: root, Root: root, Home: home, UntrustedProject: !trustProject})
	if err != nil {
		return nil, nil, err
	}
	s := &Session{cfg: cfg, cfgRep: rep, opts: Options{Root: root, TrustProject: trustProject}}
	servers, issues := s.mcpServers()
	ap := OpenMCPApprovals(home)
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]MCPEntry, 0, len(names))
	for _, n := range names {
		c := servers[n]
		fp := c.Fingerprint()
		out = append(out, MCPEntry{Name: n, Config: c, Fingerprint: fp, Trusted: c.Trust || c.Scope == mcp.ScopeUser, Approved: ap.Has(root, fp)})
	}
	return out, issues, nil
}
