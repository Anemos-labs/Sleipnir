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
)

// prompts coordinates the questions put to a human. Two guarantees matter for
// a swarm of agents sharing one screen:
//
//   - at most one prompt is on screen at a time (calls to the Prompter are
//     serialised, and a waiting caller can still be cancelled through its ctx);
//   - identical requests that arrive while one is pending are answered once:
//     they wait for the first call and share its Decision instead of asking
//     again.
type prompts struct {
	sem chan struct{}

	mu       sync.Mutex
	inflight map[string]*pending
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
}

// promptKey identifies "the same request" for coalescing: same tool, command,
// directory, paths and effects. The asking agent is deliberately not part of it.
func promptKey(r Request) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\x00%t\x00", r.Tool, r.Command, r.Cwd, r.Writes, r.Network)
	paths := append([]string(nil), r.Paths...)
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(h, "p=%s\x00", p)
	}
	if r.Command == "" && len(r.Paths) == 0 {
		h.Write(r.Input) // e.g. the URL of a fetch
	}
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

// askTimedOut and noAnswerInTime frame the refusal of a question that nobody answered in the time it was given. The sentences are fixed,
// so they cost the cache nothing, and they say what the model can do: the person is not there.
const (
	askTimedOut    = " (nobody answered within "
	noAnswerInTime = ", so nothing was approved: use an action that is allowed, or finish and say which permission you needed)"
)

// settledEarlier begins the reason of a question that an answer given while it waited decided, so that it is not asked.
const settledEarlier = "settled by an earlier answer: "

// declinedAdvice ends the refusal that a person gave. A model told only "denied by user" made the same change a moment later with
// another tool (the first real chat session: an edit that was refused came back as an apply_patch, and the person was asked a
// second time). The sentence is fixed, so it costs the cache nothing, and it is said only of a refusal by a person: not of a
// question that nobody answered, and not of a policy.
const declinedAdvice = " (the person said no to this: do not make it another way, with another tool or command; say what you wanted and ask what they want instead)"

// resolveAsk turns an "ask" outcome into a Decision by consulting the human.
func (e *Engine) resolveAsk(ctx context.Context, r Request, v verdict, again func() verdict) Decision {
	if e.cfg.Prompter == nil {
		return Decision{Reason: "approval required: " + v.reason + NoOneToAsk}
	}
	key := promptKey(r)
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
		return e.lead(ctx, key, p, r, v, again)
	}
}

// lead runs the prompt on behalf of every caller waiting on p.
func (e *Engine) lead(ctx context.Context, key string, p *pending, r Request, v verdict, again func() verdict) (d Decision) {
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
	if nv := again(); nv.kind != vAsk {
		p.canceled = false
		if nv.kind == vAllow {
			return Decision{Allow: true, Reason: settledEarlier + nv.reason}
		}
		return Decision{Reason: settledEarlier + nv.reason}
	}
	// The human sees Summary and nothing else; tell them why they are asked.
	shown := r
	shown.Summary = withWhy(r.Summary, v.reason)
	shown.Why = strings.TrimSpace(v.reason)
	if !v.askRule && len(v.rem) > 0 {
		shown.Remembers = e.rememberPhrase(v.rem)
		shown.OffersTests = e.offersTests(v.rem)
		shown.RememberRules = e.rememberRules(v.rem)
	}
	pctx := ctx
	if e.cfg.AskTimeout > 0 {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, e.cfg.AskTimeout)
		defer cancel()
	}
	d = e.cfg.Prompter(pctx, shown)
	if ctx.Err() != nil {
		return canceledDecision(ctx)
	}
	if pctx.Err() != nil && !d.Allow && d.Reason == "no answer" {
		// the time ran out and the prompter gave up on it (an answer that arrived as it did stands: it is not "no answer")
		p.canceled = false
		return Decision{Reason: "approval required: " + v.reason + askTimedOut + e.cfg.AskTimeout.String() + noAnswerInTime}
	}
	p.canceled = false
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
	var parts []string
	for _, r := range rem {
		if _, phrase := e.widen(r); phrase != "" {
			parts = append(parts, phrase)
			continue
		}
		parts = append(parts, `"`+clipRunes(r.Pattern, 50)+`"`)
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
