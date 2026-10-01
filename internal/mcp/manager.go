package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// State is where a server is in its life.
type State string

const (
	StateDisabled   State = "disabled" // disabled in configuration
	StateRefused    State = "refused"  // not approved, or unusable configuration: nothing was started
	StateConnecting State = "connecting"
	StateReady      State = "ready"
	StateRestarting State = "restarting" // the connection ended; reconnecting with backoff
	StateFailed     State = "failed"     // gave up after repeated failures (Reconnect tries again)
	StateClosed     State = "closed"
)

// Backoff shapes reconnection after a crash or a failed start.
type Backoff struct {
	// Min is the first delay, Max the ceiling; the delay doubles per consecutive
	// failure (defaults 500ms and 30s). There is no jitter: with a handful of
	// servers per harness there is no herd to spread, and determinism is worth
	// more.
	Min, Max time.Duration
	// MaxFailures is how many consecutive failures the supervisor tolerates
	// before parking the server as failed (default 5). A connection that stays up
	// for StableAfter resets the count.
	MaxFailures int
}

// Change tells the owner that what the model sees, or could see, changed. For
// tools it is an epoch event: tool changes rewrite the cached prefix of every
// agent.
type Change struct {
	Server string
	Kind   ListKind
	// Reason is "list_changed" (the server announced it) or "reconnected" (the
	// server came back with a different listing).
	Reason string
	// Hash is the hash the tool snapshot would have right now (Kind == tools).
	Hash core.Hash
}

// Options configures a Manager. Only Servers is required.
type Options struct {
	// Servers are the definitions to run, by name (see Parse).
	Servers map[string]ServerConfig

	// Env is the only source of ${VAR} values in the definitions (see
	// expand.go). Use EnvMap(os.Environ()) to hand configuration the process
	// environment on purpose; nil means no variables at all.
	Env map[string]string
	// BaseEnv is what stdio servers inherit before their own env entries. Nil
	// means SafeBaseEnv(os.Environ()): PATH, HOME, locale and the like, and
	// none of the harness's credentials.
	BaseEnv []string
	// Cwd is the default working directory of stdio servers (the workspace).
	Cwd string
	// Roots are advertised to stdio servers only.
	Roots []Root

	// Approve is asked, at most once per definition and one at a time, whether a
	// server that is not Trust may be started. It receives the definition as
	// written (with ${VAR} references unexpanded; ServerConfig.EnvRefs says
	// which variables it wants). Without it, untrusted servers are refused.
	Approve func(ServerConfig) bool

	// Dial replaces the built-in address guard as the dialer of HTTP and SSE
	// connections. The caller then owns the SSRF policy (Sleipnir hands in the
	// guard its web tools use); the built-in one refuses private, loopback,
	// link-local and reserved addresses, resolving the name itself and connecting
	// to the vetted address, so a name cannot answer differently the second time.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// AllowPrivate lets remote servers live on private and loopback addresses
	// (never link-local, metadata or reserved ones) and permits plain http://. It
	// applies to every server; an entry's own allow_private does the same for that
	// server. With a Dial hook it only decides whether plain http:// is acceptable.
	AllowPrivate bool
	// Proxy routes HTTP and SSE requests through a proxy. It is nil by default:
	// the process environment (HTTP_PROXY) is never consulted implicitly.
	Proxy func(*http.Request) (*url.URL, error)

	// ClientName and ClientVersion identify the harness to servers.
	ClientName, ClientVersion string

	// ConnectTimeout bounds one server's connect, handshake and first listing
	// (default 30s; an entry's startup_timeout overrides it).
	ConnectTimeout time.Duration
	// CallTimeout is the inactivity timeout of a tool call (default 2m; an
	// entry's timeout overrides it). A call that reports progress stays alive
	// until MaxCallDuration (default 10m).
	CallTimeout     time.Duration
	MaxCallDuration time.Duration
	// ShutdownGrace is the wait of each stage of stopping a child process
	// (default 2s).
	ShutdownGrace time.Duration
	// MaxMessageBytes bounds one incoming message (default 16 MiB).
	MaxMessageBytes int
	// MaxConcurrentStarts bounds simultaneous connection attempts (default 8).
	MaxConcurrentStarts int

	Backoff Backoff
	// StableAfter is how long a connection must last before the failure count
	// resets (default 30s).
	StableAfter time.Duration
	// ReconnectWait is how long a call waits for a restarting server before
	// failing (default 2s; negative disables waiting).
	ReconnectWait time.Duration
	// RefreshInterval is the minimum gap between re-listings of one server, so a
	// server that announces changes in a loop cannot make the harness re-list in
	// one (default 1s).
	RefreshInterval time.Duration

	// Snapshot bounds the tool list (see SnapshotOptions).
	Snapshot SnapshotOptions
	// AttachMedia adds image blocks to results for providers that accept
	// images in tool results. Off by default: media from a server is untrusted
	// and expensive, and the model sees a placeholder.
	AttachMedia bool

	// OnChange is called, from a goroutine of the manager, when a server's tools
	// change in a way that alters the snapshot (compared with the last one
	// handed out by Snapshot), and when its prompts or resources change. It must
	// not block for long and must not call Close.
	OnChange func(Change)

	// Logf receives diagnostics: connects, failures, restarts. Text is free of
	// secrets. Optional.
	Logf func(format string, args ...any)
}

