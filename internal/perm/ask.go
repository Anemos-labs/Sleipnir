package perm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// prompts coordinates the questions put to a human. Two guarantees matter for
// a swarm of agents sharing one screen:
//
//   - at most one prompt is on screen at a time (calls to the Prompter are
//     serialised, and a waiting caller can still be cancelled through its ctx);
//   - identical requests that arrive while one is pending are answered once:
//     they wait for the first call and share its Decision instead of asking
//     again;
//   - a question the person refused, or that nobody answered, is not asked
//     again in the session: a repeat is answered at once with that outcome
//     (refused), until the person changes the mode or the rules (forget).
type prompts struct {
	sem chan struct{}

	mu       sync.Mutex
	inflight map[string]*pending
	refused  map[string]Decision // by settledKey: the refusals and the unanswered questions of the session
	gen      uint64              // bumped by forget: a question put before a reset does not record its refusal after it
}

type pending struct {
	done     chan struct{}
	d        Decision
	canceled bool  // the asking caller gave up; waiters must ask again themselves
	waiters  int32 // callers coalesced onto this prompt (observed by tests)
}

// init allocates the single-prompt semaphore and the in-flight prompt index.
func (p *prompts) init() {
	p.sem = make(chan struct{}, 1)
	p.inflight = map[string]*pending{}
	p.refused = map[string]Decision{}
}

// earlier returns the outcome of a question refused or left unanswered earlier in the session under key, prefixed with
// askedEarlier; ok is false when there is none.
func (p *prompts) earlier(key string) (d Decision, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d, ok = p.refused[key]; ok {
		d.Reason = askedEarlier + d.Reason
	}
	return d, ok
}

// generation is the current reset generation (forget), taken when a question is put to the person.
func (p *prompts) generation() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gen
}

// refuse records d, a refusal by the person or a question nobody answered, as the answer to every later question under key. gen is
// the generation the question was put under: a question the mode or the rules changed under while it was on screen records nothing,
// as it was answered for what no longer holds.
func (p *prompts) refuse(key string, d Decision, gen uint64) {
	if d.Allow {
		return
	}
	p.mu.Lock()
	if gen == p.gen {
		p.refused[key] = Decision{Reason: d.Reason}
	}
	p.mu.Unlock()
}

// forget drops the recorded refusals, so that each of those questions is put to the person again: the person changed the mode or
// the rules, and the answer may now be another.
func (p *prompts) forget() {
	p.mu.Lock()
	clear(p.refused)
	p.gen++
	p.mu.Unlock()
}

// settledKey identifies the questions that one refusal answers: a question only about network access to a host (verdict.host) is
// the same question for every URL of that host, as the person refused the host, and any other is promptKey's request.
func settledKey(r Request, v verdict) string {
	if v.host != "" {
		return "host\x00" + v.host
	}
	return promptKey(r)
}

