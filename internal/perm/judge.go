package perm

import (
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/shellparse"
)

// vkind orders outcomes by severity: when several things are judged together
// the most severe wins.
type vkind int

const (
	vAllow vkind = iota
	vAsk
	vDeny
)

type verdict struct {
	kind   vkind
	reason string
	// explicit marks an allow granted by a specific (non-blanket) user rule;
	// only that may override the tool's own high-risk marker.
	explicit bool
	// askRule marks an ask caused by a user ask rule; remembering an answer
	// would not change it, so Decision.Remember is not honoured for it.
	askRule bool
	// rem are allow rules to add when the user chooses to remember an answer.
	rem []Rule
}

func allow(reason string) verdict { return verdict{kind: vAllow, reason: reason} }
func deny(reason string) verdict  { return verdict{kind: vDeny, reason: reason} }
func ask(reason string, rem []Rule) verdict {
	return verdict{kind: vAsk, reason: reason, rem: rem}
}

// planReason is what a model is told when plan mode refuses something.
func planReason(detail string) string {
	return "plan mode (read-only): " + detail + ". Present a plan instead of acting"
}

// combine merges two verdicts on the same request: the more severe wins, the
// first wins a tie; asks pool their remember rules.
func combine(a, b verdict) verdict {
	switch {
	case b.kind > a.kind:
		return b
	case b.kind < a.kind:
		return a
	case a.kind == vAllow:
		a.explicit = a.explicit && b.explicit
	case a.kind == vAsk:
		a.rem = mergeRules(a.rem, b.rem)
		a.askRule = a.askRule || b.askRule
	}
	return a
}

// mergeRules appends the rules of b that a does not already hold. Many
// operands of one command ask for the same remembered rule; keeping the list
// duplicate-free keeps merging linear.
func mergeRules(a, b []Rule) []Rule {
	for _, r := range b {
		dup := false
		for _, x := range a {
			if x == r {
				dup = true
				break
			}
		}
		if !dup {
			a = append(a[:len(a):len(a)], r)
		}
	}
	return a
}

// view is the mode and rule set a request is judged under: the session's, or
// a role's stricter overlay of it.
type view struct {
	mode  Mode
	deny  []*crule
	ask   []*crule
	allow []*crule
	role  string
}

func (u *unit) allAccesses() []access {
	all := make([]access, 0, len(u.accesses)+len(u.redirs))
	all = append(all, u.accesses...)
	return append(all, u.redirs...)
}

// judge applies the precedence: built-in protections and user deny rules,
// then user ask rules, then allow rules and the mode's defaults.
func (ev *evaluator) judge(u *unit) verdict {
	all := u.allAccesses()
	for _, a := range all {
		if a.dynamic {
			if why := dynamicSuspect(a.raw, a.write); why != "" {
				return deny("built-in protection: " + why)
			}
			if !a.prefix {
				continue
			}
			// Judge a hypothetical file inside the directory the known prefix
			// names: if that is protected, so is whatever the variable spells.
			a.lex, a.real = strings.TrimSuffix(a.lex, "/")+"/x", strings.TrimSuffix(a.real, "/")+"/x"
		}
		switch p := ev.rs.protect(a); p.tier {
		case tierHard:
			return deny("built-in protection: " + p.why)
		case tierGuarded:
			if ev.explicitAllowAccess(a) == nil {
				return deny("built-in protection: " + p.why)
			}
		}
	}
	if r := ev.restrictMatch(ev.v.deny, u, all); r != nil {
		return deny("denied by rule " + r.String())
	}
	if r := ev.restrictMatch(ev.v.ask, u, all); r != nil {
		v := ask("rule "+r.String()+" requires approval", nil)
		v.askRule = true
		return v
	}
	mode := ev.v.mode
	if u.high != nil && mode != ModeBypass && !ev.overridesRisk(u) {
		if mode == ModePlan {
			return deny(planReason(u.high.why))
		}
		return ask("high risk: "+u.high.why, ev.remember(u))
	}
	if u.tool {
		return ev.decideTool(u)
	}
	return ev.decideCommand(u)
}

