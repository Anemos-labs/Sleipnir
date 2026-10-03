package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Strategy is how a submission is combined with the integration tip.
type Strategy int

const (
	// StrategyMerge three-way merges the submitted commit into the tip. History
	// keeps every agent's commits and shows where each was integrated.
	StrategyMerge Strategy = iota
	// StrategyRebase replays the submission's commits onto the tip, so history is
	// linear.
	StrategyRebase
)

// String names the rebase strategy explicitly and treats other strategies as merge.
func (s Strategy) String() string {
	if s == StrategyRebase {
		return "rebase"
	}
	return "merge"
}

// QueueOptions configures a Queue.
type QueueOptions struct {
	Strategy Strategy
	// NoFF makes the merge strategy always create a merge commit, even when the tip
	// has not moved since the agent started and a fast-forward would do.
	NoFF bool
	// VerifyCmd is run in the integration tree after each combine, unless the
	// submission names its own. Empty means "no verification" (the queue then only
	// serializes and detects conflicts).
	VerifyCmd string
	// VerifyTimeout bounds one verification run (default 10 minutes).
	VerifyTimeout time.Duration
	// MaxVerifyOutput bounds the captured verifier output (default 64 KiB).
	MaxVerifyOutput int
	// Verify runs the command (default RunShell).
	Verify VerifyFunc
	// VerifyEnv is extra environment for the verifier.
	VerifyEnv []string
	// NoAutoCommit makes Submit refuse a tree with uncommitted changes instead of
	// committing them for the agent.
	NoAutoCommit bool
	// Resume continues an integration branch left by an earlier run of the same
	// session instead of failing on it.
	Resume bool
	// MaxPatchBytes bounds Finish (default 64 MiB).
	MaxPatchBytes int
	// OnEvent receives queue events (queued, merged, conflict, verify_failed,
	// rolled_back, rejected, fast_forward). Defaults to the manager's OnEvent.
	OnEvent EventFunc
}

// Submission is a finished tree offered to the queue.
type Submission struct {
	// Agent names the submitter (defaults to Tree.Agent).
	Agent string
	Tree  *Tree
	// Task identifies the work (a board task id or title): it appears in commit
	// messages, events and conflict advice.
	Task string
	// VerifyCmd overrides QueueOptions.VerifyCmd for this submission.
	VerifyCmd string
	// Message is the commit message for uncommitted work (default derived from
	// Task).
	Message string
	// Scope, with EnforceScope, refuses a submission that changed files outside it
	// (see Assert). Scope is checked against what this submission changed relative
	// to the integration tip, so work merged in from other agents does not count.
	Scope        []string
	EnforceScope bool
}

// Outcome is what happened to a submission.
type Outcome string

const (
	// OutcomeMerged: integrated, verified, and now the tip.
	OutcomeMerged Outcome = "merged"
	// OutcomeConflict: overlapping changes; nothing was integrated and the
	// integration tree is untouched. Result.Conflict says what to do.
	OutcomeConflict Outcome = "conflict"
	// OutcomeVerifyFailed: integrated, the verifier rejected the result, the tip
	// was rolled back. Result.Verify has the output.
	OutcomeVerifyFailed Outcome = "verify_failed"
	// OutcomeEmpty: nothing to integrate (no changes, or already integrated).
	OutcomeEmpty Outcome = "empty"
	// OutcomeRejected: refused before merging (out of scope, oversize files,
	// unresolved conflict markers, uncommitted changes with NoAutoCommit).
	OutcomeRejected Outcome = "rejected"
)

// Result reports a submission. Expected outcomes (conflict, failed verification)
// are results, not errors: errors from Submit are harness failures or a canceled
// context.
type Result struct {
	Outcome     Outcome
	Agent, Task string
	// Before and After are the integration tip around the submission; they differ
	// only for OutcomeMerged.
	Before, After string
	// Commits is how many commits the submission contributed; Files the paths it
	// changed (merged only).
	Commits int
	Files   []string
	// Conflict is set for OutcomeConflict.
	Conflict *Conflict
	// Verify is set when a verifier ran.
	Verify *VerifyResult
	// RolledBack is true when a failed verification was undone.
	RolledBack bool
	// Reason explains empty and rejected outcomes.
	Reason string
}

// Merged reports whether the submission is now part of the integration tip.
func (r *Result) Merged() bool { return r.Outcome == OutcomeMerged }

// Err converts a non-merged outcome to an error (nil for merged and empty).
func (r *Result) Err() error {
	switch r.Outcome {
	case OutcomeMerged, OutcomeEmpty:
		return nil
	case OutcomeConflict:
		return r.Conflict
	case OutcomeVerifyFailed:
		return &VerifyError{Result: *r.Verify}
	}
	return errors.New("workspace: submission rejected: " + r.Reason)
}

