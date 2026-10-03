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
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/hooks"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/memory"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/skills"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tools/fs"
	"github.com/anemos-labs/sleipnir/internal/tools/memtool"
	"github.com/anemos-labs/sleipnir/internal/tools/plantool"
	"github.com/anemos-labs/sleipnir/internal/tools/recall"
	"github.com/anemos-labs/sleipnir/internal/tools/shell"
	"github.com/anemos-labs/sleipnir/internal/tools/skilltool"
	"github.com/anemos-labs/sleipnir/internal/tools/web"
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
	// AskTimeout is how long a question to the person waits for its answer before it is refused (perm.Config.AskTimeout); zero waits.
	AskTimeout time.Duration
	// Allow are rules that need no question in this run, on top of the configuration's (sleipnir run --allow): what the person
	// who started the run pre-approved on the command line, which is the one place a run with nobody to ask can get an answer.
	Allow []string
	// OutagePatience is how long an agent keeps retrying a request that fails because the endpoint is down or overloaded (an HTTP
	// status of 500 or more, or 429), beyond the six attempts every failure gets. Zero is DefaultOutagePatience; negative is
	// the six attempts only (what a rollout wants: the runner repeats the rollout, and waiting would eat the run's own clock).
	OutagePatience time.Duration

	// Swarm runs a manager plus workers instead of a single agent.
	Swarm      bool
	MaxAgents  int
	Roles      swarm.Roles
	Verify     string // command the harness runs before a worker's task may leave "doing"
	RoleModels map[string]string
	// Interactive says a person is at the keyboard across turns (sleipnir chat).
	// Batch runs (run, swarm, RL rollouts) leave it false and the swarm holds the
	// manager: it may not give a final answer while workers are running or tasks are
	// unreviewed. An interactive session is not held (workers legitimately outlive a
	// turn) and instead wakes the idle manager when finished work needs it, with the
	// swarm running for the life of the session rather than of one turn.
	Interactive bool
	// Isolation overrides swarm.isolation for this session: "none" (every agent edits
	// the one checkout, guarded by leases) or "worktree" (every writer gets a git
	// worktree of its own and its finished work goes through a verifying merge queue;
	// see isolate.go). Empty leaves it to the configuration. It applies to swarms.
	Isolation string
	// Commit, with worktree isolation, makes the end of the run commit the verified
	// result onto the person's branch (a fast-forward; the checkout must be clean and
	// on a branch) instead of leaving it as uncommitted edits in the working tree.
	Commit bool
	// Mailman, when set, overrides swarm.mailman for this session: true routes worker
	// mail through a mailman agent that digests bursts (docs/SWARM-PROTOCOL.md section
	// 5), false delivers it at once. Nil leaves it to the configuration. It applies to
	// swarms; its model is the session's unless RoleModels names one for "mailman".
	Mailman *bool

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
	// NoMCP starts no MCP servers (RL rollouts, tests): the tool list is then the
	// built-in one whatever the configuration says.
	NoMCP bool
	// Offline skips network lookups made for convenience (model catalogue).
	Offline bool
}