func quote(s string) string { return strconv.Quote(s) }

func (ev *evaluator) decideCommand(u *unit) verdict {
	mode := ev.v.mode
	if mode != ModeBypass {
		switch {
		case u.envBad != "":
			r := "environment assignment " + u.envBad + " can change what runs"
			if mode == ModePlan {
				return deny(planReason(r))
			}
			return ask(r, nil)
		case u.dyn != "":
			if mode == ModePlan {
				return deny(planReason(u.dyn))
			}
			return ask(u.dyn, nil)
		}
	}
	rule := ev.commandAllowRule(u)
	var res verdict
	switch {
	case u.class == cmdNoop:
		res = allow("assignments and redirections only")
	case rule != nil:
		res = allow("allowed by rule " + rule.String())
		res.explicit = !rule.blanket
	case mode == ModeBypass:
		res = allow("bypass mode")
	case u.class == cmdSafe:
		res = allow(ev.modeReason(genericSafe))
	case u.class == cmdFSWrite && mode == ModeAcceptEdits:
		res = allow(ev.modeReason(genericFSWrite))
	case mode == ModePlan:
		res = deny(planReason(u.label + " is not a read-only command"))
	case u.class == cmdFSWrite:
		res = ask(ev.modeReason(u.label+" modifies files; approve it, or use accept-edits mode to allow this inside the workspace"), ev.remember(u))
	default:
		res = ask(ev.modeReason(u.label+": "+u.why), ev.remember(u))
	}
	if res.kind == vDeny {
		return res
	}
	if rule == nil && mode != ModeBypass && (u.class == cmdSafe || u.class == cmdFSWrite) {
		for _, a := range u.accesses {
			res = combineAccess(res, ev.accessVerdict(a, u))
		}
	}
	for _, a := range u.redirs {
		res = combineAccess(res, ev.accessVerdict(a, u))
	}
	return res
}

// Generic reasons of a command-level allow; an access-level reason says more.
const (
	genericSafe    = "read-only command"
	genericFSWrite = "file operation inside the workspace"
)

// modeReason names the mode a verdict rests on.
func (ev *evaluator) modeReason(s string) string { return string(ev.v.mode) + " mode: " + s }

// combineAccess is combine, except that an access-level reason replaces the
// generic reason of a command-level allow.
func combineAccess(res, av verdict) verdict {
	if res.kind == vAllow && av.kind == vAllow && !strings.HasPrefix(av.reason, "bypass") {
		out := combine(res, av)
		if strings.HasSuffix(res.reason, genericSafe) || strings.HasSuffix(res.reason, genericFSWrite) {
			out.reason = av.reason
		}
		return out
	}
	return combine(res, av)
}

func (ev *evaluator) decideTool(u *unit) verdict {
	r := *u.req
	mode := ev.v.mode
	if u.dyn != "" && mode != ModeBypass { // too many paths to judge one by one
		if mode == ModePlan {
			return deny(planReason(u.dyn))
		}
		return ask(u.dyn, nil)
	}
	rule := ev.toolAllowRule(r)
	var res verdict
	have := true
	switch {
	case rule != nil:
		res = allow("allowed by rule " + rule.String())
		res.explicit = !rule.blanket
	case u.network:
		what := "network access"
		if h := urlHost(r); h != "" {
			what += " to " + h
		}
		switch mode {
		case ModeBypass:
			res = allow("bypass mode")
		case ModePlan:
			res = deny(planReason(what + " is not allowed"))
		default:
			res = ask(what+" needs approval", ev.remember(u))
		}
	case len(u.accesses) == 0 && r.Writes:
		switch mode {
		case ModeBypass:
			res = allow("bypass mode")
		case ModePlan:
			res = deny(planReason(r.Tool + " changes state"))
		default:
			res = ask(r.Tool+" changes state outside the file system; approval needed", ev.remember(u))
		}
	case len(u.accesses) == 0 && strings.HasPrefix(normTool(r.Tool), "mcp"):
		switch mode {
		case ModeBypass:
			res = allow("bypass mode")
		case ModePlan:
			res = deny(planReason("the effects of " + r.Tool + " are unknown"))
		default:
			res = ask("the effects of "+r.Tool+" cannot be checked; approval needed", ev.remember(u))
		}
	case len(u.accesses) == 0:
		res = allow("touches no files and no network")
	default:
		have = false
	}
	if rule != nil || res.kind == vDeny {
		return res
	}
	for _, a := range u.accesses {
		av := ev.accessVerdict(a, u)
		if !have {
			res, have = av, true
		} else {
			res = combine(res, av)
		}
	}
	return res
}