func (o *Options) defaults() {
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 30 * time.Second
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 2 * time.Minute
	}
	if o.MaxCallDuration <= 0 {
		o.MaxCallDuration = 10 * time.Minute
	}
	if o.ShutdownGrace <= 0 {
		o.ShutdownGrace = 2 * time.Second
	}
	if o.MaxConcurrentStarts <= 0 {
		o.MaxConcurrentStarts = 8
	}
	if o.Backoff.Min <= 0 {
		o.Backoff.Min = 500 * time.Millisecond
	}
	if o.Backoff.Max <= 0 {
		o.Backoff.Max = 30 * time.Second
	}
	if o.Backoff.Max < o.Backoff.Min {
		o.Backoff.Max = o.Backoff.Min
	}
	if o.Backoff.MaxFailures <= 0 {
		o.Backoff.MaxFailures = 5
	}
	if o.StableAfter <= 0 {
		o.StableAfter = 30 * time.Second
	}
	if o.ReconnectWait == 0 {
		o.ReconnectWait = 2 * time.Second
	}
	if o.RefreshInterval <= 0 {
		o.RefreshInterval = time.Second
	}
	o.Snapshot.defaults()
}

// Manager runs the configured MCP servers and turns their tools into
// tools.Tool. It connects to all servers concurrently and keeps them connected:
// a server that crashes is restarted with backoff, and what the model sees does
// not change when that happens.
//
// What the model sees is the Snapshot: taken when the session freezes its tool
// list, immutable afterwards. Servers may change their tool lists at any time
// (tools/list_changed, a restart into a newer version); the manager follows,
// and tells the owner through Options.OnChange, but never alters a snapshot
// already taken. The owner adopts the new one at an epoch by calling Snapshot
// again.
type Manager struct {
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	approveMu sync.Mutex
	startSem  chan struct{}

	mu          sync.RWMutex
	servers     map[string]*server
	order       []string
	started     bool
	closed      bool
	baseline    core.Hash
	hasBaseline bool
}

// NewManager returns a manager for opts. Nothing is started until Start.
func NewManager(opts Options) *Manager {
	opts.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		opts: opts, ctx: ctx, cancel: cancel,
		startSem: make(chan struct{}, opts.MaxConcurrentStarts),
		servers:  map[string]*server{},
	}
	names := sortedKeys(opts.Servers)
	for i, name := range names {
		cfg := opts.Servers[name].Clone() // the caller's maps must not be able to change a running definition
		s := newServer(m, name, cfg)
		if i >= maxServers {
			s.state, s.errText = StateFailed, fmt.Sprintf("more than %d servers configured; this one is ignored", maxServers)
			s.fatal = true
		}
		m.servers[name] = s
		m.order = append(m.order, name)
	}
	return m
}

func (m *Manager) logf(format string, args ...any) {
	if m.opts.Logf != nil {
		m.opts.Logf(format, args...)
	}
}

// StartError reports servers that failed to start. It is not fatal: the manager
// serves the servers that did connect and keeps retrying the others.
type StartError struct{ Failed map[string]error }