// Session is a running assembly. Run may be called repeatedly; the agent (or
// manager) keeps its thread between calls.
type Session struct {
	opts Options
	// newSolo builds the single agent again on the session's current Provider and Model (SwitchModel).
	newSolo func() (*agent.Agent, error)
	plans   *plan.Store // each agent's plan, which a standing goal reads as its requirements (goal.go)
	// judgeUSD is what a standing goal's judge has cost (goal.go), under mu.
	judgeUSD float64

	lastPerm      string // the permission state last written to the log (keepPermissions)
	restoredMode  string // what a resumed session got back (restorePermissions)
	restoredRules int
	// modelRef is the provider/model the session runs on (empty when the provider was given as a value).
	modelRef string
	cfg      *config.Config

	ID, Dir string
	tmp     string // the private TMPDIR of the session's commands (under Dir); "" when the run brings its own environment
	Log     *events.Log
	Blobs   events.Blobs

	Model    cost.Model
	Provider provider.Provider
	// described are the models the session may call (the main one and the roles'), with
	// where each description came from; session.start records them.
	described map[string]described

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

	cfgRep      *config.Report // where each configuration value came from (nil when Options.Config was given)
	trust       *trustInfo     // how the project's own files came to be used, or why not (nil when it has none)
	mcp         *mcpState
	archive     *kv.Archive    // folded turns, for recall
	handles     *tools.Handles // recall handles of truncated output
	unlock      func()         // releases the lock on the session directory
	ext         *extensions
	hooks       *hooks.Runner
	hookAdapter *hookAdapter
	shell       *shell.Manager

	// refused are the commands that were refused for want of anyone to ask (permaudit.go), for the hint a run prints at its end.
	refusedMu sync.Mutex
	refused   []refusedCommand

	mu      sync.Mutex
	started bool
	closed  bool
	// endReason is why the session ended (SetEndReason), for the SessionEnd hooks.
	endReason string
	// budget is the most the session may spend (0: no limit): the swarm's cap, or the
	// single agent's --budget-usd.
	budget float64
	turn   int
	// swarmCtx is the context an interactive session's swarm runs on: it lives until
	// Close, not until the end of one turn (see swarmContext).
	swarmCtx  context.Context
	swarmStop context.CancelFunc

	// iso is the worktree isolation plan (nil: the swarm edits the shared checkout);
	// finish is the report of the end of an isolated run, once it has happened.
	iso    *isoPlan
	finish *swarm.IntegrationReport
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
	// Unfinished says what a swarm's manager left undone when it stopped (running workers, submissions nobody judged, tasks
	// nobody finished); empty when the work was settled, and for a run that is not a swarm. A caller that wants to know whether
	// the run did what it was asked reads it here instead of parsing the answer.
	Unfinished string
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
	// Said at once: a directory that is not there otherwise fails in whatever first tries to use it (the survey, the first command),
	// with a message about that and not about the directory.
	switch fi, serr := os.Stat(cwd); {
	case errors.Is(serr, os.ErrNotExist):
		return nil, fmt.Errorf("working directory %s does not exist", cwd)
	case serr != nil:
		return nil, fmt.Errorf("working directory %s: %w", cwd, serr)
	case !fi.IsDir():
		return nil, fmt.Errorf("working directory %s is not a directory", cwd)
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
		o.Dir, o.ID = dir, filepath.Base(dir)
		if prior := inspectLog(dir); prior.isolated && prior.recovery {
			if prior.id == "" || prior.id == "." || prior.id == ".." || strings.ContainsAny(prior.id, "/\\\x00\r\n") {
				return nil, errors.New("invalid session id in isolation recovery metadata")
			}
			o.ID = prior.id
		}
	}
	tinfo, err := resolveTrust(ctx, &o)
	if err != nil {
		return nil, err
	}
	cfg := o.Config
	var rep *config.Report
	if cfg == nil {
		var err error
		cfg, rep, err = config.Load(config.LoadOpts{Cwd: cwd, Root: o.Root, Home: o.Home, UntrustedProject: !o.TrustProject})
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		if !o.TrustProject && rep != nil && len(rep.ProjectRisks) > 0 && o.Sink != nil {
			var names []string
			for _, r := range rep.ProjectRisks {
				names = append(names, r.Path)
			}
			o.Sink.Notice("", "warn", "ignored security-sensitive settings from the project's config ("+strings.Join(names, ", ")+"); "+trustHint("apply"))
		}
	}
	if err := checkSwarmSize(cfg, o); err != nil {
		return nil, err
	}
	if b := o.BudgetUSD; math.IsNaN(b) || math.IsInf(b, 0) || b < 0 {
		// Anything but a positive number reads as "no budget" below; a typo must not do that.
		return nil, fmt.Errorf("budget: --budget-usd must be zero (no limit) or a positive amount, got %v", b)
	}
	s := &Session{opts: o, cfg: cfg, cfgRep: rep, trust: tinfo}

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
	if o.ShellEnv == nil {
		// Commands get a private TMPDIR: the scratch files of a build or a test run are
		// otherwise refused in the shared /tmp (and would be readable there).
		if tmp := filepath.Join(s.Dir, "tmp"); os.MkdirAll(tmp, 0o700) == nil {
			s.tmp = tmp
		}
	}
	unlock, err := lockDir(s.Dir)
	if err != nil {
		return nil, err
	}
	built := false
	defer func() {
		if !built {
			unlock()
		}
	}()
	s.unlock = unlock
	if _, err := os.Stat(filepath.Join(s.Dir, prunedMarker)); err == nil {
		return nil, errors.New("this session is being pruned and cannot be resumed")
	}
	if o.Resume != "" && !hasLog(s.Dir) {
		return nil, errors.New("this session was removed before it could be resumed")
	}
	if s.Log, err = events.Open(s.Dir, s.ID); err != nil {
		return nil, err
	}
	if s.Blobs, err = events.NewDirBlobs(filepath.Join(s.Dir, "blobs")); err != nil {
		return nil, err
	}

	// Worktree isolation is decided before anything expensive is built: a project that
	// cannot be isolated fails at once, with the reason.
	if err := s.planIsolation(ctx); err != nil {
		s.Log.Close()
		return nil, err
	}
	defer func() {
		if !built {
			s.releaseIsolation() // the merge queue and branches of a session that never started
		}
	}()

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
	}
	if o.Sink != nil {
		for _, w := range shownWarnings(s.ext.warnings) {
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
	if err := s.build(ctx); err != nil {
		s.Log.Close()
		return nil, err
	}
	if o.Resume != "" {
		if err := s.restore(); err != nil {
			if s.Swarm != nil {
				s.Swarm.Shutdown()
			}
			s.Log.Close()
			return nil, fmt.Errorf("resume: %w", err)
		}
		s.restorePermissions()
	}
	built = true
	return s, nil
}

// newID combines a UTC timestamp with three random bytes to distinguish sessions started in the
// same second.
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
		src := sourceGiven
		if o.ModelInfo != nil {
			s.Model = *o.ModelInfo
		} else {
			id := o.Model
			if id == "" {
				id = "unknown"
			}
			if m, ok := cost.Defaults().Lookup(id); ok {
				s.Model, src = m, sourceTable
			} else {
				s.Model, src = cost.Fallback(id), sourceFallback
				s.Model.Cache = o.Provider.Profile().Cache
			}
		}
		s.noteModel(s.Model, src)
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
	s.Provider, s.Model, s.modelRef = p, s.describe(ctx, p, m), ref.String()
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
				roles[name] = perm.RoleProfile{Mode: perm.ModePlan, Allow: readOnlyRoleAllow, Deny: readOnlyRoleDeny}
			}
		}
	}
	var extra []string
	if s.iso != nil {
		// The session's worktrees are part of the workspace, and the project's relative
		// rules hold inside each of them: writers work there, each confined to its own
		// (swarm.bindTree). The manager does not edit files in an isolated run (anything
		// it wrote into the shared checkout would bypass the merge queue), so the engine
		// holds it to plan mode as well as the swarm's own check.
		extra = []string{s.iso.dir}
		if _, ok := roles["manager"]; !ok {
			roles["manager"] = perm.RoleProfile{Mode: perm.ModePlan, Allow: readOnlyRoleAllow, Deny: readOnlyRoleDeny}
		}
	}
	if s.mailmanOn() {
		// The mailman is read-only and its one tool is mail (the swarm holds it to that);
		// the engine holds it to plan mode as well.
		if _, ok := roles[swarm.MailmanRoleName]; !ok {
			roles[swarm.MailmanRoleName] = perm.RoleProfile{Mode: perm.ModePlan}
		}
	}
	ask := append(append([]string(nil), protectedConfigDirs...), s.cfg.Permissions.Ask...)
	e, err := perm.NewEngine(perm.Config{
		Mode: mode, Root: o.Root, Home: o.Home, StateDir: stateRoot(o.Home), Tmp: s.tmp, TreeParents: extra,
		Allow: append(append([]string(nil), s.cfg.Permissions.Allow...), o.Allow...), Ask: ask, Deny: s.cfg.Permissions.Deny,
		Roles: roles, Prompter: s.trackAsks(s.hookPrompter(o.Prompter)), AskTimeout: o.AskTimeout, Audit: s.auditPermission,
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

// readOnlyRoleDeny are the tools a read-only role is denied by name. Background jobs
// are session-wide, and the engine alone would answer "touches no files and no
// network: allow" to a reviewer's bash_output on the output of another agent's job.
// What a read-only role runs it runs in the foreground and reads from the result.
var readOnlyRoleDeny = []string{"bash_output", "bash_kill"}

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
		kept := userScopeOnly(srcs)
		if skipped := skippedSources(srcs, kept); len(skipped) > 0 {
			s.notice("", fmt.Sprintf("instruction files of this project were not loaded because the project is not trusted (%s); %s", strings.Join(skipped, ", "), trustHint("load")))
		}
		srcs = kept
	}
	s.Memory = srcs
	if txt := memory.Render(srcs); strings.TrimSpace(txt) != "" {
		budget := instructionTokenBudget(s.cfg.Cache.InstructionMaxTokens, s.Model.ContextTokens)
		fitted, cut := fitInstructions(srcs, budget, est)
		if len(cut) > 0 {
			s.notice("", fmt.Sprintf("instruction files need about %d tokens; the limit is %d. Truncated or omitted: %s. Later, more specific files take precedence. Increase cache.instruction_max_tokens (SLEIPNIR_CACHE_INSTRUCTION_MAX_TOKENS) and restart, or shorten the files; /recon shows the loaded text", est.Tokens(txt), budget, strings.Join(cut, ", ")))
		}
		segs = append(segs, kv.Segment{Key: "instructions", Text: fitted, Vol: kv.VolEpoch})
	}
	if txt := s.ext.skillsSegment(); txt != "" {
		segs = append(segs, kv.Segment{Key: "skills", Text: txt, Vol: kv.VolEpoch})
	}
	if len(segs) > 0 {
		s.Shared = kv.NewLayer("shared", kv.KindShared, 1, segs)
	}
	return nil
}