// VerifyError is a failed verification as an error.
type VerifyError struct{ Result VerifyResult }

// Error reports failed workspace verification with its summary and captured output.
func (e *VerifyError) Error() string {
	return "workspace: verification failed: " + e.Result.Summary() + "\n" + e.Result.Output
}

// Landed is one integrated submission.
type Landed struct {
	Agent, Task string
	// Commit is the integration tip after it landed; Before the tip before.
	Commit, Before string
	Files          []string
	Commits        int
}

// QueueEntry is a submission being processed or waiting.
type QueueEntry struct {
	Agent, Task string
	// Phase is "queued", "committing", "merging", "verifying", "publishing" or
	// "rolling back".
	Phase string
}

// QueueStatus is a snapshot of the queue.
type QueueStatus struct {
	// Branch is the integration branch; Path the integration tree; Base the commit
	// the session started from; Tip the last verified integration commit.
	Branch, Path, Base, Tip string
	// Healthy is false when the integration tree could not be restored; Broken says
	// why and Repair may fix it.
	Healthy bool
	Broken  string
	Active  *QueueEntry
	Waiting []QueueEntry
	Landed  []Landed
	// Counters since the queue was created.
	Merged, Conflicts, VerifyFailures, RolledBack, Rejected, Empty int
}

// Queue is the serial, verifying merge queue. One goroutine at a time integrates;
// the rest wait in arrival order. Create it with NewQueue.
type Queue struct {
	m      *Manager
	opts   QueueOptions
	branch string
	turn   turnstile

	mu      sync.Mutex // protects everything below
	tree    *Tree
	base    string
	tip     string
	ledger  []Landed
	stats   QueueStatus
	broken  error
	closed  bool
	active  *QueueEntry
	waiting []*QueueEntry
}

const maxLedger = 500

func (o QueueOptions) withDefaults(m *Manager) QueueOptions {
	if o.VerifyTimeout <= 0 {
		o.VerifyTimeout = 10 * time.Minute
	}
	if o.MaxVerifyOutput <= 0 {
		o.MaxVerifyOutput = 64 << 10
	}
	if o.Verify == nil {
		o.Verify = RunShell
	}
	if o.MaxPatchBytes <= 0 {
		o.MaxPatchBytes = 64 << 20
	}
	if o.OnEvent == nil {
		o.OnEvent = m.OnEvent
	}
	return o
}

// NewQueue creates the integration branch (<Prefix>/_integration, at the
// manager's base) and the integration tree, a detached worktree that all merging
// and verification happen in. The tree stays on a detached HEAD and the branch
// only ever moves to a commit that passed verification, so the branch is always a
// tip that is safe to hand to the user, even if the harness dies mid-verification.
func NewQueue(ctx context.Context, m *Manager, opts QueueOptions) (*Queue, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	q := &Queue{m: m, opts: opts.withDefaults(m), branch: m.st.prefix + "/" + integrationName}
	base := m.st.baseSHA
	tip, err := m.st.base.BranchSHA(ctx, q.branch)
	switch {
	case err == nil:
		if tip != base && !opts.Resume {
			return nil, fmt.Errorf("%w: integration branch %s already holds work from an earlier run (use Resume, or Prune)", ErrExists, q.branch)
		}
		q.tip, q.base = tip, base
		if ok, err := m.st.base.IsAncestor(ctx, base, tip); err != nil {
			return nil, err
		} else if !ok {
			if q.base, err = m.st.base.MergeBase(ctx, base, tip); err != nil {
				return nil, err
			}
		}
	case gitx.KindOf(err) == gitx.KindNotFound:
		if err := m.st.base.UpdateBranch(ctx, q.branch, base, "", "sleipnir: integration branch"); err != nil {
			return nil, err
		}
		q.tip, q.base = base, base
	default:
		return nil, err
	}
	if err := q.openTree(ctx); err != nil {
		return nil, err
	}
	return q, nil
}

// openTree creates the integration worktree at the current tip, clearing a stale
// one from a dead earlier run.
func (q *Queue) openTree(ctx context.Context) error {
	m := q.m
	spec := treeSpec{integration: true, ref: "refs/heads/" + q.branch}
	t, err := m.create(ctx, integrationName, CreateOptions{Detach: true, Base: q.tip}, spec)
	if errors.Is(err, ErrExists) {
		if rerr := m.removeStaleIntegration(ctx); rerr != nil {
			return errors.Join(err, rerr)
		}
		t, err = m.create(ctx, integrationName, CreateOptions{Detach: true, Base: q.tip}, spec)
	}
	if err != nil {
		return err
	}
	q.mu.Lock()
	q.tree = t
	q.mu.Unlock()
	return nil
}

