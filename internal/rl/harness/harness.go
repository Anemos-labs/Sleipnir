// Package harness runs RL rollouts through the assembly a person uses
// (internal/session): one agent or a swarm, the real tools, the real permission
// engine, the real cache layers and compactors, against a policy endpoint.
//
// It implements env.Harness. The policy therefore learns the harness it will be
// deployed in, not a simplified stand-in: the same constitution, the same tool
// list, the same shared and role layers, the same compaction and mail protocol,
// and the same refusals from the permission engine.
//
// What differs from an interactive session is only what a rollout needs to be a
// clean experiment:
//
//   - Configuration is fixed. User and project settings files are never read, so
//     a rollout depends on neither the machine it runs on nor a hostile
//     repository's config.
//   - The agent's commands run in the scrubbed environment the runner prepared
//     (private HOME, no credentials) and, when the host can isolate, without a
//     network. The policy's API key lives in this process only.
//   - Nobody answers permission prompts: a request that would ask is refused,
//     the way an unattended session would refuse it. The default rules let the
//     agent edit inside its workspace and run the usual build and test tooling.
//   - Budgets are hard limits: steps and requests end the run as a normal
//     "budget" outcome; wall-clock is enforced by the runner through ctx.
//   - An outage of the policy endpoint pauses the run instead of failing it: the
//     agents wait it out (env.RunSpec.OutagePatience), and every model call
//     reports outages to the runner's clock (env.RunSpec.Clock), which does not
//     count them against the wall budget.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// PolicyProvider is the name the policy endpoint has inside a rollout's
// configuration. A bare model id (in RoleModels, say) resolves to it.
const PolicyProvider = env.PolicyProvider

// DefaultAllow are the permission rules rollouts add to accept-edits mode: the
// build, test and version-control tooling a working agent needs, with every
// destructive or network-reaching form still going through the engine's own
// analysis (the rules match commands as written; the engine still denies
// credential paths and asks, hence refuses, on high-risk commands).
var DefaultAllow = []string{
	"Bash(go:*)", "Bash(gofmt:*)", "Bash(make:*)", "Bash(cargo:*)", "Bash(rustc:*)",
	"Bash(npm:*)", "Bash(npx:*)", "Bash(pnpm:*)", "Bash(yarn:*)", "Bash(node:*)", "Bash(tsc:*)",
	"Bash(python:*)", "Bash(python3:*)", "Bash(pytest:*)", "Bash(pip:*)", "Bash(uv:*)",
	"Bash(mvn:*)", "Bash(gradle:*)", "Bash(./gradlew:*)", "Bash(dotnet:*)", "Bash(cmake:*)", "Bash(ctest:*)",
	"Bash(git add:*)", "Bash(git commit:*)", "Bash(git stash:*)", "Bash(git checkout:*)", "Bash(git restore:*)",
	"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
}

// Harness implements env.Harness on top of session.New.
type Harness struct {
	// Config is the configuration rollouts run under. nil means the built-in
	// defaults. It is copied, never modified.
	Config *config.Config
	// Mode is the permission mode; the default is accept-edits.
	Mode perm.Mode
	// Allow replaces DefaultAllow when non-nil (use []string{} for none).
	Allow []string
	// PolicyOptions are extra provider options for the policy endpoint (the keys
	// of config.Provider.Options: "system_role", "max_tokens_field", ...), and
	// PolicyHeaders extra request headers.
	PolicyOptions map[string]any
	PolicyHeaders map[string]string
	// PolicyAllowInsecureHTTP lets the policy's API key travel over plain http to a host
	// that is not this machine (a self-hosted policy server on a trusted network), and
	// PolicyAllowHosts names hosts, besides the policy's own base URL, that may receive
	// it. Both are the operator's word (a command-line flag, or the user's own provider
	// entry); a rollout request never sets them (provider.CheckEndpoint).
	PolicyAllowInsecureHTTP bool
	PolicyAllowHosts        []string
	// ContextTokens is the policy's context window when a task does not set one.
	// Zero keeps the model table's or the fallback's.
	ContextTokens int
	// ThreadSoftLimit is the size of an agent's thread, in tokens, at which compaction is
	// considered (config cache.thread_soft_limit_tokens). Zero keeps the default. A rollout is
	// short, so whether it is compacted at all is a setting worth comparing.
	ThreadSoftLimit int
	// IgnoreRepoInstructions stops AGENTS.md-style files in the task's repository
	// from joining the shared layer. They normally do: they are part of the task.
	IgnoreRepoInstructions bool
	// NewProvider replaces building the policy client from RunSpec.Policy: tests,
	// replay servers and recorded-response policies plug in here.
	NewProvider func(spec env.RunSpec) (provider.Provider, cost.Model, error)
	// HTTPClient is used for the policy endpoint (nil: the adapter's default).
	HTTPClient *http.Client
	// NewSink, if set, gives every agent a UI sink (a progress bar, a transcript).
	NewSink func(agentID string) agent.Sink
	// RateLimit, if set, paces the policy requests of every rollout that uses this Harness (see RateLimit).
	RateLimit *RateLimit
}