// skippedSources lists (up to three) the paths in all that are not in kept.
func skippedSources(all, kept []memory.Source) []string {
	keep := map[string]bool{}
	for _, k := range kept {
		keep[k.Path] = true
	}
	var out []string
	n := 0
	for _, a := range all {
		if keep[a.Path] {
			continue
		}
		if n++; n <= 3 {
			out = append(out, a.Path)
		}
	}
	if n > 3 {
		out = append(out, fmt.Sprintf("and %d more", n-3))
	}
	return out
}

// userScopeOnly keeps the user's own instruction files when the project is not
// trusted: project files can carry prompt injection. The user file's imports
// are the user's too; the loader shows them with a "~/" path, and only ever
// reads them from the user's own directory.
func userScopeOnly(srcs []memory.Source) []memory.Source {
	var out []memory.Source
	for _, s := range srcs {
		if s.FromUser() {
			out = append(out, s)
		}
	}
	return out
}

// build registers tools and creates the agent or swarm.
func (s *Session) build(ctx context.Context) error {
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
	s.shell = shell.NewManager(shell.Options{BaseEnv: o.ShellEnv, Wrap: o.ShellWrap, Tmp: s.tmp})
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
	s.archive = archive
	reg.Register(recall.New(archive))
	// Always registered: the tool list must not depend on the project.
	reg.Register(skilltool.New(s.Skills))
	reg.Register(memtool.New(filepath.Join(o.Home, ".sleipnir", "MEMORY.md")))
	plans := plan.NewStore() // each agent's plan: the plan tool sets it, the hot tail shows it back (internal/plan)
	s.plans = plans
	reg.Register(plantool.New(plans))
	// MCP servers add their tools here, before the list is frozen: every agent of
	// the session then sends the same tools array, byte for byte.
	s.startMCP(ctx, reg)

	constText := agent.Constitution(agent.ConstitutionOpts{Swarm: o.Swarm})
	constLayer := kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: constText, Vol: kv.VolFrozen}})

	planner := plannerFor(s.cfg.Cache)
	kvPol := kv.DefaultPolicy()
	if c := s.cfg.Cache; c.MinLayerForBreakpoint > 0 {
		kvPol.MinLayerForBreakpoint = c.MinLayerForBreakpoint
	}
	if c := s.cfg.Cache; c.SharedTTL != "" {
		kvPol.SharedTTL = c.SharedTTLDuration() // 1h asks a provider with explicit breakpoints to keep the shared layers a long time
	}
	params := o.Params
	if params.MaxTokens == 0 {
		params.MaxTokens = min(s.Model.MaxOutput, 16000)
	}
	shared := s.Shared

	files := tools.NewFileState()
	handles := tools.NewHandles()
	s.handles = handles
	est := core.NewBytesEstimator()

	if !o.Swarm {
		if len(o.RoleModels) > 0 && o.Sink != nil {
			o.Sink.Notice("", "warn", "--role-model has no effect without --swarm: this session has a single agent (use --model)")
		}
		specs, err := reg.Specs()
		if err != nil {
			return err
		}
		s.Registry, s.Specs = reg, specs
		s.budget = o.BudgetUSD
		role := soloRole()
		mk := func() (*agent.Agent, error) {
			comp, compModel, err := s.buildCompactor(ctx, o)
			if err != nil {
				return nil, err
			}
			p := params
			if o.Params.MaxTokens == 0 {
				p.MaxTokens = min(s.Model.MaxOutput, 16000)
			}
			return agent.New(agent.Config{
				ID: "main", Role: role.Name, Model: s.Model, Provider: s.Provider, Compactor: comp, CompactorModel: compModel, Tools: reg, ToolSpecs: specs,
				Const: constLayer, Shared: shared, RoleL: role.Layer(),
				Params: p, Events: s.Log, Blobs: s.Blobs, Archive: archive, Files: files,
				Hot: func(id string) []core.Block {
					if f := plan.Frame(plans.Get(id)); f != "" {
						return []core.Block{core.Text(f)}
					}
					return nil
				},
				PlanOpen: plans.Open, VerifyHint: s.testCommand(),
				Guard: writeGuard{store: s.Ckpt}, Snap: s.Ckpt, Handles: handles, Perm: s.Perm,
				Sink: o.Sink, Workdir: o.Cwd, Root: o.Root, Limits: limits,
				Planner: planner, KVPolicy: kvPol, SessionID: s.ID, Est: est, Now: o.Now,
				MaxSteps: orDefault(o.MaxSteps, 200), BudgetUSD: o.BudgetUSD, CaptureTokens: o.CaptureTokens,
				Hooks: s.agentHooks(), OutagePatience: outagePatience(o.OutagePatience),
			})
		}
		a, err := mk()
		if err != nil {
			return err
		}
		s.newSolo = mk
		s.Agent = a
		return nil
	}

	// Swarm: build it first (its tools need it), register those tools, freeze the
	// tool list, then hand the frozen list back. Every agent then sends the same
	// tools array, byte for byte.
	sc := swarm.DefaultConfig()
	sc.SessionID = s.ID
	// swarm.max_agents is the ceiling (checkSwarmSize refused a larger request); a
	// size asked for on the command line may only be smaller.
	if v := s.cfg.Swarm.MaxAgents; v > 0 {
		sc.MaxAgents = v
	}
	if o.MaxAgents > 0 {
		sc.MaxAgents = o.MaxAgents
	}
	// Allow every worker slot to hold a writer, using at least the library default
	// of four. Task scopes and write leases enforce file-level coordination.
	sc.MaxWriters = max(sc.MaxWriters, sc.MaxAgents-1)
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
	s.budget = sc.BudgetUSD
	if v := s.cfg.Cache.HotMaxTokens; v > 0 {
		sc.Hot.MaxTokens = v
	}
	sc.VerifyCmd = o.Verify
	sc.Verify = runVerify
	sc.HoldManager, sc.WakeManager = !o.Interactive, o.Interactive
	sc.Mailman = s.mailmanOn()

	comp, compModel, err := s.buildCompactor(ctx, o)
	if err != nil {
		return err
	}
	deps := swarm.Deps{
		Provider: s.Provider, Model: s.Model, Compactor: comp, CompactorModel: compModel, Registry: reg, Plans: plans,
		Const: constLayer, Shared: shared,
		Events: s.Log, Blobs: s.Blobs, Archive: archive, Files: files, Perm: s.Perm,
		Snap: s.Ckpt, Handles: handles,
		Workdir: o.Cwd, Root: o.Root, Params: params, Planner: planner, KVPolicy: kvPol, Est: est, Limits: limits, Now: o.Now,
		NewSink: o.NewSink, CaptureTokens: o.CaptureTokens, OnWrite: s.Ckpt.After, Hooks: s.agentHooks(),
		OutagePatience: outagePatience(o.OutagePatience),
	}
	if len(o.RoleModels) > 0 {
		if err := s.checkRoleModels(o.RoleModels); err != nil {
			return err
		}
		deps.RoleModels = map[string]swarm.RoleModel{}
		for role, ref := range o.RoleModels {
			if role == CompactorRole {
				continue
			}
			mr, err := ResolveModel(s.cfg, ref)
			if err != nil {
				return fmt.Errorf("role model %s=%s: %w", role, ref, err)
			}
			p, m, err := BuildProvider(s.cfg, mr, ProviderOptions{CaptureTokens: o.CaptureTokens})
			if err != nil {
				return fmt.Errorf("role model %s=%s: %w", role, ref, err)
			}
			deps.RoleModels[role] = swarm.RoleModel{Provider: p, Model: s.describe(ctx, p, m)}
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
			deps.RoleModels[d.Name] = swarm.RoleModel{Provider: p, Model: s.describe(ctx, p, m)}
		}
	}
	// models.roles in the configuration: the lowest-priority source of a role's
	// model (--role-model and a definition's model: come first).
	if len(s.cfg.Models.Roles) > 0 {
		if deps.RoleModels == nil {
			deps.RoleModels = map[string]swarm.RoleModel{}
		}
		names := make([]string, 0, len(s.cfg.Models.Roles))
		for role := range s.cfg.Models.Roles {
			names = append(names, role)
		}
		sort.Strings(names)
		for _, role := range names {
			ref := s.cfg.Models.Roles[role]
			if _, set := deps.RoleModels[role]; set || ref == "" || role == CompactorRole {
				continue
			}
			mr, err := ResolveModel(s.cfg, ref)
			if err != nil {
				return fmt.Errorf("models.roles.%s = %s: %w", role, ref, err)
			}
			p, m, err := BuildProvider(s.cfg, mr, ProviderOptions{CaptureTokens: o.CaptureTokens})
			if err != nil {
				return fmt.Errorf("models.roles.%s = %s: %w", role, ref, err)
			}
			deps.RoleModels[role] = swarm.RoleModel{Provider: p, Model: s.describe(ctx, p, m)}
		}
	}
	iso, err := s.buildIsolation(ctx)
	if err != nil {
		return err
	}
	deps.Isolation = iso
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

