package swarm

import (
	"context"
	"fmt"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// flushManager verifies and integrates direct manager changes without inventing a
// board task. It runs between model steps, before completion or publication.
func (s *Swarm) flushManager(ctx context.Context, m *member) error {
	if m.tree == nil {
		return nil
	}
	diff, err := m.tree.DiffWith(ctx, gitx.DiffOptions{NoPatch: true})
	if err != nil {
		return err
	}
	if len(diff.Files) == 0 {
		m.markClean(s.deps.Now())
		return s.syncManager(ctx, m)
	}
	if ahead, err := s.managerAhead(ctx, m); err != nil {
		return err
	} else if !ahead {
		m.markClean(s.deps.Now())
		return s.syncManager(ctx, m)
	}
	// The change may take many steps to pass verification (a manager that writes a feature itself): its duration runs from the
	// last flush that found the checkout with nothing pending, before the step that began it, so the review that covers it is
	// held to how long it took, not to the floor.
	began := m.markEditSince(s.deps.Now())
	files := make([]string, 0, len(diff.Files))
	for _, f := range diff.Files {
		files = append(files, f.Path)
	}
	if vr := s.verify(ctx, m.dir, files); !vr.ok {
		return fmt.Errorf("manager changes need repair: %s", vr.out)
	}
	result := s.integrate(ctx, m, Task{ID: "manager", Title: "direct manager changes", Owner: m.id, Role: "manager", Files: files}, filesCommitSubject(files))
	if !result.ok {
		return fmt.Errorf("manager changes were not integrated: %s %v", result.bounce, result.infra)
	}
	landed := s.deps.Now()
	m.markClean(landed)
	if result.evidence == "nothing to merge: the tree has no changes" {
		return s.syncManager(ctx, m)
	}
	s.review.mu.Lock()
	s.review.landLocked("manager changes", files, landed.Sub(began))
	s.review.mu.Unlock()
	for _, f := range files {
		s.noteManagerEdit(f)
	}
	return s.syncManager(ctx, m)
}

// managerAhead reports whether the manager's checkout holds work the integration
// branch lacks: uncommitted changes, or a head the merge queue's tip does not contain.
// The checkout's diff is taken against the commit it started from, so once accepted
// worker changes are synced into it the diff lists their files although every one of
// them is already integrated; submitting that checkout would only be refused as empty.
func (s *Swarm) managerAhead(ctx context.Context, m *member) (bool, error) {
	iso := s.deps.Isolation
	if iso == nil || iso.Queue == nil {
		return true, nil
	}
	dirty, err := m.tree.Dirty(ctx)
	if err != nil || dirty {
		return dirty, err
	}
	head, err := m.tree.Head(ctx)
	if err != nil {
		return false, err
	}
	tip := iso.Queue.Tip()
	if tip == "" {
		return true, nil
	}
	if head == tip {
		return false, nil
	}
	in, err := m.tree.Repo().IsAncestor(ctx, head, tip)
	if err != nil {
		return false, err
	}
	return !in, nil
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

// markEditSince records the start of the manager's pending change unless one is already recorded, and returns the start: the
// last time a flush found its checkout with nothing pending, or at when none has.
func (m *member) markEditSince(at time.Time) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.editSince.IsZero() {
		m.editSince = at
		if !m.cleanAt.IsZero() && m.cleanAt.Before(at) {
			m.editSince = m.cleanAt
		}
	}
	return m.editSince
}

// markClean records that the manager's checkout holds nothing pending at at: the next change it makes begins after it.
func (m *member) markClean(at time.Time) {
	m.mu.Lock()
	m.editSince, m.cleanAt = time.Time{}, at
	m.mu.Unlock()
}
