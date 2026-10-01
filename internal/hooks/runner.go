package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// Defaults for the zero value of a Runner.
const (
	// DefaultTimeout bounds a hook that names no timeout. Claude Code allows ten
	// minutes; a hook runs in the middle of a tool call, on every call, and a
	// hung one stalls an agent, so the default is short.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxTimeout is the longest timeout a hook may ask for.
	DefaultMaxTimeout = 10 * time.Minute
	// DefaultMaxStdout and DefaultMaxStderr are how much of each stream is kept.
	DefaultMaxStdout = 256 << 10
	DefaultMaxStderr = 64 << 10
	// DefaultMaxRawOutput is how much a hook may print before it is stopped.
	DefaultMaxRawOutput = 16 << 20
	// DefaultMaxContext caps the AdditionalContext of one event, in bytes (Claude
	// Code's limit is 10,000 characters).
	DefaultMaxContext = 10_000
	// DefaultMaxPayload caps the JSON sent to one hook.
	DefaultMaxPayload = 4 << 20
	// DefaultMaxParallel bounds how many hooks of one event run at once.
	DefaultMaxParallel = 8
	// DefaultMaxConcurrent bounds how many hooks run at once across all events and
	// agents sharing a Runner: fifty agents each firing PreToolUse hooks should
	// queue, not fork hundreds of processes together.
	DefaultMaxConcurrent = 32
	// DefaultKillGrace is how long a hook gets to obey SIGTERM before SIGKILL.
	DefaultKillGrace = 500 * time.Millisecond
	// DefaultPipeGrace is how long the runner waits, once a hook's process has
	// exited, for the last of its output. A background child that inherited the
	// hook's stdout would otherwise keep the hook "running" until it exits; when
	// the wait runs out the stragglers are ended. It is generous because a
	// properly detached child (output redirected) is still finishing its own
	// start-up in this window, and a loaded machine can take a while.
	DefaultPipeGrace = time.Second
)

// Runner runs the hooks of a Set. It is safe for concurrent use by many agents;
// its fields must not be changed after the first call to Run.
type Runner struct {
	// Set holds the hooks. A nil or empty Set makes every Run a no-op.
	Set *Set
	// Dir is the project directory: hooks run there unless the event names a
	// working directory, and it is exported as SLEIPNIR_PROJECT_DIR and
	// CLAUDE_PROJECT_DIR.
	Dir string
	// SessionID is used for events that do not carry one.
	SessionID string

	// Env is the environment hooks inherit ("K=V"); nil means os.Environ(). It is
	// scrubbed of credentials before a hook sees it.
	Env []string
	// PassEnv exempts variables from scrubbing; DenyEnv removes more. Both take
	// case-insensitive names with path.Match wildcards. DenyEnv is where the caller
	// names the providers' API key variables, which no name pattern can be trusted
	// to catch.
	PassEnv, DenyEnv []string

	// Trusted says every hook in Set is vouched for: it comes from the user's own
	// configuration or from a project the user chose to trust. Without it, only
	// hooks with OriginUser run, and hooks of any other origin run only if Approve
	// says yes. The zero Runner runs nothing from an unmarked source.
	Trusted bool
	// Approve is asked, at most once per distinct hook while the Runner lives, when
	// a hook is not trusted; it may prompt the user. Approval prompts are
	// serialised. nil means untrusted hooks are simply not run.
	Approve func(ctx context.Context, h Hook) bool

	// AllowHTTP enables hooks of type "http". They send the payload (tool inputs
	// and results) to a URL, so they are off by default.
	AllowHTTP bool
	// HTTPClient is used for http hooks; nil means a client that follows no
	// redirects.
	HTTPClient *http.Client

	// FailClosed makes a hook that fails (crashes, times out, prints invalid JSON)
	// block the events that can be blocked, instead of letting them proceed as
	// Claude Code does. A hook's own "failClosed" overrides it.
	FailClosed bool

	// Limits; the zero value of each means the default named above.
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxStdout      int
	MaxStderr      int
	MaxRawOutput   int64
	MaxContext     int
	MaxPayload     int
	MaxParallel    int
	MaxConcurrent  int
	KillGrace      time.Duration
	PipeGrace      time.Duration

	semOnce   sync.Once
	sem       chan struct{}
	mu        sync.Mutex
	approveMu sync.Mutex
	approved  map[string]bool
	engines   map[string]*perm.Engine
}