// accessVerdict applies allow rules and the mode's defaults to one file access.
func (ev *evaluator) accessVerdict(a access, u *unit) verdict {
	mode := ev.v.mode
	rem := func() []Rule { return ev.rememberAccess(a, u) } // only needed when it asks
	if a.dynamic {
		switch {
		case mode == ModeBypass:
			return allow("bypass mode")
		case mode == ModePlan && a.write:
			return deny(planReason("cannot tell which file " + quote(a.raw) + " is"))
		}
		return ask("cannot tell statically which path "+quote(a.raw)+" is", nil)
	}
	if a.write && !a.read && harmlessDevice(fold(a.real)) {
		return allow("harmless device")
	}
	if r := ev.accessAllowRule(a); r != nil {
		v := allow("allowed by rule " + r.String())
		v.explicit = !r.blanket
		return v
	}
	switch mode {
	case ModeBypass:
		return allow("bypass mode")
	case ModePlan:
		if a.write {
			return deny(planReason("it would write " + a.raw))
		}
	case ModeAcceptEdits:
		if a.write {
			if ev.rs.inWorkspace(a.real) {
				return allow(ev.modeReason("write inside the workspace"))
			}
			return ask(ev.modeReason("writes "+a.raw+" outside the workspace; approval needed"), rem())
		}
	default:
		if a.write {
			return ask(ev.modeReason("writing "+a.raw+" needs approval"), rem())
		}
	}
	if ev.rs.inWorkspace(a.real) {
		return allow(ev.modeReason("read inside the workspace"))
	}
	return ask(ev.modeReason("reads "+a.raw+" outside the workspace; approval needed"), rem())
}

// ruleHits reports whether a path rule covers the access. Deny and ask rules
// (restrict) look at the lexical and the resolved path so nothing slips through
// either way; an allow rule needs the resolved path to match, so a symlink
// cannot borrow the permission of the place it looks like.
func ruleHits(r *crule, a access, restrict bool) bool {
	if r.blanket {
		return true
	}
	for _, g := range r.globs {
		if restrict && a.lex != "" && g.matches(a.lex) {
			return true
		}
		if a.real != "" && g.matches(a.real) {
			return true
		}
	}
	return false
}

func (ev *evaluator) restrictMatch(rules []*crule, u *unit, all []access) *crule {
	for _, r := range rules {
		switch r.class {
		case classBash:
			if u.simple != nil && r.matchCommand(*u.simple, true) {
				return r
			}
		case classRead:
			for _, a := range all {
				if a.read && ruleHits(r, a, true) {
					return r
				}
			}
		case classWrite:
			for _, a := range all {
				if a.write && ruleHits(r, a, true) {
					return r
				}
			}
		case classWeb:
			if u.tool && r.matchWeb(*u.req) {
				return r
			}
		default:
			if u.tool && r.matchOther(*u.req) {
				return r
			}
		}
	}
	return nil
}

func (ev *evaluator) accessAllowRule(a access) *crule {
	for _, r := range ev.v.allow {
		if (a.write && r.class == classWrite || !a.write && r.class == classRead) && ruleHits(r, a, false) {
			return r
		}
	}
	return nil
}

// explicitAllowAccess finds a user allow rule that names the access (blanket
// rules do not count): the only thing that lifts a guarded built-in protection.
func (ev *evaluator) explicitAllowAccess(a access) *crule {
	for _, r := range ev.v.allow {
		if r.blanket || !ruleHits(r, a, false) {
			continue
		}
		if r.class == classWrite || (r.class == classRead && !a.write) {
			return r
		}
	}
	return nil
}

