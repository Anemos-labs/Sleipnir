package perm

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config configures an Engine.
type Config struct {
	// Mode is the session's posture; empty means ModeDefault.
	Mode Mode
	// Root is the workspace: reads inside it are ordinary, and relative paths
	// in rules and commands are relative to it. Home is the user's home for
	// "~" and the credential protections; empty means the current user's.
	Root string
	Home string
	// StateDir is the harness's own directory when it is not ~/.sleipnir (the caller lists that one itself, with the other places that
	// steer a later session): what the person trusted, their settings and skills and the record of every session are in it. A write
	// there asks, in every mode, as a write to ~/.sleipnir does; an unattended session refuses it.
	StateDir string
	// Tmp is the session's private scratch directory: a workspace root, and what $TMPDIR and ${TMPDIR} stand for in a command
	// (the shell tool sets TMPDIR to it).
	Tmp string
	// ExtraRoots are further directories treated as part of the workspace
	// (per-agent git worktrees, scratch directories).
	ExtraRoots []string
	// TreeParents are directories whose immediate subdirectories are per-agent trees
	// (git worktrees) of the project. They are workspace roots, and every rule that is
	// relative to the workspace (Edit(./.sleipnir/**), Deny(Read(./secrets/**)),
	// Allow(Edit(src/**))) applies inside each of those trees exactly as it does inside
	// Root: an agent that works in its own copy of the project is held to the project's
	// rules, not to none. (Its work reaches the person's checkout by a merge, and a rule
	// that only guarded the checkout would guard nothing.)
	TreeParents []string

	// Allow, Ask and Deny are rule strings in the form ParseRule accepts.
	Allow, Ask, Deny []string

	// Roles maps Request.Role to a profile that can only tighten the session.
	Roles map[string]RoleProfile

	// Prompter is asked when a request needs a human decision. With no
	// Prompter such requests are denied ("approval required").
	Prompter Prompter
	// AskTimeout, when positive, is how long a question may wait for its answer, counted from the moment the Prompter is given it (a
	// question that waited its turn behind another is not charged for that). One that nobody answers is refused, by "no one" as when
	// there is no Prompter, and the refusal says so: a worker held at a question for hours, with the person away, is held for nothing.
	// Zero waits for ever, which is right where the person is at the keys.
	AskTimeout time.Duration
	// Persist is called (outside any engine lock) when a project-scope rule is
	// added, so the caller can write it to the project's settings.
	Persist func(Scope, Rule)
	// Audit hears of every question the engine has to put and every refusal, for the event log: what was asked and why,
	// and what came of it. Plain allows are not reported (there is one per tool call). It is called outside any engine
	// lock and must not block.
	Audit func(Audit)
}

// Audit is one step of a request that was not simply allowed.
type Audit struct {
	// Kind is "ask" when the engine is about to put a question (Decision is empty), and "decide" for its outcome, or for
	// a refusal that needed no question.
	Kind    string
	Request Request
	// Reason is why it was asked, or why it was decided.
	Reason   string
	Decision Decision
	// By says who decided: "user" (the prompter), "no one" (there was nobody to ask: a run, a swarm, a rollout), "policy" (a
	// deny rule, a built-in protection or the mode) or "canceled" (the request was cancelled while it waited).
	By string
}

func (e *Engine) audit(a Audit) {
	if e.cfg.Audit != nil {
		e.cfg.Audit(a)
	}
}

// RoleProfile overlays a stricter posture for one role. The role is judged
// twice, under the session and under the overlay, and gets the more severe
// outcome; a profile can therefore add denials and questions and choose a
// stricter mode, but never grant what the session would not.
//
// The overlay inherits the session's rules. If Mode is stricter than the
// session's, the session's Allow rules are left out (the role is read-only,
// say, whatever the session has allowed) and only the profile's own Allow rules
// carve exceptions, for example letting a plan-mode reviewer run "go test" -
// still never beyond what the session itself permits.
type RoleProfile struct {
	Mode             Mode
	Allow, Ask, Deny []string
}

type profile struct {
	mode             Mode
	allow, ask, deny []*crule
}

// Engine decides permission requests. It is safe for concurrent use.
type Engine struct {
	cfg Config
	rs  *resolver
	pr  prompts

	mu               sync.RWMutex
	mode             Mode
	allow, ask, deny []*crule
	roles            map[string]*profile
	persisted        map[Rule]bool // rules already handed to Config.Persist
	granted          []string      // the allow rules added while the session ran (what the person said yes to), in order
	confined         map[string]rootPair
}