func (r *Runner) maxStdout() int {
	if r.MaxStdout > 0 {
		return r.MaxStdout
	}
	return DefaultMaxStdout
}

func (r *Runner) maxStderr() int {
	if r.MaxStderr > 0 {
		return r.MaxStderr
	}
	return DefaultMaxStderr
}

func (r *Runner) maxRaw() int64 {
	if r.MaxRawOutput > 0 {
		return r.MaxRawOutput
	}
	return DefaultMaxRawOutput
}

func (r *Runner) maxContext() int {
	if r.MaxContext > 0 {
		return r.MaxContext
	}
	return DefaultMaxContext
}

func (r *Runner) maxPayload() int {
	if r.MaxPayload > 0 {
		return r.MaxPayload
	}
	return DefaultMaxPayload
}

func (r *Runner) maxParallel() int {
	if r.MaxParallel > 0 {
		return r.MaxParallel
	}
	return DefaultMaxParallel
}

// acquire takes one of the Runner-wide slots, waiting for one to free up; it
// returns false if ctx ends first. The wait does not count against the hook's
// timeout, which starts when the hook does.
func (r *Runner) acquire(ctx context.Context) (release func(), ok bool) {
	r.semOnce.Do(func() {
		n := r.MaxConcurrent
		if n <= 0 {
			n = DefaultMaxConcurrent
		}
		r.sem = make(chan struct{}, n)
	})
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem }, true
	case <-ctx.Done():
		return nil, false
	}
}

func (r *Runner) killGrace() time.Duration {
	if r.KillGrace > 0 {
		return r.KillGrace
	}
	return DefaultKillGrace
}

func (r *Runner) pipeGrace() time.Duration {
	if r.PipeGrace > 0 {
		return r.PipeGrace
	}
	return DefaultPipeGrace
}

// timeoutFor is the deadline of one hook: its own, else the default, never
// above the maximum.
func (r *Runner) timeoutFor(h Hook) time.Duration {
	t := h.Timeout
	if t <= 0 {
		t = r.DefaultTimeout
	}
	if t <= 0 {
		t = DefaultTimeout
	}
	limit := r.MaxTimeout
	if limit <= 0 {
		limit = DefaultMaxTimeout
	}
	return min(t, limit)
}

func (r *Runner) failClosed(h Hook) bool {
	if h.FailClosed != nil {
		return *h.FailClosed
	}
	return r.FailClosed
}

// hookResult is what one hook said, before the results are combined.
type hookResult struct {
	hook        Hook
	run         Run
	errs        []HookError
	block       bool
	blockReason string
	decision    Decision
	reason      string
	context     string
	updated     json.RawMessage
	stop        bool
	stopReason  string
	message     string
}

