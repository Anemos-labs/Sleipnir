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
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/anemos-labs/sleipnir/internal/delivery"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/hooks"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/memory"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/research"
	"github.com/anemos-labs/sleipnir/internal/sandbox"
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
	webui "github.com/anemos-labs/sleipnir/internal/web"
)

// Version is stamped into session.start; the CLI overrides it at build time.
var Version = "dev"

// Options describes one session. Zero values mean "sensible default".
type Options struct {
	// AutoFix and AutoMerge authorize the GitHub lifecycle of the next objective only.
	AutoFix, AutoMerge bool
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
	// TrainingLogs keeps the full record a trajectory export needs: every request's prompt manifest blobs, every response's
	// completion blob, the stable layer texts and every snapshot an agent writes. Off, the session keeps what a resume reads:
	// the event log, tool outputs and each agent's newest snapshot (a run of 32 agents wrote 13 GB of superseded snapshots).
	TrainingLogs bool

	// ID names the session; Dir is where events.jsonl and blobs/ live (default
	// <state>/sessions/<id>). With Resume, ID is the resumed directory's name
	// unless set (a rollout's log names its session otherwise).
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
	// Unattended says that nobody is there to answer a question (sleipnir run with stdin not a terminal). When the session would
	// run in the default mode and neither Mode nor an Allow rule chose the policy, it runs in bypass mode instead: a question it
	// could not ask would only be refused, and a run that may not edit what it was asked to change does nothing useful. The
	// configuration's permissions.mode counts only when it is another mode (sleipnir init writes "default" for every user). Built-in
	// protections, deny rules and guarded paths hold in bypass as always.
	Unattended bool
	// AskTimeout is how long a question to the person waits for its answer before it is refused (perm.Config.AskTimeout); zero waits.
	AskTimeout time.Duration
	// ManagerAnswersAfter, when positive, overrides permissions.manager_answers_after: in a team, how long a worker's question waits
	// for the person before the manager answers it for them (perm.Config.Fallback). ManagerAnswersUpTo, when set, overrides
	// permissions.manager_answers_up_to: the most dangerous kind of question it may answer.
	ManagerAnswersAfter time.Duration
	ManagerAnswersUpTo  perm.Danger
	// Allow are rules that need no question in this run, on top of the configuration's (sleipnir run --allow): what the person
	// who started the run pre-approved on the command line, which is the one place a run with nobody to ask can get an answer.
	Allow []string
	// OutagePatience is how long an agent keeps retrying a request that fails because the endpoint is down or overloaded (an HTTP
	// status of 500 or more, or 429), counted from the request's first failure, beyond the six attempts every failure gets. Zero
	// is DefaultOutagePatience; negative is the six attempts only.
	OutagePatience time.Duration
	// WaitUnreachable makes a failure in which nothing answered (a refused or reset connection, no response in time) an outage
	// too, for OutagePatience (agent.Config.WaitUnreachable): for an endpoint known to exist, such as a rollout's gateway.
	WaitUnreachable bool
	// WrapProvider, if set, is put in front of every provider the session builds from the configuration (the main model when
	// Provider is nil, the roles' models, the compactor's, a /model switch): a rollout watches them all for outages. The
	// Provider given here is the caller's and is used as given.
	WrapProvider func(provider.Provider) provider.Provider
	// Savepoints makes the session's agents keep recoverable snapshots at tool-batch boundaries and share a barrier that
	// Savepoint uses to pause writes for a moment: what a long rollout needs to be resumed mid-run.
	Savepoints bool

	// Swarm runs a manager plus workers instead of a single agent.
	Swarm bool
	// Workers is the most workers the manager may run (--swarm N), the manager not counted; 0 is
	// swarm.max_workers, or the swarm's own default when that is unset too.
	Workers int
	Roles   swarm.Roles
	// Owners assigns repository paths to roles (an RL task's team.owns): only agents
	// of an owning role may write such a path (swarm.Deps.Owners). Nil: no path is
	// owned. It applies to swarms.
	Owners     swarm.PathOwners
	Verify     string // command the harness runs before a worker's task may leave "doing"
	RoleModels map[string]string
	// DefinitionModel, when set, rewrites the model: of a loaded agent definition before it is resolved. A definition comes
	// with the project; an embedder that pins which endpoints a session may reach (an RL rollout, whose credentials belong
	// to the operator) maps it onto an allowed provider here. An empty result runs the role on the session's own provider
	// and model. Nil keeps the definition's model.
	DefinitionModel func(model string) string
	// Team, when set, is the team's composition: one slot per entry, a role named as
	// often as it has slots, each slot on its own model or ("") its role's
	// (--role-model given more than once for a role, or with a bare role). Without it
	// see DefaultTeam. Workers is then len(Team).
	Team []TeamSlot
	// DefaultTeam gives a team session without a Team the default team (one slot per
	// role, swarm.DefaultTeam), as the chat, run and web commands ask; without it the
	// roles are limited by Workers alone (tests, training rollouts, embedders).
	DefaultTeam bool
	// AutoDocs, when set, overrides swarm.auto_docs (default true): the team's docs
	// worker keeps the documentation current after every accepted task and agreement
	// without being asked (swarm.Config.AutoDocs). It applies to a team with a docs
	// slot.
	AutoDocs *bool
	// ManagerApproves, when set, overrides permissions.manager_approves (default true): in a team, a worker's routine permission
	// question goes to the manager before the person (swarm.Config.ManagerApproves).
	ManagerApproves *bool
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
	MaxSteps int
	// FixedSteps keeps MaxSteps, and every team role's step limit, a ceiling on one run (agent.Config.ExtendSteps off): a
	// rollout's step budget is part of its task, and a person who names a limit means it. Without it a run that is still
	// making edits at its limit goes on for another window of the same length.
	FixedSteps bool
	BudgetUSD  float64
	// NoBudget lifts the cap a swarm has from swarm.budget_usd (the built-in cap unless the configuration sets another) when BudgetUSD
	// sets none; BudgetUSD still wins. It is SLEIPNIR_SWARM_BUDGET_USD=0 for one session. A single agent has no such cap.
	NoBudget      bool
	ContextWindow int // overrides the model's window (RL: small windows force compaction)
	Params        core.Params
	// CompactAtPercent, when set, overrides cache.compact_at_percent for this session (--compact-at): the share of each model's
	// context window, in percent from 10 to 90, that an agent's conversation may fill before compaction is considered. 0 leaves
	// it to the configuration.
	CompactAtPercent int

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
	// Sandbox, when enabled, runs every shell command of the session (the agents',
	// the verification's) inside one container of its own (--sandbox): the project
	// and the team's trees are mounted at their own paths, the network is off
	// unless Sandbox.Network, and the harness, its keys and its files stay outside.
	// It is started as the session opens and removed when it closes.
	Sandbox sandbox.Spec
	// Meta is recorded on the session.start event (run provenance: task, sample,
	// seed, policy and sampling of an RL rollout).
	Meta map[string]any
	// NoWeb omits the web tools.
	NoWeb bool
	// NoMCP starts no MCP servers (RL rollouts, tests): the tool list is then the
	// built-in one whatever the configuration says.
	NoMCP bool
	// MCPServers are MCP server entries the command line names (run --mcp-config), by server name, in
	// the shape of the configuration's mcp section. They are the person's own word, trusted as the user
	// configuration is, and an entry replaces a configured server of the same name for this session.
	MCPServers map[string]json.RawMessage
	// Offline skips network lookups made for convenience (model catalogue).
	Offline bool
	// NoMemory starts the session without the global memory: the notes in ~/.sleipnir/MEMORY.md are not read, and the memory tool
	// (still in the tool list, which never depends on the session) neither reads nor writes them (--no-memory).
	NoMemory bool
}