// removeStaleIntegration deletes the integration tree of a dead earlier run.
func (m *Manager) removeStaleIntegration(ctx context.Context) error {
	dest := filepath.Join(m.st.dirReal, integrationName)
	m.st.lock.Lock()
	defer m.st.lock.Unlock()
	_, mk, err := m.verifyTreeDir(ctx, dest)
	if errors.Is(err, errMissingDir) {
		_, mk, err = m.verifyMissing(dest)
	}
	if err != nil {
		return err
	}
	if !mk.Integration || mk.owner() == ownerAlive {
		return fmt.Errorf("%w: the integration tree at %s belongs to a running session", ErrExists, dest)
	}
	m.mu.Lock()
	delete(m.trees, integrationName)
	m.mu.Unlock()
	// The dead run may have died while moving the branch; git leaves the lock file of
	// a process that did not finish, and it would block every publish of the new one.
	_ = os.Remove(filepath.Join(m.st.base.CommonDir(), "refs", "heads", filepath.FromSlash(m.st.prefix), integrationName+".lock"))
	return m.st.base.WorktreeRemove(ctx, dest, true)
}

// Branch is the integration branch name.
func (q *Queue) Branch() string { return q.branch }

// Base is the commit the session started from.
func (q *Queue) Base() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.base
}

// Tip is the last verified integration commit. New agents should be created with
// CreateOptions{Base: q.Tip()} so they start from fresh code.
func (q *Queue) Tip() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.tip
}

// Status returns a snapshot.
func (q *Queue) Status() QueueStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.stats
	st.Branch, st.Base, st.Tip = q.branch, q.base, q.tip
	if q.tree != nil {
		st.Path = q.tree.Path
	}
	st.Healthy = q.broken == nil && !q.closed
	if q.broken != nil {
		st.Broken = q.broken.Error()
	}
	if q.active != nil {
		a := *q.active
		st.Active = &a
	}
	for _, w := range q.waiting {
		st.Waiting = append(st.Waiting, *w)
	}
	st.Landed = append([]Landed(nil), q.ledger...)
	return st
}

// emit forwards merge-queue events through the configured event hook.
func (q *Queue) emit(typ, agent, task string, data map[string]any) {
	q.opts.OnEvent.emit(typ, agent, task, data)
}

// author returns the merge queue's Git author identity stamped with the manager clock.
func (q *Queue) author() gitx.Author {
	return gitx.Author{Name: "Sleipnir merge queue", Email: "queue@sleipnir.invalid", When: q.m.now()}
}

// setPhase updates a queue entry's phase under the queue lock.
func (q *Queue) setPhase(e *QueueEntry, phase string) {
	q.mu.Lock()
	e.Phase = phase
	q.mu.Unlock()
}

// turnstile is a FIFO mutex: waiters are served in arrival order, so submissions
// integrate in the order agents finished (sync.Mutex makes no such promise).
type turnstile struct {
	mu   sync.Mutex
	busy bool
	q    []chan struct{}
}

func (t *turnstile) acquire(ctx context.Context) error {
	t.mu.Lock()
	if !t.busy {
		t.busy = true
		t.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	t.q = append(t.q, ch)
	t.mu.Unlock()
	select {
	case <-ch:
		return nil // the turn was handed to us; busy stays true
	case <-ctx.Done():
		t.mu.Lock()
		for i, c := range t.q {
			if c == ch {
				t.q = append(t.q[:i], t.q[i+1:]...)
				t.mu.Unlock()
				return ctx.Err()
			}
		}
		t.mu.Unlock()
		// We were handed the turn just as the context ended: pass it on.
		t.release()
		return ctx.Err()
	}
}

func (t *turnstile) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.q) > 0 {
		next := t.q[0]
		t.q = t.q[1:]
		close(next)
		return
	}
	t.busy = false
}

