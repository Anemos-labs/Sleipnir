// Package session assembles a working Sleipnir: it builds the provider, tools,
// permission engine, checkpoints, layered prompt (constitution, shared project
// pin, role pins), event log and either one agent or a manager-led swarm, and
// runs goals through them.
//
// Everything here is wiring. The policies (what to cache, when to compact, who
// may write) live in the packages it connects; the session's job is to connect
// them the same way for the CLI, the RL rollout harness and tests, so a run
// recorded in one place behaves like a run in another.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/checkpoint"
	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/hooks"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/memory"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/skills"
	"github.com/reee344/sleipnir/internal/swarm"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/tools/fs"
	"github.com/reee344/sleipnir/internal/tools/recall"
	"github.com/reee344/sleipnir/internal/tools/shell"
	"github.com/reee344/sleipnir/internal/tools/skilltool"
	"github.com/reee344/sleipnir/internal/tools/web"
)

// Version is stamped into session.start; the CLI overrides it at build time.
var Version = "dev"

// Options describes one session. Zero values mean "sensible default".
type Options struct {
	// Cwd is where the agent works; Root is the project root (default: found by
	// walking up from Cwd to a .git or .sleipnir directory).
	Cwd, Root string
	Home      string
	// Config is the resolved configuration; nil loads it from Cwd/Root/Home.
	Config *config.Config
	// Model is "provider/model" or a bare model id (see ResolveModel).
	Model string
	// Provider and ModelInfo, when set, replace provider construction (tests, the
	// RL harness with its own policy client).
	Provider  provider.Provider
	ModelInfo *cost.Model
	// CaptureTokens asks the endpoint for token ids and logprobs (RL rollouts).
	CaptureTokens bool

	// ID names the session; Dir is where events.jsonl and blobs/ live (default
	// <state>/sessions/<id>).
	ID, Dir string
	// Resume continues an earlier single-agent session instead of starting a new
	// one: a session id, a session directory, or "latest" (the newest session of
	// this project). The conversation, notes and spine come back from the newest
	// snapshot, the log and checkpoints continue in the same directory, and the
	// first request re-writes the cached prefix once (see agent.Snapshot).
	Resume string

	// Permissions.
	Mode     perm.Mode
	Prompter perm.Prompter

	// Swarm runs a manager plus workers instead of a single agent.
	Swarm      bool
	MaxAgents  int
	Roles      swarm.Roles
	Verify     string // command the harness runs before a worker's task may leave "doing"
	RoleModels map[string]string

	// Limits.
	MaxSteps      int
	BudgetUSD     float64
	ContextWindow int // overrides the model's window (RL: small windows force compaction)
	Params        core.Params

	// Shared-layer contents.
	NoRecon     bool
	ReconBudget int // tokens (default 5000)
	// TrustProject allows project-level instruction files to be loaded. Headless
	// callers set it explicitly: a hostile repository's AGENTS.md is a prompt
	// injection vector.
	TrustProject bool

	// UI.
	Sink    agent.Sink
	NewSink func(agentID string) agent.Sink

	Now func() time.Time
	// ShellEnv, when non-nil, is the whole environment shell commands start from
	// (RL rollouts: a private HOME, no credentials); nil inherits the process's.
	ShellEnv []string
	// ShellWrap is an argv prefix every shell command runs under (a network-less
	// namespace for RL rollouts).
	ShellWrap []string
	// Meta is recorded on the session.start event (run provenance: task, sample,
	// seed, policy and sampling of an RL rollout).
	Meta map[string]any
	// NoWeb omits the web tools.
	NoWeb bool
	// Offline skips network lookups made for convenience (model catalogue).
	Offline bool
}

// Session is a running assembly. Run may be called repeatedly; the agent (or
// manager) keeps its thread between calls.
type Session struct {
	opts Options
	cfg  *config.Config

	ID, Dir string
	Log     *events.Log
	Blobs   events.Blobs

	Model    cost.Model
	Provider provider.Provider

	Registry *tools.Registry
	Specs    []core.ToolSpec
	Perm     *perm.Engine
	Ckpt     *checkpoint.Store
	Shared   *kv.Layer
	Recon    *Recon
	Memory   []memory.Source

	Agent *agent.Agent
	Swarm *swarm.Swarm

	// Skills is the catalogue of skills the session discovered; Roles the swarm's
	// roles, built-in plus project and user definitions.
	Skills *skills.Catalog
	Roles  swarm.Roles

	ext         *extensions
	hooks       *hooks.Runner
	hookAdapter *hookAdapter
	shell       *shell.Manager

	mu      sync.Mutex
	started bool
	closed  bool
	turn    int
}

