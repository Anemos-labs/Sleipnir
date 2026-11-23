package acpserve

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/catalog"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/mcpserve"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// ProtocolVersion is the ACP major version this server speaks.
const ProtocolVersion = 1

// Defaults of a Config.
const (
	DefaultMaxJobs      = 4
	DefaultMode         = "bypass" // what an unattended `sleipnir run` uses with no flags
	maxRequestBytes     = 4 << 20  // one JSON-RPC line
	maxSessions         = 256      // persisted ACP sessions kept in the state directory
	maxListPage         = 100
	defaultContextSize  = 200000
	maxReplayUpdates    = 400
	maxModelChoices     = 64
	modelDiscoveryLimit = 3 * time.Second
)

// JSON-RPC and ACP error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeAuthRequired   = -32000
	codeNotFound       = -32002
)

// Config is what the person decided when starting the server. Nothing an ACP
// client sends can widen these limits.
type Config struct {
	// Exe is the Sleipnir binary that runs each turn (normally os.Executable()).
	Exe string
	// Roots are the directories a session's cwd must be inside (at least one).
	Roots []string
	// Dir is the default session directory and the base of a relative cwd.
	Dir string
	// Mode is the most permissive permission mode a turn may use.
	Mode string
	// Allow rules are passed to every turn.
	Allow []string
	// TrustProject passes --trust-project to every turn.
	TrustProject bool
	// MaxJobs bounds the turns that run at once.
	MaxJobs int
	// Version is reported as agentInfo.version.
	Version string
	// Env is the environment of a turn (nil: this process's).
	Env []string
	// Prepare is called with each turn's command before it starts, and returns
	// the function to call with Start's error (sched.PassKeys).
	Prepare func(*exec.Cmd) func(error)
	// Home is the user's home, used for configuration, the catalogue and the
	// state directory. Empty means the current user's home.
	Home string
	// StateDir holds the persisted sessions. Empty means <state>/acp-sessions.
	StateDir string
	// DefaultModel overrides the model advertised and used when a client does
	// not choose one.
	DefaultModel string
	// NoMCP ignores the MCP servers a client sends in session setup.
	NoMCP bool
	// Now is the clock used for timestamps; nil means time.Now.
	Now func() time.Time
	// NewID returns a new session id; nil means a random one. Tests set it.
	NewID func() string
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func fail(code int, format string, args ...any) *rpcError {
	return &rpcError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// message is the part of a JSON-RPC message this server reads.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// capabilities are what the client on this connection advertised.
type capabilities struct {
	Notices  bool
	Plan     bool
	Subagent bool
	Boolean  bool
}

// Server is one ACP connection's state.
type Server struct {
	cfg      Config
	mcpCfg   mcpserve.Config
	stateDir string
	now      func() time.Time
	newID    func() string
	models   []modelChoice
	model    string
	contexts map[string]int

	outMu sync.Mutex
	out   *json.Encoder

	mu               sync.Mutex
	createMu         sync.Mutex // serializes capacity check through session installation
	afterRead        func()     // test hook: pauses a load after reading its record
	afterLookup      func()     // test hook: pauses a prompt between lookup and admission
	afterCloseRetire func()     // test hook: pauses close after retirement, before cancel
	stateMu          sync.Mutex // serializes per-id state changes with load/resume swaps
	initialized      bool
	closing          bool
	caps             capabilities
	sessions         map[string]*Session
	order            []string
}

// ModeOptions returns the permission modes this server may use, from strictest
// to most permissive, limited by the configured ceiling.
func (c Config) ModeOptions() []string {
	ceiling := c.Mode
	if ceiling == "" {
		ceiling = DefaultMode
	}
	allowed := make([]string, 0, len(mcpserve.Modes))
	for _, mode := range mcpserve.Modes {
		allowed = append(allowed, mode)
		if mode == ceiling {
			break
		}
	}
	return allowed
}

// New validates cfg and reads the offline model catalogue. It does not open a
// connection or write to stdout.
func New(cfg Config) (*Server, error) {
	if cfg.Mode == "" {
		cfg.Mode = DefaultMode
	}
	if cfg.MaxJobs <= 0 {
		cfg.MaxJobs = DefaultMaxJobs
	}
	mcCfg := mcpserve.Config{
		Exe: cfg.Exe, Roots: cfg.Roots, Dir: cfg.Dir, Mode: cfg.Mode,
		Allow: cfg.Allow, TrustProject: cfg.TrustProject, MaxJobs: cfg.MaxJobs,
		Version: cfg.Version, Env: cfg.Env, Prepare: cfg.Prepare,
	}
	mc, err := mcpserve.New(mcCfg)
	if err != nil {
		return nil, fmt.Errorf("acp serve: %w", err)
	}
	// Keep the normalized roots and directory: a symlinked or relative root
	// must be compared in the same canonical form as a resolved session cwd.
	mcCfg = mc.Config()
	cfg.Roots, cfg.Dir = mcCfg.Roots, mcCfg.Dir
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.Home == "" {
		cfg.Home, _ = os.UserHomeDir()
	}
	if cfg.StateDir == "" {
		cfg.StateDir = filepath.Join(session.StateRoot(cfg.Home), "acp-sessions")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("acp serve: state directory %s: %w", cfg.StateDir, err)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	newID := cfg.NewID
	if newID == nil {
		newID = randomID
	}
	s := &Server{
		cfg: cfg, mcpCfg: mcCfg, stateDir: cfg.StateDir, now: now, newID: newID,
		sessions: map[string]*Session{}, contexts: map[string]int{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelDiscoveryLimit)
	defer cancel()
	s.models, s.model = discoverModels(ctx, cfg)
	for _, m := range s.models {
		if m.Context > 0 {
			s.contexts[m.Value] = m.Context
		}
	}
	return s, nil
}

// randomID returns an opaque id safe to use in a file name and a JSON-RPC id.
func randomID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("acp%d", time.Now().UnixNano())
	}
	return "acp_" + hex.EncodeToString(b)
}

// Serve answers JSON-RPC messages read from in on out until in ends or ctx is
// done, then cancels the turns that still run and waits for them to end.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.outMu.Lock()
	s.out = json.NewEncoder(out)
	s.outMu.Unlock()
	handlerCtx, cancelHandlers := context.WithCancel(ctx)
	defer cancelHandlers()
	lines := make(chan []byte, 64)
	readErr := make(chan error, 1)
	go func() {
		br := bufio.NewReaderSize(in, 64<<10)
		for {
			line, err := mcpserve.ReadLine(br, maxRequestBytes)
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					readErr <- ctx.Err()
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				readErr <- err
				return
			}
		}
	}()
	var wg sync.WaitGroup
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err = <-readErr:
			break loop
		case line := <-lines:
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.handle(handlerCtx, line)
			}()
		}
	}
	// Stop new turns before looking at the ones that already run: a handler
	// that is between its lookup and setActive must not install a child after
	// shutdown has taken its snapshot.
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	cancelHandlers()
	s.shutdown()
	wg.Wait()
	return err
}