// promptKey identifies "the same request" for coalescing; the asking agent is
// deliberately not part of it. A shell command is the same request when the tool,
// command, directory, paths and effects are: its description or other input does not
// change what runs. Any other request is the same only when it is byte-identical (tool,
// directory, paths, effects, summary and input): two edits of one path with different
// content are two questions, as the person answered for one content, not for another.
func promptKey(r Request) string {
	h := sha256.New()
	paths := append([]string(nil), r.Paths...)
	sort.Strings(paths)
	if r.Command != "" {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\x00%t\x00", r.Tool, r.Command, r.Cwd, r.Writes, r.Network)
		for _, p := range paths {
			fmt.Fprintf(h, "p=%s\x00", p)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	fmt.Fprintf(h, "%d:%s\x00%d:%s\x00%t\x00%t\x00%d:%s\x00", len(r.Tool), r.Tool, len(r.Cwd), r.Cwd,
		r.Writes, r.Network, len(r.Summary), r.Summary)
	for _, p := range paths {
		fmt.Fprintf(h, "p%d:%s\x00", len(p), p)
	}
	in := sha256.Sum256(r.Input)
	h.Write(in[:])
	return hex.EncodeToString(h.Sum(nil))
}

// withWhy appends the reason a question is being asked to the request's
// one-line summary, clipped so a prompt stays a prompt.
func withWhy(summary, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return summary
	}
	if len(reason) > 300 {
		reason = reason[:297] + "..."
	}
	if strings.TrimSpace(summary) == "" {
		return reason
	}
	return summary + " [" + reason + "]"
}

// canceledDecision creates a denied approval result; the supplied context must already be
// canceled.
func canceledDecision(ctx context.Context) Decision {
	return Decision{Reason: "approval canceled: " + ctx.Err().Error()}
}

// NoOneToAsk ends the refusal of a run that has nobody to ask (run, swarm, a rollout). A model
// told only "approval required" spends its steps on other ways to the same action: a first run
// against a real model made twenty-four tool calls of that kind (a script, another command, a
// program that runs the tests) before it gave up. The text is fixed, so it costs the cache nothing.
const NoOneToAsk = " (" + noOneToAskPhrase + ", so nothing can be approved: use an action that is allowed, or finish and say which permission you needed)"

// noOneToAskPhrase is how that refusal says it; IsNoOneToAsk tells it from any other, for whoever counts them.
const noOneToAskPhrase = "this run has no one to ask"

// IsNoOneToAsk says whether text, a tool's refusal, is that of a run with nobody to ask.
func IsNoOneToAsk(text string) bool { return strings.Contains(text, noOneToAskPhrase) }

// IsPolicyRefusal says whether text, a tool's refusal, is one a fixed policy made: an agent's confinement to its own tree, a built-in
// protection, a deny rule, or plan mode and the read-only profile of a role (a reviewer, a scout), which refuse and never ask. No approval
// changes such a refusal, and neither does another spelling of the same access ($HOME for ~, a cd first, a script), so whoever counts
// refusals may count these whether or not the run has someone to ask.
func IsPolicyRefusal(text string) bool {
	if !strings.Contains(text, "permission denied") {
		return false
	}
	for _, s := range []string{"isolation: ", "built-in protection: ", "denied by rule ", planModeRefusal, readOnlyRoleRefusal} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// planModeRefusal and readOnlyRoleRefusal are how the refusals of plan mode (planReason) and of a read-only role (the swarm's wording of
// it) say so.
const (
	planModeRefusal     = "plan mode (read-only): "
	readOnlyRoleRefusal = " role is read-only: "
)

// askTimedOut and noAnswerInTime frame the refusal of a question that nobody answered in the time it was given, and unanswered that
// of a question the prompter gave up on without an answer (no page had the interface open, the input ended). The sentences are
// fixed, so they cost the cache nothing, and they say what the model can do: the person is not there, and asking again is refused
// the same way. A manager told only "no answer" asked for the same network probe eight times in eleven minutes.
const (
	askTimedOut    = " (nobody answered within "
	noAnswerInTime = ", so nothing was approved: " + noAnswerAdvice
	unanswered     = " (no answer, so nothing was approved: " + noAnswerAdvice
	noAnswerAdvice = "do not ask for it again; use an action that is allowed, or finish and say which permission you needed)"
)

// settledEarlier begins the reason of a question that an answer given while it waited decided, so that it is not asked.
const settledEarlier = "settled by an earlier answer: "

// askedEarlier begins the reason of a question that was refused, or that nobody answered, earlier in the session (settledKey): it is
// not put to the person again, and the earlier refusal, fixed text, follows. A scout that nobody answered asked about the same three
// hosts twice each in a few minutes.
const askedEarlier = "asked earlier in this session: "

// declinedAdvice ends the refusal that a person gave. A model told only "denied by user" made the same change a moment later with
// another tool (the first real chat session: an edit that was refused came back as an apply_patch, and the person was asked a
// second time). The sentence is fixed, so it costs the cache nothing, and it is said only of a refusal by a person: not of a
// question that nobody answered, and not of a policy.
const declinedAdvice = " (the person said no to this: do not make it another way, with another tool or command; say what you wanted and ask what they want instead)"

// resolveAsk turns an "ask" outcome into a Decision by consulting the human. Check has already answered a question refused earlier
// (prompts.earlier); one refused while this one waited is answered in lead.
func (e *Engine) resolveAsk(ctx context.Context, r Request, v verdict, again func() verdict) Decision {
	if e.cfg.Prompter == nil {
		return Decision{Reason: "approval required: " + v.reason + NoOneToAsk}
	}
	skey := settledKey(r, v)
	// Waiters share the leader's answer, so they share a question only when the
	// same answerers may settle it: an ask rule, or a stricter danger, in one
	// waiter's policy keeps the fallback from answering for it.
	key := fmt.Sprintf("%s\x00%s\x00%t\x00%t", promptKey(r), v.danger(r), v.askRule, e.fallbackFor(r, v))
	for {
		if ctx.Err() != nil {
			return canceledDecision(ctx)
		}
		e.pr.mu.Lock()
		if p, ok := e.pr.inflight[key]; ok {
			atomic.AddInt32(&p.waiters, 1)
			e.pr.mu.Unlock()
			select {
			case <-p.done:
				if p.canceled {
					continue // the first asker gave up; take over
				}
				return p.d
			case <-ctx.Done():
				return canceledDecision(ctx)
			}
		}
		p := &pending{done: make(chan struct{})}
		e.pr.inflight[key] = p
		e.pr.mu.Unlock()
		return e.lead(ctx, key, skey, p, r, v, again)
	}
}

// lead runs the prompt on behalf of every caller waiting on p. key is the request's promptKey, skey its settledKey: a refusal or a
// question nobody answered is recorded under skey.
func (e *Engine) lead(ctx context.Context, key, skey string, p *pending, r Request, v verdict, again func() verdict) (d Decision) {
	p.canceled = true // stays true if the prompter panics
	defer func() {
		p.d = d
		e.pr.mu.Lock()
		delete(e.pr.inflight, key)
		e.pr.mu.Unlock()
		close(p.done)
	}()
	select {
	case e.pr.sem <- struct{}{}:
	case <-ctx.Done():
		return canceledDecision(ctx)
	}
	defer func() { <-e.pr.sem }()

	// The questions are asked one at a time, and the person may have answered one of them "and don't ask again" while this one waited
	// (a team of eight asks several at once): the rules may settle it now.
	nv := again()
	if nv.kind != vAsk {
		p.canceled = false
		if nv.kind == vAllow {
			return Decision{Allow: true, Reason: settledEarlier + nv.reason}
		}
		return Decision{Reason: settledEarlier + nv.reason}
	}
	// Still a question, but of the policy as it is now: an ask rule added while it
	// waited decides who may answer it.
	v = nv
	// Or the person refused, or did not answer, a question it is the same as (another URL of one host) while it waited.
	if ed, ok := e.pr.earlier(skey); ok {
		p.canceled = false
		return ed
	}
	// The human sees Summary and nothing else; tell them why they are asked.
	shown := r
	shown.Summary = withWhy(r.Summary, v.reason)
	shown.Why = strings.TrimSpace(v.reason)
	shown.PackageIndex = v.index
	// "Don't ask again" is offered only when the rules it adds would let this very request through: a part that is only known when
	// it runs, or that another rule or a stricter role keeps asking about, would make it a promise the next question breaks.
	if v.askRule || len(v.rem) == 0 || !e.settles(r, v.rem) {
		v.rem = nil
	}
	if len(v.rem) > 0 {
		shown.Remembers = e.rememberPhrase(v.rem)
		shown.RememberRules = e.rememberRules(v.rem)
	}
	shown.OffersTests = !v.askRule && (v.tests || e.offersTests(v.rem))
	gen := e.pr.generation()
	pctx := ctx
	if e.cfg.AskTimeout > 0 {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, e.cfg.AskTimeout)
		defer cancel()
	}
	var fell chan Decision // the fallback's answer, when it settled the question before the person did
	if e.fallbackFor(r, v) {
		var withdrawCause context.CancelCauseFunc
		pctx, withdrawCause = context.WithCancelCause(pctx)
		withdraw := func() { withdrawCause(nil) }
		defer withdraw()
		fell = make(chan Decision, 1)
		go func() {
			t := time.NewTimer(e.cfg.FallbackAfter)
			defer t.Stop()
			select {
			case <-t.C:
			case <-pctx.Done():
				return
			}
			asked := shown
			asked.Remembers, asked.RememberRules, asked.OffersTests = "", nil, false
			if fd, by := e.cfg.Fallback(pctx, asked); by != "" && pctx.Err() == nil {
				fd.by, fd.Remember, fd.Preset = by, "", ""
				fell <- fd
				withdrawCause(&Settled{Allow: fd.Allow, By: by}) // the person's question is answered: take it off their screen
			}
		}()
	}
	d = e.cfg.Prompter(pctx, shown)
	if ctx.Err() != nil {
		return canceledDecision(ctx)
	}
	if !d.Allow && d.Reason == "no answer" { // the person did not answer: the fallback may have, and withdrawn the question
		select {
		case fd := <-fell: // nil when there is no fallback: never ready
			p.canceled = false
			if !fd.Allow {
				e.pr.refuse(skey, fd, gen)
			}
			return fd
		default:
		}
	}
	if pctx.Err() != nil && !d.Allow && d.Reason == "no answer" {
		// the time ran out and the prompter gave up on it (an answer that arrived as it did stands: it is not "no answer")
		p.canceled = false
		d = Decision{Reason: "approval required: " + v.reason + askTimedOut + e.cfg.AskTimeout.String() + noAnswerInTime}
		e.pr.refuse(skey, d, gen)
		return d
	}
	p.canceled = false
	if !d.Allow && d.Reason == "no answer" {
		d = Decision{Reason: "approval required: " + v.reason + unanswered}
		e.pr.refuse(skey, d, gen)
		return d
	}
	if d.Reason == "" {
		if d.Allow {
			d.Reason = "approved by the user"
		} else {
			d.Reason = "declined by the user"
		}
	}
	if !d.Allow && (d.Reason == "denied by user" || d.Reason == "declined by the user") {
		d.Reason += declinedAdvice
	}
	e.pr.refuse(skey, d, gen)
	e.remember(d, v)
	return d
}

// remember honours Decision.Remember by adding the rules that make a repeat of
// the request pass (or, for a refusal, fail). An answer to a question that a
// user ask rule caused is not remembered: the rule would ask again anyway.
func (e *Engine) remember(d Decision, v verdict) {
	if d.Allow && d.Preset != "" { // "allow builds and tests for this session": one yes for every worker's go test, go build and the like
		e.addPreset(d.Preset)
	}
	if d.Remember == ScopeOnce || v.askRule {
		return
	}
	for _, rule := range v.rem {
		if !d.Allow {
			rule.Action = Deny
		} else if d.Remember == ScopeSession {
			// A no, and anything kept beyond the session, stay exact; a yes for the session may be wider (see widen).
			rule, _ = e.widen(rule)
		}
		origin := OriginRemembered
		if d.Remember == ScopeProject {
			origin = OriginProject
		}
		e.addRule(d.Remember, rule, origin)
	}
}

// settles reports whether the request would be allowed without a question once the rules of a "don't ask again" answer (rem, widened
// as that answer widens them) are added for the session.
func (e *Engine) settles(r Request, rem []Rule) bool {
	extra := make([]*crule, 0, len(rem))
	for _, rule := range rem {
		w, _ := e.widen(rule)
		w.Action = Allow
		c, err := compileRule(w, e.rs)
		if err != nil {
			return false
		}
		extra = append(extra, c)
	}
	sess, prof := e.snapshotWith(r.Role, extra)
	if e.evaluate(sess, r).kind != vAllow {
		return false
	}
	return prof == nil || e.evaluate(prof, r).kind == vAllow
}

// rememberRules are the rules a yes "and don't ask again" for the session would add
// (remember, with the widening of a session answer), as text.
func (e *Engine) rememberRules(rem []Rule) []string {
	out := make([]string, 0, len(rem))
	for _, r := range rem {
		w, _ := e.widen(r)
		w.Action = Allow
		if s := w.String(); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// rememberPhrase says what a yes for the rest of the session would remember, in the words of the dialog's option: for one command the wider
// phrase when there is one ("go test" commands), and for a line of several commands each of them, since a line of "npm test; rm -rf /tmp/x" that
// is allowed remembers both, and "npm test commands" alone would hide the second.
func (e *Engine) rememberPhrase(rem []Rule) string {
	if len(rem) == 1 {
		_, phrase := e.widen(rem[0])
		return phrase
	}
	// Several rules can widen to the same phrase (make -C a test && make -C b build are both "make" commands): it is said once.
	var parts []string
	for _, r := range rem {
		phrase := `"` + clipRunes(r.Pattern, 50) + `"`
		if _, p := e.widen(r); p != "" {
			phrase = p
		}
		if !slices.Contains(parts, phrase) {
			parts = append(parts, phrase)
		}
	}
	return strings.Join(parts, " and ")
}

// offersTests says whether a question is about a build or test command that the tests preset covers, so that the dialog may offer to allow them
// all for the session.
func (e *Engine) offersTests(rem []Rule) bool {
	for _, r := range rem {
		if w, _ := e.widen(r); w.Tool == "Bash" && slices.Contains(TestsAllow, w.String()) {
			return true
		}
	}
	return false
}

// clipRunes caps a string at n runes including an ellipsis when truncated; n must be positive.
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// BypassRefusal opens the refusal of what bypass mode would otherwise have asked about; bypassAdvice ends it.
const (
	BypassRefusal = "bypass mode asks nothing, so this is refused: it was flagged because "
	bypassAdvice  = " (do what you need a safer way: a narrower command, a path inside the workspace, no sudo or forced push; or finish and say which permission you needed)"
)

// bypassRefusal is the refusal of a question bypass mode does not ask, flagged for why.
func bypassRefusal(why string) string { return BypassRefusal + why + bypassAdvice }

// Danger is how far Config.Fallback may answer for the person: the most dangerous kind of question it settles.
type Danger string

// The kinds of question, from the least dangerous.
const (
	// DangerLow is a routine question (routine.go): inside the workspace, nothing guarded, no network but this machine.
	DangerLow Danger = "low"
	// DangerMedium is any question but the high-risk class, a guarded path and an ask rule of the person's: the network, a file
	// outside the workspace, a command that cannot be checked beforehand.
	DangerMedium Danger = "medium"
	// DangerHigh is any question: the high-risk class, guarded paths and ask rules too.
	DangerHigh Danger = "high"
)

// ParseDanger reads a Danger; "" is no fallback.
func ParseDanger(s string) (Danger, error) {
	switch d := Danger(strings.ToLower(strings.TrimSpace(s))); d {
	case "", DangerLow, DangerMedium, DangerHigh:
		return d, nil
	}
	return "", fmt.Errorf("unknown danger level %q (low, medium or high)", s)
}

// danger is the kind of question a verdict that asks is.
func (v verdict) danger(r Request) Danger {
	switch {
	case v.routine:
		return DangerLow
	case v.askRule || v.tier == TierGuarded || v.tier == TierHard || r.Risk == RiskHigh:
		return DangerHigh
	}
	return DangerMedium
}

// fallbackFor says whether Config.Fallback may answer a question of verdict v.
func (e *Engine) fallbackFor(r Request, v verdict) bool {
	if e.cfg.Fallback == nil || e.cfg.FallbackAfter <= 0 {
		return false
	}
	rank := map[Danger]int{DangerLow: 1, DangerMedium: 2, DangerHigh: 3}
	return rank[v.danger(r)] <= rank[e.cfg.FallbackUpTo]
}

// Settled is the cause of the end of a question the person was asked that Config.Fallback answered first (context.Cause of the
// Prompter's context): the prompter shows who answered it, and how, instead of a cancellation.
type Settled struct {
	Allow bool
	By    string
}

func (s *Settled) Error() string { return "answered by the " + s.By }
