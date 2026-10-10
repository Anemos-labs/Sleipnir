package settings

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/commands"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/hooks"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/skills"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// skillsBudgetNote says where the skills listing lives.
const skillsBudgetNote = "the skills listing sits in the shared layer (G1); changing it costs one cache re-write"

// handleSkills is GET /api/sessions/{id}/skills: the skills the model can load (the session's catalogue, or the one a session of the
// tab's project would find), what their listing costs, the custom commands, and the hooks with the layer each came from (command
// lines with tokens withheld, never header or environment values).
func (s *service) handleSkills(w http.ResponseWriter, r *http.Request) {
	tc, err := s.tabOf(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	home := s.homeOf(tc)
	v := wire.SkillsView{Skills: []wire.Skill{}, Commands: []wire.CustomCommand{}}
	cat := (*skills.Catalog)(nil)
	if tc.sess != nil && tc.sess.Skills != nil {
		cat = tc.sess.Skills
	} else {
		cat, _ = skills.Discover(skills.Opts{Root: tc.root, Home: home, TrustProject: tc.trusted})
	}
	for _, sk := range cat.Skills() {
		row := wire.Skill{
			Name: provider.SanitizeText(sk.Name, 80), Summary: provider.SanitizeText(sk.Summary(), 400), Source: s.sourceOf(sk.Path, tc),
			Scope: string(sk.Scope), YouOnly: sk.DisableModelInvocation, ArgumentHint: provider.SanitizeText(sk.ArgumentHint, 80),
		}
		if row.YouOnly {
			row.Note = "disable-model-invocation: true: only you can load it (/" + row.Name + ")"
		}
		v.Skills = append(v.Skills, row)
	}
	listing := cat.Listing(skills.DefaultListingTokens, core.NewBytesEstimator())
	v.SkillsBudget = &wire.SkillsBudget{Tokens: skills.DefaultListingTokens, Used: core.NewBytesEstimator().Tokens(listing), Note: skillsBudgetNote}
	var reg *commands.Registry
	if tc.sess != nil {
		reg = tc.sess.Commands()
	}
	if reg == nil {
		reg, _ = commands.Load(commands.Opts{Root: tc.root, Home: home, TrustProject: tc.trusted})
	}
	if reg != nil {
		for _, c := range reg.List() {
			v.Commands = append(v.Commands, wire.CustomCommand{
				Name: provider.SanitizeText(c.Name, 80), Description: provider.SanitizeText(c.Description, 400),
				ArgumentHint: provider.SanitizeText(c.ArgumentHint, 80), Source: s.sourceOf(c.Path, tc), Scope: string(c.Scope),
			})
		}
	}
	v.Hooks = &wire.HooksView{Events: hooks.Events(), Configured: s.hookRows(tc)}
	reply(w, v, nil)
}

// sourceOf shows where a definition file is: relative to the project root inside it, ~/... under the home directory.
func (s *service) sourceOf(path string, tc *tabCtx) string {
	if tc.root != "" {
		if rel, ok := relUnder(tc.root, path); ok {
			return rel
		}
	}
	return s.display(path)
}

// hookRows lists the configured hooks, event by event in the order hooks.Events gives, each with the layer of the configuration
// that supplied it (a trusted project's hooks run after the user's).
func (s *service) hookRows(tc *tabCtx) []wire.Hook {
	out := []wire.Hook{}
	opts := s.loadOpts(tc)
	for _, ev := range hooks.Events() {
		groups, err := config.ListOrigins(opts, "hooks", ev)
		if err != nil {
			return out
		}
		for _, g := range groups {
			raw, err := json.Marshal([]any{g.Value})
			if err != nil {
				continue
			}
			origin := hooks.OriginUser
			if g.Kind == "project" || g.Kind == "local" {
				origin = hooks.OriginProject
			}
			set, err := hooks.ParseAs(origin, g.Source, map[string]json.RawMessage{ev: raw})
			if err != nil || set == nil {
				continue
			}
			for _, name := range set.Events() {
				for _, h := range set.Hooks(name) {
					out = append(out, s.hookRow(h, g.Kind, tc))
				}
			}
		}
	}
	return out
}

// hookRow is one hook as the page shows it: the command line (or the URL of an HTTP hook) with credentials withheld, the timeout in
// seconds, the layer and file it came from, and its condition (the configuration has no purpose text of its own).
func (s *service) hookRow(h hooks.Hook, kind string, tc *tabCtx) wire.Hook {
	cmd := h.Command
	if h.Type == hooks.TypeHTTP {
		cmd = config.RedactURL(h.URL)
		if len(h.Headers) > 0 {
			names := sortedNames(h.Headers)
			sort.Strings(names)
			cmd += " (headers: " + joinNames(names) + ")"
		}
	}
	origin := layerOrigin[kind]
	if origin == "" {
		origin = kind
	}
	if f := s.ruleFile(config.RuleOrigin{Layer: kind, File: h.Source}, tc); f != "" {
		origin += " · " + f
	}
	purpose := ""
	if h.If != "" {
		purpose = "runs only if " + provider.SanitizeText(h.If, 200)
	}
	return wire.Hook{
		Event: h.Event, Matcher: provider.SanitizeText(h.Matcher, 200), Command: cleanText(cmd), Timeout: int(h.Timeout.Seconds()),
		Origin: origin, Purpose: purpose,
	}
}

// joinNames joins names with commas.
func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
