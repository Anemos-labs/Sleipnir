package settings

import (
	"net/http"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// permModes are the permission modes with the one line the Permissions page shows of each (docs/CONFIGURATION.md section 8).
// Cycle marks the modes shift+tab steps through; Danger the two that ask nothing.
var permModes = []wire.PermMode{
	{ID: string(perm.ModeDefault), Cycle: true, Text: "Reads inside the workspace and read-only shell commands are allowed; everything else asks"},
	{ID: string(perm.ModeAcceptEdits), Cycle: true, Text: "Also writes inside the workspace (file tools, redirections, mkdir, touch, cp, mv, rm, rmdir, tee on workspace paths) and the commands that build and test a project (go test, npm test, cargo build, pytest, make test… the --allow tests list); ask and deny rules still win"},
	{ID: string(perm.ModePlan), Cycle: true, Text: "Read-only. Writes, network access and commands that are not provably read-only are refused with a message that says to present a plan. An allow rule still carves an exception (say Edit(docs/plan.md))"},
	{ID: string(perm.ModeBypass), Danger: true, Text: "Full control without asking, except about the very dangerous: the high-risk class (sudo, a recursive delete of the workspace, home or /, a forced push to a shared branch, disk tools, shutdown) still asks. Hard denies, deny rules, guarded paths without an allow rule and ask rules still apply"},
	{ID: string(perm.ModeYolo), Danger: true, Text: "Never asks anything, for runs with nobody there: what bypass allows, and the high-risk class too. Hard denies, deny rules and guarded paths still refuse, and so does a rule that would have asked. For sandboxes. --continue never brings bypass or yolo back"},
}

// permOrder is the order the engine judges a request in.
var permOrder = []string{
	"hard denies (built-in protections and your deny rules)", "your ask rules", "high-risk shell commands", "your allow rules",
	"the mode's defaults",
}

// managerWritesText is what a manager's write is refused with: the text of internal/swarm's managerWritesMsg (a test keeps the two
// equal).
const managerWritesText = "the manager does not edit files: spawn a worker for the change (or reuse an idle one with spawn agent=...), then review its work"

// managerWritesNote says what the refusal covers.
const managerWritesNote = "the manager edits no file in any mode: its writes and writing shell commands are refused at run time"

// Origins of configuration rules, as the Permissions page names them.
var layerOrigin = map[string]string{
	"user": "user config", "project": "project config", "local": "local config", "env": "environment", "overrides": "flag",
}

// Notes shown on the first rule of a kind.
const (
	noteProjectAllow = "applied because the project's files are trusted (a security-sensitive key)"
	noteProjectDeny  = "a project can add to your deny and ask lists, never remove from them"
	noteBuiltin      = "a write into a config directory always asks, in every mode (bypass included); an unattended run refuses it"
	noteFlag         = "given when the session started (--allow)"
)

// handlePermissions is GET /api/sessions/{id}/permissions: the mode, the modes, the order rules are judged in, the manager's
// refusal, the tests preset, the rules in force grouped by effect with the origin of each (configuration layers and their files,
// built-in protections, the session's --allow flag) and the session's own rules.
func (s *service) handlePermissions(w http.ResponseWriter, r *http.Request) {
	tc, err := s.tabOf(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v := wire.PermissionsView{
		Mode: string(perm.ModeDefault), Modes: permModes, Order: permOrder,
		ManagerWrites: wire.ManagerWrites{Text: managerWritesText, Note: managerWritesNote},
		TestsPreset:   wire.TestsPreset{Name: config.TestsPreset, Summary: config.TestsPresetSummary, Rules: append([]string(nil), perm.TestsAllow...)},
		Rules:         map[string][]wire.Rule{"allow": {}, "deny": {}, "ask": {}},
		Session:       []wire.Rule{},
	}
	if tc.info.Mode != "" {
		v.Mode = string(tc.info.Mode)
	}
	origins, err := config.RuleOrigins(s.loadOpts(tc))
	if err != nil {
		origins = s.fallbackOrigins(tc)
	} else if tc.sess == nil {
		if cfg, _, lerr := config.Load(s.loadOpts(tc)); lerr == nil && cfg.Permissions.Mode != "" {
			v.Mode = cfg.Permissions.Mode
		}
	}
	noted := map[string]bool{}
	note := func(key, text string) string {
		if noted[key] {
			return ""
		}
		noted[key] = true
		return text
	}
	for _, ro := range origins {
		rule := wire.Rule{Effect: ro.Effect, Rule: ro.Rule, Origin: layerOrigin[ro.Layer], File: s.ruleFile(ro, tc), Fixed: true}
		if rule.Origin == "" {
			rule.Origin = ro.Layer
		}
		switch {
		case ro.Role != "":
			rule.Note = "for the " + ro.Role + " role"
		case (ro.Layer == "project" || ro.Layer == "local") && ro.Effect == "allow":
			rule.Note = note("project-allow", noteProjectAllow)
		case ro.Layer == "project" || ro.Layer == "local":
			rule.Note = note("project-deny", noteProjectDeny)
		}
		v.Rules[ro.Effect] = append(v.Rules[ro.Effect], rule)
	}
	for _, b := range session.ProtectedConfigRules() {
		v.Rules["ask"] = append(v.Rules["ask"], wire.Rule{Effect: "ask", Rule: b, Origin: "built-in protection", Note: note("builtin", noteBuiltin), Fixed: true})
	}
	for _, a := range tc.info.FlagAllow {
		v.Rules["allow"] = append(v.Rules["allow"], wire.Rule{Effect: "allow", Rule: a, Origin: "flag", File: "--allow", Note: note("flag", noteFlag), Fixed: true})
	}
	if rs, err := tc.tab.Rules(r.Context()); err == nil {
		for _, rule := range rs {
			if !rule.Fixed {
				v.Session = append(v.Session, rule)
			}
		}
	}
	reply(w, v, nil)
}

// ruleFile names the file of a configuration rule: a project file relative to the project root, a user file as ~/..., an
// environment variable by its name, the flags as "flags".
func (s *service) ruleFile(ro config.RuleOrigin, tc *tabCtx) string {
	switch {
	case strings.HasPrefix(ro.File, "env:"):
		return strings.TrimPrefix(ro.File, "env:")
	case ro.File == "overrides":
		return "flags"
	case (ro.Layer == "project" || ro.Layer == "local") && tc.root != "":
		if rel, ok := relUnder(tc.root, ro.File); ok {
			return rel
		}
	}
	return s.display(ro.File)
}

// fallbackOrigins lists the rules of the session's own configuration when the files cannot be read now (a file was broken after the
// session started): each list is attributed to the layer that supplied it last, as the report names it.
func (s *service) fallbackOrigins(tc *tabCtx) []config.RuleOrigin {
	cfg, rep := tc.info.Config, tc.info.Report
	if cfg == nil {
		return nil
	}
	var out []config.RuleOrigin
	for _, e := range []struct {
		effect string
		rules  []string
	}{{"deny", cfg.Permissions.Deny}, {"ask", cfg.Permissions.Ask}, {"allow", cfg.Permissions.Allow}} {
		src := ""
		if rep != nil {
			src = rep.Origins["permissions."+e.effect]
		}
		kind := rep.LayerKind(src)
		for _, r := range e.rules {
			out = append(out, config.RuleOrigin{Effect: e.effect, Rule: r, Layer: kind, File: src})
		}
	}
	return out
}
