package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

const prunedMarker = ".pruned"

// RemoveStoredSession removes an inactive session's log, blobs and checkpoints.
// Isolated worker edits are first salvaged into Git commits; branches holding
// unique work remain. The session lock, rather than file age, excludes live runs.
func RemoveStoredSession(ctx context.Context, dir string) (*workspace.PruneReport, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !hasLog(dir) {
		return nil, fmt.Errorf("%s is not a session directory", dir)
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	record, _, err := readIsolation(dir)
	if err != nil {
		var corrupt *events.CorruptError
		// A damaged ordinary log can still be explicitly deleted. Recovery
		// metadata, when present anywhere in the log, must be readable before
		// touching its worktrees or forgetting their session.
		if !errors.As(err, &corrupt) || record != nil || inspectLog(dir).recovery {
			return nil, err
		}
	}
	var report *workspace.PruneReport
	if record != nil {
		li := inspectLog(dir)
		if record.Prefix != "sleipnir/"+li.id || filepath.Base(record.Dir) != li.id {
			return nil, errors.New("isolation recovery namespace does not match the session")
		}
		repo, err := gitx.Open(record.Root)
		if err != nil {
			return nil, err
		}
		if !samePathLoose(repo.Root(), record.Root) {
			return nil, errors.New("the recorded repository root has changed")
		}
		pin := record.Prefix + "/_resume"
		if sha, err := repo.BranchSHA(ctx, pin); err == nil && sha != record.Base {
			return nil, errors.New("the session recovery reference was changed; worktrees were left intact")
		} else if err != nil && !errors.Is(err, gitx.ErrNotFound) {
			return nil, err
		}
		m := &workspace.Manager{Repo: repo, Dir: record.Dir, Prefix: record.Prefix, Base: record.Base}
		// Preserve recovery refs until all trees have been released. In particular,
		// a worker that outlived its session lock must prevent any pruning.
		refs := []string{pin, record.Prefix + "/_base", record.Prefix + "/_integration", record.Prefix + "/_apply"}
		report, err = m.Prune(ctx, workspace.PruneOptions{Salvage: true, RequireInactive: true, KeepPrefixes: refs})
		if err != nil {
			return report, err
		}
		if report.Live > 0 || len(report.Kept) > 0 {
			return report, errors.New("worker trees could not be safely released; the session was kept")
		}
		remaining, err := m.Prune(ctx, workspace.PruneOptions{Salvage: true, RequireInactive: true})
		if err != nil {
			return report, err
		}
		report.BranchesRemoved = append(report.BranchesRemoved, remaining.BranchesRemoved...)
		report.BranchesKept = append(report.BranchesKept, remaining.BranchesKept...)
		if remaining.Live > 0 || len(remaining.Kept) > 0 {
			return report, errors.New("worker ownership changed during pruning; the session was kept")
		}
		// The branches kept by Prune contain unique work; only the recovery pin is
		// obsolete when the user deliberately removes the session history.
		if _, err := repo.BranchSHA(ctx, pin); err == nil {
			if err := repo.DeleteBranch(ctx, pin); err != nil {
				return report, err
			}
		} else if !errors.Is(err, gitx.ErrNotFound) {
			return report, err
		}
		_ = os.Remove(record.Dir) // only the empty session cache directory
	}
	// Closing the lock before deleting it is needed on Windows. Mark the session
	// first so a resume that already resolved this path cannot start in that gap.
	if err := os.WriteFile(filepath.Join(dir, prunedMarker), nil, 0o600); err != nil {
		return report, err
	}
	unlock()
	return report, os.RemoveAll(dir)
}