// Confine binds an agent to one directory of the workspace (a git worktree of its
// own): from now on whatever it reads or writes inside a workspace root (the shared
// checkout, the roots of other agents' trees) must lie inside dir, whatever the mode
// and the rules say. The check is on resolved paths, so a link inside dir that leads
// into another tree is another tree, and it covers shell commands as well as the file
// tools, because it sits where every access of a request is judged. Paths outside all
// workspace roots (the system, the home directory) are not affected: the ordinary
// rules keep deciding those. A path that cannot be resolved statically is left to the
// mode, as everywhere else.
func (e *Engine) Confine(agent, dir string) {
	if agent == "" || dir == "" {
		return
	}
	l := cleanAbs(dir)
	rp := rootPair{lex: l, real: realPath(l)}
	e.mu.Lock()
	if e.confined == nil {
		e.confined = map[string]rootPair{}
	}
	e.confined[agent] = rp
	e.mu.Unlock()
}

// Unconfine lifts Confine (the agent's tree is gone).
func (e *Engine) Unconfine(agent string) {
	e.mu.Lock()
	delete(e.confined, agent)
	e.mu.Unlock()
}

func (e *Engine) confinement(agent string) (rootPair, bool) {
	if agent == "" {
		return rootPair{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	rp, ok := e.confined[agent]
	return rp, ok
}

var _ Requester = (*Engine)(nil)

// modeRank orders modes by permissiveness.
func modeRank(m Mode) int {
	switch m {
	case ModePlan:
		return 0
	case ModeDefault:
		return 1
	case ModeAcceptEdits:
		return 2
	case ModeBypass:
		return 3
	}
	return -1
}

func validMode(m Mode) bool { return modeRank(m) >= 0 }

func stricter(a, b Mode) Mode {
	if b == "" || modeRank(b) < 0 {
		return a
	}
	if modeRank(b) < modeRank(a) {
		return b
	}
	return a
}

func compileList(action Action, list []string, rs *resolver) ([]*crule, error) {
	out := make([]*crule, 0, len(list))
	for _, s := range list {
		r, err := ParseRule(action, s)
		if err != nil {
			return nil, err
		}
		c, err := compileRule(r, rs)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// NewEngine validates cfg and returns an engine. Every rule and role profile is
// parsed up front, so a typo in a rule fails at start-up rather than silently
// not matching.
func NewEngine(cfg Config) (*Engine, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeDefault
	}
	if !validMode(mode) {
		return nil, fmt.Errorf("perm: unknown mode %q", cfg.Mode)
	}
	home := cfg.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	rs := newResolver(home, cfg.Root, append(append([]string(nil), cfg.ExtraRoots...), cfg.TreeParents...))
	if cfg.Tmp != "" {
		rs.tmp = cleanAbs(cfg.Tmp)
		rs.roots = append(rs.roots, rootPair{lex: rs.tmp, real: realPath(rs.tmp)})
	}
	rs.addTreeParents(cfg.TreeParents)
	e := &Engine{cfg: cfg, rs: rs, mode: mode, roles: map[string]*profile{}, persisted: map[Rule]bool{}}
	var err error
	if e.allow, err = compileList(Allow, cfg.Allow, rs); err != nil {
		return nil, err
	}
	ask := cfg.Ask
	if cfg.StateDir != "" {
		ask = append(append([]string(nil), ask...), "Edit("+escapeGlob(cleanAbs(cfg.StateDir))+"/**)")
	}
	if e.ask, err = compileList(Ask, ask, rs); err != nil {
		return nil, err
	}
	if e.deny, err = compileList(Deny, cfg.Deny, rs); err != nil {
		return nil, err
	}
	for name, rp := range cfg.Roles {
		if rp.Mode != "" && !validMode(rp.Mode) {
			return nil, fmt.Errorf("perm: role %q: unknown mode %q", name, rp.Mode)
		}
		p := &profile{mode: rp.Mode}
		if p.allow, err = compileList(Allow, rp.Allow, rs); err != nil {
			return nil, fmt.Errorf("role %q: %w", name, err)
		}
		if p.ask, err = compileList(Ask, rp.Ask, rs); err != nil {
			return nil, fmt.Errorf("role %q: %w", name, err)
		}
		if p.deny, err = compileList(Deny, rp.Deny, rs); err != nil {
			return nil, fmt.Errorf("role %q: %w", name, err)
		}
		e.roles[name] = p
	}
	e.pr.init()
	return e, nil
}

// Mode returns the session mode.
func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// SetMode changes the session mode. An unknown mode is ignored.
func (e *Engine) SetMode(m Mode) {
	if !validMode(m) {
		return
	}
	e.mu.Lock()
	e.mode = m
	e.mu.Unlock()
}

// AddRule adds a rule for the rest of the session (ScopeSession) or, for
// ScopeProject, also hands it to Config.Persist (once per distinct rule, outside
// the engine's locks). ScopeOnce adds nothing. A rule that does not compile is
// ignored.
func (e *Engine) AddRule(scope Scope, rule Rule) {
	if scope == ScopeOnce {
		return
	}
	c, err := compileRule(rule, e.rs)
	if err != nil {
		return
	}
	e.mu.Lock()
	var list *[]*crule
	switch rule.Action {
	case Allow:
		list = &e.allow
	case Ask:
		list = &e.ask
	case Deny:
		list = &e.deny
	default:
		e.mu.Unlock()
		return
	}
	dup := false
	for _, x := range *list {
		dup = dup || x.Rule == rule
	}
	if !dup {
		// Copy on write: evaluations in flight keep reading the old slice.
		*list = append(append(make([]*crule, 0, len(*list)+1), *list...), c)
		if rule.Action == Allow {
			e.granted = append(e.granted, rule.String())
		}
	}
	// A rule is persisted once, even if it was first added for the session and
	// is promoted to the project later; repeats would only duplicate settings.
	persist := scope == ScopeProject && e.cfg.Persist != nil && !e.persisted[rule]
	if persist {
		e.persisted[rule] = true
	}
	e.mu.Unlock()
	if persist {
		e.cfg.Persist(scope, rule)
	}
}

// snapshot captures the session view and, for a configured role, the role's
// stricter overlay.
func (e *Engine) snapshot(role string) (sess, prof *view) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	sess = &view{mode: e.mode, deny: e.deny, ask: e.ask, allow: e.allow}
	if p, ok := e.roles[role]; ok && role != "" {
		cat := func(a, b []*crule) []*crule {
			return append(append(make([]*crule, 0, len(a)+len(b)), a...), b...)
		}
		prof = &view{
			mode: stricter(e.mode, p.mode), role: role,
			deny: cat(e.deny, p.deny), ask: cat(e.ask, p.ask), allow: cat(e.allow, p.allow),
		}
		if modeRank(prof.mode) < modeRank(e.mode) {
			// A role that takes a stricter posture is judged from scratch under
			// it: the session's allow rules do not carve exceptions out of the
			// role's mode, only the role's own Allow rules do.
			prof.allow = append([]*crule(nil), p.allow...)
		}
	}
	return sess, prof
}

// Check implements Requester.
func (e *Engine) Check(ctx context.Context, r Request) Decision {
	judge := func() verdict {
		sess, prof := e.snapshot(r.Role)
		v := e.evaluate(sess, r)
		if prof != nil {
			if pv := e.evaluate(prof, r); pv.kind > v.kind {
				pv.reason = "role " + r.Role + ": " + pv.reason
				v = pv
			}
		}
		return v
	}
	v := judge()
	switch v.kind {
	case vAllow:
		return Decision{Allow: true, Reason: v.reason}
	case vDeny:
		d := Decision{Reason: v.reason}
		e.audit(Audit{Kind: "decide", Request: r, Reason: v.reason, Decision: d, By: "policy"})
		return d
	}
	e.audit(Audit{Kind: "ask", Request: r, Reason: v.reason})
	d := e.resolveAsk(ctx, r, v, judge)
	by := "user"
	switch {
	case strings.HasPrefix(d.Reason, settledEarlier):
		by = "policy"
	case e.cfg.Prompter == nil, !d.Allow && strings.Contains(d.Reason, askTimedOut):
		by = "no one"
	case !d.Allow && strings.HasPrefix(d.Reason, "approval canceled"):
		by = "canceled"
	}
	e.audit(Audit{Kind: "decide", Request: r, Reason: d.Reason, Decision: d, By: by})
	return d
}

// Granted are the allow rules added while the session ran (/allow, and "don't ask again"), in the order they were given: what a session that
// starts again in its place has to be told, or the person is asked what they have already answered.
func (e *Engine) Granted() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.granted...)
}

// Rules returns the active rules of one action as text, sorted; it is for
// diagnostics and tests.
func (e *Engine) Rules(a Action) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var src []*crule
	switch a {
	case Allow:
		src = e.allow
	case Ask:
		src = e.ask
	case Deny:
		src = e.deny
	}
	out := make([]string, len(src))
	for i, c := range src {
		out[i] = c.Rule.String()
	}
	sort.Strings(out)
	return out
}