func (e *StartError) Error() string {
	var parts []string
	for _, k := range sortedKeys(e.Failed) {
		parts = append(parts, fmt.Sprintf("%s: %v", k, e.Failed[k]))
	}
	return "mcp: " + fmt.Sprint(len(e.Failed)) + " server(s) did not start: " + strings.Join(parts, "; ")
}

func (e *StartError) Unwrap() []error {
	out := make([]error, 0, len(e.Failed))
	for _, k := range sortedKeys(e.Failed) {
		out = append(out, e.Failed[k])
	}
	return out
}

// Start connects to every enabled server concurrently (at most
// MaxConcurrentStarts at a time, each bounded by its own timeout) and returns
// when all have connected or failed. A non-nil result is a *StartError and is
// informational; the manager is usable either way, and supervisors keep
// working on the failed servers in the background.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return errors.New("mcp: manager already started or closed")
	}
	m.started = true
	m.mu.Unlock()

	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(m.order))
	n := 0
	for _, name := range m.order {
		s := m.servers[name]
		if s.cfg.Disabled {
			s.setState(StateDisabled, "")
			continue
		}
		if s.fatal { // over the server limit
			results <- result{name, errors.New(s.errText)}
			n++
			continue
		}
		n++
		first := make(chan error, 1)
		m.wg.Add(1)
		go s.run(ctx, first)
		go func(name string) { results <- result{name, <-first} }(name)
	}
	failed := map[string]error{}
	for i := 0; i < n; i++ {
		if r := <-results; r.err != nil {
			failed[r.name] = r.err
		}
	}
	if len(failed) > 0 {
		return &StartError{Failed: failed}
	}
	return nil
}

// Close stops every server (orderly, then by force) and waits for the manager's
// goroutines. It is safe to call more than once.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
	return nil
}

func (m *Manager) serverByName(name string) *server {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.servers[name]
}

// Reconnect makes a server that is refused, failed or restarting try again now,
// resetting its failure count and re-asking for approval if it was refused.
func (m *Manager) Reconnect(name string) error {
	s := m.serverByName(name)
	if s == nil {
		return fmt.Errorf("%w: %q", ErrUnknownServer, clipForError(name))
	}
	s.mu.Lock()
	s.approvalKnown = false
	s.mu.Unlock()
	select {
	case s.reconnect <- struct{}{}:
	default:
	}
	return nil
}

// ServerStatus describes one server for a UI or a log.
type ServerStatus struct {
	Name            string
	Type            string
	Scope           Scope
	State           State
	Error           string // why it is not ready; sanitised and free of secrets
	Tools           int    // tools the server currently lists (before the snapshot's filters and budgets)
	Prompts         int
	ServerName      string
	ServerVersion   string
	ProtocolVersion string
	Instructions    string // untrusted, sanitised, capped
	Restarts        int
	Warnings        []string
}

// Status reports every configured server, sorted by name.
func (m *Manager) Status() []ServerStatus {
	m.mu.RLock()
	names := append([]string(nil), m.order...)
	m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(names))
	for _, n := range names {
		out = append(out, m.servers[n].status())
	}
	return out
}

// Snapshot takes the current tool snapshot and records it as the baseline that
// OnChange compares against: call it when you adopt it (freeze the tool list,
// or install a new epoch). Tools is a shorthand for Snapshot().Tools().
func (m *Manager) Snapshot() *Snapshot { return m.snapshot(true) }

// Preview is Snapshot without adopting: it does not move the baseline.
func (m *Manager) Preview() *Snapshot { return m.snapshot(false) }

// Tools returns the tools of a fresh, adopted snapshot, sorted by name.
func (m *Manager) Tools() []tools.Tool { return m.Snapshot().Tools() }

func (m *Manager) snapshot(adopt bool) *Snapshot {
	m.mu.RLock()
	names := append([]string(nil), m.order...)
	m.mu.RUnlock()
	var in []serverTools
	for _, n := range names {
		s := m.servers[n]
		s.mu.Lock()
		if len(s.tools) > 0 {
			in = append(in, serverTools{name: n, cfg: s.cfg, tools: append([]Tool(nil), s.tools...), remote: s.cfg.Remote()})
		}
		s.mu.Unlock()
	}
	snap := buildSnapshot(m, in, m.opts.Snapshot)
	if adopt {
		m.mu.Lock()
		m.baseline, m.hasBaseline = snap.hash, true
		m.mu.Unlock()
	}
	return snap
}