// checkSwarmSize refuses a swarm larger than swarm.max_agents allows. The setting is
// a ceiling that the user's file or the environment puts on every session; a request
// for more (--swarm N asks for N agents, the manager included) would otherwise override it
// without a word.
func checkSwarmSize(cfg *config.Config, o Options) error {
	ceil := cfg.Swarm.MaxAgents
	if !o.Swarm || ceil <= 0 || o.MaxAgents <= ceil {
		return nil
	}
	return fmt.Errorf("swarm: %d agents requested (the manager included) but swarm.max_agents caps a session at %d; raise swarm.max_agents or ask for fewer agents", o.MaxAgents, ceil)
}

// CompactorRole is the name under which models.roles and --role-model give the model that writes the
// compaction summaries. It is not a swarm role (no agent has it), so it is handled apart from the others.
const CompactorRole = "compactor"

// buildCompactor builds the model named for the compactor (--role-model compactor=M, else models.roles.compactor),
// or nothing when none is named, in which case every agent compacts on its own model and its own cache.
func (s *Session) buildCompactor(ctx context.Context, o Options) (provider.Provider, cost.Model, error) {
	ref := o.RoleModels[CompactorRole]
	if ref == "" {
		ref = s.cfg.Models.Roles[CompactorRole]
	}
	if ref == "" {
		return nil, cost.Model{}, nil
	}
	mr, err := ResolveModel(s.cfg, ref)
	if err != nil {
		return nil, cost.Model{}, fmt.Errorf("compactor model %s: %w", ref, err)
	}
	p, m, err := BuildProvider(s.cfg, mr, ProviderOptions{CaptureTokens: o.CaptureTokens})
	if err != nil {
		return nil, cost.Model{}, fmt.Errorf("compactor model %s: %w", ref, err)
	}
	return p, s.describe(ctx, p, m), nil
}