// shutdown cancels the turns of every active session and waits for them to
// finish, then writes their metadata to disk.
func (s *Server) shutdown() {
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		if j := sess.cancelJob(); j != nil {
			<-j.done
		}
		s.discardMCP(sess)
		sess.save()
	}
}

// reply writes one JSON-RPC response.
func (s *Server) reply(id json.RawMessage, result any, e *rpcError) {
	if len(id) == 0 {
		return // a notification gets no answer
	}
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		msg["error"] = e
	} else {
		msg["result"] = result
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.out != nil {
		_ = s.out.Encode(msg)
	}
}

// notify writes one session/update notification. Client capabilities are
// checked by the caller; this method only writes.
func (s *Server) notify(sessionID string, update map[string]any) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update":    update,
		},
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.out != nil {
		_ = s.out.Encode(msg)
	}
}

// handle decodes and dispatches one message.
func (s *Server) handle(ctx context.Context, line []byte) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		s.reply(json.RawMessage("null"), nil, fail(codeParseError, "parse error"))
		return
	}
	if m.Method == "" {
		return // a response to a request we did not make
	}
	if m.Method != "initialize" {
		s.mu.Lock()
		ready := s.initialized
		s.mu.Unlock()
		if !ready {
			s.reply(m.ID, nil, fail(codeInvalidRequest, "initialize must be called first"))
			return
		}
	}
	s.dispatch(ctx, &m)
}

// dispatch routes a method to its handler.
func (s *Server) dispatch(ctx context.Context, m *message) {
	switch m.Method {
	case "initialize":
		s.initialize(m)
	case "session/new":
		s.newSession(m)
	case "session/list":
		s.listSessions(m)
	case "session/load", "session/resume":
		s.openSession(m, m.Method == "session/load")
	case "session/close":
		s.closeSession(m)
	case "session/prompt":
		s.prompt(ctx, m)
	case "session/cancel":
		s.cancel(m)
	case "session/set_config_option":
		s.setConfigOption(m)
	case "session/set_mode":
		s.setMode(m)
	default:
		s.reply(m.ID, nil, fail(codeMethodNotFound, "method not found: %s", m.Method))
	}
}