// Result is what a Run produced.
type Result struct {
	Text        string
	Steps       int
	Usage       core.Usage
	CostUSD     float64
	Stop        core.StopReason
	Compactions int
	SessionID   string
	Dir         string
}

// New builds a session.
func New(ctx context.Context, o Options) (*Session, error) {
	if o.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		o.Cwd = wd
	}
	cwd, err := filepath.Abs(o.Cwd)
	if err != nil {
		return nil, err
	}
	o.Cwd = cwd
	if o.Root == "" {
		o.Root, _ = config.FindRoot(cwd)
	}
	if root, err := filepath.Abs(o.Root); err == nil {
		o.Root = root
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Resume != "" {
		dir, err := ResolveResume(o.Home, o.Root, o.Resume)
		if err != nil {
			return nil, err
		}
		if o.Swarm {
			return nil, errors.New("resuming a swarm session is not supported yet")
		}
		o.Dir, o.ID = dir, filepath.Base(dir)
	}
	cfg := o.Config
	if cfg == nil {
		var err error
		var rep *config.Report
		cfg, rep, err = config.Load(config.LoadOpts{Cwd: cwd, Root: o.Root, Home: o.Home, UntrustedProject: !o.TrustProject})
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		if !o.TrustProject && rep != nil && len(rep.ProjectRisks) > 0 && o.Sink != nil {
			var names []string
			for _, r := range rep.ProjectRisks {
				names = append(names, r.Path)
			}
			o.Sink.Notice("", "warn", "ignored security-sensitive settings from the project's config ("+strings.Join(names, ", ")+"); pass --trust-project to apply them")
		}
	}
	s := &Session{opts: o, cfg: cfg}

	// Identity and storage.
	s.ID = o.ID
	if s.ID == "" {
		s.ID = newID(o.Now())
	}
	s.Dir = o.Dir
	if s.Dir == "" {
		s.Dir = filepath.Join(stateRoot(o.Home), "sessions", s.ID)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	if s.Log, err = events.Open(s.Dir, s.ID); err != nil {
		return nil, err
	}
	if s.Blobs, err = events.NewDirBlobs(filepath.Join(s.Dir, "blobs")); err != nil {
		return nil, err
	}

	// Model and provider.
	if err := s.buildProvider(ctx); err != nil {
		s.Log.Close()
		return nil, err
	}
	if o.ContextWindow > 0 {
		s.Model.ContextTokens = o.ContextWindow
	}

	// Hooks, then skills and role definitions, then permissions (which need the
	// roles' profiles and put PermissionRequest hooks in front of the prompter).
	var hookWarns []string
	if s.hooks, hookWarns = newHookRunner(cfg, s.ID, o.Root); s.hooks != nil {
		s.hookAdapter = &hookAdapter{s: s, run: s.hooks}
	}
	s.loadExtensions()
	s.ext.warnings = append(s.ext.warnings, hookWarns...)
	s.Skills, s.Roles = s.ext.skills, s.ext.roles
	for _, w := range s.ext.warnings {
		s.Log.Emit("", "notice", map[string]any{"level": "warn", "msg": w})
		if o.Sink != nil {
			o.Sink.Notice("", "warn", w)
		}
	}

	// Permissions and checkpoints.
	if err := s.buildPerm(); err != nil {
		s.Log.Close()
		return nil, err
	}
	if s.Ckpt, err = checkpoint.New(filepath.Join(s.Dir, "checkpoints"), s.Blobs, o.Root); err != nil {
		s.Log.Close()
		return nil, fmt.Errorf("checkpoints: %w", err)
	}

	// Prompt layers, then tools, then the agent or swarm.
	if err := s.buildShared(ctx); err != nil {
		s.Log.Close()
		return nil, err
	}
	if err := s.build(); err != nil {
		s.Log.Close()
		return nil, err
	}
	if o.Resume != "" {
		if err := s.restore(); err != nil {
			s.Log.Close()
			return nil, fmt.Errorf("resume: %w", err)
		}
	}
	return s, nil
}

func newID(t time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return t.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// stateRoot is where sessions are stored: $SLEIPNIR_HOME, else ~/.sleipnir.
func stateRoot(home string) string {
	if v := os.Getenv("SLEIPNIR_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".sleipnir")
}

func (s *Session) buildProvider(ctx context.Context) error {
	o := s.opts
	if o.Provider != nil {
		s.Provider = o.Provider
		if o.ModelInfo != nil {
			s.Model = *o.ModelInfo
		} else {
			id := o.Model
			if id == "" {
				id = "unknown"
			}
			if m, ok := cost.Defaults().Lookup(id); ok {
				s.Model = m
			} else {
				s.Model = cost.Fallback(id)
				s.Model.Cache = o.Provider.Profile().Cache
			}
		}
		return nil
	}
	ref, err := ResolveModel(s.cfg, o.Model)
	if err != nil {
		return err
	}
	p, m, err := BuildProvider(s.cfg, ref, ProviderOptions{CaptureTokens: o.CaptureTokens})
	if err != nil {
		return err
	}
	s.Provider, s.Model = p, m
	if !o.Offline {
		s.Model = EnrichModel(ctx, filepath.Join(stateRoot(o.Home), "cache"), p.Profile().BaseURL, s.Model)
	}
	return nil
}

func (s *Session) buildPerm() error {
	o := s.opts
	mode := o.Mode
	if mode == "" {
		mode = perm.Mode(s.cfg.Permissions.Mode)
	}
	roles := map[string]perm.RoleProfile{}
	for name, rp := range s.cfg.Permissions.Roles {
		roles[name] = perm.RoleProfile{Mode: perm.Mode(rp.Mode), Allow: rp.Allow, Ask: rp.Ask, Deny: rp.Deny}
	}
	// A role definition can only tighten what its role may do.
	for name, p := range s.ext.profiles {
		if _, ok := roles[name]; !ok {
			roles[name] = p
		}
	}
	// Read-only roles are read-only in the engine, not merely by a command
	// allowlist in the swarm: the engine understands shell syntax.
	for name, role := range s.ext.roles {
		if role.ReadOnly {
			if _, ok := roles[name]; !ok {
				roles[name] = perm.RoleProfile{Mode: perm.ModePlan, Allow: readOnlyRoleAllow}
			}
		}
	}
	ask := append(append([]string(nil), protectedConfigDirs...), s.cfg.Permissions.Ask...)
	e, err := perm.NewEngine(perm.Config{
		Mode: mode, Root: o.Root, Home: o.Home,
		Allow: s.cfg.Permissions.Allow, Ask: ask, Deny: s.cfg.Permissions.Deny,
		Roles: roles, Prompter: s.hookPrompter(o.Prompter),
	})
	if err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	s.Perm = e
	return nil
}

// readOnlyRoleAllow lets read-only roles (reviewers, scouts) verify what they
// review: run the project's tests and static checks. Everything that writes or
// executes anything else stays denied by the plan-mode profile; the engine
// understands shell syntax, so "go test ./... && rm -rf x" is not allowed by
// the first half.
var readOnlyRoleAllow = []string{
	"Bash(go test:*)", "Bash(go vet:*)", "Bash(pytest:*)", "Bash(npm test)", "Bash(npm run test:*)",
	"Bash(cargo test:*)", "Bash(cargo check:*)", "Bash(make test)",
}

// buildShared assembles the shared pin: the deterministic project survey plus the
// project's own instruction files, both budgeted (see ReconOptions).
func (s *Session) buildShared(ctx context.Context) error {
	var segs []kv.Segment
	est := core.NewBytesEstimator()
	if !s.opts.NoRecon {
		rc, err := BuildRecon(ctx, ReconOptions{Root: s.opts.Root, BudgetTokens: s.opts.ReconBudget, Est: est})
		if err == nil {
			s.Recon = rc
			segs = append(segs, rc.Segments...)
		}
	}
	srcs, err := memory.Load(memory.Opts{Root: s.opts.Root, Cwd: s.opts.Cwd, Home: s.opts.Home})
	if err != nil {
		// Unreadable instruction files are reported, never fatal.
		s.Log.Emit("", "notice", map[string]any{"level": "warn", "msg": "instruction files: " + err.Error()})
	}
	if !s.opts.TrustProject {
		srcs = userScopeOnly(srcs)
	}
	s.Memory = srcs
	if txt := memory.Render(srcs); strings.TrimSpace(txt) != "" {
		segs = append(segs, kv.Segment{Key: "instructions", Text: fitTokens(txt, 3000, est), Vol: kv.VolEpoch})
	}
	if txt := s.ext.skillsSegment(); txt != "" {
		segs = append(segs, kv.Segment{Key: "skills", Text: txt, Vol: kv.VolEpoch})
	}
	if len(segs) > 0 {
		s.Shared = kv.NewLayer("shared", kv.KindShared, 1, segs)
	}
	return nil
}

// userScopeOnly keeps the user's own instruction files when the project is not
// trusted: project files can carry prompt injection. The user file's imports
// are the user's too; the loader shows them with a "~/" path, and only ever
// reads them from the user's own directory.
func userScopeOnly(srcs []memory.Source) []memory.Source {
	var out []memory.Source
	for _, s := range srcs {
		if s.Scope == memory.ScopeUser || (s.Scope == memory.ScopeImport && strings.HasPrefix(s.Path, "~/")) {
			out = append(out, s)
		}
	}
	return out
}

// build registers tools and creates the agent or swarm.
func (s *Session) build() error {
	o := s.opts
	limits := tools.DefaultLimits()
	if t := s.cfg.Tools; t.MaxOutputChars > 0 {
		limits.MaxOutputChars = t.MaxOutputChars
	}
	if t := s.cfg.Tools; t.DefaultTimeoutSec > 0 {
		limits.DefaultTimeout = t.DefaultTimeout()
	}
	if t := s.cfg.Tools; t.MaxTimeoutSec > 0 {
		limits.MaxTimeout = t.MaxTimeout()
	}

	reg := tools.NewRegistry()
	fs.Register(reg)
	s.shell = shell.NewManager(shell.Options{BaseEnv: o.ShellEnv, Wrap: o.ShellWrap})
	shell.Register(reg, s.shell)
	if !o.NoWeb {
		// ConfigFromEnv routes through HTTPS_PROXY when one is set (sandboxed and
		// corporate networks resolve names at the proxy) and enables web_search when
		// a search backend key is configured in the environment.
		wc := web.ConfigFromEnv()
		wc.AllowPrivate = s.cfg.Tools.WebAllowPrivate
		wc.AllowHosts = s.cfg.Tools.WebAllowHosts
		web.Register(reg, wc)
	}
	archive := kv.NewArchive(s.Blobs)
	reg.Register(recall.New(archive))
	// Always registered: the tool list must not depend on the project.
	reg.Register(skilltool.New(s.Skills))

	constText := agent.Constitution(agent.ConstitutionOpts{Swarm: o.Swarm})
	constLayer := kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: constText, Vol: kv.VolFrozen}})

	planner := kv.DefaultPlanner()
	if c := s.cfg.Cache; c.ThreadSoftLimitTokens > 0 {
		planner.SoftThreadTokens = c.ThreadSoftLimitTokens
	}
	if c := s.cfg.Cache; c.CompactThresholdTokens > 0 {
		planner.HardThreadTokens = c.CompactThresholdTokens
	}
	kvPol := kv.DefaultPolicy()
	if c := s.cfg.Cache; c.MinLayerForBreakpoint > 0 {
		kvPol.MinLayerForBreakpoint = c.MinLayerForBreakpoint
	}
	params := o.Params
	if params.MaxTokens == 0 {
		params.MaxTokens = min(s.Model.MaxOutput, 16000)
	}
	shared := s.Shared

	files := tools.NewFileState()
	handles := tools.NewHandles()
	est := core.NewBytesEstimator()

	if !o.Swarm {
		specs, err := reg.Specs()
		if err != nil {
			return err
		}
		s.Registry, s.Specs = reg, specs
		role := soloRole()
		a, err := agent.New(agent.Config{
			ID: "main", Role: role.Name, Model: s.Model, Provider: s.Provider, Tools: reg, ToolSpecs: specs,
			Const: constLayer, Shared: shared, RoleL: role.Layer(),
			Params: params, Events: s.Log, Blobs: s.Blobs, Archive: archive, Files: files,
			Guard: writeGuard{store: s.Ckpt}, Snap: s.Ckpt, Handles: handles, Perm: s.Perm,
			Sink: o.Sink, Workdir: o.Cwd, Root: o.Root, Limits: limits,
			Planner: planner, KVPolicy: kvPol, SessionID: s.ID, Est: est, Now: o.Now,
			MaxSteps: orDefault(o.MaxSteps, 200), BudgetUSD: o.BudgetUSD, CaptureTokens: o.CaptureTokens,
			Hooks: s.agentHooks(),
		})
		if err != nil {
			return err
		}
		s.Agent = a
		return nil
	}

	// Swarm: build it first (its tools need it), register those tools, freeze the
	// tool list, then hand the frozen list back. Every agent then sends the same
	// tools array, byte for byte.
	sc := swarm.DefaultConfig()
	sc.SessionID = s.ID
	if v := s.cfg.Swarm.MaxAgents; v > 0 {
		sc.MaxAgents = v
	}
	if o.MaxAgents > 0 {
		sc.MaxAgents = o.MaxAgents
	}
	if v := s.cfg.Swarm.RequestsPerMinute; v > 0 {
		sc.RPM = v
	}
	if v := s.cfg.Swarm.MaxConcurrentRequests; v > 0 {
		sc.MaxConcurrent = v
	}
	if v := s.cfg.Cache.AffinityShards; v > 0 {
		sc.AffinityShards = v
	}
	if v := s.cfg.Swarm.BudgetUSD; v > 0 {
		sc.BudgetUSD = v
	}
	if o.BudgetUSD > 0 {
		sc.BudgetUSD = o.BudgetUSD
	}
	if v := s.cfg.Cache.HotMaxTokens; v > 0 {
		sc.Hot.MaxTokens = v
	}
	sc.VerifyCmd = o.Verify
	sc.Verify = runVerify

	deps := swarm.Deps{
		Provider: s.Provider, Model: s.Model, Registry: reg,
		Const: constLayer, Shared: shared,
		Events: s.Log, Blobs: s.Blobs, Archive: archive, Files: files, Perm: s.Perm,
		Snap: s.Ckpt, Handles: handles,
		Workdir: o.Cwd, Root: o.Root, Params: params, Planner: planner, Est: est, Limits: limits, Now: o.Now,
		NewSink: o.NewSink, CaptureTokens: o.CaptureTokens, OnWrite: s.Ckpt.After, Hooks: s.agentHooks(),
	}
	if len(o.RoleModels) > 0 {
		deps.RoleModels = map[string]swarm.RoleModel{}
		for role, ref := range o.RoleModels {
			mr, err := ResolveModel(s.cfg, ref)
			if err != nil {
				return fmt.Errorf("role model %s=%s: %w", role, ref, err)
			}
			p, m, err := BuildProvider(s.cfg, mr, ProviderOptions{CaptureTokens: o.CaptureTokens})
			if err != nil {
				return fmt.Errorf("role model %s=%s: %w", role, ref, err)
			}
			deps.RoleModels[role] = swarm.RoleModel{Provider: p, Model: m}
		}
	}
	if len(s.ext.defs) > 0 {
		if deps.RoleModels == nil {
			deps.RoleModels = map[string]swarm.RoleModel{}
		}
		for _, d := range s.ext.defs {
			if d.Model == "" || o.RoleModels[d.Name] != "" {
				continue
			}
			mr, err := ResolveModel(s.cfg, d.Model)
			if err != nil {
				return fmt.Errorf("role %s: model %s: %w", d.Name, d.Model, err)
			}
			p, m, err := BuildProvider(s.cfg, mr, ProviderOptions{CaptureTokens: o.CaptureTokens})
			if err != nil {
				return fmt.Errorf("role %s: model %s: %w", d.Name, d.Model, err)
			}
			deps.RoleModels[d.Name] = swarm.RoleModel{Provider: p, Model: m}
		}
	}
	sw := swarm.New(sc, deps, s.ext.roles)
	for _, t := range sw.Tools() {
		reg.Register(t)
	}
	specs, err := reg.Specs()
	if err != nil {
		return err
	}
	sw.SetToolset(reg, specs)
	s.Registry, s.Specs, s.Swarm = reg, specs, sw
	return nil
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

// soloRole is the role of a single-agent session. It is deliberately close to a
// swarm worker so a policy trained alone transfers to a team.
func soloRole() swarm.Role {
	return swarm.Role{Name: "worker", Short: "w", Priority: agent.PrioInteractive, MaxSteps: 200, Pin: soloPin}
}

const soloPin = `You are working alone on the user's task. Understand it, make the change with the smallest correct diff, and check your work by running the project's own build and tests before you report. Say plainly what you did, what you verified and what remains.`

// writeGuard routes post-write notifications to the checkpoint store so a rewind
// can tell an agent's last write from a human's later edit.
type writeGuard struct{ store *checkpoint.Store }

func (writeGuard) BeforeWrite(string, string) error { return nil }
func (g writeGuard) AfterWrite(agent, path string)  { g.store.After(agent, path) }

// Run gives the session a goal and returns when the agent (or manager) finishes.
func (s *Session) Run(ctx context.Context, goal string) (*Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("session is closed")
	}
	first := !s.started
	s.started = true
	s.turn++
	turn := s.turn
	s.mu.Unlock()

	goal, err := s.promptHook(ctx, first, goal)
	if err != nil {
		return nil, err
	}

	if first {
		start := map[string]any{
			"version": Version, "model": s.Model.ID, "provider": s.Provider.Profile().Name, "dialect": s.Provider.Profile().Dialect,
			"swarm": s.opts.Swarm, "root": s.opts.Root, "cwd": s.opts.Cwd, "renderer": kv.RendererVersion,
			"recon_tokens": reconTokens(s.Recon), "shared_hash": s.Shared.Hash().Short(),
		}
		if len(s.opts.Meta) > 0 {
			start["meta"] = s.opts.Meta
		}
		if s.opts.Resume != "" {
			start["resumed"] = true
		}
		s.Log.Emit("", events.TypeSessionStart, start)
	}
	s.Ckpt.Begin(fmt.Sprintf("turn %d: %s", turn, oneLine(goal, 60)))

	if s.Swarm != nil {
		s.Swarm.Start(ctx)
		res, err := s.Swarm.RunManager(ctx, goal)
		return s.result(res, err)
	}
	res, err := s.Agent.Run(ctx, goal)
	return s.result(res, err)
}