// Run runs the hooks that match ev, in parallel, and combines their answers.
//
// Hooks are chosen by their matcher and filtered by trust and by their "if"
// condition; hooks that would do the same thing (the same command configured
// twice) run once; each surviving command hook gets ev as JSON on stdin and a scrubbed
// environment. Their answers are then folded in configuration order, never in
// completion order, so the result does not depend on which hook was fastest:
// Deny beats Ask beats Allow, reasons and context are concatenated in order, and
// when several hooks rewrite the tool input the first wins.
//
// Failures of hooks (a crash, a timeout, invalid output) are reported in
// Result.Errors and do not stop the others. The error return is for the caller's
// mistakes (an unknown event, an event that cannot be encoded) and for a
// cancelled ctx, in which case the hooks are killed and the partial Result is
// returned with it.
func (r *Runner) Run(ctx context.Context, ev Event) (Result, error) {
	event, ok := Canonical(ev.Name)
	if !ok {
		return Result{}, fmt.Errorf("hooks: unknown event %q", clip(ev.Name, 40))
	}
	if r == nil || r.Set.Empty() {
		return Result{}, nil
	}
	matched, err := r.Set.Matching(ev)
	if err != nil || len(matched) == 0 {
		return Result{}, err
	}

	type slot struct {
		hook    Hook
		skipped string
		idx     int // index into run, or -1
	}
	var (
		res       Result
		slots     []slot
		run       []Hook
		untrusted []Hook
		seen      = map[string]bool{}
	)
	for _, h := range matched {
		s := slot{hook: h, idx: -1}
		switch {
		case !r.conditionHolds(ctx, h, ev):
			s.skipped = "the \"if\" condition does not match"
		case r.gate(ctx, h) != "":
			s.skipped = "not run: the hook comes from a source that is not trusted"
			untrusted = append(untrusted, h)
		case h.Type == TypeHTTP && !r.AllowHTTP:
			s.skipped = "not run: http hooks are disabled"
			res.Errors = append(res.Errors, HookError{Hook: h.String(), Message: "http hooks are disabled (Runner.AllowHTTP is false); the hook was not run"})
		case seen[h.behaviorKey()]:
			// The same command configured twice (in two files, or under two matchers
			// that both match) runs once per event. Hooks that were not allowed to run
			// never reach here, so an untrusted copy cannot shadow a trusted one.
			s.skipped = "duplicate of a hook that runs for this event"
		default:
			seen[h.behaviorKey()] = true
			s.idx = len(run)
			run = append(run, h)
		}
		slots = append(slots, s)
	}
	if len(untrusted) > 0 {
		names := make([]string, 0, 3)
		for i, h := range untrusted {
			if i == 3 {
				names = append(names, fmt.Sprintf("and %d more", len(untrusted)-3))
				break
			}
			names = append(names, h.String())
		}
		res.Errors = append(res.Errors, HookError{
			Hook:    "hooks",
			Message: fmt.Sprintf("%d hook(s) were not run because they come from a source that is not trusted: %s", len(untrusted), strings.Join(names, "; ")),
		})
	}

	var results []hookResult
	if len(run) > 0 {
		payload, err := r.payload(event, ev)
		if err != nil {
			return res, err
		}
		results = make([]hookResult, len(run))
		sem := make(chan struct{}, r.maxParallel())
		var wg sync.WaitGroup
		for i, h := range run {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[i] = r.runOne(ctx, event, h, ev, payload)
			}()
		}
		wg.Wait()
	}

	f := folder{event: event, maxContext: r.maxContext()}
	for _, s := range slots {
		if s.idx < 0 {
			res.Runs = append(res.Runs, Run{Hook: s.hook.String(), Origin: s.hook.Origin, ExitCode: -1, Skipped: s.skipped})
			continue
		}
		f.add(&res, results[s.idx])
	}
	f.finish(&res)
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, nil
}

// hookDir is where a hook runs: the event's working directory when it exists,
// else the project directory.
func (r *Runner) hookDir(ev Event) string {
	if ev.Cwd != "" {
		if fi, err := os.Stat(ev.Cwd); err == nil && fi.IsDir() {
			return ev.Cwd
		}
	}
	return r.Dir
}

// runOne runs one hook and interprets its outcome. A panic anywhere in it (a
// bug in a custom HTTPClient, say) becomes an error of that hook: the goroutine
// runs on behalf of an agent and must not take every agent down with it.
func (r *Runner) runOne(ctx context.Context, event string, h Hook, ev Event, payload []byte) (hr hookResult) {
	defer func() {
		if p := recover(); p != nil {
			hr = hookResult{hook: h, run: Run{Hook: h.String(), Origin: h.Origin, ExitCode: -1}}
			hr.errs = []HookError{{Hook: h.String(), Message: fmt.Sprintf("internal error: %v", p)}}
		}
	}()
	timeout := r.timeoutFor(h)
	release, ok := r.acquire(ctx)
	if !ok {
		return r.interpret(event, h, outcome{exit: -1, canceled: true}, timeout)
	}
	defer release()
	var out outcome
	if h.Type == TypeHTTP {
		out = r.postHook(ctx, h, payload, timeout)
	} else {
		out = r.execCommand(ctx, h.Command, payload, r.hookDir(ev), r.hookEnv(event, ev), timeout)
	}
	return r.interpret(event, h, out, timeout)
}

// canBlock reports whether a failing hook has anything to block on this event.
func canBlock(event string) bool { return exit2Blocks(event) || hasDecision(event) }