// initialize negotiates the version and advertises Sleipnir's capabilities.
func (s *Server) initialize(m *message) {
	var p struct {
		ProtocolVersion    int `json:"protocolVersion"`
		ClientCapabilities struct {
			Session *struct {
				Notices *struct{} `json:"notices"`
			} `json:"session"`
			Plan          *struct{} `json:"plan"`
			Subagents     *struct{} `json:"subagents"`
			SessionConfig *struct {
				Boolean *struct{} `json:"boolean"`
			} `json:"configOptions"`
		} `json:"clientCapabilities"`
	}
	if len(m.Params) > 0 && string(m.Params) != "null" {
		if err := json.Unmarshal(m.Params, &p); err != nil {
			s.reply(m.ID, nil, fail(codeInvalidParams, "invalid initialize params: %v", err))
			return
		}
	}
	caps := capabilities{
		Notices:  p.ClientCapabilities.Session != nil && p.ClientCapabilities.Session.Notices != nil,
		Plan:     p.ClientCapabilities.Plan != nil,
		Subagent: p.ClientCapabilities.Subagents != nil,
		Boolean:  p.ClientCapabilities.SessionConfig != nil && p.ClientCapabilities.SessionConfig.Boolean != nil,
	}
	s.mu.Lock()
	s.caps = caps
	s.initialized = true
	s.mu.Unlock()
	s.reply(m.ID, map[string]any{
		"protocolVersion": ProtocolVersion,
		"agentCapabilities": map[string]any{
			"loadSession": true,
			"promptCapabilities": map[string]any{
				"image": false, "audio": false, "embeddedContext": true,
			},
			"mcpCapabilities": map[string]any{"http": false, "sse": false},
			"sessionCapabilities": map[string]any{
				"list": map[string]any{}, "resume": map[string]any{}, "close": map[string]any{},
			},
		},
		"agentInfo":   map[string]any{"name": "sleipnir", "title": "Sleipnir", "version": s.cfg.Version},
		"authMethods": []any{},
	}, nil)
}

// setupParams are the fields of session/new, session/load and session/resume
// this agent uses.
type setupParams struct {
	SessionID             string          `json:"sessionId"`
	Cwd                   string          `json:"cwd"`
	MCPServers            json.RawMessage `json:"mcpServers"`
	SystemPrompt          json.RawMessage `json:"systemPrompt"`
	AdditionalDirectories []string        `json:"additionalDirectories"`
}

// parseSetup decodes the setup params without rejecting fields this agent
// does not use: a client may send newer optional fields.
func parseSetup(m *message) (setupParams, error) {
	var p setupParams
	if len(m.Params) == 0 || string(m.Params) == "null" {
		return p, nil
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return p, err
	}
	return p, nil
}

// newSession creates a session, stores it and returns its setup state.
func (s *Server) newSession(m *message) {
	// Capacity is decided against the attached set and the record count; keep
	// that decision, the installation and the first save in one critical
	// section so two concurrent creators cannot both take the last slot.
	s.createMu.Lock()
	defer s.createMu.Unlock()
	p, err := parseSetup(m)
	if err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "invalid session/new params: %v", err))
		return
	}
	dir, err := s.mcpCfg.ResolveDir(p.Cwd, false)
	if err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	if err := s.makeRoom(); err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	mcpFile, err := writeMCPConfig(s, p.MCPServers)
	if err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "mcpServers: %v", err))
		return
	}
	sess := newSession(s, dir, p.Cwd, mcpFile, systemText(p.SystemPrompt))
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.order = append(s.order, sess.ID)
	s.mu.Unlock()
	sess.save()
	s.reply(m.ID, sess.setupResult(), nil)
}

