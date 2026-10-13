package swarm

import (
	"context"
	"fmt"
)

// flushManager verifies and integrates direct manager changes without inventing a
// board task. It runs between model steps, before completion or publication.
func (s *Swarm) flushManager(ctx context.Context, m *member) error {
	if m.tree == nil {
		return nil
	}
	patch, err := m.tree.Diff(ctx)
	if err != nil {
		return err
	}
	if patch == "" {
		return s.syncManager(ctx, m)
	}
	if vr := s.verify(ctx, m.dir, nil); !vr.ok {
		return fmt.Errorf("manager changes need repair: %s", vr.out)
	}
	result := s.integrate(ctx, m, Task{ID: "manager", Title: "direct manager changes", Owner: m.id, Role: "manager"})
	if !result.ok {
		return fmt.Errorf("manager changes were not integrated: %s %v", result.bounce, result.infra)
	}
	if result.evidence == "nothing to merge: the tree has no changes" {
		return s.syncManager(ctx, m)
	}
	s.review.mu.Lock()
	s.review.merges++
	s.review.last = "manager changes"
	s.review.mu.Unlock()
	return s.syncManager(ctx, m)
}

// ManagerWorkdir is the manager's writable checkout, or the session directory before startup.
func (s *Swarm) ManagerWorkdir() string {
	if m := s.get(s.ManagerID()); m != nil {
		return m.dir
	}
	return s.deps.Workdir
}

// syncManager brings accepted worker changes into the publication checkout and
// requires the manager to resolve conflicts before publishing or completing.
func (s *Swarm) syncManager(ctx context.Context, m *member) error {
	note, err := s.syncTree(ctx, m)
	if err != nil {
		return err
	}
	if note != "" {
		return fmt.Errorf("manager checkout needs repair: %s", note)
	}
	return nil
}