// interpret turns a process outcome into the hook's answer.
func (r *Runner) interpret(event string, h Hook, out outcome, timeout time.Duration) hookResult {
	hr := hookResult{hook: h, run: Run{Hook: h.String(), Origin: h.Origin, Duration: out.dur, ExitCode: out.exit, TimedOut: out.timedOut}}
	// fail records a failure that does not block, unless the hook is fail-closed
	// and the event can be blocked.
	fail := func(msg string) {
		hr.errs = append(hr.errs, HookError{Hook: h.String(), Message: msg})
		if r.failClosed(h) && canBlock(event) {
			hr.block = true
			hr.blockReason = "a hook failed and is configured to fail closed: " + msg
			if hasDecision(event) {
				hr.decision = Deny
			}
		}
	}
	switch {
	case out.canceled:
		return hr
	case out.startErr != nil:
		fail("could not run: " + oneLine(cleanText([]byte(out.startErr.Error()))))
		return hr
	case out.timedOut:
		fail(fmt.Sprintf("timed out after %v and was stopped", timeout))
		return hr
	case out.overflow:
		fail("printed more than it may and was stopped")
		return hr
	}
	if out.strays {
		hr.errs = append(hr.errs, HookError{Hook: h.String(), Message: "left background processes attached to its output; they were stopped (redirect their output to keep them running)"})
	}
	stderr := capText(strings.TrimSpace(cleanText(out.stderr)), maxReason)
	detail := ""
	if stderr != "" {
		detail = ": " + oneLine(stderr)
	}
	switch {
	case out.exit == 0:
		p, isJSON, err := parseOutput(event, out.stdout)
		switch {
		case err != nil:
			fail(err.Error())
		case isJSON:
			for _, prob := range p.problems {
				hr.errs = append(hr.errs, HookError{Hook: h.String(), Message: "ignored a field of the output: " + prob})
			}
			hr.decision, hr.reason = p.decision, p.reason
			if p.decision == Deny || p.block {
				hr.block, hr.blockReason = true, p.reason
				if hr.blockReason == "" {
					hr.blockReason = "blocked by a hook, which gave no reason"
				}
			}
			hr.context, hr.updated = p.context, p.updated
			hr.stop, hr.stopReason, hr.message = p.stop, p.stopReason, p.message
		case stdoutIsContext(event):
			if text := strings.TrimSpace(cleanText(out.stdout)); text != "" {
				hr.context = text
			}
		}
	case out.exit == 2 && exit2Blocks(event):
		hr.block = true
		hr.blockReason = stderr
		if hr.blockReason == "" {
			hr.blockReason = "blocked by a hook, which gave no reason"
		}
		if hasDecision(event) {
			hr.decision = Deny
		}
	case out.exit == 2:
		hr.errs = append(hr.errs, HookError{Hook: h.String(), Message: "exited with status 2, which blocks nothing on " + event + detail})
	case out.exit > 128:
		fail(fmt.Sprintf("was killed by signal %d%s", out.exit-128, detail))
	case out.exit < 0:
		fail("did not report an exit status")
	default:
		fail(fmt.Sprintf("exited with status %d%s", out.exit, detail))
	}
	return hr
}