func (ev *evaluator) commandAllowRule(u *unit) *crule {
	if u.simple == nil || u.envBad != "" || u.dyn != "" {
		return nil
	}
	var blanket *crule
	for _, r := range ev.v.allow {
		if r.class != classBash || !r.matchCommand(*u.simple, false) {
			continue
		}
		if !r.blanket {
			return r
		}
		blanket = r
	}
	return blanket
}

// overridesRisk reports whether an allow rule speaks specifically enough to
// permit a command flagged high risk.
func (ev *evaluator) overridesRisk(u *unit) bool {
	if u.simple == nil || u.envBad != "" || u.dyn != "" {
		return false
	}
	for _, r := range ev.v.allow {
		if r.class != classBash || r.blanket || !r.matchCommand(*u.simple, false) {
			continue
		}
		if !r.anyArgs || len(u.high.markers) == 0 {
			return true
		}
		for _, m := range u.high.markers {
			for _, t := range r.argv {
				if strings.Contains(t, m) {
					return true
				}
			}
		}
	}
	return false
}

func (ev *evaluator) toolAllowRule(r Request) *crule {
	var blanket *crule
	for _, cr := range ev.v.allow {
		if !(cr.class == classWeb && cr.matchWeb(r) || cr.class == classOther && cr.matchOther(r)) {
			continue
		}
		if !cr.blanket {
			return cr
		}
		blanket = cr
	}
	return blanket
}

// remember builds the allow rules that would make a repeat of the unit's
// request pass: an exact command for shell commands, exact paths or the host
// for tools. It returns nil when no rule could ever match (dynamic pieces).
func (ev *evaluator) remember(u *unit) []Rule {
	if !u.remDone {
		u.rem, u.remDone = ev.buildRemember(u), true
	}
	return u.rem
}

func (ev *evaluator) buildRemember(u *unit) []Rule {
	if u.tool {
		var rules []Rule
		seen := map[string]bool{}
		for _, a := range u.accesses {
			if r, ok := pathRule(a); ok && !seen[r.String()] {
				seen[r.String()] = true
				rules = append(rules, r)
			}
		}
		if u.network {
			if h := urlHost(*u.req); h != "" {
				return append(rules, Rule{Action: Allow, Tool: "WebFetch", Pattern: "domain:" + h})
			}
			return nil
		}
		if len(rules) == 0 && u.req.Tool != "" {
			rules = append(rules, Rule{Action: Allow, Tool: u.req.Tool})
		}
		return rules
	}
	s := u.simple
	if s == nil || u.envBad != "" || u.dyn != "" || s.Program == "" {
		return nil
	}
	argv := append([]string{s.Program}, s.Args...)
	for _, w := range argv {
		if strings.Contains(w, "*") {
			return nil // it would become a wildcard in the rule
		}
	}
	pat := shellparse.Join(argv)
	if strings.HasSuffix(pat, ":*") || strings.HasSuffix(pat, " *") {
		return nil
	}
	return []Rule{{Action: Allow, Tool: "Bash", Pattern: pat}}
}

func (ev *evaluator) rememberAccess(a access, u *unit) []Rule {
	if a.dynamic {
		return nil
	}
	if u.tool || a.redir {
		if r, ok := pathRule(a); ok {
			return []Rule{r}
		}
		return nil
	}
	return ev.remember(u)
}

// pathRule is the exact-path allow rule for an access.
func pathRule(a access) (Rule, bool) {
	if a.real == "" {
		return Rule{}, false
	}
	tool := "Read"
	if a.write {
		tool = "Edit"
	}
	return Rule{Action: Allow, Tool: tool, Pattern: escapeGlob(a.real)}, true
}

// escapeGlob makes a literal path safe to use as a glob pattern.
func escapeGlob(p string) string {
	if !strings.ContainsAny(p, `*?[\`) {
		return p
	}
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(p)
}