// checkRoleModels refuses a role model override for a role the session does not have:
// it would never apply, and the person would believe a worker ran on the model they
// named. (models.roles in the configuration is not checked this way: a file may serve
// projects whose roles differ.)
func (s *Session) checkRoleModels(overrides map[string]string) error {
	known := map[string]bool{CompactorRole: true}
	for name := range s.ext.roles {
		known[name] = true
	}
	if s.mailmanOn() {
		known[swarm.MailmanRoleName] = true
	}
	var bad []string
	for role := range overrides {
		if !known[role] {
			bad = append(bad, strconv.Quote(role))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("--role-model: no role named %s in this session (its roles: %s)", strings.Join(bad, ", "), strings.Join(names, ", "))
}

// DefaultOutagePatience is how long an agent waits out an endpoint that is down or overloaded when nothing says otherwise: a
// swarm worker that gave up after forty seconds took its task with it, and the endpoints of a marketplace go down for minutes.
const DefaultOutagePatience = 5 * time.Minute

// outagePatience is Options.OutagePatience as the agents take it: zero stands for the default, negative for none.
func outagePatience(d time.Duration) time.Duration {
	switch {
	case d < 0:
		return 0
	case d == 0:
		return DefaultOutagePatience
	}
	return d
}

// orDefault uses a positive value or the supplied fallback.
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

// BeforeWrite permits the write; this guard records completed writes rather than authorizing them.
func (writeGuard) BeforeWrite(string, string) error { return nil }

// AfterWrite records the agent and path in the checkpoint store after a write completes.
func (g writeGuard) AfterWrite(agent, path string) { g.store.After(agent, path) }

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
		start["models"] = s.modelRecords()
		if info := s.mcpInfo(); info != nil {
			start["mcp"] = info
		}
		if rec := s.trust.record(); rec != nil {
			start["trust"] = rec
		}
		// What the run's swarm was set up to do to files and mail, when it is not the default.
		if s.iso != nil {
			start["isolation"] = config.IsolationWorktree
		}
		if s.mailmanOn() {
			start["mailman"] = true
		}
		if s.opts.Resume != "" {
			start["resumed"] = true
		}
		s.Log.Emit("", events.TypeSessionStart, start)
	}
	s.Ckpt.Begin(fmt.Sprintf("turn %d: %s", turn, oneLine(goal, 60)))

	if s.Swarm != nil {
		s.Swarm.Start(s.swarmContext(ctx))
		res, err := s.Swarm.RunManager(ctx, goal)
		return s.result(res, err)
	}
	res, err := s.Agent.Run(ctx, goal)
	return s.result(res, err)
}

