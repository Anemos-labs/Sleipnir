package perm

import (
	"strings"
)

// Origins of rules and protections, as Classify reports them.
const (
	// OriginBuiltIn is a protection of the engine itself (credentials, .env files,
	// the state directory, high-risk commands).
	OriginBuiltIn = "built-in protection"
	// OriginConfig is a rule of the configuration the engine was built with
	// (Config.RuleOrigin may name its file or flag instead).
	OriginConfig = "configuration"
	// OriginSession is a rule added for the rest of the session (AddRule).
	OriginSession = "this session"
	// OriginProject is a rule added for the project (AddRule with ScopeProject).
	OriginProject = "this project"
	// OriginRemembered is a rule a "don't ask again" answer added.
	OriginRemembered = "don't ask again"
	// OriginTests is a rule of the tests preset (TestsAllow).
	OriginTests = "the tests preset"
	// OriginIsolation is the confinement of a worker to its own tree.
	OriginIsolation = "isolation"
)

// Tiers of a decision, as Classify reports them.
const (
	// TierHard is a refusal no mode and no rule lifts.
	TierHard = "hard"
	// TierGuarded is a protection only a specific allow rule lifts.
	TierGuarded = "guarded"
	// TierRule is a decision a rule made.
	TierRule = "rule"
	// TierMode is a decision the mode's defaults made.
	TierMode = "mode"
)

// Classification is what the engine would do with a request, without asking anyone.
type Classification struct {
	Verdict Action // Allow, Deny or Ask
	Rule    string // the rule that decided, "" when the mode's default did
	Origin  string // where the rule came from: built-in protection, user config, project config, this session, flag
	Tier    string // hard (no mode lifts it), guarded, rule, mode
	Why     string // one sentence
	// NoOneToAsk says the request would be put to a person, and the verdict is Deny only
	// because the engine has no Prompter.
	NoOneToAsk bool
}

// Classify judges a request in the engine's order (hard denies, ask rules, high-risk
// shell, allow rules, the mode) and never prompts: the verdict is the one Check would
// reach before asking anyone. A request Check would put to a person is Ask, or Deny
// when the engine has no Prompter (Check refuses it then). Nothing is audited and no
// rule is added.
func (e *Engine) Classify(r Request) Classification {
	sess, prof := e.snapshot(r.Role)
	v := e.evaluate(sess, r)
	if prof != nil {
		if pv := e.evaluate(prof, r); pv.kind > v.kind {
			pv.reason = "role " + r.Role + ": " + pv.reason
			v = pv
		}
	}
	c := Classification{Rule: v.rule, Origin: v.origin, Tier: v.tier, Why: v.reason}
	if c.Tier == "" {
		c.Tier = TierMode
	}
	switch v.kind {
	case vAllow:
		c.Verdict = Allow
	case vDeny:
		c.Verdict = Deny
	default:
		c.Verdict = Ask
		if e.cfg.Prompter == nil {
			c.Verdict, c.NoOneToAsk = Deny, true
			c.Why = "approval required: " + v.reason + NoOneToAsk
		}
	}
	return c
}

// RemoveRule removes a session-scope rule; it reports whether one was removed. Rules
// from configuration cannot be removed: only rules added while the session ran (AddRule,
// a remembered answer, the tests preset) are. rule is the rule's text ("Bash(go test:*)");
// a rule of that text is removed from every action it was added to.
func (e *Engine) RemoveRule(rule string) bool {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return false
	}
	canon := rule
	if r, err := ParseRule(Allow, rule); err == nil {
		canon = r.String()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	removed := false
	for _, list := range []*[]*crule{&e.allow, &e.ask, &e.deny} {
		var kept []*crule
		hit := false
		for _, c := range *list {
			if c.runtime && c.String() == canon {
				hit = true
				continue
			}
			kept = append(kept, c)
		}
		if hit {
			// Copy on write: evaluations in flight keep reading the old slice.
			*list = append(make([]*crule, 0, len(kept)), kept...)
			removed = true
		}
	}
	if removed {
		var granted []string
		for _, g := range e.granted {
			if g != canon {
				granted = append(granted, g)
			}
		}
		e.granted = granted
		e.allowEdits = withTests(e.allow, e.tests)
		for r := range e.persisted {
			if r.String() == canon {
				delete(e.persisted, r)
			}
		}
	}
	return removed
}

// RuleInfo is one active rule with its origin, as Classify would name it.
type RuleInfo struct {
	Action  Action
	Rule    string
	Origin  string
	Runtime bool // added while the session ran: RemoveRule can remove it
}

// RuleInfos lists the active rules of the session (allow, ask, deny, in the order they
// apply) with their origins. The tests preset's rules are listed only when they were
// added (the accept-edits mode applies them without adding them).
func (e *Engine) RuleInfos() []RuleInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []RuleInfo
	for _, l := range []struct {
		a    Action
		list []*crule
	}{{Allow, e.allow}, {Ask, e.ask}, {Deny, e.deny}} {
		for _, c := range l.list {
			out = append(out, RuleInfo{Action: l.a, Rule: c.String(), Origin: c.origin, Runtime: c.runtime})
		}
	}
	return out
}

// DeclinedWith is the refusal reason of a person's "no" with an instruction: the
// engine's advice not to work around the refusal, then "The person says: " and the note
// (empty note: "denied by user", to which the engine adds the same advice).
func DeclinedWith(note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return "denied by user"
	}
	return "denied by user" + declinedAdvice + " The person says: " + note
}