// toolsChanged reports whether the snapshot would differ from the adopted one.
func (m *Manager) toolsChanged() (core.Hash, bool) {
	h := m.snapshot(false).hash
	m.mu.RLock()
	defer m.mu.RUnlock()
	return h, m.hasBaseline && h != m.baseline
}

func (m *Manager) notify(c Change) {
	if m.opts.OnChange == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			m.logf("mcp: OnChange panicked: %v", r)
		}
	}()
	m.opts.OnChange(c)
}

// ---- prompts and resources ----

// PromptEntry is one server prompt, exposed like a slash command.
type PromptEntry struct {
	// Command is "/mcp__<server>__<prompt>": the same naming as tools.
	Command     string
	Server      string
	Name        string // the server's own prompt name
	Title       string
	Description string
	Arguments   []PromptArgument
}

// Prompts lists the prompts of all ready servers as slash-command entries,
// sorted by command. Text is sanitised; nothing here reaches the model until
// the user picks an entry.
func (m *Manager) Prompts() []PromptEntry {
	m.mu.RLock()
	names := append([]string(nil), m.order...)
	m.mu.RUnlock()
	var all []PromptEntry
	byName := map[string]int{}
	for _, n := range names {
		s := m.servers[n]
		s.mu.Lock()
		prompts := append([]Prompt(nil), s.prompts...)
		s.mu.Unlock()
		for _, p := range prompts {
			if p.Problem != "" {
				continue
			}
			e := PromptEntry{Server: n, Name: p.Name, Title: p.Title, Description: p.Description, Arguments: p.Arguments}
			e.Command = "/" + exposedName(n, p.Name)
			byName[e.Command]++
			all = append(all, e)
		}
	}
	out := all[:0]
	for _, e := range all {
		if byName[e.Command] > 1 {
			e.Command = "/" + hashedName(e.Server, e.Name)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}

// GetPrompt expands a prompt entry (by its Command) with arguments.
func (m *Manager) GetPrompt(ctx context.Context, command string, args map[string]string) (*GetPromptResult, error) {
	var entry *PromptEntry
	for _, e := range m.Prompts() {
		if e.Command == command {
			e := e
			entry = &e
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("mcp: unknown prompt %q", clipForError(command))
	}
	for _, a := range entry.Arguments {
		if a.Required && args[a.Name] == "" {
			return nil, fmt.Errorf("mcp: prompt %s needs the argument %q", command, a.Name)
		}
	}
	c, err := m.clientFor(ctx, entry.Server)
	if err != nil {
		return nil, err
	}
	return c.GetPrompt(ctx, entry.Name, args)
}

// ListResources lists a server's resources.
func (m *Manager) ListResources(ctx context.Context, server string) ([]Resource, error) {
	c, err := m.clientFor(ctx, server)
	if err != nil {
		return nil, err
	}
	r, _, err := c.ListResources(ctx)
	return r, err
}

// ListResourceTemplates lists a server's resource templates.
func (m *Manager) ListResourceTemplates(ctx context.Context, server string) ([]ResourceTemplate, error) {
	c, err := m.clientFor(ctx, server)
	if err != nil {
		return nil, err
	}
	r, _, err := c.ListResourceTemplates(ctx)
	return r, err
}

// ReadResource reads a resource from a server. The caller (a UI, not the model
// directly) decides what to do with the contents; text is sanitised.
func (m *Manager) ReadResource(ctx context.Context, server, uri string) (*ReadResourceResult, error) {
	c, err := m.clientFor(ctx, server)
	if err != nil {
		return nil, err
	}
	return c.ReadResource(ctx, uri)
}

func (m *Manager) clientFor(ctx context.Context, server string) (*Client, error) {
	s := m.serverByName(server)
	if s == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServer, clipForError(server))
	}
	return s.awaitClient(ctx)
}

// ---- one server ----

type server struct {
	m    *Manager
	name string
	cfg  ServerConfig // as configured: ${VAR} unexpanded

	reconnect chan struct{}
	refresh   chan struct{}

	mu            sync.Mutex
	state         State
	errText       string
	fatal         bool // not worth retrying (refused, invalid, command not found)
	client        *Client
	gen           int
	init          *InitializeResult
	tools         []Tool
	prompts       []Prompt
	warnings      []string
	restarts      int
	dirty         map[ListKind]bool
	approvalKnown bool
	approved      bool
	red           *redactor // secrets of the expanded entry, for redacting results
}

func newServer(m *Manager, name string, cfg ServerConfig) *server {
	s := &server{
		m: m, name: name, cfg: cfg, state: StateConnecting,
		reconnect: make(chan struct{}, 1), refresh: make(chan struct{}, 1),
		dirty: map[ListKind]bool{},
	}
	// The redactor is built from the entry as expanded at connect time; until
	// then the unexpanded values (placeholders) are all there is to hide.
	s.red = newRedactor(cfg.secrets())
	return s
}

func (s *server) setState(st State, errText string) {
	s.mu.Lock()
	s.state, s.errText = st, errText
	s.mu.Unlock()
}

func (s *server) status() ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ServerStatus{
		Name: s.name, Type: s.cfg.EffectiveType(), Scope: s.cfg.Scope, State: s.state, Error: s.errText,
		Tools: len(s.tools), Prompts: len(s.prompts), Restarts: s.restarts,
		Warnings: append([]string(nil), s.warnings...),
	}
	if s.init != nil {
		st.ServerName, st.ServerVersion, st.ProtocolVersion = s.init.ServerInfo.Name, s.init.ServerInfo.Version, s.init.ProtocolVersion
		st.Instructions = s.init.Instructions
	}
	return st
}