// Session is a running assembly. Run may be called repeatedly; the agent (or
// manager) keeps its thread between calls.
type Session struct {
	// mcpRefused is set when a server the command line names (run --mcp-config) cannot start because the
	// configuration's server limit leaves it out; the session then fails to build rather than run without it.
	mcpRefused error
	// shownAtStart are the warnings of the session itself an earlier start of this resumed session logged (startNotice).
	shownAtStart    map[string]bool
	delivery        *delivery.Controller
	deliveryOptions delivery.Options
	opts            Options
	// newSolo builds the single agent again on the session's current Provider and Model (SwitchModel).
	newSolo func() (*agent.Agent, error)
	plans   *plan.Store // each agent's plan, which a standing goal reads as its requirements (goal.go)
	// judgeUSD is what a standing goal's judge has cost (goal.go), under mu.
	judgeUSD float64
	// judgeListUSD is the API list-price value of the judge's usage when its model is on a subscription plan, and judgePlan says the
	// judge used one, under mu.
	judgeListUSD float64
	judgePlan    bool
	// judgeP and judgeModel are the model a standing goal is judged on when the judge role names one (buildJudge), judgeRef its
	// provider/model; judgeP is nil when none is named and the session's own model judges.
	judgeP     provider.Provider
	judgeModel cost.Model
	judgeRef   string
	// fallbackP is the model every other model of the session falls back to while its endpoint is out (buildFallback),
	// fallbackRef its provider/model; nil when none is named.
	fallbackP     provider.Provider
	fallbackModel string
	fallbackRef   string
	// lessons is the lesson memory (buildLessons), nil when no embedder model is named or the session has no global memory.
	lessons *lessonMemory

	// team is the team the swarm was built with (Team).
	team []TeamSlot

	lastPerm         string // the permission state last written to the log (keepPermissions)
	restoredMode     string // what a resumed session got back (restorePermissions)
	unattendedBypass bool   // bypass was chosen because nobody can answer (Options.Unattended)
	restoredRules    int
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
	// unlisted are the providers' model lists (models.go) that could not be read: none is asked twice in a session.
	unlisted map[string]bool
	// served are the windows servers of this machine said they serve models with (served.go), by server and model, 0 for none: each
	// is asked once in a session.
	served map[string]int

	Registry *tools.Registry
	Specs    []core.ToolSpec
	Perm     *perm.Engine
	Ckpt     *checkpoint.Store
	Shared   *kv.Layer
	Recon    *Recon
	Memory   []memory.Source

	Agent *agent.Agent
	Swarm *swarm.Swarm
	// research is the project's research runs and researchTool the tool over them (nil when the research plugin is off);
	// researchPinned and researchText are the published kickoff run whose context is in the shared layer.
	research       *research.Store
	box            *sandbox.Box // the container commands run in (Options.Sandbox); nil when they run on the host
	webReader      *web.Reader
	researchFonts  iofs.FS
	researchPinned string
	researchText   string
	researchSite   *researchRender
	// scan is the project's deep scans (nil when the scan plugin is off).
	scan *scanState
	// barrier is shared by the agents and the swarm's done check when Options.Savepoints is set (Savepoint).
	barrier *agent.SavepointBarrier
	// savepoints counts the savepoints written (Savepoint), under mu.
	savepoints int

	// Skills is the catalogue of skills the session discovered; Roles the swarm's
	// roles, built-in plus project and user definitions.
	Skills *skills.Catalog
	Roles  swarm.Roles

	cfgRep      *config.Report // where each configuration value came from (nil when Options.Config was given)
	plugins     *plugin.Set    // the plugins on when the session started (plugins.go)
	kernel      *plugin.Kernel // activates the plugins that register tools (build)
	asked       askedShape     // the team the options asked for, before the plugins had their say
	trust       *trustInfo     // how the project's own files came to be used, or why not (nil when it has none)
	mcp         *mcpState
	archive     *kv.Archive    // folded turns, for recall
	past        pastArchives   // earlier sessions' turns, for recall session=…
	handles     *tools.Handles // recall handles of truncated output
	unlock      func()         // releases the lock on the session directory
	ckptLog     *checkpointLog // writes the "checkpoint" events (web_access.go)
	ext         *extensions
	hooks       *hooks.Runner
	hookAdapter *hookAdapter
	shell       *shell.Manager

	// refused are the commands that were refused for want of anyone to ask (permaudit.go), for the hint a run prints at its end.
	refusedMu sync.Mutex
	refused   []refusedCommand

	mu     sync.Mutex
	effort provider.EffortSetting
	// objective is the standing goal's objective as last saved or loaded (goal.go), under mu.
	objective string
	started   bool
	closed    bool
	// endReason is why the session ended (SetEndReason), for the SessionEnd hooks.
	endReason string
	// budget is the most the session may spend (0: no limit): the swarm's cap, or the
	// single agent's --budget-usd.
	budget float64
	turn   int
	// interrupted says that the last turn was cancelled by the person (an interrupt), under mu: the next turn's message is then
	// headed by InterruptNote, so that it is what the model answers instead of the work that was cut short.
	interrupted bool
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
	// ListUSD is the value at the API list price of the usage a subscription plan covered, and Plan says the session ran on one (see
	// cost.Model.Plan): a summary shows CostUSD+ListUSD marked as the plan's. Budgets count CostUSD only.
	ListUSD float64
	Plan    bool
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
		o.Dir = dir
		if o.ID == "" {
			o.ID = filepath.Base(dir)
		}
		if prior := LatestStart(dir); len(o.Team) == 0 && len(prior.Team) > 0 && o.Swarm {
			// a continued team keeps the composition it last started with (a /restart may have moved its slots), unless one is given now
			o.Team = append([]TeamSlot(nil), prior.Team...)
			o.Workers = max(o.Workers, len(o.Team))
		}
		if prior := LatestStart(dir); !o.Sandbox.Enabled() && prior.Sandbox != "" {
			// a session that ran its commands in a sandbox resumes in one: resuming it on the host would quietly drop the confinement
			// its commands were started under
			spec, err := sandbox.Parse(prior.Sandbox)
			if err != nil {
				return nil, fmt.Errorf("the session ran in the sandbox %q, which cannot be started again: %w", prior.Sandbox, err)
			}
			spec.Network = prior.SandboxNetwork
			o.Sandbox = spec
		}
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
	// Plugins: what is on in this session. A team asked for while the team plugin is off runs as one agent; what was asked is
	// kept for the restart flags (StartFlags), so turning the plugin back on brings the team back.
	plugs, plugWarns := resolvePlugins(cfg, rep, o)
	asked := askedShape{swarm: o.Swarm, workers: o.Workers, team: o.Team, defaultTeam: o.DefaultTeam}
	if o.Swarm && !plugs.On(plugin.Swarm) {
		o.Swarm, o.Workers, o.Team, o.DefaultTeam = false, 0, nil, false
		plugWarns = append(plugWarns, "plugins: the team plugin is off, so this session is a single agent")
	}
	if err := checkSwarmSize(cfg, o); err != nil {
		return nil, err
	}
	if b := o.BudgetUSD; math.IsNaN(b) || math.IsInf(b, 0) || b < 0 {
		// Anything but a positive number reads as "no budget" below; a typo must not do that.
		return nil, fmt.Errorf("budget: --budget-usd must be zero (no limit) or a positive amount, got %v", b)
	}
	if err := CheckCompactAt(o.CompactAtPercent); err != nil {
		return nil, err
	}
	if !plugs.On(plugin.Delivery) {
		o.AutoFix, o.AutoMerge = false, false
	}
	// The variables the configuration names as provider keys are credentials whatever they are called: they leave the
	// environment before any command could inherit them.
	for _, p := range cfg.Providers {
		harden.Register(p.APIKeyEnv)
	}
	s := &Session{opts: o, cfg: cfg, cfgRep: rep, trust: tinfo, plugins: plugs, asked: asked, deliveryOptions: delivery.Options{AutoFix: o.AutoFix, AutoMerge: o.AutoMerge}}
	if o.Savepoints {
		s.barrier = agent.NewSavepointBarrier()
	}

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
	if o.Resume != "" {
		s.shownAtStart = loggedStartNotices(s.Dir)
	}
	shown := s.shownAtStart
	if s.Log, err = events.Open(s.Dir, s.ID); err != nil {
		return nil, err
	}
	s.emitPlugins()
	for _, w := range plugWarns {
		s.startNotice(w)
	}
	s.effort.Set(o.Params.Effort)
	if o.Resume != "" {
		s.restoreEffort()
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
		s.Model.ContextTokens, s.Model.WindowGuessed = o.ContextWindow, false
	}
	if err := s.buildJudge(ctx); err != nil {
		s.Log.Close()
		return nil, err
	}
	if err := s.buildLessons(); err != nil {
		s.Log.Close()
		return nil, err
	}

	// Hooks, then skills and role definitions, then permissions (which need the
	// roles' profiles and put PermissionRequest hooks in front of the prompter).
	var hookWarns []string
	hookCfg, extWarns := s.externalHooks(cfg)
	if !s.pluginOn(plugin.Hooks) {
		hookCfg = &config.Config{}
	}
	if s.hooks, hookWarns = newHookRunner(hookCfg, s.ID, o.Root); s.hooks != nil {
		s.hookAdapter = &hookAdapter{s: s, run: s.hooks}
	}
	s.loadExtensions()
	s.ext.warnings = append(s.ext.warnings, hookWarns...)
	s.ext.warnings = append(s.ext.warnings, extWarns...)
	s.Skills, s.Roles = s.ext.skills, s.ext.roles
	fresh := slices.DeleteFunc(slices.Clone(s.ext.warnings), func(w string) bool { return shown[w] })
	for _, w := range fresh {
		s.Log.Emit("", "notice", map[string]any{"level": "warn", "msg": w})
	}
	if o.Sink != nil {
		for _, w := range shownWarnings(fresh) {
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
	s.logCheckpoints() // web_access.go: each checkpoint that begins or changes is a "checkpoint" log event

	// Detect the command shell for shared runtime guidance before building agents.
	if o.Sandbox.Enabled() {
		if err := s.startSandbox(ctx); err != nil {
			s.Log.Close()
			return nil, err
		}
		o = s.opts
		defer func() {
			if !built {
				s.stopSandbox() // a session that failed to open leaves no container behind
			}
		}()
	}
	shOpts := shell.Options{BaseEnv: o.ShellEnv, Wrap: o.ShellWrap, Tmp: s.tmp}
	if o.ShellEnv == nil {
		// Programs a plugin needs that were located or found outside PATH (plugin.PrepareNeeds).
		if bin := plugin.PrepareNeeds(stateRoot(o.Home), plugs); bin != "" {
			shOpts.Path = []string{bin}
		}
	}
	s.shell = shell.NewManager(shOpts)
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
	if o.Interactive && !o.Offline {
		go s.checkReach(s.Provider)
	}
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
	if err := s.buildFallback(); err != nil {
		return err
	}
	p, m, err := s.clientFor(ref)
	if err != nil {
		return err
	}
	s.Provider, s.Model, s.modelRef = p, s.describe(ctx, p, m), ref.String()
	return nil
}

// FallbackRole is the name under which models.roles and --role-model give the model that the session's other models fall back to
// while their endpoint is out. Like the compactor it is not a swarm role.
const FallbackRole = "fallback"

// FallbackAfter is how long a model's endpoint stays in an outage before its requests go to the fallback model, and
// FallbackRetry how long they stay there before the model's own endpoint is tried again.
var (
	FallbackAfter = 60 * time.Second
	FallbackRetry = 10 * time.Minute
)

// buildFallback builds the model named for the fallback (--role-model fallback=M, else models.roles.fallback), which clientFor
// puts behind every other model of the session (provider.Failover); nothing when none is named.
func (s *Session) buildFallback() error {
	ref := s.roleRef(FallbackRole)
	if ref == "" {
		return nil
	}
	mr, err := ResolveModel(s.cfg, ref)
	if err != nil {
		return fmt.Errorf("fallback model %s: %w", ref, err)
	}
	p, m, err := s.clientFor(mr)
	if err != nil {
		return fmt.Errorf("fallback model %s: %w", ref, err)
	}
	s.fallbackP, s.fallbackModel, s.fallbackRef = p, m.ID, mr.String()
	return nil
}

// clientFor is BuildProvider for this session: the client of ref, with the session's token capture, behind Options.WrapProvider
// when one is set.
func (s *Session) clientFor(ref ModelRef) (provider.Provider, cost.Model, error) {
	p, m, err := BuildProvider(s.cfg, ref, ProviderOptions{CaptureTokens: s.opts.CaptureTokens})
	if err == nil && s.opts.WrapProvider != nil {
		p = s.opts.WrapProvider(p)
	}
	if err == nil && s.fallbackP != nil && ref.String() != s.fallbackRef {
		name, to := ref.String(), s.fallbackRef
		p = &provider.Failover{Primary: p, Secondary: s.fallbackP, SecondaryModel: s.fallbackModel, After: FallbackAfter, Retry: FallbackRetry,
			OnSwitch: func(toFallback bool, cause error) {
				if toFallback {
					s.notice("", fmt.Sprintf("%s has been out for %s (%v): its requests go to the fallback model %s, and %s is tried again in %s", name, FallbackAfter, cause, to, name, FallbackRetry))
					return
				}
				s.noticeInfo("", name+" answers again: its requests are back on it")
			}}
	}
	return p, m, err
}

// UnattendedBypass says whether the session runs in bypass mode because nobody can answer a question and nothing else chose a
// mode (Options.Unattended).
func (s *Session) UnattendedBypass() bool { return s.unattendedBypass }

func (s *Session) buildPerm() error {
	o := s.opts
	mode := o.Mode
	if mode == "" {
		mode = perm.Mode(s.cfg.Permissions.Mode)
		if o.Unattended && len(o.Allow) == 0 && (mode == perm.ModeDefault || mode == "") {
			mode, s.unattendedBypass = perm.ModeBypass, true
		}
	}
	roles := map[string]perm.RoleProfile{}
	for name, rp := range s.cfg.Permissions.Roles {
		roles[name] = perm.RoleProfile{Mode: perm.Mode(rp.Mode), Allow: rp.Allow, Ask: rp.Ask, Deny: rp.Deny}
	}
	// A role definition can only tighten what its role may do: with a configuration entry of the same name as well, each is
	// judged on its own and the stricter answer stands (perm.RoleProfile.Also), so neither one's denials or mode are lost.
	also := func(name string, p perm.RoleProfile) {
		if have, ok := roles[name]; ok {
			have.Also = append(have.Also, p)
			roles[name] = have
		} else {
			roles[name] = p
		}
	}
	for name, p := range s.ext.profiles {
		also(name, p)
	}
	// Read-only roles are read-only in the engine, not merely by a command
	// allowlist in the swarm: the engine understands shell syntax. A role that has
	// a profile already keeps it and is held to the read-only one besides (its plan
	// mode with the research and check exceptions): a profile of its own can narrow a
	// read-only role, never make it write.
	for name, role := range s.ext.roles {
		if role.ReadOnly {
			if _, ok := roles[name]; !ok {
				roles[name] = perm.RoleProfile{Mode: perm.ModePlan, Allow: readOnlyRoleAllow, Deny: readOnlyRoleDeny}
			} else {
				also(name, perm.RoleProfile{Mode: perm.ModePlan, Allow: readOnlyRoleAllow, Deny: readOnlyRoleDeny})
			}
		}
	}
	// The manager uses the session policy, just like a solo agent.
	var extra []string
	var ignored func(string) bool
	if s.iso != nil {
		ignored = s.iso.ignoredChecker()
		// The session's worktrees are part of the workspace, and the project's relative
		// rules hold inside each of them: writers work there, each confined to its own
		// (swarm.bindTree).
		extra = []string{s.iso.dir}
	}
	if s.mailmanOn() {
		// The mailman is read-only and its one tool is mail (the swarm holds it to that);
		// the engine holds it to plan mode as well.
		if _, ok := roles[swarm.MailmanRoleName]; !ok {
			roles[swarm.MailmanRoleName] = perm.RoleProfile{Mode: perm.ModePlan}
		}
	}
	ask := append(append([]string(nil), protectedConfigDirs...), s.cfg.Permissions.Ask...)
	after, upTo, err := s.managerAnswers()
	if err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	e, err := perm.NewEngine(perm.Config{
		Mode: mode, Root: o.Root, Home: o.Home, StateDir: stateRoot(o.Home), RecordDirs: s.recordDirs(), Tmp: s.tmp, TreeParents: extra, Ignored: ignored,
		Allow: append(append([]string(nil), s.cfg.Permissions.Allow...), o.Allow...), Ask: ask, Deny: s.cfg.Permissions.Deny,
		Roles: roles, Prompter: s.trackAsks(s.hookPrompter(o.Prompter)), AskTimeout: o.AskTimeout, Audit: s.auditPermission,
		Delegate: s.delegateApproval, Fallback: s.fallbackApproval, FallbackAfter: after, FallbackUpTo: upTo,
	})
	if err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	s.Perm = e
	return nil
}

// recordDirs is where the session keeps its record, when that is not under the state root the engine guards already: a resume
// takes the approvals recorded there for the person's, so the agents may not write it unasked.
func (s *Session) recordDirs() []string {
	if s.Dir == "" {
		return nil
	}
	if rel, err := filepath.Rel(stateRoot(s.opts.Home), s.Dir); err == nil && filepath.IsLocal(rel) {
		return nil
	}
	return []string{s.Dir}
}

// readOnlyRoleAllow lets read-only roles (reviewers, scouts, architects) verify
// what they review and research what they plan (perm.ReadOnlyAllow: the project's
// tests and checks, the web). The session's own mode and rules still decide (the
// profile never grants more than the session does).
var readOnlyRoleAllow = perm.ReadOnlyAllow

// readOnlyRoleDeny are the tools a read-only role is denied by name. Background jobs
// are session-wide, and the engine alone would answer "touches no files and no
// network: allow" to a reviewer's bash_output on the output of another agent's job.
// What a read-only role runs it runs in the foreground and reads from the result.
var readOnlyRoleDeny = []string{"bash_output", "bash_kill"}

// buildShared assembles runtime guidance and the budgeted project survey and
// instruction files. Runtime guidance is frozen for the session.
func (s *Session) buildShared(ctx context.Context) error {
	segs := []kv.Segment{{Key: "runtime", Text: s.shell.RuntimeContext(), Vol: kv.VolFrozen}}
	est := core.NewBytesEstimator()
	if !s.opts.NoRecon && s.pluginOn(plugin.Recon) {
		// The machine's half of the survey: which build and test commands exist, so an agent knows before it writes a test whether it
		// can run it. Frozen like the runtime: an install during the session does not change the cached prefix.
		segs = append(segs, kv.Segment{Key: "toolchains", Text: s.shell.ToolchainContext(), Vol: kv.VolFrozen})
		rc, err := BuildRecon(ctx, ReconOptions{Root: s.opts.Root, BudgetTokens: s.opts.ReconBudget, Est: est})
		if err == nil {
			s.Recon = rc
			segs = append(segs, rc.Segments...)
		}
	}
	var srcs []memory.Source
	var err error
	if s.pluginOn(plugin.Instructions) {
		// The notes of ~/.sleipnir/MEMORY.md are the memory plugin's: --no-memory or the plugin off leaves them out.
		srcs, err = memory.Load(memory.Opts{Root: s.opts.Root, Cwd: s.opts.Cwd, Home: s.opts.Home, NoNotes: s.opts.NoMemory || !s.pluginOn(plugin.MemoryTool)})
	}
	if err != nil {
		// Unreadable instruction files are reported, never fatal.
		s.startNotice("instruction files: " + err.Error())
	}
	if !s.opts.TrustProject {
		kept := userScopeOnly(srcs)
		if skipped := skippedSources(srcs, kept); len(skipped) > 0 {
			s.startNotice(fmt.Sprintf("instruction files of this project were not loaded because the project is not trusted (%s); %s", strings.Join(skipped, ", "), trustHint("load")))
		}
		srcs = kept
	}
	s.Memory = srcs
	if txt := memory.Render(srcs); strings.TrimSpace(txt) != "" {
		budget := instructionTokenBudget(s.cfg.Cache.InstructionMaxTokens, s.Model.ContextTokens)
		fitted, cut := fitInstructions(srcs, budget, est)
		if len(cut) > 0 {
			s.startNotice(fmt.Sprintf("instruction files need about %d tokens; the limit is %d. Truncated or omitted: %s. Later, more specific files take precedence. Increase cache.instruction_max_tokens (SLEIPNIR_CACHE_INSTRUCTION_MAX_TOKENS) and restart, or shorten the files; /recon shows the loaded text", est.Tokens(txt), budget, strings.Join(cut, ", ")))
		}
		segs = append(segs, kv.Segment{Key: "instructions", Text: fitted, Vol: kv.VolEpoch})
	}
	if txt := s.ext.skillsSegment(); txt != "" {
		segs = append(segs, kv.Segment{Key: "skills", Text: txt, Vol: kv.VolEpoch})
	}
	if s.pluginOn(plugin.Research) {
		s.research = research.NewStore(s.opts.Root)
		s.researchFonts = webui.Fonts()
		if seg, ok := s.researchShared(); ok {
			s.researchText = seg.Text
			segs = append(segs, seg)
		}
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
	archive := kv.NewArchive(s.Blobs)
	s.archive = archive
	plans := plan.NewStore() // each agent's plan: the plan tool sets it, the hot tail shows it back (internal/plan)
	s.plans = plans
	// Each tool plugin registers its tools through the plugin kernel, in the set's order. A plugin that is off adds
	// nothing, for every agent alike: the tool list never depends on the project or the role (docs/CACHE-DESIGN.md).
	s.kernel = plugin.NewKernel(map[string]plugin.Activator{
		plugin.FS:    func(*plugin.Scope) error { fs.Register(reg); return nil },
		plugin.Shell: func(*plugin.Scope) error { shell.Register(reg, s.shell); return nil },
		plugin.Web: func(*plugin.Scope) error {
			if o.NoWeb {
				return nil
			}
			// ConfigFromEnv routes through HTTPS_PROXY when one is set (sandboxed and
			// corporate networks resolve names at the proxy) and enables web_search when
			// a search backend key is configured in the environment.
			wc := web.ConfigFromEnv()
			wc.AllowPrivate = s.cfg.Tools.WebAllowPrivate
			wc.AllowHosts = s.cfg.Tools.WebAllowHosts
			s.webReader = web.RegisterReader(reg, wc)
			return nil
		},
		plugin.Research: func(*plugin.Scope) error {
			if s.research == nil {
				s.research = research.NewStore(o.Root)
			}
			var rd research.Reader
			if s.webReader != nil {
				rd = s.webReader
			}
			t := research.NewTool(s.research, rd)
			t.Fonts, t.OnBuild = s.researchFonts, s.researchBuilt
			reg.Register(t)
			return nil
		},
		plugin.Scan: func(*plugin.Scope) error {
			reg.Register(s.newScanTool())
			return nil
		},
		plugin.Recall: func(*plugin.Scope) error {
			rt := recall.New(archive)
			rt.Past = s.pastArchive
			reg.Register(rt)
			return nil
		},
		// Registered whenever its plugin is on, whatever the project holds: the tool list must not depend on the project.
		plugin.SkillTool: func(*plugin.Scope) error { reg.Register(skilltool.New(s.Skills)); return nil },
		plugin.MemoryTool: func(*plugin.Scope) error {
			if o.NoMemory { // --no-memory keeps the tool in the list (the cache key) and has it refuse
				reg.Register(memtool.Off())
				return nil
			}
			proj := "" // an untrusted project's notes file is the repository's text, not the agent's memory
			if o.TrustProject {
				proj = filepath.Join(o.Root, filepath.FromSlash(memory.ProjectNotes))
			}
			reg.Register(memtool.NewScoped(filepath.Join(o.Home, ".sleipnir", "MEMORY.md"), proj))
			return nil
		},
		plugin.PlanTool: func(*plugin.Scope) error { reg.Register(plantool.New(plans)); return nil },
		// MCP servers add their tools here, before the list is frozen: every agent of
		// the session then sends the same tools array, byte for byte.
		plugin.MCP: func(*plugin.Scope) error { s.startMCP(ctx, reg); return nil },
	})
	s.kernel.OnState = func(id, state, reason string) {
		if state == plugin.StateError {
			s.Log.Emit("", "plugin.state", map[string]any{"id": id, "state": state, "reason": reason})
			s.notice("", "plugin "+id+" could not start: "+reason)
		}
	}
	if err := s.kernel.Start(s.plugins); err != nil {
		return err
	}
	if s.mcpRefused != nil {
		return s.mcpRefused
	}

	constText := agent.Constitution(agent.ConstitutionOpts{Swarm: o.Swarm})
	constLayer := kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: constText, Vol: kv.VolFrozen}})

	planner := plannerFor(cacheSettings(s.cfg.Cache, o))
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
		if o.Sink != nil {
			for role := range o.RoleModels {
				if !ServiceRole(role) {
					o.Sink.Notice("", "warn", "--role-model has no effect without --swarm for "+role+": this session has a single agent (use --model); only the compactor, judge, embedder and reranker roles apply to it")
					break
				}
			}
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
			var health *agent.CompactorHealth
			if comp != nil {
				health = agent.NewCompactorHealth()
			}
			p := params
			if o.Params.MaxTokens == 0 {
				p.MaxTokens = min(s.Model.MaxOutput, 16000)
			}
			return agent.New(agent.Config{
				ID: "main", Role: role.Name, Model: s.Model, Provider: s.Provider, Compactor: comp, CompactorModel: compModel, CompactorHealth: health,
				Tools: reg, ToolSpecs: specs,
				Const: constLayer, Shared: shared, RoleL: role.Layer(),
				Params: p, Effort: &s.effort, Events: s.Log, Blobs: s.Blobs, Archive: archive, Files: files,
				Hot: func(id string) []core.Block {
					if f := plan.Frame(plans.Get(id)); f != "" {
						return []core.Block{core.Text(f)}
					}
					return nil
				},
				PlanOpen: plans.Open, VerifyHint: s.testCommand(),
				Guard: writeGuard{store: s.Ckpt}, Snap: s.Ckpt, Handles: handles, Perm: s.Perm,
				Sink: o.Sink, Workdir: o.Cwd, Root: o.Root, Tmp: s.tmp, Limits: limits,
				Planner: planner, KVPolicy: kvPol, SessionID: s.ID, Est: est, Now: o.Now,
				BlockingCompaction: s.cfg.Cache.CompactionMode == "blocking",
				MaxSteps:           orDefault(o.MaxSteps, 200), ExtendSteps: !o.FixedSteps, BudgetUSD: o.BudgetUSD, CaptureTokens: o.CaptureTokens, TrainingLogs: o.TrainingLogs,
				Hooks: s.agentHooks(), OutagePatience: outagePatience(o.OutagePatience), WaitUnreachable: o.WaitUnreachable,
				Barrier: s.agentBarrier(),
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
	sc.PersistentPools = o.DefaultTeam || len(o.Team) > 0
	sc.SessionID = s.ID
	// swarm.max_workers is the ceiling (checkSwarmSize refused a larger request); a
	// size asked for on the command line may only be smaller.
	if v := s.cfg.Swarm.MaxWorkers; v > 0 {
		sc.MaxWorkers = v
	}
	if o.Workers > 0 {
		sc.MaxWorkers = o.Workers
	}
	// Allow every worker slot to hold a writer, using at least the library default
	// of four. Task scopes and write leases enforce file-level coordination.
	sc.MaxWriters = max(sc.MaxWriters, sc.MaxWorkers)
	if v := s.cfg.Swarm.RequestsPerMinute; v > 0 {
		sc.RPM = v
	}
	if v := s.cfg.Swarm.MaxConcurrentRequests; v > 0 {
		sc.MaxConcurrent = v
	}
	if v := s.cfg.Cache.AffinityShards; v > 0 {
		sc.AffinityShards = v
	}
	if v := s.cfg.Swarm.BudgetUSD; v > 0 && !o.NoBudget {
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
	sc.Verify = s.verifier()
	sc.FixedSteps = o.FixedSteps
	sc.HoldManager, sc.WakeManager = sc.PersistentPools || !o.Interactive, o.Interactive
	sc.Mailman = s.mailmanOn()

	comp, compModel, err := s.buildCompactor(ctx, o)
	if err != nil {
		return err
	}
	var health *agent.CompactorHealth
	if comp != nil {
		health = agent.NewCompactorHealth()
	}
	deps := swarm.Deps{
		Provider: s.Provider, Model: s.Model, Compactor: comp, CompactorModel: compModel, CompactorHealth: health, Registry: reg, Plans: plans,
		Const: constLayer, Shared: shared,
		Events: s.Log, Blobs: s.Blobs, Archive: archive, Files: files, Perm: s.Perm,
		Snap: s.Ckpt, Handles: handles,
		Workdir: o.Cwd, Root: o.Root, Tmp: s.tmp, Params: params, Effort: &s.effort, Planner: planner, KVPolicy: kvPol, Est: est, Limits: limits, Now: o.Now,
		NewSink: o.NewSink, CaptureTokens: o.CaptureTokens, TrainingLogs: o.TrainingLogs, OnWrite: s.Ckpt.After, RunEnded: s.shell.EndAgent, Hooks: s.agentHooks(),
		OutagePatience:     outagePatience(o.OutagePatience),
		WaitUnreachable:    o.WaitUnreachable,
		BlockingCompaction: s.cfg.Cache.CompactionMode == "blocking",
		Owners:             o.Owners,
		Barrier:            s.agentBarrier(),
	}
	if len(o.RoleModels) > 0 {
		if err := s.checkRoleModels(o.RoleModels); err != nil {
			return err
		}
		deps.RoleModels = map[string]swarm.RoleModel{}
		for role, ref := range o.RoleModels {
			if ServiceRole(role) {
				continue
			}
			mr, err := ResolveModel(s.cfg, ref)
			if err != nil {
				return fmt.Errorf("role model %s=%s: %w", role, ref, err)
			}
			p, m, err := s.clientFor(mr)
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
			ref := d.Model
			if o.DefinitionModel != nil {
				if ref = o.DefinitionModel(ref); ref == "" {
					deps.RoleModels[d.Name] = swarm.RoleModel{Provider: s.Provider, Model: s.Model}
					continue
				}
			}
			mr, err := ResolveModel(s.cfg, ref)
			if err != nil {
				return fmt.Errorf("role %s: model %s: %w", d.Name, d.Model, err)
			}
			p, m, err := s.clientFor(mr)
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
			if _, set := deps.RoleModels[role]; set || ref == "" || ServiceRole(role) {
				continue
			}
			mr, err := ResolveModel(s.cfg, ref)
			if err != nil {
				return fmt.Errorf("models.roles.%s = %s: %w", role, ref, err)
			}
			p, m, err := s.clientFor(mr)
			if err != nil {
				return fmt.Errorf("models.roles.%s = %s: %w", role, ref, err)
			}
			deps.RoleModels[role] = swarm.RoleModel{Provider: p, Model: s.describe(ctx, p, m)}
		}
	}
	team, err := s.teamFor(s.ext.roles, sc.MaxWorkers)
	if err != nil {
		return err
	}
	if len(o.Team) > 0 {
		sc.MaxWorkers = max(sc.MaxWorkers, len(team))
		sc.MaxWriters = max(sc.MaxWriters, sc.MaxWorkers)
	}
	sc.Team = teamNames(team)
	s.team = team
	autoDocs := s.autoDocsOn() && slices.Contains(sc.Team, swarm.DocsRole)
	sc.AutoDocs = autoDocs
	sc.PoolMax = s.cfg.Swarm.PoolMax
	sc.JudgeManagerShell = true
	sc.ManagerApproves = s.managerApprovesOn()
	for i, t := range team {
		if t.Model == "" {
			continue
		}
		mr, err := ResolveModel(s.cfg, t.Model)
		if err != nil {
			return fmt.Errorf("team slot %s=%s: %w", t.Role, t.Model, err)
		}
		p, m, err := s.clientFor(mr)
		if err != nil {
			return fmt.Errorf("team slot %s=%s: %w", t.Role, t.Model, err)
		}
		if deps.SlotModels == nil {
			deps.SlotModels = make([]swarm.RoleModel, len(team))
		}
		deps.SlotModels[i] = swarm.RoleModel{Provider: p, Model: s.describe(ctx, p, m)}
	}
	iso, err := s.buildIsolation(ctx)
	if err != nil {
		return err
	}
	deps.Isolation = iso
	roles := s.teamRoles(s.ext.roles, team, autoDocs)
	if st := s.buildLore(team); st != nil {
		roles = s.loreRoles(st, roles)
		sc.OnLearned = s.saveLore(st)
	}
	sw := swarm.New(sc, deps, roles)
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

// TeamSlot is one slot of a team (Options.Team): its role, and the model its worker
// runs on ("" is the role's model).
type TeamSlot struct {
	Role  string `json:"role"`
	Model string `json:"model,omitempty"`
}

// checkSwarmSize refuses a swarm larger than swarm.max_workers allows (DefaultMaxTeamWorkers
// when it is not set), and a negative one. The setting is a ceiling that the user's file or
// the environment puts on every session; a request for more (--swarm N asks for a manager
// and N workers) would otherwise override it without a word.
func checkSwarmSize(cfg *config.Config, o Options) error {
	if !o.Swarm {
		return nil
	}
	if o.Workers < 0 {
		return fmt.Errorf("swarm: %d workers requested; a team has %d workers or more (0 is a single agent)", o.Workers, MinTeamWorkers)
	}
	ceil := TeamCeiling(cfg)
	if o.Workers <= ceil {
		return nil
	}
	if cfg.Swarm.MaxWorkers <= 0 {
		return fmt.Errorf("swarm: %d workers requested (and the manager) but a session has at most %d workers unless swarm.max_workers allows more; ask for fewer workers or raise swarm.max_workers", o.Workers, ceil)
	}
	return fmt.Errorf("swarm: %d workers requested (and the manager) but swarm.max_workers caps a session at %d workers; raise swarm.max_workers or ask for fewer workers", o.Workers, ceil)
}

// TeamCeiling is the most workers a session may have: swarm.max_workers when it is set, else DefaultMaxTeamWorkers.
func TeamCeiling(cfg *config.Config) int {
	if cfg != nil && cfg.Swarm.MaxWorkers > 0 {
		return cfg.Swarm.MaxWorkers
	}
	return DefaultMaxTeamWorkers
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
	p, m, err := s.clientFor(mr)
	if err != nil {
		return nil, cost.Model{}, fmt.Errorf("compactor model %s: %w", ref, err)
	}
	return p, s.describe(ctx, p, m), nil
}

// JudgeRole is the name under which models.roles and --role-model give the model a standing goal is judged on. Like the
// compactor it is not a swarm role: no agent has it. Without one the session's own model judges.
const JudgeRole = "judge"

// ServiceRole reports whether role names a model that serves the session rather than an agent of a team (the compactor, the judge
// and the lesson memory's embedder and reranker): one a single-agent session can set too, and one no team role takes.
func ServiceRole(role string) bool {
	switch role {
	case CompactorRole, JudgeRole, EmbedderRole, RerankerRole, FallbackRole:
		return true
	}
	return false
}

// buildJudge builds the model named for the judge (--role-model judge=M, else models.roles.judge), or nothing when none is named.
func (s *Session) buildJudge(ctx context.Context) error {
	ref := s.opts.RoleModels[JudgeRole]
	if ref == "" {
		ref = s.cfg.Models.Roles[JudgeRole]
	}
	if ref == "" {
		return nil
	}
	mr, err := ResolveModel(s.cfg, ref)
	if err != nil {
		return fmt.Errorf("judge model %s: %w", ref, err)
	}
	p, m, err := s.clientFor(mr)
	if err != nil {
		return fmt.Errorf("judge model %s: %w", ref, err)
	}
	s.judgeP, s.judgeModel, s.judgeRef = p, s.describe(ctx, p, m), mr.String()
	return nil
}

// checkRoleModels refuses a role model override for a role the session does not have:
// it would never apply, and the person would believe a worker ran on the model they
// named. (models.roles in the configuration is not checked this way: a file may serve
// projects whose roles differ.)
func (s *Session) checkRoleModels(overrides map[string]string) error {
	known := map[string]bool{CompactorRole: true, JudgeRole: true, EmbedderRole: true, RerankerRole: true, FallbackRole: true}
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

const soloPin = `You are working alone on the user's task, with no team to delegate to. Understand it, list its requirements, make the change with the smallest correct diff, and check your work by running the project's own build and tests (and the program itself when your change alters what its users see) before you report. When a test tool is missing, set it up inside the project (for Python, a virtual environment in .venv) rather than skipping the run. Say plainly what you did, what you verified and what remains.`

// writeGuard routes post-write notifications to the checkpoint store so a rewind
// can tell an agent's last write from a human's later edit.
type writeGuard struct{ store *checkpoint.Store }

// BeforeWrite permits the write; this guard records completed writes rather than authorizing them.
func (writeGuard) BeforeWrite(string, string) error { return nil }

// AfterWrite records the agent and path in the checkpoint store after a write completes.
func (g writeGuard) AfterWrite(agent, path string) { g.store.After(agent, path) }

// InterruptNote heads the first message after a turn the person interrupted: the conversation still ends with the work that was cut
// short, and a model reading it tends to carry that on instead of answering what the person says now.
const InterruptNote = "[The person interrupted your previous turn. Answer the message below first; go back to the interrupted work only if it asks you to.]\n\n"

// runTurn gives the session one model turn without starting a GitHub controller.
func (s *Session) runTurn(ctx context.Context, goal string) (*Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("session is closed")
	}
	first := !s.started
	s.started = true
	s.turn++
	turn := s.turn
	after := s.interrupted
	box := s.box // Close clears it under s.mu
	s.interrupted = false
	s.mu.Unlock()
	defer s.flushCheckpoints()
	defer func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.mu.Lock()
			s.interrupted = true
			s.mu.Unlock()
		}
	}()

	goal, err := s.promptHook(ctx, first, goal)
	if err != nil {
		return nil, err
	}
	if after {
		goal = InterruptNote + goal
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
		if box != nil {
			start["sandbox"] = box.Spec.String() // a resume runs in one again
			if box.Spec.Network {
				start["sandbox_network"] = true
			}
		}
		if len(s.opts.Team) > 0 && s.opts.Swarm {
			start["team"] = s.opts.Team // a custom composition: a resume keeps it
		}
		if s.opts.Verify != "" {
			start["verify"] = s.opts.Verify // a resume keeps it (asking first where it raises what the session may do)
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
	out.ListUSD, out.Plan = s.listCost()
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

// Options returns a copy of the options the session was built with (an in-process restart rebuilds a session from them);
// Sink, NewSink and Prompter are cleared in the copy, and its maps and slices are copies that the caller may change. ID, Dir
// and Resume are those the session was opened with: a resumed session has its directory in Dir.
func (s *Session) Options() Options {
	o := s.opts
	s.mu.Lock()
	o.AutoFix, o.AutoMerge = s.deliveryOptions.AutoFix, s.deliveryOptions.AutoMerge
	s.mu.Unlock()
	o.Sink, o.NewSink, o.Prompter = nil, nil, nil
	o.Allow = append([]string(nil), o.Allow...)
	o.ShellEnv = append([]string(nil), o.ShellEnv...)
	o.ShellWrap = append([]string(nil), o.ShellWrap...)
	if s.opts.ShellEnv == nil {
		o.ShellEnv = nil // nil inherits the process's environment; an empty copy would be an empty environment
	}
	if s.opts.ShellWrap == nil || s.opts.Sandbox.Enabled() {
		o.ShellWrap = nil // a sandbox's wrapper belongs to its container: a session rebuilt from these options starts its own
	}
	if o.RoleModels != nil {
		rm := make(map[string]string, len(o.RoleModels))
		for k, v := range o.RoleModels {
			rm[k] = v
		}
		o.RoleModels = rm
	}
	if o.MCPServers != nil {
		servers := make(map[string]json.RawMessage, len(o.MCPServers))
		for name, entry := range o.MCPServers {
			servers[name] = append(json.RawMessage(nil), entry...)
		}
		o.MCPServers = servers
	}
	if o.Meta != nil {
		meta := make(map[string]any, len(o.Meta))
		for k, v := range o.Meta {
			meta[k] = v
		}
		o.Meta = meta
	}
	if o.Mailman != nil {
		v := *o.Mailman
		o.Mailman = &v
	}
	if o.AutoDocs != nil {
		v := *o.AutoDocs
		o.AutoDocs = &v
	}
	if o.ManagerApproves != nil {
		v := *o.ManagerApproves
		o.ManagerApproves = &v
	}
	o.Team = append([]TeamSlot(nil), o.Team...)
	return o
}

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
	s.stopSandbox()
	s.closeMCP()
	if started && s.hookAdapter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.hookAdapter.fire(ctx, hooks.Event{Name: hooks.SessionEnd, Agent: s.mainAgent(), Extra: map[string]any{"reason": reason}})
		cancel()
	}
	if s.ckptLog != nil {
		s.ckptLog.close() // the checkpoints' last changes, before the log ends
	}
	if started {
		end := map[string]any{"cost_usd": s.cost(), "reason": reason}
		if list, plan := s.listCost(); plan {
			// What a subscription plan covered, at the API list price; cost_usd stays what was billed.
			end["list_usd"], end["plan"] = list, true
		}
		s.Log.Emit("", events.TypeSessionEnd, end)
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

// listCost is cost's counterpart for the usage a subscription plan covered: its value at the API list price (cost.Model.ListUSD), which
// the interfaces show and no budget counts. plan reports whether the session's model is on a plan or any agent's, compactor's or the
// goal judge's request was accounted on a plan's model: a plan's model whose list price is not known adds nothing to list and is still
// a plan's (a worker of a billed team that runs such a model, say).
func (s *Session) listCost() (list float64, plan bool) {
	s.mu.Lock()
	list, plan = s.judgeListUSD, s.judgePlan
	s.mu.Unlock()
	switch {
	case s.Swarm != nil:
		l, p := s.Swarm.TotalListCost()
		list, plan = list+l, plan || p
	case s.Agent != nil:
		l, p := s.Agent.PlanUsage()
		list, plan = list+l, plan || p
	}
	return list, plan || s.Model.Plan
}

// cacheSettings are the configuration's cache settings with what the session's options say over them: --compact-at over
// compact_at_percent.
func cacheSettings(c config.Cache, o Options) config.Cache {
	if o.CompactAtPercent > 0 {
		c.CompactAtPercent = o.CompactAtPercent
	}
	return c
}

// CheckCompactAt refuses a compaction share that is not one: 0 (the configuration's) or a percentage from 10 to 90, as
// cache.compact_at_percent takes it.
func CheckCompactAt(percent int) error {
	if percent != 0 && (percent < 10 || percent > 90) {
		return fmt.Errorf("--compact-at must be from 10 to 90 (percent of each model's context window), or 0 for the configuration's, got %d", percent)
	}
	return nil
}

// CompactAtPercent is the share of each model's context window, in percent, at which the cache settings c start compaction:
// compact_at_percent, else 25. It is 0 when token limits set without compact_at_percent apply to every model instead (plannerFor).
func CompactAtPercent(c config.Cache) int {
	p := plannerFor(c)
	if p.CompactAt < 0 {
		return 0
	}
	return int(math.Round(p.CompactAt * 100))
}

// plannerFor is the compaction planner that the cache settings ask for. Compaction starts at a share of each model's window
// (compact_at_percent, 25 by default); token limits set without it keep applying to every model, as they did before the share
// existed, and apply to a model whose window is not known in any case. A soft limit that is set above the default hard one
// would never be reached, the hard limit forcing the compaction first, so the hard limit follows it up unless it was set too.
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
	switch {
	case c.CompactAtPercent > 0:
		p.CompactAt = float64(c.CompactAtPercent) / 100
	case c.ThreadSoftLimitTokens > 0 || c.CompactThresholdTokens > 0:
		p.CompactAt = -1
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