func (s *Session) result(res *agent.Result, err error) (*Result, error) {
	out := &Result{SessionID: s.ID, Dir: s.Dir}
	if res != nil {
		out.Text, out.Steps, out.Usage, out.CostUSD, out.Stop, out.Compactions = res.Text, res.Steps, res.Usage, res.CostUSD, res.Stop, res.Compactions
	}
	if s.Swarm != nil {
		out.CostUSD = s.Swarm.TotalCost()
	}
	return out, err
}

func reconTokens(r *Recon) int {
	if r == nil {
		return 0
	}
	return r.Tokens
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// Compact folds the agent's (or the manager's) thread now, at the user's request,
// with an optional word on what matters most. PreCompact and PostCompact hooks run
// around it (trigger "manual"); a PreCompact hook can refuse.
func (s *Session) Compact(ctx context.Context, focus string) (agent.CompactReport, error) {
	a := s.Agent
	if s.Swarm != nil {
		a = s.Swarm.Manager()
	}
	if a == nil {
		return agent.CompactReport{Mode: "none"}, errors.New("nothing to compact yet: send a goal first")
	}
	if s.hookAdapter != nil {
		res := s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.PreCompact, Agent: a.ID(), Extra: map[string]any{"trigger": "manual"}})
		if res.Blocked {
			return agent.CompactReport{Mode: "none"}, fmt.Errorf("blocked by a hook: %s", firstNonEmpty(res.Reason, "no reason given"))
		}
	}
	rep, err := a.CompactNow(ctx, focus)
	if err == nil && s.hookAdapter != nil {
		s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.PostCompact, Agent: a.ID(), Extra: map[string]any{"trigger": "manual"}})
	}
	return rep, err
}

// Send steers a running agent (or the manager) with a message at its next turn
// boundary.
func (s *Session) Send(text string) {
	switch {
	case s.Swarm != nil:
		if m := s.Swarm.Manager(); m != nil {
			m.Send(text)
		}
	case s.Agent != nil:
		s.Agent.Send(text)
	}
}

// Close stops agents, background jobs and the log. It is safe to call twice.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.started
	s.mu.Unlock()
	if s.Swarm != nil {
		s.Swarm.Shutdown()
	}
	if s.shell != nil {
		s.shell.Shutdown()
	}
	if started && s.hookAdapter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.SessionEnd, Agent: s.mainAgent(), Extra: map[string]any{"reason": "other"}})
		cancel()
	}
	if started {
		s.Log.Emit("", events.TypeSessionEnd, map[string]any{"cost_usd": s.cost()})
	}
	return s.Log.Close()
}

func (s *Session) cost() float64 {
	if s.Swarm != nil {
		return s.Swarm.TotalCost()
	}
	if s.Agent != nil {
		_, c := s.Agent.Usage()
		return c
	}
	return 0
}