// redactor returns the current secret masker (it is replaced on every connect,
// when the entry is expanded afresh).
func (s *server) redactor() *redactor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.red
}

func (s *server) callTimeout() time.Duration {
	if s.cfg.Timeout > 0 {
		return s.cfg.Timeout
	}
	return s.m.opts.CallTimeout
}

// awaitClient returns the live connection, waiting briefly for a restart to
// finish: a call that lands in the half second between a crash and its restart
// is far more useful delayed than failed. It polls (every 10ms, and only while a
// server is down) rather than being woken: the state changes under the
// supervisor's feet in several places, and a missed wake-up here would turn
// into a stuck agent, while a poll cannot miss anything.
func (s *server) awaitClient(ctx context.Context) (*Client, error) {
	wait := s.m.opts.ReconnectWait
	var deadline <-chan time.Time
	for {
		s.mu.Lock()
		st, c, why := s.state, s.client, s.errText
		s.mu.Unlock()
		if st == StateReady && c != nil {
			if !c.ended() {
				return c, nil
			}
			// The connection has just died and the supervisor has not retired it
			// yet: that is a restart in progress, not a live server.
			st = StateRestarting
		}
		if (st == StateConnecting || st == StateRestarting) && wait > 0 {
			if deadline == nil {
				t := time.NewTimer(wait)
				defer t.Stop()
				deadline = t.C
			}
			select {
			case <-time.After(10 * time.Millisecond):
				continue
			case <-deadline:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if why != "" {
			why = ": " + why
		}
		return nil, fmt.Errorf("%w: server %q is %s%s", ErrNotConnected, s.name, st, why)
	}
}

// fatalError marks a failure that retrying cannot fix.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// gate decides whether the server may be started at all.
func (s *server) gate() error {
	s.mu.Lock()
	known, ok := s.approvalKnown, s.approved
	s.mu.Unlock()
	if known {
		if ok {
			return nil
		}
		return &fatalError{fmt.Errorf("%w: it was not approved", ErrNotApproved)}
	}
	ok = s.cfg.Trust
	if !ok {
		if s.m.opts.Approve == nil {
			return s.recordApproval(false, &fatalError{fmt.Errorf(
				"%w: %s servers are not started without approval (mark the entry trusted from user configuration, or provide an approval hook)",
				ErrNotApproved, scopeLabel(s.cfg.Scope))})
		}
		s.m.approveMu.Lock()
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.m.logf("mcp: approval hook panicked: %v", r)
					ok = false
				}
			}()
			ok = s.m.opts.Approve(s.cfg.Clone())
		}()
		s.m.approveMu.Unlock()
	}
	if !ok {
		return s.recordApproval(false, &fatalError{fmt.Errorf("%w: it was not approved", ErrNotApproved)})
	}
	return s.recordApproval(true, nil)
}

