package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// SwitchModel moves the single agent to another model between turns, keeping its thread, notes, spine and bill. The agent is
// built again on the new provider and the snapshot of the old one is restored into it (the path a resume takes), so the next
// request is a declared rebase: the new model has none of the old one's prompt cache. A swarm is refused: its workers run on
// their roles' models (--role-model, models.roles), which is where to change them; the chat's /model starts a team again on another model.
func (s *Session) SwitchModel(ctx context.Context, ref string) (string, error) {
	if s.newSolo == nil || s.Swarm != nil {
		return "", errors.New("/model changes the model of a single agent; a swarm runs on its roles' models (--role-model, models.roles)")
	}
	mr, err := ResolveModel(s.cfg, ref)
	if err != nil {
		return "", err
	}
	p, m, err := BuildProvider(s.cfg, mr, ProviderOptions{CaptureTokens: s.opts.CaptureTokens})
	if err != nil {
		return "", err
	}
	m = s.describe(ctx, p, m)
	oldProv, oldModel, old := s.Provider, s.Model, s.Agent
	snap := old.Snapshot()
	s.Provider, s.Model = p, m
	fresh, err := s.newSolo()
	if err == nil {
		err = fresh.Restore(snap)
	}
	if err != nil {
		s.Provider, s.Model = oldProv, oldModel
		if fresh != nil {
			_ = fresh.Close()
		}
		return "", fmt.Errorf("/model: staying on %s: %w", oldModel.ID, err)
	}
	_ = old.Close() // waits for a compaction still running, and frees the old agent's archive index
	if _, aerr := agent.RebuildArchive(s.Dir, fresh.ID(), s.archive); aerr != nil {
		s.notice("", "/model: the archive could not be re-indexed, so recall of folded turns may miss: "+aerr.Error())
	}
	s.Agent, s.modelRef = fresh, mr.String()
	s.Log.Emit("", events.TypeModelSwitch, map[string]any{"from": oldModel.ID, "to": m.ID, "provider": mr.Provider})
	return mr.String(), nil
}

// CheckModel resolves a model reference as --model and /model do, and changes nothing: the reference as the session names it, or why it
// cannot be used (an unknown provider, no key for it). A command that restarts the chat checks first, so that a typo does not end it.
func (s *Session) CheckModel(ref string) (string, error) {
	mr, err := ResolveModel(s.cfg, ref)
	if err != nil {
		return "", err
	}
	p, ok := lookupProvider(s.cfg, mr.Provider)
	switch {
	case !ok:
		return "", fmt.Errorf("unknown provider %q (known: %s)", mr.Provider, strings.Join(providerNames(s.cfg), ", "))
	case p.Auth == config.AuthChatGPTPlan && !ProviderReady(p):
		return "", fmt.Errorf("provider %q is not signed in: /login %s", mr.Provider, mr.Provider)
	case p.APIKeyEnv != "" && p.APIKey() == "":
		return "", fmt.Errorf("provider %q has no key: /login %s, or set %s", mr.Provider, mr.Provider, p.APIKeyEnv)
	}
	return mr.String(), nil
}

// SetBudget changes the dollar budget of the single agent for the turns that follow (0 removes it): the chat's /budget. A swarm's budget is
// the session's, fixed when it starts.
func (s *Session) SetBudget(usd float64) error {
	if s.Swarm != nil || s.Agent == nil {
		return errors.New("a swarm's budget is set when it starts (--budget-usd or swarm.budget_usd)")
	}
	if err := s.Agent.SetBudget(usd); err != nil {
		return err
	}
	s.budget = usd
	return nil
}

// AllowForSession adds an allow rule for the rest of the session, in the form --allow takes: Bash(go test:*), Edit(src/**), WebFetch(docs.example.com).
func (s *Session) AllowForSession(rule string) error {
	r, err := perm.ParseRule(perm.Allow, rule)
	if err != nil {
		return err
	}
	s.Perm.AddRule(perm.ScopeSession, r)
	return nil
}

// Cost is what the session has spent, in dollars.
func (s *Session) Cost() float64 { return s.cost() }

