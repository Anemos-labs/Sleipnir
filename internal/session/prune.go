package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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

// A session is a directory of the state directory: its event log, the blobs the log points at, the checkpoints. Nothing deletes one
// by itself, and a person who uses the harness every day has some thousands of them within a year. Prune is how they go: the old
// ones, never the newest few, never one that may still be written to; RemoveRecorded deletes the ones a person chose. Both delete
// through RemoveStoredSession, which takes the session's lock, salvages the edits left in worker trees as Git branches and reports
// the branches it kept.

// PruneInUse is how recently a session's log must have been written for prune to take it for one that is running.
const PruneInUse = 10 * time.Minute

// PrunePlan is what a prune deletes, and what it leaves and why.
type PrunePlan struct {
	// Delete are the sessions to delete, oldest first; Bytes their size.
	Delete []Recorded
	Bytes  int64
	// Total counts the sessions found; KeptNewest the newest ones kept whatever their age (live ones among them).
	Total, KeptNewest int
	// InUse are sessions old enough but written to within PruneInUse; Locked old enough but held by a process (PlanPrune only);
	// Skipped are directories that are not sessions (no event log): never touched.
	InUse, Locked, Skipped []string
}

// PlanPrune applies the prune rule to the state directory's sessions: older than olderThan, not among the newest keep, not written
// in the last ten minutes. The sessions named in live (the ones this process hosts) count toward keep, as the newest, and are never
// deleted; a session another process holds is moved to Locked. A state directory without sessions gives an empty plan. The rows
// to delete carry what a listing shows of them.
func PlanPrune(home string, now time.Time, olderThan time.Duration, keep int, live []string) (*PrunePlan, error) {
	p, err := PlanPruneDir(SessionsDir(home), now, olderThan, keep, live)
	if errors.Is(err, fs.ErrNotExist) {
		return &PrunePlan{}, nil
	}
	if err != nil {
		return nil, err
	}
	kept := p.Delete[:0]
	for _, r := range p.Delete {
		if Locked(r.Dir) {
			p.Locked = append(p.Locked, r.ID)
			p.Bytes -= r.Bytes
			continue
		}
		if fi, err := os.Lstat(r.Dir); err == nil {
			if log, err := os.Stat(filepath.Join(r.Dir, "events.jsonl")); err == nil {
				full := recordedOf(r.ID, r.Dir, fi, log, readListing(r.Dir, fi, log))
				full.Bytes, full.LastWritten = r.Bytes, r.LastWritten
				r = full
			}
		}
		kept = append(kept, r)
	}
	p.Delete = kept
	return p, nil
}

// PlanPruneDir is the prune rule over the sessions directory root, as `sleipnir sessions prune` applies it: every session whose
// newest file (the directory or its log) is older than olderThan, except the newest keep sessions and any written to within
// PruneInUse of now. The sessions named in live count as the newest and are never deleted. A directory without an events.jsonl is not
// a session and is left alone (the state directory is the person's, and may hold other things), and so is anything that is not a
// plain directory. Locks are not probed. The error of reading root is returned as it is (fs.ErrNotExist when there is none).
func PlanPruneDir(root string, now time.Time, olderThan time.Duration, keep int, live []string) (*PrunePlan, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	isLive := map[string]bool{}
	for _, id := range live {
		isLive[id] = true
	}
	type sess struct {
		r    Recorded
		age  time.Duration
		live bool
	}
	var all []sess
	p := &PrunePlan{}
	for _, e := range ents {
		path := filepath.Join(root, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() { // a file, or a link somebody made: not ours to delete
			continue
		}
		log, err := os.Stat(filepath.Join(path, "events.jsonl"))
		if err != nil {
			p.Skipped = append(p.Skipped, e.Name())
			continue
		}
		mod := fi.ModTime()
		if log.ModTime().After(mod) {
			mod = log.ModTime()
		}
		all = append(all, sess{r: Recorded{ID: e.Name(), Dir: path, LastWritten: mod}, age: max(now.Sub(mod), 0), live: isLive[e.Name()]})
	}
	p.Total = len(all)
	sort.Slice(all, func(i, j int) bool { // newest first, the live ones before the rest
		if all[i].live != all[j].live {
			return all[i].live
		}
		return all[i].r.LastWritten.After(all[j].r.LastWritten)
	})
	for i, s := range all {
		switch {
		case i < keep || s.live:
			p.KeptNewest++
		case s.age < olderThan:
		case s.age < PruneInUse:
			p.InUse = append(p.InUse, s.r.ID)
		default:
			s.r.Bytes = DirSize(s.r.Dir)
			p.Delete = append(p.Delete, s.r)
			p.Bytes += s.r.Bytes
		}
	}
	for i, j := 0, len(p.Delete)-1; i < j; i, j = i+1, j-1 { // oldest first
		p.Delete[i], p.Delete[j] = p.Delete[j], p.Delete[i]
	}
	sort.Strings(p.Skipped)
	return p, nil
}

// ParseAge reads a duration the way a person writes one for a directory of old things: 36h, 90m, 30d, 2w, or 0.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			v, err := strconv.ParseFloat(n, 64)
			if err != nil || v < 0 {
				return 0, fmt.Errorf("%q is not an age (try 30d, 36h or 2w)", s)
			}
			return time.Duration(v * float64(unit)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q is not an age (try 30d, 36h or 2w)", s)
	}
	return d, nil
}

// Removal is the outcome of deleting one recorded session: its size, the Git branches kept because they hold work no other branch
// has, and the error that kept it when it could not be deleted.
type Removal struct {
	ID    string
	Bytes int64
	Kept  []workspace.PruneAction
	Err   error
}

// removeTimeout bounds the deletion of one session (salvaging worker trees runs git).
const removeTimeout = 3 * time.Minute

// RemoveRecorded deletes the named sessions of the state directory, one at a time, each through RemoveStoredSession (its lock, its
// salvage of worker edits, its report of the branches kept). An id that is not a session id, or names no session directory, is
// refused without touching anything; a session that a process holds is refused by its lock. The removals are in the order of ids.
func RemoveRecorded(ctx context.Context, home string, ids []string) []Removal {
	root := SessionsDir(home)
	out := make([]Removal, 0, len(ids))
	for _, id := range ids {
		rm := Removal{ID: id}
		if !ValidID(id) { // checked before the id is joined into a path
			rm.Err = fmt.Errorf("%q is not a session id", id)
			out = append(out, rm)
			continue
		}
		dir := filepath.Join(root, id)
		switch fi, err := os.Lstat(dir); {
		case err != nil:
			rm.Err = fmt.Errorf("no session %s", id)
		case !fi.IsDir() || !hasLog(dir):
			rm.Err = fmt.Errorf("%s is not a session directory", id)
		}
		if rm.Err == nil {
			rm.Bytes = DirSize(dir)
			rctx, cancel := context.WithTimeout(ctx, removeTimeout)
			report, err := RemoveStoredSession(rctx, dir)
			cancel()
			if report != nil {
				rm.Kept = report.BranchesKept
			}
			rm.Err = err
			listCache.Lock()
			delete(listCache.m, dir)
			listCache.Unlock()
		}
		out = append(out, rm)
	}
	return out
}