var _ env.Harness = (*Harness)(nil)

// Run implements env.Harness.
func (h *Harness) Run(ctx context.Context, spec env.RunSpec) (env.RunResult, error) {
	if strings.TrimSpace(spec.Task.Prompt) == "" {
		return env.RunResult{}, env.Infraf("task", "task %q has no prompt", spec.Task.ID)
	}
	prov, model, err := h.policy(spec)
	if err != nil {
		return env.RunResult{}, env.Infra("policy", err)
	}
	// Every model of the rollout, the policy's and the roles', reports its outages to the runner's clock.
	watch := &outageWatch{clock: spec.Clock}
	wrap := func(p provider.Provider) provider.Provider {
		p = watch.wrap(p)
		if h.RateLimit != nil {
			p = &paced{Provider: p, limit: h.RateLimit}
		}
		return p
	}
	lim := &limited{Provider: wrap(prov), max: int64(spec.Budget.Requests)}
	cfg, err := h.config(spec)
	if err != nil {
		return env.RunResult{}, env.Infra("config", err)
	}
	opts, err := h.options(spec, cfg, lim, model)
	if err != nil {
		return env.RunResult{}, env.Infra("task", err)
	}
	// The roles' models are the policy's too (env routes them to it): they share its pacing and its request budget.
	opts.WrapProvider = func(p provider.Provider) provider.Provider { return &budgeted{Provider: wrap(p), l: lim} }
	sess, err := session.New(ctx, opts)
	if err != nil {
		return env.RunResult{}, env.Infra("session", err)
	}
	watch.attach(sess.Log)
	if hook := spec.Savepoints; hook != nil {
		hook.Bind(func(ctx context.Context, capture func(context.Context) error) (env.SavepointInfo, error) {
			r, err := sess.Savepoint(ctx, hook.Polite, hook.Firm, capture)
			info := env.SavepointInfo{N: r.N, Consistent: r.Consistent, LogBytes: r.LogBytes, Seq: r.Seq}
			if len(r.Agents) > 0 {
				info.Agents = make(map[string]string, len(r.Agents))
				for id, h := range r.Agents {
					info.Agents[id] = string(h)
				}
			}
			return info, err
		})
	}
	// A resumed session has the task in its thread already: it is told to go on.
	prompt := spec.Task.Prompt
	switch {
	case spec.Resume && opts.Swarm:
		prompt = env.ResumeNoteTeam
	case spec.Resume:
		prompt = env.ResumeNote
	}
	if spec.Resume && spec.CompactFirst {
		// A branch that summarises first: its siblings fold the same state, each with its own seed. A compaction that cannot
		// run (too little to fold) leaves the branch to go on as it is; the episode shows whether one happened.
		_, _ = sess.Compact(ctx, "")
	}
	res, runErr := sess.Run(ctx, prompt)
	if spec.Savepoints != nil {
		spec.Savepoints.Bind(nil) // no savepoint of a session that is closing
	}
	watch.attach(nil) // a request still returning must not write to a log that is being closed
	// The runner appends the outcome events to the same log afterwards, so the log
	// has to be closed before this function returns.
	if err := sess.Close(); err != nil {
		return env.RunResult{}, env.Infra("log", err)
	}
	return h.result(ctx, res, runErr, lim)
}