// gate decides whether a hook may run: hooks the user vouches for always do,
// others only when the Runner is trusted or Approve agrees. It returns why not,
// or "".
func (r *Runner) gate(ctx context.Context, h Hook) string {
	if r.Trusted || h.Origin == OriginUser {
		return ""
	}
	if r.Approve == nil {
		return "not trusted and no approval is configured"
	}
	key := h.Key()
	if v, ok := r.cached(key); ok {
		if v {
			return ""
		}
		return "the user did not approve this hook"
	}
	r.approveMu.Lock() // one question at a time, and no duplicate questions
	defer r.approveMu.Unlock()
	if v, ok := r.cached(key); ok {
		if v {
			return ""
		}
		return "the user did not approve this hook"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	yes := r.Approve(ctx, h)
	if ctx.Err() != nil {
		return "cancelled" // an answer given while cancelled is not remembered
	}
	r.mu.Lock()
	if r.approved == nil {
		r.approved = map[string]bool{}
	}
	r.approved[key] = yes
	r.mu.Unlock()
	if yes {
		return ""
	}
	return "the user did not approve this hook"
}

func (r *Runner) cached(key string) (approved, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	approved, ok = r.approved[key]
	return
}

// conditionHolds evaluates a hook's "if" condition, a permission rule such as
// "Bash(git commit:*)" or "Edit(src/**)". It reuses the permission engine's own
// matching, so the rule means exactly what it means in permission settings: an
// engine that has the condition as its only deny rule refuses the tool call when
// and only when the condition matches it.
//
// A condition that cannot be evaluated does not hold: a hook that was meant to
// apply to one command must not run for all of them, least of all a hook that
// answers a permission question.
func (r *Runner) conditionHolds(ctx context.Context, h Hook, ev Event) bool {
	if h.If == "" {
		return true
	}
	eng, err := r.condEngine(h.If)
	if err != nil {
		return false
	}
	d := eng.Check(ctx, r.condRequest(ev))
	// The engine also refuses what its built-in protections forbid (a recursive
	// delete of "/", a write under .git), whatever the rule says; only a refusal
	// that names the rule means the rule matched. A request the protections
	// refuse is not going to run, so a hook that is not consulted about it loses
	// nothing.
	return !d.Allow && strings.HasPrefix(d.Reason, "denied by rule ")
}

func (r *Runner) condEngine(cond string) (*perm.Engine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.engines[cond]; ok {
		if e == nil {
			return nil, fmt.Errorf("the condition %q cannot be evaluated", cond)
		}
		return e, nil
	}
	if r.engines == nil {
		r.engines = map[string]*perm.Engine{}
	}
	e, err := perm.NewEngine(perm.Config{Mode: perm.ModeBypass, Root: r.Dir, Deny: []string{cond}})
	if err != nil {
		r.engines[cond] = nil
		return nil, err
	}
	r.engines[cond] = e
	return e, nil
}

// condRequest describes the tool call to the permission engine the way a tool
// would: the shell command, the paths it touches, whether it writes.
func (r *Runner) condRequest(ev Event) perm.Request {
	req := perm.Request{Agent: ev.Agent, Role: ev.Role, Tool: ev.Tool, Input: ev.Input, Cwd: ev.Cwd, Summary: "hook condition"}
	if req.Cwd == "" {
		req.Cwd = r.Dir
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(ev.Input, &in) == nil {
		text := func(key string) string {
			var s string
			if raw, ok := in[key]; ok && json.Unmarshal(raw, &s) == nil {
				return s
			}
			return ""
		}
		req.Command = text("command")
		for _, key := range []string{"path", "file_path", "notebook_path"} {
			if p := text(key); p != "" {
				if !filepath.IsAbs(p) {
					p = filepath.Join(req.Cwd, p)
				}
				req.Paths = append(req.Paths, p)
			}
		}
	}
	switch normTool(ev.Tool) {
	case "write", "edit", "multiedit", "notebookedit", "applypatch", "patch":
		req.Writes = true
	}
	return req
}

// Prompter returns a perm.Prompter that consults PermissionRequest hooks before
// asking a human. The permission engine calls its prompter only for questions
// its rules leave open, never for what it denies outright, so a hook can answer
// a question on the user's behalf but cannot override a denial.
//
// A hook that denies, or blocks with status 2, refuses the request; a hook that
// allows it lets it through; anything else (no hook, no opinion, "ask", a failing
// hook) falls through to next, or, when next is nil, to a refusal, as the engine
// itself refuses when it has no prompter. A hook's answer is never remembered as
// a rule.
func (r *Runner) Prompter(next perm.Prompter) perm.Prompter {
	return func(ctx context.Context, req perm.Request) perm.Decision {
		extra := map[string]any{}
		if req.Summary != "" {
			extra["summary"] = req.Summary
		}
		if req.Command != "" {
			extra["command"] = req.Command
		}
		if len(req.Paths) > 0 {
			extra["paths"] = req.Paths
		}
		res, err := r.Run(ctx, Event{
			Name: PermissionRequest, Tool: req.Tool, Input: req.Input, Agent: req.Agent, Role: req.Role, Cwd: req.Cwd, Extra: extra,
		})
		if err == nil {
			switch {
			case res.Blocked || res.Decision == Deny:
				reason := res.Reason
				if reason == "" {
					reason = "no reason given"
				}
				return perm.Decision{Allow: false, Reason: "denied by a hook: " + reason}
			case res.Decision == Allow:
				return perm.Decision{Allow: true, Reason: "allowed by a hook"}
			}
		}
		if next == nil {
			return perm.Decision{Allow: false, Reason: "approval required and no one is available to give it"}
		}
		r.notify(req)
		return next(ctx, req)
	}
}

// notify tells Notification hooks that a person is about to be asked something,
// so a hook can pull them back to the terminal (a desktop notification, a
// sound). It runs beside the question, never in front of it: the prompt must not
// wait for a slow hook, and a hook cannot answer it (that is PermissionRequest's
// job).
func (r *Runner) notify(req perm.Request) {
	if r == nil || r.Set.Empty() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = r.Run(ctx, Event{
			Name: Notification, Agent: req.Agent, Role: req.Role, Cwd: req.Cwd,
			Extra: map[string]any{"notification_type": "permission_prompt", "message": "Sleipnir needs your permission: " + clip(req.Summary, 200)},
		})
	}()
}