// openSession loads or resumes a persisted session and replays its updates
// when the client asked for a load.
func (s *Server) openSession(m *message, replay bool) {
	p, err := parseSetup(m)
	if err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "invalid session setup params: %v", err))
		return
	}
	if p.SessionID == "" {
		s.reply(m.ID, nil, fail(codeInvalidParams, "sessionId is required"))
		return
	}
	// A load attaches an existing record; serialize it with the capacity
	// decision in newSession so a detached record cannot be evicted while it
	// is being loaded. createMu is always taken before stateMu and s.mu.
	s.createMu.Lock()
	defer s.createMu.Unlock()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	// Settle the attached turn before reading the record: its final save holds
	// the resume id and replay the replacement must start from. Option changes
	// take the same lock, so none can land between the read and the swap.
	s.mu.Lock()
	attached := s.sessions[p.SessionID]
	s.mu.Unlock()
	if attached != nil {
		// Refuse new turns before cancelling, and keep them refused until the
		// replacement is installed. A prompt admitted in that window would be
		// unowned after the swap.
		attached.retire()
		if j := attached.cancelJob(); j != nil {
			<-j.done
		}
		attached.save()
	}
	rec, err := s.readRecord(p.SessionID)
	if err != nil {
		if attached != nil {
			attached.unretire()
		}
		s.reply(m.ID, nil, fail(codeNotFound, "unknown session %s", p.SessionID))
		return
	}
	if s.afterRead != nil {
		s.afterRead()
	}
	dir := rec.Dir
	if p.Cwd != "" {
		dir, err = s.mcpCfg.ResolveDir(p.Cwd, false)
		if err != nil {
			if attached != nil {
				attached.unretire()
			}
			s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
			return
		}
		rec.Cwd = dir // subsequent turns start in the directory the client named
	}
	if system := systemText(p.SystemPrompt); system != "" {
		rec.System = system
	}
	mcpFile, err := writeMCPConfig(s, p.MCPServers)
	if err != nil {
		if attached != nil {
			attached.unretire()
		}
		s.reply(m.ID, nil, fail(codeInvalidParams, "mcpServers: %v", err))
		return
	}
	sess := sessionFromRecord(s, rec, dir, mcpFile)
	s.mu.Lock()
	old := s.sessions[sess.ID]
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	if old != nil {
		old.retire()
		s.discardMCP(old)
	}
	if replay {
		for _, update := range rec.Replay {
			s.notify(sess.ID, update)
		}
	}
	s.reply(m.ID, sess.setupResult(), nil)
}

// closeSession cancels a session's turn and forgets it on this connection.
func (s *Server) closeSession(m *message) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(m, &p); err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess != nil {
		// Refuse new turns before cancelling the old one: without this,
		// a prompt arriving during cleanup could attach an unowned child.
		sess.retire()
		if s.afterCloseRetire != nil {
			s.afterCloseRetire()
		}
		if j := sess.cancelJob(); j != nil {
			<-j.done
		}
		s.discardMCP(sess)
		sess.save() // while still attached, so the final state is committed
		s.mu.Lock()
		if s.sessions[p.SessionID] == sess {
			delete(s.sessions, p.SessionID)
		}
		s.mu.Unlock()
	}
	// Closing an unknown session succeeds, as ACP requires.
	s.reply(m.ID, map[string]any{}, nil)
}