// swarmContext is the context the swarm runs on. A batch run's swarm lives on the
// context of its one Run. An interactive session's must outlive a turn: the chat
// loop cancels each turn's context when the turn ends, which would stop every
// worker the manager left running and end the swarm before a person could talk to
// the manager about it (or before the swarm could wake the manager). Ctrl-C still
// works: it cancels the turn's context, which stops the manager's run and, through
// RunManager, the workers.
func (s *Session) swarmContext(turn context.Context) context.Context {
	if !s.opts.Interactive {
		return turn
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.swarmCtx == nil {
		s.swarmCtx, s.swarmStop = context.WithCancel(context.WithoutCancel(turn))
	}
	return s.swarmCtx
}

// result persists permission decisions, attempts to flush the event log, and assembles the session
// result with swarm totals when available, preserving the supplied error.
func (s *Session) result(res *agent.Result, err error) (*Result, error) {
	// Flush final turn events before returning so log readers can observe the
	// completed turn without waiting for the background flush interval.
	s.keepPermissions()
	if s.Log != nil {
		_ = s.Log.Flush()
	}
	out := &Result{SessionID: s.ID, Dir: s.Dir}
	if res != nil {
		out.Text, out.Steps, out.Usage, out.CostUSD, out.Stop, out.Compactions = res.Text, res.Steps, res.Usage, res.CostUSD, res.Stop, res.Compactions
	}
	if s.Swarm != nil {
		out.CostUSD = s.Swarm.TotalCost()
		out.Unfinished = s.Swarm.Unfinished()
	}
	return out, err
}

// reconTokens returns the reconnaissance token estimate or zero for absent reconnaissance.
func reconTokens(r *Recon) int {
	if r == nil {
		return 0
	}
	return r.Tokens
}

// oneLine collapses whitespace and clips by bytes before appending an ellipsis; it may split
// UTF-8.
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

// Resumed reports whether this session continues an earlier one.
func (s *Session) Resumed() bool { return s.opts.Resume != "" }

// Main is the agent a person talks to: the single agent, or a team's manager (nil until it has started, which a resumed team's does at once).
func (s *Session) Main() *agent.Agent {
	if s.Swarm != nil {
		return s.Swarm.Manager()
	}
	return s.Agent
}

// Send steers a running agent (or the manager) with a message at its next turn
// boundary.
func (s *Session) Send(text string) {
	switch {
	case s.Swarm != nil:
		if m := s.Swarm.Manager(); m != nil {
			s.Swarm.HumanInput()
			m.Send(text)
		}
	case s.Agent != nil:
		s.Agent.Send(text)
	}
}

// Budget is the most the session may spend, in US dollars (0: no limit): for a swarm
// the built-in or configured cap or --budget-usd, for a single agent --budget-usd.
func (s *Session) Budget() float64 { return s.budget }

// End reasons the commands give a session (SetEndReason); the default is "other".
const (
	EndCompleted   = "completed"   // a run finished
	EndExit        = "exit"        // the person left a chat
	EndInterrupted = "interrupted" // Ctrl-C or SIGTERM ended it
	EndBudget      = "budget"      // the budget was spent
	EndError       = "error"       // the run failed
)

// SetEndReason says why the session is ending, for the SessionEnd hooks (their
// matcher and their "reason" field) and the session.end event. Call it before Close;
// the last call wins, and without one the reason is "other".
func (s *Session) SetEndReason(reason string) {
	s.mu.Lock()
	s.endReason = reason
	s.mu.Unlock()
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
	reason := s.endReason
	if reason == "" {
		reason = "other"
	}
	s.mu.Unlock()
	if s.Swarm != nil {
		if s.iso != nil {
			// An isolated run ends by putting its verified result into the checkout and
			// removing its trees (Finish stops the swarm first). A caller that already
			// finished, to print the report, gets the same report back and nothing repeats.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			s.Finish(ctx)
			cancel()
		} else {
			s.Swarm.Shutdown()
		}
	}
	s.mu.Lock()
	stop := s.swarmStop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if s.Agent != nil {
		_ = s.Agent.Close() // waits for a compaction still running, and frees the agent's archive index
	}
	if s.shell != nil {
		s.shell.Shutdown()
	}
	s.closeMCP()
	if started && s.hookAdapter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.SessionEnd, Agent: s.mainAgent(), Extra: map[string]any{"reason": reason}})
		cancel()
	}
	if started {
		s.Log.Emit("", events.TypeSessionEnd, map[string]any{"cost_usd": s.cost(), "reason": reason})
	}
	err := s.Log.Close()
	if s.unlock != nil {
		s.unlock()
	}
	return err
}

