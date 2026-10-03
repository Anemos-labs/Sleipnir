package swarm

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// IntegrationState records which verified commits reached the user's checkout.
// Pending is written durably before a checkout mutation, and cleared afterwards.
type IntegrationState struct {
	Applied   string              `json:"applied"`
	Committed string              `json:"committed,omitempty"`
	Files     []string            `json:"files,omitempty"`
	Pending   *IntegrationAttempt `json:"pending,omitempty"`
}

// IntegrationAttempt records enough state to distinguish an unapplied patch from
// an applied one after a crash. Before and After are pinned commits describing
// checkout contents. Branch selects a commit-mode fast-forward instead of a patch.
type IntegrationAttempt struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Before string   `json:"before,omitempty"`
	After  string   `json:"after,omitempty"`
	Branch string   `json:"branch,omitempty"`
	Paths  []string `json:"paths,omitempty"`
}

// saveIntegration persists the application cursor while the caller holds apply.mu.
func (s *Swarm) saveIntegration() error {
	if save := s.deps.Isolation.SaveIntegration; save != nil {
		return save(IntegrationState{Applied: s.apply.applied, Committed: s.apply.committed,
			Files: slices.Sorted(maps.Keys(s.apply.files)), Pending: s.apply.pending})
	}
	return nil
}

// RestoreIntegration restores and reconciles the checkout cursor before agents
// start. Ambiguous or partially applied writes are preserved for manual recovery.
func (s *Swarm) RestoreIntegration(ctx context.Context, state IntegrationState) error {
	if !s.isolated() {
		return fmt.Errorf("integration recovery requires worktree isolation")
	}
	s.apply.mu.Lock()
	defer s.apply.mu.Unlock()
	q := s.deps.Isolation.Queue
	applied := state.Applied
	if applied == "" {
		applied = q.Base()
	}
	if ok, err := s.deps.Isolation.Manager.Repository().IsAncestor(ctx, applied, q.Tip()); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("recorded integration position %s is not on %s", applied, q.Branch())
	}
	s.apply.applied, s.apply.committed, s.apply.pending = applied, state.Committed, state.Pending
	s.apply.files = map[string]bool{}
	for _, path := range state.Files {
		s.apply.files[path] = true
	}
	return s.recoverApplication(ctx)
}

// prepareApplication pins the before/after checkout contents and durably records
// the intent before applying a patch. The real index and user files stay untouched.
func (s *Swarm) prepareApplication(ctx context.Context, rep *IntegrationReport, from string, diff *gitx.Diff) error {
	iso := s.deps.Isolation
	if iso.SaveIntegration == nil {
		return nil
	}
	repo := iso.Manager.Repository()
	before, err := repo.SnapshotTree(ctx)
	if err != nil {
		return err
	}
	after, err := repo.PatchedTree(ctx, before, diff.Patch)
	if err != nil {
		return err
	}
	author := gitx.Author{Name: "Sleipnir", Email: "sleipnir@localhost"}
	before, err = repo.CommitTree(ctx, before, []string{from}, "sleipnir: checkout before integration", author)
	if err != nil {
		return err
	}
	after, err = repo.CommitTree(ctx, after, []string{before}, "sleipnir: checkout after integration", author)
	if err != nil {
		return err
	}
	pin := strings.TrimSuffix(rep.Branch, "_integration") + "_apply"
	old, err := repo.BranchSHA(ctx, pin)
	if err != nil && gitx.KindOf(err) != gitx.KindNotFound {
		return err
	}
	if err := repo.UpdateBranch(ctx, pin, after, old, "sleipnir: integration recovery"); err != nil {
		return err
	}
	paths := map[string]bool{}
	for _, file := range diff.Files {
		paths[file.Path] = true
		if file.OldPath != "" {
			paths[file.OldPath] = true
		}
	}
	s.apply.pending = &IntegrationAttempt{From: from, To: rep.Tip, Before: before, After: after, Paths: slices.Sorted(maps.Keys(paths))}
	return s.saveIntegration()
}

// recoverApplication resolves a durable intent by observing the checkout. It
// never reapplies a patch or overwrites edits to guess which side of a crash won.
func (s *Swarm) recoverApplication(ctx context.Context) error {
	pending := s.apply.pending
	if pending == nil {
		return nil
	}
	repo := s.deps.Isolation.Manager.Repository()
	q := s.deps.Isolation.Queue
	if ok, err := repo.IsAncestor(ctx, pending.To, q.Tip()); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("pending integration %s is not on %s", pending.To, q.Branch())
	}
	var applied, untouched bool
	if pending.Branch != "" {
		branch, err := repo.Branch(ctx)
		if err != nil {
			return err
		}
		if branch != pending.Branch {
			return fmt.Errorf("integration was interrupted on branch %s; return to that branch before resuming", pending.Branch)
		}
		head, err := repo.Head(ctx)
		if err != nil {
			return err
		}
		applied, untouched = head == pending.To, head == pending.From
	} else {
		if pending.Before == "" || pending.After == "" || len(pending.Paths) == 0 {
			return fmt.Errorf("incomplete integration recovery record; checkout left unchanged")
		}
		current, err := repo.SnapshotTree(ctx)
		if err != nil {
			return err
		}
		matches := func(commit string) (bool, error) {
			d, err := repo.Diff(ctx, commit, gitx.DiffOptions{To: current, Paths: pending.Paths, NoPatch: true, MaxFiles: 1 << 20})
			return err == nil && d.Empty() && !d.Truncated, err
		}
		if applied, err = matches(pending.After); err != nil {
			return err
		}
		if untouched, err = matches(pending.Before); err != nil {
			return err
		}
	}
	if !applied && !untouched {
		return fmt.Errorf("integration was interrupted and the affected files match neither its before nor after state; preserve your edits and reconcile %s before resuming (no files changed)", q.Branch())
	}
	if applied {
		s.noteApplied(&IntegrationReport{Tip: pending.To, Files: pending.Paths}, pending.Branch)
	}
	s.apply.pending = nil
	if err := s.saveIntegration(); err != nil {
		s.apply.pending = pending
		return err
	}
	return nil
}