// listSessions returns the sessions kept in the state directory, newest first.
func (s *Server) listSessions(m *message) {
	var p struct {
		Cwd    string `json:"cwd"`
		Cursor string `json:"cursor"`
	}
	if err := decodeParams(m, &p); err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	offset := 0
	if p.Cursor != "" {
		if _, err := fmt.Sscanf(p.Cursor, "p%d", &offset); err != nil || offset < 0 {
			s.reply(m.ID, nil, fail(codeInvalidParams, "invalid cursor"))
			return
		}
	}
	recs, err := s.records()
	if err != nil {
		s.reply(m.ID, nil, fail(codeInternal, "sessions: %v", err))
		return
	}
	rows := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		if p.Cwd != "" && rec.Cwd != p.Cwd {
			continue
		}
		rows = append(rows, map[string]any{
			"sessionId": rec.ID, "cwd": rec.Cwd, "title": rec.Title,
			"updatedAt": rec.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	next := ""
	if offset > len(rows) {
		offset = len(rows)
	}
	page := rows[offset:min(offset+maxListPage, len(rows))]
	if offset+maxListPage < len(rows) {
		next = fmt.Sprintf("p%d", offset+maxListPage)
	}
	result := map[string]any{"sessions": page}
	if next != "" {
		result["nextCursor"] = next
	}
	s.reply(m.ID, result, nil)
}

// setConfigOption changes one option of an active session.
func (s *Server) setConfigOption(m *message) {
	var p struct {
		SessionID string `json:"sessionId"`
		ConfigID  string `json:"configId"`
		Value     string `json:"value"`
	}
	if err := decodeParams(m, &p); err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		s.reply(m.ID, nil, fail(codeNotFound, "unknown session %s", p.SessionID))
		return
	}
	if !sess.setValue(p.ConfigID, p.Value) {
		s.reply(m.ID, nil, fail(codeInvalidParams, "unknown option or value %s=%q", p.ConfigID, p.Value))
		return
	}
	sess.save()
	s.reply(m.ID, map[string]any{"configOptions": sess.configOptions()}, nil)
}

// setMode changes the legacy mode selector of an active session.
func (s *Server) setMode(m *message) {
	var p struct {
		SessionID string `json:"sessionId"`
		ModeID    string `json:"modeId"`
	}
	if err := decodeParams(m, &p); err != nil {
		s.reply(m.ID, nil, fail(codeInvalidParams, "%v", err))
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		s.reply(m.ID, nil, fail(codeNotFound, "unknown session %s", p.SessionID))
		return
	}
	if !sess.setValue("mode", p.ModeID) {
		s.reply(m.ID, nil, fail(codeInvalidParams, "unknown mode %q", p.ModeID))
		return
	}
	sess.save()
	s.notify(p.SessionID, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": p.ModeID})
	s.reply(m.ID, map[string]any{}, nil)
}

// decodeParams decodes a method's params into v, treating an omitted or null
// params object as empty.
func decodeParams(m *message, v any) error {
	if len(m.Params) == 0 || string(m.Params) == "null" {
		return nil
	}
	return json.Unmarshal(m.Params, v)
}

// makeRoom removes the oldest persisted sessions that are not attached to
// this connection, and refuses a new session when every retained record is
// attached. Sessions on a connection stay live until their client closes
// them; their records are not deleted from under them.
func (s *Server) makeRoom() error {
	recs, err := s.records()
	if err != nil {
		return nil
	}
	s.mu.Lock()
	attached := make(map[string]bool, len(s.sessions))
	for id := range s.sessions {
		attached[id] = true
	}
	s.mu.Unlock()
	// Attached sessions come first: they stay live until their client closes
	// them. Then keep the newest unattached records, leaving one slot for the
	// session this call is about to create.
	keep := map[string]bool{}
	used := 0
	for _, rec := range recs {
		if attached[rec.ID] {
			keep[rec.ID] = true
			used++
		}
	}
	if used >= maxSessions {
		return fmt.Errorf("%d sessions are attached; close one before starting another", maxSessions)
	}
	for _, rec := range recs {
		if used >= maxSessions-1 {
			break
		}
		if !attached[rec.ID] {
			keep[rec.ID] = true
			used++
		}
	}
	for _, rec := range recs {
		if !keep[rec.ID] {
			_ = os.Remove(s.recordPath(rec.ID))
		}
	}
	return nil
}

// modelChoice is one model of the ACP "model" selector.
type modelChoice struct {
	Value   string
	Name    string
	Context int
}

// discoverModels reads the offline catalogue. It never reaches the network:
// an ACP client asks for a session quickly, and a catalogue that is not cached
// simply leaves the configured model as the only choice.
func discoverModels(ctx context.Context, cfg Config) ([]modelChoice, string) {
	loaded, _, err := config.Load(config.LoadOpts{Home: cfg.Home, UntrustedProject: true})
	current := cfg.DefaultModel
	if err == nil && current == "" {
		current = loaded.Models.Default
	}
	choices := []modelChoice{}
	if current != "" {
		choices = append(choices, modelChoice{Value: current, Name: modelLabel(current)})
	}
	if err != nil {
		return choices, current
	}
	res := catalog.FetchResult(ctx, catalog.Options{Home: cfg.Home, Config: loaded, Offline: true})
	seen := map[string]bool{current: true}
	for _, e := range res.Entries {
		if len(choices) >= maxModelChoices {
			break
		}
		if e.Ref == "" || seen[e.Ref] || !(catalog.Filter{}).Keep(e, nil) {
			continue
		}
		seen[e.Ref] = true
		choices = append(choices, modelChoice{Value: e.Ref, Name: modelLabel(e.Ref), Context: modelContext(e)})
	}
	return choices, current
}

// modelLabel is a short display name for a "<provider>/<model>" reference.
func modelLabel(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// modelContext reads a catalogue entry's context window when it is known.
func modelContext(e catalog.Entry) int {
	row := catalog.RowOf(e)
	return row.Context
}

// sortRecords orders session records newest first.
func sortRecords(recs []*sessionRecord) {
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].UpdatedAt.Equal(recs[j].UpdatedAt) {
			return recs[i].ID < recs[j].ID
		}
		return recs[i].UpdatedAt.After(recs[j].UpdatedAt)
	})
}