// cost returns agent or swarm spending plus the separately tracked goal-judge cost.
func (s *Session) cost() float64 {
	s.mu.Lock()
	judge := s.judgeUSD // what a standing goal's judge has cost (goal.go): it is the session's spend, and not any agent's
	s.mu.Unlock()
	if s.Swarm != nil {
		return s.Swarm.TotalCost() + judge
	}
	if s.Agent != nil {
		_, c := s.Agent.Usage()
		return c + judge
	}
	return judge
}

// plannerFor is the compaction planner that the cache settings ask for. A soft limit that is set above the default hard one would never
// be reached, the hard limit forcing the compaction first, so the hard limit follows it up unless it was set too.
func plannerFor(c config.Cache) kv.Planner {
	p := kv.DefaultPlanner()
	if c.ThreadSoftLimitTokens > 0 {
		p.SoftThreadTokens = c.ThreadSoftLimitTokens
	}
	if c.CompactThresholdTokens > 0 {
		p.HardThreadTokens = c.CompactThresholdTokens
	} else if p.SoftThreadTokens > p.HardThreadTokens {
		p.HardThreadTokens = p.SoftThreadTokens
	}
	return p
}

// testCommand is the project's test command as the survey found it ("" when it found none): what an answer that follows an edit and no test run is
// sent back to run.
func (s *Session) testCommand() string {
	if s.Recon == nil {
		return ""
	}
	for _, c := range s.Recon.Commands {
		if strings.Contains(c, "test") {
			return c
		}
	}
	return ""
}