// result maps how a run ended onto the contract of env.Harness: policy-caused
// endings are normal results, only faults of the machinery are errors.
func (h *Harness) result(ctx context.Context, res *session.Result, runErr error, lim *limited) (env.RunResult, error) {
	out := env.RunResult{Claimed: "done"}
	if res != nil {
		out.FinalMessage = res.Text
	}
	if runErr == nil {
		return out, nil
	}
	// The runner owns wall-clock and cancellation: it knows which one ended the
	// run, and it turns a deadline it set into a budget outcome, but only when
	// nothing was claimed: a run that was cut off did not say it was done.
	if ctx.Err() != nil {
		out.Claimed = ""
		return out, ctx.Err()
	}
	// Not the run's context, whatever the error wraps: a provider that gave up
	// waiting wraps context.DeadlineExceeded too, and is the endpoint's fault
	// (classified below), not a normal ending.
	out.Err = runErr
	switch {
	case lim.exhausted() || errors.Is(runErr, agent.ErrBudget) || isStepLimit(runErr):
		out.Claimed = "budget"
	case isInfra(runErr):
		return env.RunResult{}, env.Infra("provider", runErr)
	default:
		out.Claimed = "gave_up"
	}
	return out, nil
}

// isInfra reports whether a provider failure says nothing about the policy: the
// endpoint is unreachable, overloaded, or rejects the credentials. (An
// oversized prompt, a refusal or a malformed request is the policy's doing.)
func isInfra(err error) bool {
	var pe *provider.Error
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.Kind {
	case provider.ErrNetwork, provider.ErrTimeout, provider.ErrAuth, provider.ErrRateLimit,
		provider.ErrOverloaded, provider.ErrServer:
		return true
	}
	return false
}

// isStepLimit recognizes the step-limit diagnostic in a nonnil error's text.
func isStepLimit(err error) bool { return strings.Contains(err.Error(), "step limit") }

// options translates a RunSpec into session options.
func (h *Harness) options(spec env.RunSpec, cfg *config.Config, p provider.Provider, model cost.Model) (session.Options, error) {
	task := spec.Task
	o := session.Options{
		Cwd: spec.Workspace, Root: spec.Workspace, Home: homeOf(spec.Env, spec.RunDir),
		Config: cfg, Model: spec.Policy.Model, Provider: p, ModelInfo: &model,
		CaptureTokens: spec.Capture,
		TrainingLogs:  true, // a rollout is exported as a trajectory
		ID:            sessionID(spec), Dir: spec.RunDir,
		Savepoints: spec.Savepoints != nil,
		Mode:       h.mode(), // no Prompter: nobody answers, and the engine then refuses with the reason and what to do instead
		MaxSteps:   spec.Budget.Steps, BudgetUSD: spec.Budget.USD,
		FixedSteps:    true, // the step budget is part of the task
		ContextWindow: firstPositive(spec.Budget.ContextWindow, h.ContextTokens),
		TrustProject:  !h.IgnoreRepoInstructions,
		// The task's repository may bring agent definitions; a model one names runs on the policy endpoint, never on a
		// provider of the operator's configuration with the operator's credential (a "vendor/model" id is taken whole).
		// With an injected policy client there is no endpoint to name, and the role runs on that client.
		DefinitionModel: func(m string) string {
			if h.NewProvider != nil {
				return ""
			}
			if strings.HasPrefix(m, PolicyProvider+"/") {
				return m
			}
			return PolicyProvider + "/" + m
		},
		NoWeb: !task.Network, Offline: true,
		// A rollout is a closed, reproducible episode: no tool servers, whatever the
		// task's repository or the user's configuration says.
		NoMCP:    true,
		ShellEnv: spec.Env, ShellWrap: spec.NetPrefix,
		NewSink: h.NewSink,
		// What the log says about its own policy: the extractor reads it back, so an
		// episode's policy is what the run recorded, not what a caller remembers.
		Meta: map[string]any{"rl": map[string]any{
			"task": task.ID, "kind": task.Kind, "sample": spec.Sample, "group": spec.Group, "attempt": spec.Attempt,
			"seed": spec.Seed, "capture": spec.Capture, "policy": spec.Policy.Model, "sampling": json.RawMessage(nonEmpty(spec.Policy.Sampling)),
			"endpoint": hostOf(spec.Policy.BaseURL), "role_models": spec.RoleModels, "target_price": spec.TargetPrice,
		}},
	}
	if spec.Resume {
		// The run directory's log, cut back to a savepoint by the runner, is the session to continue.
		o.ID, o.Dir, o.Resume = loggedSession(spec.RunDir), "", spec.RunDir
	}
	if o.ShellEnv == nil {
		// Never fall back to the operator's environment: keys and cloud
		// credentials live there. A run without a prepared environment gets the
		// bare minimum to find its tools.
		o.ShellEnv = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + o.Home, "TMPDIR=" + o.Home}
	}
	params, _ := samplingParams(spec.Policy.Sampling)
	o.Params = params
	// A rollout waits out an endpoint that is down, whether it says so (a 5xx, a 429) or answers nothing (a refused or reset
	// connection, no response in time): the endpoint is the one the run has been using, so silence is an outage and not a wrong
	// address. The runner's clock stops meanwhile (spec.Clock), so the wait costs the rollout none of its wall budget. Without a
	// patience the agents give up after the usual attempts, and the runner repeats the rollout whole.
	if spec.OutagePatience > 0 {
		o.OutagePatience, o.WaitUnreachable = spec.OutagePatience, true
	} else {
		o.OutagePatience = -1
	}
	if !spec.Single && (spec.Swarm || task.Team.Mode == "swarm") {
		o.Swarm = true
		// A team size counts workers (`--swarm N` and `--mode swarm:N` are N workers and a manager, and a composite
		// task's team is one worker per part).
		if n := firstPositive(spec.Agents, task.Team.Agents); n > 0 {
			o.Workers = n
		}
		o.RoleModels = spec.RoleModels
		roles, err := rolesFor(task.Team)
		if err != nil {
			return o, err
		}
		o.Roles = roles
		own, err := ownersFor(task.Team, roles)
		if err != nil {
			return o, err
		}
		if own != nil {
			o.Owners = own
		}
		o.Verify = visibleVerify(task)
	}
	return o, nil
}