// Submit integrates a finished tree. It blocks until every earlier submission is
// done, then, alone:
//
//  1. commits the tree's uncommitted work (unless NoAutoCommit);
//  2. merges (or rebases) the commit onto the integration tip in the integration
//     tree. A textual conflict aborts cleanly - the integration tree ends exactly
//     where it began - and comes back as Result.Conflict;
//  3. runs the verification command there. On failure the tip is rolled back and
//     the verifier's output is returned in Result.Verify;
//  4. only then advances the integration branch (compare-and-swap).
//
// The integration branch therefore only ever names verified commits. Errors are
// harness failures or a canceled context; everything the queue expects to happen
// is a Result.
func (q *Queue) Submit(ctx context.Context, s Submission) (*Result, error) {
	if s.Tree == nil {
		return nil, errors.New("workspace: submission has no tree")
	}
	if s.Tree.m != q.m {
		return nil, fmt.Errorf("%w: the tree was created by another manager", ErrForeign)
	}
	if s.Tree.integration {
		return nil, fmt.Errorf("%w: the integration tree cannot be submitted", ErrForeign)
	}
	if s.Agent == "" {
		s.Agent = s.Tree.Agent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry := &QueueEntry{Agent: s.Agent, Task: s.Task, Phase: "queued"}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil, ErrClosed
	}
	q.waiting = append(q.waiting, entry)
	position := len(q.waiting)
	if q.active != nil {
		position++
	}
	q.mu.Unlock()
	q.emit(EventQueued, s.Agent, s.Task, map[string]any{"position": position})

	err := q.turn.acquire(ctx)
	q.mu.Lock()
	for i, w := range q.waiting {
		if w == entry {
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			break
		}
	}
	if err != nil {
		q.mu.Unlock()
		return nil, err
	}
	q.active = entry
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.active = nil
		q.mu.Unlock()
		q.turn.release()
	}()

	q.mu.Lock()
	closed, broken := q.closed, q.broken
	q.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	if broken != nil {
		if err := q.repairLocked(ctx); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBroken, broken)
		}
	}
	return q.integrate(ctx, s, entry)
}

