package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
)

// SwitchModel moves the single agent to another model between turns, keeping its thread, notes, spine and bill. The agent is
// built again on the new provider and the snapshot of the old one is restored into it (the path a resume takes), so the next
// request is a declared rebase: the new model has none of the old one's prompt cache. A swarm is refused: its workers run on
// their roles' models (--role-model, models.roles), which is where to change them.
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

// ModelRef is the provider/model the session runs on, as --model takes it ("" when the provider was handed in as a value).
func (s *Session) ModelRef() string { return s.modelRef }
