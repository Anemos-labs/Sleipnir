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
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
const PolicyProvider = "rl-policy"

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
	if h.RateLimit != nil {
		prov = &paced{Provider: prov, limit: h.RateLimit}
	}
	lim := &limited{Provider: prov, max: int64(spec.Budget.Requests)}
	cfg, err := h.config(spec)
	if err != nil {
		return env.RunResult{}, env.Infra("config", err)
	}
	opts, err := h.options(spec, cfg, lim, model)
	if err != nil {
		return env.RunResult{}, env.Infra("task", err)
	}
	sess, err := session.New(ctx, opts)
	if err != nil {
		return env.RunResult{}, env.Infra("session", err)
	}
	res, runErr := sess.Run(ctx, spec.Task.Prompt)
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
		ID:            sessionID(spec), Dir: spec.RunDir,
		Mode:     h.mode(), // no Prompter: nobody answers, and the engine then refuses with the reason and what to do instead
		MaxSteps: spec.Budget.Steps, BudgetUSD: spec.Budget.USD,
		ContextWindow: firstPositive(spec.Budget.ContextWindow, h.ContextTokens),
		TrustProject:  !h.IgnoreRepoInstructions,
		NoWeb:         !task.Network, Offline: true,
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
	if o.ShellEnv == nil {
		// Never fall back to the operator's environment: keys and cloud
		// credentials live there. A run without a prepared environment gets the
		// bare minimum to find its tools.
		o.ShellEnv = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + o.Home, "TMPDIR=" + o.Home}
	}
	params, _ := samplingParams(spec.Policy.Sampling)
	o.Params = params
	// A rollout does not wait out an endpoint that is down: the runner repeats a rollout that ended for that reason, whole and at no
	// cost to the statistics, and a wait inside it would spend the run's own clock and end it as a budget episode.
	o.OutagePatience = -1
	if !spec.Single && (spec.Swarm || task.Team.Mode == "swarm") {
		o.Swarm = true
		// A team size counts workers here (`run --swarm N` is N agents, so N-1 workers: cmd/sleipnir converts, and a composite task's
		// team is one worker per part); MaxAgents counts the manager too. Passing N as it stood gave a task
		// of three parts two workers.
		if n := firstPositive(spec.Agents, task.Team.Agents); n > 0 {
			o.MaxAgents = n + 1
		}
		o.RoleModels = spec.RoleModels
		roles, err := rolesFor(task.Team)
		if err != nil {
			return o, err
		}
		o.Roles = roles
		o.Verify = visibleVerify(task)
	}
	return o, nil
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
		merged, _ := opts["extra_body"].(map[string]any)
		if merged == nil {
			merged = map[string]any{}
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
	if n := l.n.Add(1); l.max > 0 && n > l.max {
		l.n.Add(-1)
		l.hit.Store(true)
		return nil, &provider.Error{Kind: provider.ErrPayment, Message: fmt.Sprintf("request budget of %d exhausted", l.max)}
	}
	resp, err := l.Provider.Do(ctx, req, on)
	if err != nil && unanswered(err) {
		l.n.Add(-1)
		l.retried.Add(1)
	}
	return resp, err
}

// unanswered reports whether a request failed in a way the agent repeats it for: the endpoint could not be
// reached, timed out, refused for rate, or failed (the kinds that say nothing about the policy, see isInfra).
func unanswered(err error) bool {
	var pe *provider.Error
	return errors.As(err, &pe) && pe.Retryable()
}

// exhausted atomically reports whether the request limit has been reached.
func (l *limited) exhausted() bool { return l.hit.Load() }