// oneLine collapses whitespace and truncates by bytes without an omission marker; max must be
// nonnegative.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func (q *Queue) integrate(ctx context.Context, s Submission, entry *QueueEntry) (*Result, error) {
	m := q.m
	// History queries go through the repository the trees belong to, not through the
	// integration tree: they must keep working while that tree is damaged (settle
	// below is what repairs it).
	hist := m.st.base
	res := &Result{Agent: s.Agent, Task: s.Task}
	q.mu.Lock()
	prev := q.tip
	q.mu.Unlock()
	res.Before, res.After = prev, prev

	// 1. The submission must be a commit.
	q.setPhase(entry, "committing")
	theirs, reject, err := q.commitSubmission(ctx, s)
	if err != nil {
		return nil, err
	}
	if reject != "" {
		return q.reject(res, reject), nil
	}
	if theirs == prev {
		return q.empty(res, "the submission is the integration tip itself"), nil
	}
	if ok, err := hist.IsAncestor(ctx, theirs, prev); err != nil {
		return nil, err
	} else if ok {
		return q.empty(res, "everything in the submission is already integrated"), nil
	}
	mb, err := hist.MergeBase(ctx, prev, theirs)
	if err != nil {
		if gitx.KindOf(err) == gitx.KindNotFound {
			return q.reject(res, "the submission shares no history with the integration branch"), nil
		}
		return nil, err
	}

	// 1b. What the agent committed itself meets the guards that what we commit for it
	// does: nothing oversized or unexplained enters the project's history.
	if err := m.checkCommitted(ctx, hist, mb, theirs); err != nil {
		var tl *TooLargeError
		var nr *NestedRepoError
		switch {
		case errors.As(err, &tl):
			return q.reject(res, tl.Error()), nil
		case errors.As(err, &nr):
			return q.reject(res, nr.Error()), nil
		}
		return nil, err
	}

	// 2. Scope, judged on what this submission itself changed.
	if s.EnforceScope && len(s.Scope) > 0 {
		d, err := hist.Diff(ctx, mb, gitx.DiffOptions{To: theirs, NoPatch: true, MaxFiles: 1 << 20})
		if err != nil {
			return nil, err
		}
		var files []string
		for _, f := range d.Files {
			files = append(files, f.Path)
			if f.OldPath != "" {
				files = append(files, f.OldPath)
			}
		}
		if out := OutOfScope(files, s.Scope); len(out) > 0 {
			res.Files = out
			return q.reject(res, fmt.Sprintf("changed %d file(s) outside its scope: %s", len(out), strings.Join(firstN(out, 8), ", "))), nil
		}
	}

	// 3. Combine, in a tree that is exactly at the tip and clean. settle may have to
	// rebuild the tree, so the handle is taken afterwards, never before.
	q.setPhase(entry, "merging")
	if err := q.settle(ctx, prev); err != nil {
		return nil, err
	}
	repo := q.tree.repo
	var out *gitx.MergeResult
	switch q.opts.Strategy {
	case StrategyRebase:
		out, err = repo.Rebase(ctx, gitx.RebaseOptions{Onto: prev, Upstream: mb, Branch: theirs, Author: q.author()})
	default:
		msg := "Merge " + s.Agent
		if t := oneLine(s.Task, 100); t != "" {
			msg += ": " + t
		}
		out, err = repo.Merge(ctx, gitx.MergeOptions{Ref: theirs, Message: msg, NoFF: q.opts.NoFF, Author: q.author()})
	}
	if err != nil {
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return nil, err
	}
	if out.Conflicted {
		c, cerr := buildConflict(ctx, repo, conflictOpts{agent: s.Agent, task: s.Task, tip: prev, theirs: theirs, mergeBase: mb, landedBy: q.landedBy(ctx, mb, prev)})
		q.setPhase(entry, "rolling back")
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			return nil, errors.Join(rerr, cerr)
		}
		if cerr != nil {
			return nil, cerr
		}
		res.Outcome, res.Conflict = OutcomeConflict, c
		q.mu.Lock()
		q.stats.Conflicts++
		q.mu.Unlock()
		q.emit(EventConflict, s.Agent, s.Task, map[string]any{"files": c.Files, "hunks": len(c.Hunks), "tip": prev, "theirs": theirs})
		return res, nil
	}
	after := out.Head
	if out.UpToDate || after == prev {
		if err := q.settle(ctx, prev); err != nil {
			return nil, err
		}
		return q.empty(res, "the merge changed nothing"), nil
	}
	res.After = after
	if files, err := q.changedBetween(ctx, prev, after); err == nil {
		res.Files = files
	} else {
		_ = q.rollbackTo(ctx, prev)
		return nil, err
	}
	res.Commits, _ = hist.CountCommits(ctx, mb, theirs)

	// 4. Verify the combined result.
	cmd := s.VerifyCmd
	if cmd == "" {
		cmd = q.opts.VerifyCmd
	}
	if cmd != "" {
		q.setPhase(entry, "verifying")
		vr := q.opts.Verify(ctx, VerifyRequest{
			Dir: q.tree.Path, Cmd: cmd, Agent: s.Agent, Task: s.Task,
			Timeout: q.opts.VerifyTimeout, MaxOutput: q.opts.MaxVerifyOutput, Env: q.opts.VerifyEnv,
		})
		res.Verify = &vr
		if err := ctx.Err(); err != nil {
			q.setPhase(entry, "rolling back")
			if rerr := q.rollbackTo(ctx, prev); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return nil, err
		}
		if !vr.OK() {
			q.emit(EventVerifyFailed, s.Agent, s.Task, map[string]any{
				"cmd": vr.Cmd, "exit_code": vr.ExitCode, "timed_out": vr.TimedOut, "output": tailOf(vr.Output, 4000),
			})
			q.setPhase(entry, "rolling back")
			if rerr := q.rollbackTo(ctx, prev); rerr != nil {
				return nil, rerr
			}
			res.Outcome, res.RolledBack = OutcomeVerifyFailed, true
			res.After, res.Files = prev, nil
			q.mu.Lock()
			q.stats.VerifyFailures++
			q.stats.RolledBack++
			q.mu.Unlock()
			q.emit(EventRolledBack, s.Agent, s.Task, map[string]any{"tip": prev})
			return res, nil
		}
	}

	// 5. Publish: the branch only ever moves to a verified commit, and only from the
	// tip we expect. A caller that gave up before this point costs nothing but the
	// merge, which is undone; after it, the move is not interruptible (see publish).
	q.setPhase(entry, "publishing")
	if err := ctx.Err(); err != nil {
		q.setPhase(entry, "rolling back")
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return nil, err
	}
	if err := q.publish(ctx, after, prev, s.Agent); err != nil {
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return nil, err
	}
	q.mu.Lock()
	q.tip = after
	q.ledger = append(q.ledger, Landed{Agent: s.Agent, Task: s.Task, Commit: after, Before: prev, Files: res.Files, Commits: res.Commits})
	if len(q.ledger) > maxLedger {
		q.ledger = append([]Landed(nil), q.ledger[len(q.ledger)-maxLedger:]...)
	}
	q.stats.Merged++
	q.mu.Unlock()
	res.Outcome = OutcomeMerged
	q.emit(EventMerged, s.Agent, s.Task, map[string]any{
		"before": prev, "after": after, "commits": res.Commits, "files": firstN(res.Files, 50), "file_count": len(res.Files),
		"strategy": q.opts.Strategy.String(), "verified": cmd != "",
	})
	return res, nil
}