// ownersFor compiles the task's team.owns for the swarm (nil when it owns
// nothing). Every owning role must be one of the team's roles: roles, or every
// built-in role when roles is nil.
func ownersFor(t rl.Team, roles swarm.Roles) (*env.Ownership, error) {
	own, err := env.CompileOwnership(t.Owns)
	if err != nil || own == nil {
		return nil, err
	}
	if roles == nil {
		roles = swarm.BuiltinRoles()
	}
	for _, r := range own.Roles() {
		role, ok := roles[r]
		switch {
		case !ok:
			return nil, fmt.Errorf("task team.owns names role %q, which is not in the team (%s)", r, strings.Join(roles.Names(), ", "))
		case r == "manager" || role.ReadOnly:
			return nil, fmt.Errorf("task team.owns names role %q, which does not write files", r)
		}
	}
	return own, nil
}

// hostOf is the host of a URL, never its credentials or path.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// nonEmpty substitutes JSON null for absent raw JSON without validating nonempty input.
func nonEmpty(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

// firstPositive returns the first value greater than zero or zero when none exists.
func firstPositive(vs ...int) int {
	for _, v := range vs {
		if v > 0 {
			return v
		}
	}
	return 0
}

// mode uses an explicit harness permission mode or defaults to accept-edits.
func (h *Harness) mode() perm.Mode {
	if h.Mode != "" {
		return h.Mode
	}
	return perm.ModeAcceptEdits
}

// config builds the fixed configuration of a rollout: the defaults, the policy
// endpoint as a provider, and the permission rules.
func (h *Harness) config(spec env.RunSpec) (*config.Config, error) {
	base := h.Config
	if base == nil {
		base = config.Defaults()
	}
	cfg := *base
	cfg.Providers = map[string]config.Provider{}
	for k, v := range base.Providers {
		cfg.Providers[k] = v
	}
	if h.NewProvider == nil {
		pol, err := h.policyProviderConfig(spec)
		if err != nil {
			return nil, err
		}
		cfg.Providers[PolicyProvider] = pol
	} else if _, ok := cfg.Providers[PolicyProvider]; !ok {
		// Role-model references need a provider to resolve against even when the
		// policy client itself is injected.
		cfg.Providers[PolicyProvider] = config.Provider{Dialect: config.DialectOpenAIChat, BaseURL: "http://policy.invalid/v1"}
	}
	allow := h.Allow
	if allow == nil {
		allow = DefaultAllow
	}
	cfg.Permissions.Allow = append(append([]string(nil), base.Permissions.Allow...), allow...)
	cfg.Permissions.Mode = string(h.mode())
	if h.ThreadSoftLimit > 0 {
		cfg.Cache.ThreadSoftLimitTokens = h.ThreadSoftLimit
	}
	// Rollouts never use the user's hooks, MCP servers or training settings.
	cfg.Hooks, cfg.MCP = nil, nil
	return &cfg, nil
}

// policy builds the client for the policy endpoint.
func (h *Harness) policy(spec env.RunSpec) (provider.Provider, cost.Model, error) {
	if h.NewProvider != nil {
		return h.NewProvider(spec)
	}
	pc, err := h.policyProviderConfig(spec)
	if err != nil {
		return nil, cost.Model{}, err
	}
	cfg := &config.Config{Providers: map[string]config.Provider{PolicyProvider: pc}}
	p, m, err := session.BuildProvider(cfg, session.ModelRef{Provider: PolicyProvider, Model: spec.Policy.Model},
		session.ProviderOptions{CaptureTokens: spec.Capture, HTTPClient: h.HTTPClient})
	var refused *provider.EndpointError
	if errors.As(err, &refused) {
		// The refusal names the user's configuration file, which chat and run read; a policy
		// is set up on the command line.
		err = fmt.Errorf("%w\nfor an RL policy the setting is the --allow-insecure-http flag of `sleipnir rl rollout`, `rl eval` and `rl serve` (the configuration snippet above is for chat and run)", err)
	}
	return p, m, err
}

// policyProviderConfig describes the policy endpoint. The API key is read from
// the variable the spec names, in this process only: the agent's shell never
// sees it.
func (h *Harness) policyProviderConfig(spec env.RunSpec) (config.Provider, error) {
	extra, headers := h.PolicyOptions, h.PolicyHeaders
	pol := spec.Policy
	if pol.Model == "" {
		return config.Provider{}, errors.New("the policy has no model")
	}
	if pol.BaseURL == "" {
		return config.Provider{}, errors.New("the policy has no base URL")
	}
	opts := map[string]any{}
	for k, v := range extra {
		opts[k] = v
	}
	if body := samplingBody(pol.Sampling, spec.Seed); len(body) > 0 {
		// The provider entry's own extra_body is shared by every rollout of this Harness, which
		// run concurrently: merge into a copy, never into the entry's map.
		merged := map[string]any{}
		if shared, ok := opts["extra_body"].(map[string]any); ok {
			for k, v := range shared {
				merged[k] = v
			}
		}
		for k, v := range body {
			merged[k] = v
		}
		opts["extra_body"] = merged
	}
	if spec.Capture {
		opts["capture_tokens"] = true
	}
	return config.Provider{
		Dialect: config.DialectOpenAIChat, BaseURL: pol.BaseURL, APIKeyEnv: pol.APIKeyEnv, Options: opts, Headers: headers,
		AllowHosts: h.PolicyAllowHosts, AllowInsecureHTTP: h.PolicyAllowInsecureHTTP,
	}, nil
}

// samplingParams reads the sampling knobs the session models itself.
func samplingParams(raw json.RawMessage) (core.Params, map[string]any) {
	var p core.Params
	m := decodeSampling(raw)
	if v, ok := number(m["temperature"]); ok {
		p.Temperature = &v
	}
	if v, ok := number(m["max_tokens"]); ok && v > 0 {
		p.MaxTokens = int(v)
	}
	return p, m
}

// samplingBody is what goes into every request body besides temperature and
// max_tokens: top_p, top_k, min_p, repetition_penalty and so on, plus the
// rollout's seed unless the sampling block already fixes one.
func samplingBody(raw json.RawMessage, seed int64) map[string]any {
	m := decodeSampling(raw)
	body := map[string]any{}
	for k, v := range m {
		switch k {
		case "temperature", "max_tokens", "model", "messages", "tools", "stream", "stream_options":
			// modelled by the session, or owned by the adapter
		default:
			body[k] = v
		}
	}
	if _, ok := body["seed"]; !ok && seed != 0 {
		body["seed"] = seed & env.MaxWireSeed // larger seeds are refused by some endpoints (env.MaxWireSeed)
	}
	return body
}

// decodeSampling decodes a JSON object and returns nil for absent, null, or malformed sampling
// options.
func decodeSampling(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// number accepts only float64 values, as produced by default JSON number decoding.
func number(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// rolesFor restricts the swarm to the roles the task names (the manager is
// always present). An empty list means every built-in role.
func rolesFor(t rl.Team) (swarm.Roles, error) {
	if len(t.Roles) == 0 {
		return nil, nil
	}
	all := swarm.BuiltinRoles()

	out := swarm.Roles{}
	if m, ok := all["manager"]; ok {
		out["manager"] = m
	}
	for _, name := range t.Roles {
		r, ok := all[name]
		if !ok {
			return nil, fmt.Errorf("task names unknown role %q", name)
		}
		out[name] = r
	}
	return out, nil
}

// visibleVerify is the command a swarm's harness-owned "done" gate runs. It is
// the task's verifier command only when that verifier hides nothing: a command
// that depends on hidden files must not be visible to, or runnable by, the
// policy. The clean-checkout verification after the episode stays the ground
// truth either way.
func visibleVerify(t rl.Task) string {
	if len(t.Verifier.Hidden) > 0 || t.Verifier.Pass == "json-score" || strings.HasPrefix(t.Verifier.Pass, "regex:") {
		return ""
	}
	return t.Verifier.Cmd
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sessionID sanitizes the task ID and combines it with sample and one-based attempt numbers.
func sessionID(spec env.RunSpec) string {
	id := unsafeID.ReplaceAllString(spec.Task.ID, "-")
	if id == "" {
		id = "task"
	}
	return fmt.Sprintf("%s-s%d-a%d", id, spec.Sample, max(spec.Attempt, 1))
}

// loggedSession is the session id the first event of the run directory's log
// carries ("" when there is none): a resumed rollout goes on under it.
func loggedSession(dir string) string {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var e struct {
		Session string `json:"session"`
	}
	if json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&e) != nil || unsafeID.MatchString(e.Session) {
		return ""
	}
	return e.Session
}

// homeOf finds the HOME of the prepared environment; instruction files and
// caches are looked up there, so it must be the rollout's private one. The
// fallback keeps a run without an environment away from the operator's home.
func homeOf(env []string, runDir string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok && v != "" {
			return v
		}
	}
	return runDir + "/home"
}

// limited counts requests and refuses the ones over the task's request budget.
// The refusal is a payment error, which the agent never retries, so the run ends
// there and is reported as a budget outcome.
//
// Only requests the endpoint answered count. One it did not answer (a 429, a 5xx, a
// dropped connection) is one the agent repeats, and it is not the policy's doing: counting it ended runs as
// "budget" for requests that produced nothing, so on an endpoint that is busy now and then a benchmark
// measured the endpoint's moods instead of the model's work.
type limited struct {
	provider.Provider
	max     int64
	n       atomic.Int64 // requests answered, plus those in flight
	hit     atomic.Bool
	retried atomic.Int64 // requests the endpoint did not answer, which the agent repeated
}

// Do implements provider.Provider.
func (l *limited) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	return l.do(ctx, l.Provider, req, on)
}

// do sends req through p, counted against l's budget.
func (l *limited) do(ctx context.Context, p provider.Provider, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	if n := l.n.Add(1); l.max > 0 && n > l.max {
		l.n.Add(-1)
		l.hit.Store(true)
		return nil, &provider.Error{Kind: provider.ErrPayment, Message: fmt.Sprintf("request budget of %d exhausted", l.max)}
	}
	resp, err := p.Do(ctx, req, on)
	if err != nil && unanswered(err) {
		l.n.Add(-1)
		l.retried.Add(1)
	}
	return resp, err
}

// budgeted is another client of the rollout (a role's model), counted against the budget of the policy's client l.
type budgeted struct {
	provider.Provider
	l *limited
}

// Do implements provider.Provider.
func (b *budgeted) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	return b.l.do(ctx, b.Provider, req, on)
}

// unanswered reports whether a request failed in a way the agent repeats it for: the endpoint could not be
// reached, timed out, refused for rate, or failed (the kinds that say nothing about the policy, see isInfra),
// or went silent during an outage the agent waits out.
func unanswered(err error) bool {
	var pe *provider.Error
	return errors.As(err, &pe) && (pe.Retryable() || pe.Outage() != provider.NotOutage)
}

// exhausted atomically reports whether the request limit has been reached.
func (l *limited) exhausted() bool { return l.hit.Load() }
