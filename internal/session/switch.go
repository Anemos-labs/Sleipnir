package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
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
	s.Agent = fresh
	s.Log.Emit("", events.TypeModelSwitch, map[string]any{"from": oldModel.ID, "to": m.ID, "provider": mr.Provider})
	return mr.String(), nil
}