// publishTimeout bounds the move of the integration branch, a single ref update
// that takes milliseconds unless the disk is in trouble.
const publishTimeout = time.Minute

// publish moves the integration branch from prev to after (compare-and-swap), and
// cannot be cancelled by the caller once it starts. The queue's bookkeeping is
// only correct if the branch and q.tip agree after every submission, and a git
// process that is stopped half way through the update leaves either nothing
// changed (plus, if it was killed rather than asked, a stale lock that blocks
// every later publish) or the branch already moved while the caller is told it
// was not. So the update runs on its own bounded context, and if it still fails
// (timeout, killed) the branch itself is asked what happened: a branch that is at
// after was published, whatever the error said.
func (q *Queue) publish(ctx context.Context, after, prev, agent string) error {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	base := q.m.st.base
	err := base.UpdateBranch(pctx, q.branch, after, prev, "sleipnir: integrate "+agent)
	if err == nil {
		return nil
	}
	// The update's own deadline may be what failed, so the question gets a fresh one.
	qctx, qcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer qcancel()
	if cur, berr := base.BranchSHA(qctx, q.branch); berr == nil && cur == after {
		return nil
	}
	if gitx.KindOf(err) == gitx.KindConflict {
		return fmt.Errorf("workspace: the integration branch moved underneath the queue: %w", err)
	}
	return fmt.Errorf("workspace: publishing the integration branch failed: %w", err)
}

// firstN returns a shared prefix of at most n strings; n must be nonnegative.
func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// tailOf retains the last n bytes with a leading truncation marker when needed; n must be
// nonnegative.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// reject marks a result rejected, updates the counter under lock, and emits its rejection reason.
func (q *Queue) reject(res *Result, reason string) *Result {
	res.Outcome, res.Reason = OutcomeRejected, reason
	q.mu.Lock()
	q.stats.Rejected++
	q.mu.Unlock()
	q.emit(EventRejected, res.Agent, res.Task, map[string]any{"reason": reason})
	return res
}

// empty marks a result empty, updates the counter under lock, and emits a rejection event tagged
// as empty.
func (q *Queue) empty(res *Result, reason string) *Result {
	res.Outcome, res.Reason = OutcomeEmpty, reason
	q.mu.Lock()
	q.stats.Empty++
	q.mu.Unlock()
	q.emit(EventRejected, res.Agent, res.Task, map[string]any{"reason": reason, "empty": true})
	return res
}

// commitSubmission returns the submission's commit, committing pending work first.
// A non-empty reject string means the submission is refused for a reason its
// author can fix.
func (q *Queue) commitSubmission(ctx context.Context, s Submission) (sha, reject string, err error) {
	t := s.Tree
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.check(); err != nil {
		return "", "", err
	}
	clean, err := t.repo.IsClean(ctx)
	if err != nil {
		return "", "", err
	}
	if !clean {
		if q.opts.NoAutoCommit {
			return "", "the tree has uncommitted changes; commit them first", nil
		}
		msg := s.Message
		if strings.TrimSpace(msg) == "" {
			msg = "sleipnir: " + s.Agent
			if task := oneLine(s.Task, 100); task != "" {
				msg += ": " + task
			}
		}
		if _, err := t.commitLocked(ctx, msg); err != nil {
			var tl *TooLargeError
			var nr *NestedRepoError
			switch {
			case errors.As(err, &tl):
				return "", tl.Error(), nil
			case errors.As(err, &nr):
				return "", nr.Error(), nil
			case errors.Is(err, gitx.ErrConflict):
				return "", "unresolved conflict markers: " + oneLine(err.Error(), 400), nil
			}
			return "", "", err
		}
	}
	if t.repo.InProgress() != "" {
		return "", "the tree is in the middle of a " + t.repo.InProgress() + "; finish or abort it first", nil
	}
	sha, err = t.repo.Head(ctx)
	return sha, "", err
}

// landedBy returns a lookup from path to the agents whose already-integrated
// changes (landed after mb) touch it.
func (q *Queue) landedBy(ctx context.Context, mb, tip string) func(string) []string {
	res, err := q.m.st.base.Git(ctx, "rev-list", "--max-count=5000", mb+".."+tip)
	if err != nil {
		return nil
	}
	after := map[string]bool{}
	for _, l := range strings.Fields(res.Stdout) {
		after[l] = true
	}
	q.mu.Lock()
	ledger := append([]Landed(nil), q.ledger...)
	q.mu.Unlock()
	return func(path string) []string {
		seen := map[string]bool{}
		var who []string
		for _, e := range ledger {
			if !after[e.Commit] {
				continue
			}
			for _, f := range e.Files {
				if f == path && !seen[e.Agent] {
					seen[e.Agent] = true
					who = append(who, e.Agent)
				}
			}
		}
		sort.Strings(who)
		return who
	}
}