// Cwd is the directory the session works in.
func (s *Session) Cwd() string { return s.opts.Cwd }

// Home is the home directory the session reads and writes the user's own files under (~/.sleipnir).
func (s *Session) Home() string { return s.opts.Home }

// ModelRef is the provider/model the session runs on, as --model takes it ("" when the provider was handed in as a value).
func (s *Session) ModelRef() string { return s.modelRef }

// RoleModel is one line of the table of who runs on which model.
type RoleModel struct {
	Role  string // "default", a swarm role, or "compactor"
	Model string // provider/model, or what stands in when none is named
	From  string // where it came from: "--role-model", "an agent definition", "models.roles", "the session's model", "the agent's own model"
}

// RoleModels says which model each role runs on, in the order the precedence reads: the session's own, then each swarm role (flags beat an agent
// definition, which beats models.roles), then the compactor. In a single-agent session only the default and the compactor apply.
func (s *Session) RoleModels() []RoleModel {
	def := s.modelRef
	if def == "" {
		def = s.Model.ID
	}
	out := []RoleModel{{"default", def, "the session's model"}}
	if s.Swarm != nil {
		defs := map[string]string{}
		for _, d := range s.ext.defs {
			defs[d.Name] = d.Model
		}
		names := make([]string, 0, len(s.Roles))
		for name := range s.Roles {
			names = append(names, name)
		}
		if s.mailmanOn() {
			names = append(names, swarm.MailmanRoleName)
		}
		sort.Slice(names, func(i, j int) bool { // the manager leads, the rest by name
			if (names[i] == "manager") != (names[j] == "manager") {
				return names[i] == "manager"
			}
			return names[i] < names[j]
		})
		for _, name := range names {
			m, from := def, "the session's model"
			switch {
			case s.opts.RoleModels[name] != "":
				m, from = s.opts.RoleModels[name], "--role-model"
			case defs[name] != "":
				m, from = defs[name], "an agent definition"
			case s.cfg.Models.Roles[name] != "":
				m, from = s.cfg.Models.Roles[name], "models.roles"
			}
			out = append(out, RoleModel{name, m, from})
		}
	}
	comp := RoleModel{CompactorRole, "", ""}
	switch {
	case s.opts.RoleModels[CompactorRole] != "":
		comp.Model, comp.From = s.opts.RoleModels[CompactorRole], "--role-model"
	case s.cfg.Models.Roles[CompactorRole] != "":
		comp.Model, comp.From = s.cfg.Models.Roles[CompactorRole], "models.roles"
	default:
		comp.Model, comp.From = "(each agent's own)", "no compactor model is set"
	}
	return append(out, comp)
}

// StartFlags are the `sleipnir chat` flags that reproduce this session's shape (a team and its size, the verifier, isolation, tool servers, trust,
// the budget, the roles named by flags), for a restart that changes one thing and keeps the rest.
func (s *Session) StartFlags() []string {
	o := s.opts
	var f []string
	if o.Swarm {
		n := o.Workers
		if n <= 0 && s.Swarm != nil {
			n = s.Swarm.MaxWorkers() // the size came from the configuration or the swarm's default
		}
		f = append(f, "--swarm", strconv.Itoa(n))
	} else {
		f = append(f, "--swarm", "0") // a single agent stays one: the default on a terminal is a team
	}
	if o.Verify != "" {
		f = append(f, "--verify", o.Verify)
	}
	if o.Isolation != "" {
		f = append(f, "--isolation", o.Isolation)
	}
	if o.Commit {
		f = append(f, "--commit")
	}
	if o.Mailman != nil {
		f = append(f, "--mailman="+strconv.FormatBool(*o.Mailman))
	}
	if o.NoMCP {
		f = append(f, "--no-mcp")
	}
	if o.TrustProject {
		f = append(f, "--trust-project")
	}
	if o.BudgetUSD > 0 {
		f = append(f, "--budget-usd", strconv.FormatFloat(o.BudgetUSD, 'f', -1, 64))
	}
	roles := make([]string, 0, len(o.RoleModels))
	for role := range o.RoleModels {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		f = append(f, "--role-model", role+"="+o.RoleModels[role])
	}
	return f
}