func (s *server) recordApproval(ok bool, err error) error {
	s.mu.Lock()
	s.approvalKnown, s.approved = true, ok
	s.mu.Unlock()
	return err
}

func scopeLabel(sc Scope) string {
	if sc == ScopeUser {
		return "user"
	}
	return "project-scoped"
}

// run supervises the server for the manager's lifetime: connect, wait for the
// connection to end, reconnect with backoff, give up (park) after repeated
// failures until told to retry.
func (s *server) run(startCtx context.Context, first chan<- error) {
	m := s.m
	defer m.wg.Done()
	ctx := m.ctx
	failures := 0
	reported := false
	report := func(err error) {
		if !reported {
			reported = true
			first <- err
		}
	}
	for {
		began := time.Now()
		attempt := ctx
		var stopAttempt func() bool
		var cancelAttempt context.CancelFunc = func() {}
		if !reported {
			// The first attempt also ends when Start's own context does.
			attempt, cancelAttempt = context.WithCancel(ctx)
			stopAttempt = context.AfterFunc(startCtx, cancelAttempt)
		}
		err := s.connect(attempt)
		if stopAttempt != nil {
			stopAttempt()
		}
		cancelAttempt()
		// Record why the attempt failed before Start hears about it: a caller reads
		// Status as soon as Start returns and must see the outcome, not the moment
		// before it.
		fatal := err != nil && ctx.Err() == nil && s.recordFailure(err)
		report(err)

		if ctx.Err() != nil {
			s.shutdown()
			return
		}
		var cause error
		switch {
		case err == nil:
			client := s.currentClient()
			select {
			case <-client.Done():
			case <-ctx.Done():
				s.shutdown()
				return
			}
			cause = client.Err()
			s.markDown(client, cause)
			if time.Since(began) >= m.opts.StableAfter {
				failures = 0
			}
			if errors.Is(cause, ErrSessionExpired) && time.Since(began) > time.Second {
				// A session that lived a while and then expired is routine (the server
				// restarted, or timed it out): start a fresh one at once. One that
				// expires immediately, over and over, is a failing server and takes the
				// backoff path below.
				m.logf("mcp: server %q: session expired; reconnecting", s.name)
				continue
			}
		case fatal:
			m.logf("mcp: server %q not started: %v", s.name, err)
			if !s.park(ctx) {
				s.shutdown()
				return
			}
			failures = 0
			continue
		default:
			cause = err
		}
		failures++
		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
		m.logf("mcp: server %q: %v (failure %d of %d)", s.name, cause, failures, m.opts.Backoff.MaxFailures)
		if failures >= m.opts.Backoff.MaxFailures {
			s.mu.Lock()
			s.state = StateFailed
			s.mu.Unlock()
			if !s.park(ctx) {
				s.shutdown()
				return
			}
			failures = 0
			continue
		}
		delay := m.opts.Backoff.Min << (failures - 1)
		if delay <= 0 || delay > m.opts.Backoff.Max {
			delay = m.opts.Backoff.Max
		}
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-s.reconnect:
			t.Stop()
			failures = 0
		case <-ctx.Done():
			t.Stop()
			s.shutdown()
			return
		}
	}
}

// recordFailure stores why a connection attempt failed and, when no retry can
// help (see fatalError), the terminal state: refused when the server was not
// approved, failed otherwise. It reports whether the failure is fatal.
func (s *server) recordFailure(err error) bool {
	txt := s.errorText(err) // takes s.mu itself: compute before locking
	var fe *fatalError
	fatal := errors.As(err, &fe)
	s.mu.Lock()
	s.errText = txt
	if fatal {
		s.state, s.fatal = StateFailed, true
		if errors.Is(err, ErrNotApproved) {
			s.state = StateRefused
		}
	}
	s.mu.Unlock()
	return fatal
}