func (q *Queue) changedBetween(ctx context.Context, a, b string) ([]string, error) {
	d, err := q.m.st.base.Diff(ctx, a, gitx.DiffOptions{To: b, NoPatch: true, MaxFiles: 1 << 20})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range d.Files {
		for _, p := range []string{f.OldPath, f.Path} {
			if p != "" && !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// settle makes sure the integration tree is at commit to and clean.
func (q *Queue) settle(ctx context.Context, to string) error {
	repo := q.tree.repo
	if repo.InProgress() == "" {
		head, herr := repo.Head(ctx)
		clean, cerr := repo.IsClean(ctx)
		if herr == nil && cerr == nil && head == to && clean {
			return nil
		}
	}
	return q.rollbackTo(ctx, to)
}

// rollbackTo restores the integration tree to commit to: it abandons any merge or
// rebase in progress, hard-resets, removes untracked files, then *checks* the
// result. It must complete even if the caller's context is gone (a canceled
// submission must not leave a half-merged tree for the next one), so it runs on a
// detached, time-limited context. If the tree cannot be restored the queue
// rebuilds it from scratch; if that fails too the queue is marked broken.
func (q *Queue) rollbackTo(ctx context.Context, to string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	err := q.restore(cctx, to)
	if err == nil {
		return nil
	}
	if rerr := q.rebuild(cctx, to); rerr != nil {
		err = fmt.Errorf("%w: %v; rebuilding the integration tree failed: %v", ErrBroken, err, rerr)
		q.mu.Lock()
		q.broken = err
		q.mu.Unlock()
		return err
	}
	return nil
}

func (q *Queue) restore(ctx context.Context, to string) error {
	repo := q.tree.repo
	if repo.InProgress() != "" {
		_ = repo.Abort(ctx) // git returns to a place of its choosing; the reset below is what counts
	}
	if err := repo.ResetHard(ctx, to); err != nil {
		return err
	}
	if err := repo.CleanUntracked(ctx); err != nil {
		return err
	}
	head, err := repo.Head(ctx)
	if err != nil {
		return err
	}
	if head != to {
		return fmt.Errorf("integration tree is at %s after reset, want %s", head, to)
	}
	clean, err := repo.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("integration tree is not clean after reset")
	}
	return nil
}

// rebuild throws the integration tree away and makes a new one at commit to.
func (q *Queue) rebuild(ctx context.Context, to string) error {
	if q.tree != nil {
		if err := q.m.removeTree(ctx, q.tree, true); err != nil {
			return err
		}
	}
	q.mu.Lock()
	saved := q.tip
	q.tip = to
	q.mu.Unlock()
	err := q.openTree(ctx)
	q.mu.Lock()
	q.tip = saved
	q.mu.Unlock()
	return err
}

// Repair rebuilds a broken integration tree at the current tip. Submit does this
// on its own before accepting more work; Repair exists for callers that want to
// know.
func (q *Queue) Repair(ctx context.Context) error {
	if err := q.turn.acquire(ctx); err != nil {
		return err
	}
	defer q.turn.release()
	return q.repairLocked(ctx)
}

func (q *Queue) repairLocked(ctx context.Context) error {
	q.mu.Lock()
	tip := q.tip
	q.mu.Unlock()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	if err := q.rebuild(cctx, tip); err != nil {
		return err
	}
	q.mu.Lock()
	q.broken = nil
	q.mu.Unlock()
	return nil
}

// Finish returns the session's result as a patch: everything the integration tip
// changed relative to the base, binary-safe and applicable with `git apply` (or
// Repo.Apply) to a checkout of the base - or, with a Snapshot base, to the user's
// working tree, since the user's own edits are part of the base and so not in the
// patch. It waits for the submission in progress. The integration branch holds
// the same result as commits for hand-back by merge.
func (q *Queue) Finish(ctx context.Context) (string, error) {
	if err := q.turn.acquire(ctx); err != nil {
		return "", err
	}
	defer q.turn.release()
	q.mu.Lock()
	base, tip := q.base, q.tip
	q.mu.Unlock()
	if base == tip {
		return "", nil
	}
	d, err := q.m.st.base.Diff(ctx, base, gitx.DiffOptions{To: tip, Renames: true, MaxPatchBytes: q.opts.MaxPatchBytes})
	if err != nil {
		return "", err
	}
	if d.Truncated {
		return "", fmt.Errorf("%w: the result is larger than %d bytes as a patch; hand back the branch %s instead", ErrTooLarge, q.opts.MaxPatchBytes, q.branch)
	}
	return d.Patch, nil
}

// fastForwardTimeout bounds the update of the user's branch and work tree.
const fastForwardTimeout = 5 * time.Minute

// FastForwardResult reports FastForward.
type FastForwardResult struct {
	// Branch is the user's branch that moved (empty when nothing had to move).
	Branch   string
	From, To string
}

// FastForward moves the user's checked-out branch to the integration tip, and
// updates their working tree with it. It is only ever done on explicit request and
// only when nothing can be lost: the user's work tree must be clean (paths under
// Manager.Dir do not count), HEAD must be on a branch, and that branch must be an
// ancestor of the tip (the user has not committed anything the session did not
// see). Otherwise it fails (ErrDirty, ErrNotFastForward) and changes nothing;
// Finish's patch or the integration branch remain the way to hand the result back.
// In copy mode there is no user repository to move.
func (q *Queue) FastForward(ctx context.Context) (*FastForwardResult, error) {
	m := q.m
	if m.st.mode != ModeWorktree || m.Repo == nil {
		return nil, fmt.Errorf("%w: copy mode has no repository branch to fast-forward; apply Finish's patch", ErrUnsupported)
	}
	if err := q.turn.acquire(ctx); err != nil {
		return nil, err
	}
	defer q.turn.release()
	q.mu.Lock()
	tip := q.tip
	q.mu.Unlock()
	r := m.Repo

	branch, err := r.Branch(ctx)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("%w: HEAD is detached, there is no branch to move", ErrNotFastForward)
	}
	head, err := r.BranchSHA(ctx, branch)
	if err != nil {
		return nil, err
	}
	if head == tip {
		return &FastForwardResult{From: head, To: tip}, nil
	}
	ok, err := r.IsAncestor(ctx, head, tip)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s has commits the session did not start from; merge %s or apply Finish's patch", ErrNotFastForward, branch, q.branch)
	}
	if !r.IsBare() {
		// "all": with collapsed directories, a Dir inside the repository would hide
		// behind its parent (.sleipnir/) and look like user content.
		st, err := r.StatusWith(ctx, gitx.StatusOptions{Untracked: "all"})
		if err != nil {
			return nil, err
		}
		if dirty := m.dirtyPaths(st); len(dirty) > 0 {
			return nil, fmt.Errorf("%w: the working tree has %d uncommitted change(s) (%s); commit or stash them first", ErrDirty, len(dirty), strings.Join(firstN(dirty, 5), ", "))
		}
	}
	// Past the last check the move cannot be interrupted by the caller: it rewrites
	// files of the user's own working tree, where a half-done update is worse than
	// either end state. It runs on its own generous bound instead.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fastForwardTimeout)
	defer cancel()
	if r.IsBare() {
		if err := r.UpdateBranch(mctx, branch, tip, head, "sleipnir: fast-forward to the integration tip"); err != nil {
			return nil, err
		}
	} else if _, err := r.Merge(mctx, gitx.MergeOptions{Ref: tip, FFOnly: true}); err != nil {
		return nil, err
	}
	q.emit(EventFastForward, "", "", map[string]any{"branch": branch, "from": head, "to": tip})
	return &FastForwardResult{Branch: branch, From: head, To: tip}, nil
}