// park waits, doing nothing, until Reconnect or shutdown; it reports whether
// to go on.
func (s *server) park(ctx context.Context) bool {
	select {
	case <-s.reconnect:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *server) currentClient() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// markDown records the end of a connection: calls now wait for the restart or
// fail, and the last known tool list stays (so a crash does not change the
// snapshot, and with it the cached prefix).
func (s *server) markDown(c *Client, cause error) {
	txt := s.errorText(cause) // takes s.mu itself: compute before locking
	s.mu.Lock()
	if s.client == c {
		s.client = nil
		s.state = StateRestarting
		s.errText = txt
	}
	s.mu.Unlock()
	_ = c.Close() // reap the process; safe to call from here (not from the transport's goroutine)
}

// errorText renders an error for status text: sanitised, capped, redacted.
func (s *server) errorText(err error) string {
	if err == nil {
		return ""
	}
	msg, _ := truncateRunes(oneLine(err.Error(), 600), 500)
	return s.redactor().apply(msg)
}

func (s *server) shutdown() {
	s.mu.Lock()
	c := s.client
	s.client = nil
	s.state = StateClosed
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// connect makes one connection attempt: approval, dial, handshake, first
// listings, install.
func (s *server) connect(ctx context.Context) error {
	m := s.m
	if err := s.gate(); err != nil {
		return err
	}
	select {
	case m.startSem <- struct{}{}:
		defer func() { <-m.startSem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	if s.restarts > 0 || s.gen > 0 {
		s.state = StateRestarting
	} else {
		s.state = StateConnecting
	}
	s.gen++
	gen := s.gen
	s.mu.Unlock()

	x, xerr := s.cfg.Expand(m.opts.Env)
	if xerr == nil {
		s.mu.Lock()
		s.red = newRedactor(x.secrets())
		s.mu.Unlock()
	}
	prefix := fmt.Sprintf("mcp %s: ", s.name)
	client, err := Dial(ctx, s.name, s.cfg, DialOptions{
		Env: m.opts.Env, BaseEnv: m.opts.BaseEnv, Cwd: m.opts.Cwd,
		Net:            NetOptions{Dial: m.opts.Dial, AllowPrivate: m.opts.AllowPrivate, Proxy: m.opts.Proxy},
		StartupTimeout: m.opts.ConnectTimeout, ShutdownGrace: m.opts.ShutdownGrace, MaxMessageBytes: m.opts.MaxMessageBytes,
		Client: ClientOptions{
			Name: m.opts.ClientName, Version: m.opts.ClientVersion, Roots: m.opts.Roots,
			MaxCallDuration: m.opts.MaxCallDuration,
			OnListChanged:   func(k ListKind) { s.signalRefresh(gen, k) },
			Logf:            func(f string, a ...any) { m.logf(prefix+f, a...) },
		},
	})
	if err != nil {
		if isFatalDial(err) {
			return &fatalError{err}
		}
		return err
	}

	timeout := m.opts.ConnectTimeout
	if s.cfg.StartupTimeout > 0 {
		timeout = s.cfg.StartupTimeout
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	toolList, warns, err := s.listTools(lctx, client)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("listing tools: %w", err)
	}
	prompts, pwarns, err := s.listPrompts(lctx, client)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("listing prompts: %w", err)
	}

	changed := false
	s.mu.Lock()
	s.client, s.init = client, client.Initialized()
	s.tools, s.prompts = toolList, prompts
	s.warnings = append(warns, pwarns...)
	s.state, s.errText = StateReady, ""
	s.dirty = map[ListKind]bool{}
	wasRestart := gen > 1
	s.mu.Unlock()
	if wasRestart {
		_, changed = m.toolsChanged()
	}
	m.logf("mcp: server %q connected (%d tools, %d prompts)", s.name, len(toolList), len(prompts))
	m.wg.Add(1)
	go s.refreshLoop(m.ctx, client, gen)
	if changed {
		h, _ := m.toolsChanged()
		m.notify(Change{Server: s.name, Kind: ListTools, Reason: "reconnected", Hash: h})
	}
	return nil
}

// isFatalDial reports errors no retry can fix: configuration that does not
// expand or validate, a program that does not exist, a protocol version we
// cannot speak, an address the guard refuses.
func isFatalDial(err error) bool {
	return errors.Is(err, ErrProtocolVersion) || errors.Is(err, ErrBlocked) || errors.Is(err, errConfig) ||
		errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || errors.Is(err, exec.ErrNotFound)
}

func (s *server) listTools(ctx context.Context, c *Client) ([]Tool, []string, error) {
	list, warns, err := c.ListTools(ctx)
	if optionalMissing(err) {
		return nil, nil, nil
	}
	return list, prefixAll(s.name, warns), err
}

func (s *server) listPrompts(ctx context.Context, c *Client) ([]Prompt, []string, error) {
	list, warns, err := c.ListPrompts(ctx)
	if optionalMissing(err) {
		return nil, nil, nil
	}
	return list, prefixAll(s.name, warns), err
}

// optionalMissing is true when a listing failed only because the server does
// not offer that capability.
func optionalMissing(err error) bool {
	var rpc *RPCError
	return errors.Is(err, ErrUnsupported) || errors.As(err, &rpc) && rpc.Code == CodeMethodNotFound
}

func prefixAll(name string, in []string) []string {
	out := make([]string, len(in))
	for i, w := range in {
		out[i] = fmt.Sprintf("server %q: %s", name, w)
	}
	return out
}

// signalRefresh records that a server announced a list change and wakes the
// refresher. It runs on the client's dispatcher and never blocks.
func (s *server) signalRefresh(gen int, k ListKind) {
	s.mu.Lock()
	if gen == s.gen {
		s.dirty[k] = true
	}
	s.mu.Unlock()
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

// refreshLoop re-lists after change announcements, at most once per
// RefreshInterval, and tells the owner if that changed what the model sees. A
// failed re-listing keeps the old list: an outage must not empty the tool set.
func (s *server) refreshLoop(ctx context.Context, c *Client, gen int) {
	m := s.m
	defer m.wg.Done()
	var last time.Time
	for {
		select {
		case <-c.Done():
			return
		case <-ctx.Done():
			return
		case <-s.refresh:
		}
		if wait := m.opts.RefreshInterval - time.Since(last); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-c.Done():
				t.Stop()
				return
			case <-ctx.Done():
				t.Stop()
				return
			}
		}
		last = time.Now()
		s.mu.Lock()
		if gen != s.gen {
			s.mu.Unlock()
			return
		}
		kinds := make([]ListKind, 0, 3)
		for _, k := range listKinds {
			if s.dirty[k] {
				kinds = append(kinds, k)
			}
		}
		s.dirty = map[ListKind]bool{}
		s.mu.Unlock()
		for _, k := range kinds {
			s.refreshKind(ctx, c, gen, k)
		}
	}
}

func (s *server) refreshKind(ctx context.Context, c *Client, gen int, k ListKind) {
	m := s.m
	rctx, cancel := context.WithTimeout(ctx, m.opts.ConnectTimeout)
	defer cancel()
	switch k {
	case ListTools:
		list, warns, err := s.listTools(rctx, c)
		if err != nil {
			m.logf("mcp: server %q: re-listing tools failed: %v", s.name, s.redactor().apply(cleanText(err.Error())))
			return
		}
		s.mu.Lock()
		if gen != s.gen || s.client != c {
			s.mu.Unlock()
			return
		}
		s.tools = list
		s.warnings = warns
		s.mu.Unlock()
		if h, changed := m.toolsChanged(); changed {
			m.notify(Change{Server: s.name, Kind: ListTools, Reason: "list_changed", Hash: h})
		}
	case ListPrompts:
		list, _, err := s.listPrompts(rctx, c)
		if err != nil {
			m.logf("mcp: server %q: re-listing prompts failed: %v", s.name, s.redactor().apply(cleanText(err.Error())))
			return
		}
		s.mu.Lock()
		if gen != s.gen || s.client != c {
			s.mu.Unlock()
			return
		}
		s.prompts = list
		s.mu.Unlock()
		m.notify(Change{Server: s.name, Kind: ListPrompts, Reason: "list_changed"})
	case ListResources:
		m.notify(Change{Server: s.name, Kind: ListResources, Reason: "list_changed"})
	}
}