// dirtyPaths lists the changes that block a fast-forward: everything except
// untracked entries that lie under the workspace directory (a Dir inside the
// repository is our own doing).
func (m *Manager) dirtyPaths(st *gitx.Status) []string {
	var out []string
	for _, c := range st.Staged {
		out = append(out, c.Path)
	}
	for _, c := range st.Unstaged {
		out = append(out, c.Path)
	}
	for _, c := range st.Conflicted {
		out = append(out, c.Path)
	}
	root := m.Repo.Root()
	for _, u := range st.Untracked {
		abs := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(u, "/")))
		if strictlyUnder(m.st.dirReal, resolveLoose(abs)) || samePath(abs, m.st.dirReal) {
			continue
		}
		out = append(out, u)
	}
	if st.Truncated {
		out = append(out, "(more)")
	}
	sort.Strings(out)
	return out
}

// Close removes the integration tree (the integration branch and its commits stay:
// they are the session's result). Further submissions fail with ErrClosed.
func (q *Queue) Close(ctx context.Context) error {
	if err := q.turn.acquire(ctx); err != nil {
		return err
	}
	defer q.turn.release()
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	t := q.tree
	q.mu.Unlock()
	if t == nil {
		return nil
	}
	return q.m.removeTree(ctx, t, true)
}
