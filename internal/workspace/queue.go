package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	// rolled_back, rejected, fast_forward, batch). Defaults to the manager's OnEvent.
	OnEvent EventFunc
	// MaxBatch is how many waiting submissions one verification may cover (default 8; 1 integrates one at a time).
	// The queue is serial and the verifier usually dominates its time, so a queue that verified each submission alone
	// could land at most one per verification however many agents fed it. When several submissions are waiting, they
	// are merged onto the tip one after another and the combined result is verified once: on a pass they all land, on a
	// failure the tip is untouched and the batch is split in halves that are tried again, down to single submissions,
	// which are judged exactly as without batching. Only submissions with the same verify command are batched, unless
	// CombineVerify names one command for them all.
	MaxBatch int
	// CombineVerify, when set, gives the verify command for a batch of submissions whose own commands differ (each
	// submission's command is subs[i].VerifyCmd, or VerifyCmd when it names none), run in dir, the integration tree:
	// for example one command over the union of the directories each would check. ok false: they are not batched.
	CombineVerify func(dir string, subs []Submission) (cmd string, ok bool)
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
	// VerifyScope is what VerifyCmd was made for (for example the task's paths), for QueueOptions.CombineVerify; the
	// queue itself does not read it.
	VerifyScope []string
	// Message is the commit message for uncommitted work (default derived from
	// Task).
	Message string
	// Scope, with EnforceScope, refuses a submission that changed files outside it
	// (see OutOfScopeForMerge: a regenerated dependency lockfile such as Cargo.lock
	// does not count). Scope is checked against what this submission changed relative
	// to the integration tip, so work merged in from other agents does not count.
	Scope        []string
	EnforceScope bool
	// AllowOutside, when set, lets a file outside Scope through when it answers true for it (the swarm: a file no
	// other task in flight claims, in a run where every writer has a tree of its own).
	AllowOutside func(path string) bool
	// Refuse, when set, is asked about every file the submission changed (judged
	// like Scope, on the submission's own changes, renames at both ends); a file it
	// answers with a non-empty reason is refused, and the submission is rejected
	// with the reasons. The swarm uses it for paths that only another role may
	// change.
	Refuse func(path string) string
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
	// verifies are the verifications the queue ran, oldest first (at most maxLedger).
	verifies []VerifyRecord
	// pending are the submissions waiting for their turn, in arrival order. Whoever has the turn takes a batch of them
	// (its own first or among the first) and integrates it; the others find their result delivered when their turn comes.
	pending []*job
	// held, while a batch is being integrated (non-nil), keeps the events it would emit until it lands: a batch that is
	// split and tried again reports what its parts come to, not what the whole attempt saw.
	held []queueEvent
}

// job is one waiting submission: what it is, its caller's context, and where its result goes.
type job struct {
	ctx   context.Context
	s     Submission
	entry *QueueEntry
	done  chan jobResult // buffered: the result is sent once
}

type jobResult struct {
	res *Result
	err error
}

const maxLedger = 500

// VerifyRecord is one verification the queue ran on a merged result: who submitted
// what, when it finished, and the verifier's result (Result.OK: the merge went on to
// be published; otherwise it was rolled back).
type VerifyRecord struct {
	Agent, Task string
	At          time.Time
	Result      VerifyResult
}

// Verifications lists the verifications the queue ran for a task (every task when
// task is ""), oldest first; the queue keeps the last 500.
func (q *Queue) Verifications(task string) []VerifyRecord {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []VerifyRecord
	for _, v := range q.verifies {
		if task == "" || v.Task == task {
			out = append(out, v)
		}
	}
	return out
}

// noteVerify keeps a verification's record.
func (q *Queue) noteVerify(s Submission, vr VerifyResult, at time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.verifies = append(q.verifies, VerifyRecord{Agent: s.Agent, Task: s.Task, At: at, Result: vr})
	if len(q.verifies) > maxLedger {
		q.verifies = append([]VerifyRecord(nil), q.verifies[len(q.verifies)-maxLedger:]...)
	}
}

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
	if o.MaxBatch <= 0 {
		o.MaxBatch = defaultMaxBatch
	}
	return o
}

// defaultMaxBatch is QueueOptions.MaxBatch when none is given.
const defaultMaxBatch = 8

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
	admin, mk, err := m.verifyTreeDir(ctx, dest)
	if errors.Is(err, errMissingDir) {
		admin, mk, err = m.verifyMissing(dest)
	}
	if err != nil {
		return err
	}
	owner := m.ownerOf(mk, admin)
	if !mk.Integration || owner == ownerAlive {
		return fmt.Errorf("%w: the integration tree at %s belongs to a running session", ErrExists, dest)
	}
	if owner == ownerUnknown {
		return fmt.Errorf("%w: the owner of the integration tree at %s cannot be verified as stopped", ErrExists, dest)
	}
	m.mu.Lock()
	delete(m.trees, integrationName)
	m.mu.Unlock()
	// The dead run may have died while moving the branch; git leaves the lock file of
	// a process that did not finish, and it would block every publish of the new one.
	_ = os.Remove(filepath.Join(m.st.base.CommonDir(), "refs", "heads", filepath.FromSlash(m.st.prefix), integrationName+".lock"))
	return m.st.base.WorktreeRemove(ctx, dest, true)
}

// unnamedTip reports whether a submission with message whose tip is theirs would, fast-forwarded, leave the work without a
// commit that names it: the tip's subject is not the message's, as when the task's work was all saved by an earlier commit
// ("Work in progress: …" when the harness merged integrated work into the tree) and nothing was left to commit at the end. A
// merge commit then records the finished work by its message. A submission without a message, or a tip that cannot be
// read, fast-forwards as before.
func (q *Queue) unnamedTip(ctx context.Context, theirs, message string) bool {
	want, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	if want = strings.TrimSpace(want); want == "" {
		return false
	}
	c, err := q.m.st.base.CommitInfo(ctx, theirs)
	if err != nil {
		return false
	}
	return strings.TrimSpace(c.Subject) != want
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
	if typ != EventQueued && typ != EventBatch {
		q.mu.Lock()
		if q.held != nil {
			q.held = append(q.held, queueEvent{typ, agent, task, data})
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
	}
	q.opts.OnEvent.emit(typ, agent, task, data)
}

// author returns the merge queue's Git author identity stamped with the manager clock.
func (q *Queue) author() gitx.Author {
	if q.m.hasOwner() {
		return q.m.agentAuthor("")
	}
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

// acquire takes a place in line and waits for the turn.
func (t *turnstile) acquire(ctx context.Context) error {
	return t.wait(ctx, t.ticket())
}

// ticket takes a place in line without waiting: nil means the turn is the
// caller's now, otherwise wait on the returned channel. A caller that must keep
// its place consistent with another record (Submit's waiting list) takes the
// ticket while holding that record's lock.
func (t *turnstile) ticket() chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.busy {
		t.busy = true
		return nil
	}
	ch := make(chan struct{})
	t.q = append(t.q, ch)
	return ch
}

// wait blocks until the ticket's turn comes; when ctx ends first the place in
// line is given up (or, if the turn arrived at that moment, passed on).
func (t *turnstile) wait(ctx context.Context, ch chan struct{}) error {
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil // the turn was handed to us; busy stays true
	case <-ctx.Done():
		t.abandon(ch)
		return ctx.Err()
	}
}

// abandon gives up a ticket that has not been waited for to the end: its place in line, or, when the turn was handed to it
// meanwhile, the turn itself, which passes to the next in line.
func (t *turnstile) abandon(ch chan struct{}) {
	t.mu.Lock()
	for i, c := range t.q {
		if c == ch {
			t.q = append(t.q[:i], t.q[i+1:]...)
			t.mu.Unlock()
			return
		}
	}
	t.mu.Unlock()
	t.release()
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
	j := &job{ctx: ctx, s: s, entry: entry, done: make(chan jobResult, 1)}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil, ErrClosed
	}
	q.waiting = append(q.waiting, entry)
	q.pending = append(q.pending, j)
	// The place in the turnstile is taken together with the place in the waiting
	// list: taken later, a submission that arrived after this one could get its
	// turn first while Status still shows them in arrival order.
	tk := q.turn.ticket()
	position := len(q.waiting)
	if q.active != nil {
		position++
	}
	q.mu.Unlock()
	q.emit(EventQueued, s.Agent, s.Task, map[string]any{"position": position})

	if tk != nil {
		select {
		case <-tk: // our turn; busy stays true
		case r := <-j.done: // integrated in a batch that a submission ahead of this one ran
			q.turn.abandon(tk)
			return r.res, r.err
		case <-ctx.Done():
			q.turn.abandon(tk)
			if q.unqueue(j) {
				return nil, ctx.Err()
			}
			// Taken into a batch already: the batch sees this context end, leaves the
			// submission out and answers.
			r := <-j.done
			return r.res, r.err
		}
	}
	defer q.turn.release()
	for {
		select {
		case r := <-j.done:
			return r.res, r.err
		default:
		}
		q.mu.Lock()
		closed := q.closed
		q.mu.Unlock()
		if closed {
			if q.unqueue(j) {
				return nil, ErrClosed
			}
			continue // taken and answered: the loop reads it
		}
		batch := q.takeBatch()
		if len(batch) == 0 {
			// Nothing waits, so this submission's answer has been sent.
			r := <-j.done
			return r.res, r.err
		}
		q.runGroup(batch)
	}
}

// unqueue takes j out of the waiting submissions; false when a batch has taken it.
func (q *Queue) unqueue(j *job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := slices.Index(q.pending, j)
	if i < 0 {
		return false
	}
	q.pending = slices.Delete(q.pending, i, i+1)
	if k := slices.Index(q.waiting, j.entry); k >= 0 {
		q.waiting = slices.Delete(q.waiting, k, k+1)
	}
	return true
}

// verifyCmdOf is the command the queue verifies s with.
func (q *Queue) verifyCmdOf(s Submission) string {
	if s.VerifyCmd != "" {
		return s.VerifyCmd
	}
	return q.opts.VerifyCmd
}

// takeBatch takes the next submissions to integrate together, oldest first: the oldest waiting one, and after it as many
// of the next as MaxBatch allows that can be verified by one command (the same one, or CombineVerify's). Without a verifier
// there is nothing to share, so each goes alone. A submission whose caller has given up is answered and skipped.
func (q *Queue) takeBatch() []*job {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []*job
	var first string
	passed := false // once one is passed over, the rest wait too: the queue keeps arrival order
	keep := q.pending[:0]
	for _, j := range q.pending {
		if j.ctx.Err() != nil {
			j.done <- jobResult{err: j.ctx.Err()}
			if k := slices.Index(q.waiting, j.entry); k >= 0 {
				q.waiting = slices.Delete(q.waiting, k, k+1)
			}
			continue
		}
		cmd := q.verifyCmdOf(j.s)
		take := len(out) == 0 ||
			!passed && len(out) < q.opts.MaxBatch && first != "" && (cmd == first || q.opts.CombineVerify != nil)
		if !take {
			passed = true
			keep = append(keep, j)
			continue
		}
		if len(out) == 0 {
			first = cmd
		}
		out = append(out, j)
		if k := slices.Index(q.waiting, j.entry); k >= 0 {
			q.waiting = slices.Delete(q.waiting, k, k+1)
		}
	}
	clear(q.pending[len(keep):])
	q.pending = keep
	return out
}

// deliver answers j.
func (q *Queue) deliver(j *job, res *Result, err error) {
	j.done <- jobResult{res: res, err: err}
}

// runGroup integrates jobs, oldest first, and answers each: alone exactly as a single submission is integrated, together as
// one batch when there are several. A batch that cannot land as a whole (its combined result failed verification, a caller
// gave up, the harness failed) leaves the tip as it was and is split in halves that are tried again in order, so a bad
// submission ends up alone and is judged as it would have been without batching, and the good ones still land together.
func (q *Queue) runGroup(jobs []*job) {
	q.mu.Lock()
	broken := q.broken
	q.mu.Unlock()
	if broken != nil {
		if err := q.repairLocked(jobs[0].ctx); err != nil {
			for _, j := range jobs {
				q.deliver(j, nil, fmt.Errorf("%w: %v", ErrBroken, broken))
			}
			return
		}
	}
	if len(jobs) == 1 {
		j := jobs[0]
		q.setActive(j.entry)
		res, err := q.integrate(j.ctx, j.s, j.entry)
		q.setActive(nil)
		q.deliver(j, res, err)
		return
	}
	if results, ok := q.integrateBatch(jobs); ok {
		for i, j := range jobs {
			q.deliver(j, results[i], nil)
		}
		return
	}
	var live []*job
	for _, j := range jobs {
		if err := j.ctx.Err(); err != nil {
			q.deliver(j, nil, err)
			continue
		}
		live = append(live, j)
	}
	switch {
	case len(live) == 0:
	case len(live) < len(jobs):
		q.runGroup(live) // a caller gave up: the others are tried again as they were
	default:
		h := len(live) / 2
		q.runGroup(live[:h])
		q.runGroup(live[h:])
	}
}

// setActive names the submission being integrated (nil: none).
func (q *Queue) setActive(e *QueueEntry) {
	q.mu.Lock()
	q.active = e
	q.mu.Unlock()
}

// queueEvent is an event held back while a batch has not landed.
type queueEvent struct {
	typ, agent, task string
	data             map[string]any
}

// integrateBatch merges jobs onto the tip one after another and verifies the combined result once. ok: every job has its
// result (merged into the published tip, or refused, conflicting or empty as judged against the batch merged before it).
// Not ok: nothing was published, the tree is back at the tip, and nothing the attempt did is counted or reported: the
// caller tries the jobs again in smaller groups.
func (q *Queue) integrateBatch(jobs []*job) (results []*Result, ok bool) {
	subs := make([]Submission, len(jobs))
	for i, j := range jobs {
		subs[i] = j.s
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(jobs[0].ctx))
	defer cancel()
	for _, j := range jobs {
		stop := context.AfterFunc(j.ctx, cancel)
		defer stop()
	}
	q.mu.Lock()
	prev, saved := q.tip, q.stats
	q.held = []queueEvent{}
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		held := q.held
		q.held = nil
		if !ok {
			q.stats = saved
		}
		q.mu.Unlock()
		if ok {
			for _, e := range held {
				q.opts.OnEvent.emit(e.typ, e.agent, e.task, e.data)
			}
		}
		q.setActive(nil)
	}()
	if err := q.settle(ctx, prev); err != nil {
		return nil, false
	}
	cmd, same := q.verifyCmdOf(subs[0]), true
	for _, s := range subs[1:] {
		same = same && q.verifyCmdOf(s) == cmd
	}
	if !same {
		var can bool
		if q.opts.CombineVerify == nil {
			return nil, false
		}
		if cmd, can = q.opts.CombineVerify(q.tree.Path, subs); !can {
			return nil, false
		}
	}
	if cmd == "" {
		return nil, false
	}
	head := prev
	results = make([]*Result, len(jobs))
	afters := make([]string, len(jobs))
	var merged []int
	for i, j := range jobs {
		q.setActive(j.entry)
		res, after, err := q.combine(ctx, j.s, j.entry, head)
		if err != nil || ctx.Err() != nil {
			_ = q.rollbackTo(ctx, prev)
			return nil, false
		}
		results[i] = res
		if res.Outcome == "" {
			head, afters[i] = after, after
			merged = append(merged, i)
		}
	}
	tasks := make([]string, len(jobs))
	for i, j := range jobs {
		tasks[i] = j.s.Task
	}
	if len(merged) == 0 {
		return results, true
	}
	for _, i := range merged {
		q.setPhase(jobs[i].entry, "verifying")
	}
	q.setActive(jobs[merged[0]].entry)
	vr := q.opts.Verify(ctx, VerifyRequest{
		Dir: q.tree.Path, Cmd: cmd, Agent: jobs[merged[0]].s.Agent, Task: fmt.Sprintf("%s and %d more", jobs[merged[0]].s.Task, len(merged)-1),
		Timeout: q.opts.VerifyTimeout, MaxOutput: q.opts.MaxVerifyOutput, Env: q.opts.VerifyEnv,
	})
	if !vr.OK() || ctx.Err() != nil {
		_ = q.rollbackTo(ctx, prev)
		if ctx.Err() == nil {
			q.emit(EventBatch, "", "", map[string]any{"tasks": tasks, "merged": len(merged), "ok": false, "split": true,
				"cmd": vr.Cmd, "exit_code": vr.ExitCode, "timed_out": vr.TimedOut, "output": tailOf(vr.Output, 2000)})
		}
		return nil, false
	}
	vr.Wrote = q.verifyWrote(ctx)
	agents := make([]string, 0, len(merged))
	for _, i := range merged {
		q.setPhase(jobs[i].entry, "publishing")
		if !slices.Contains(agents, jobs[i].s.Agent) {
			agents = append(agents, jobs[i].s.Agent)
		}
	}
	if err := q.publish(ctx, head, prev, strings.Join(agents, ", ")); err != nil {
		_ = q.rollbackTo(ctx, prev)
		return nil, false
	}
	now := q.m.now()
	q.mu.Lock()
	q.tip = head
	for _, i := range merged {
		r := results[i]
		q.ledger = append(q.ledger, Landed{Agent: r.Agent, Task: r.Task, Commit: afters[i], Before: r.Before, Files: r.Files, Commits: r.Commits})
	}
	if len(q.ledger) > maxLedger {
		q.ledger = append([]Landed(nil), q.ledger[len(q.ledger)-maxLedger:]...)
	}
	q.stats.Merged += len(merged)
	q.mu.Unlock()
	for _, i := range merged {
		r := results[i]
		v := vr
		r.Verify, r.Outcome, r.After = &v, OutcomeMerged, head
		q.noteVerify(jobs[i].s, vr, now)
		q.emit(EventMerged, r.Agent, r.Task, map[string]any{
			"before": r.Before, "after": head, "commit": afters[i], "commits": r.Commits, "files": firstN(r.Files, 50), "file_count": len(r.Files),
			"strategy": q.opts.Strategy.String(), "verified": true, "batch": len(merged),
		})
	}
	q.emit(EventBatch, "", "", map[string]any{"tasks": tasks, "merged": len(merged), "ok": true, "tip": head})
	return results, true
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
	q.mu.Lock()
	prev := q.tip
	q.mu.Unlock()
	res, after, err := q.combine(ctx, s, entry, prev)
	if err != nil || res.Outcome != "" {
		return res, err
	}
	return q.verifyAndPublish(ctx, s, entry, res, prev, after)
}

// combine is steps 1 to 3b of integrate: it commits the submission, judges it, and merges (or rebases) it onto head in
// the integration tree, which must be at head. A submission that cannot land comes back with its Outcome set and the
// tree back at head; one that merged comes back with no Outcome yet, res.After and after naming the merge, and the tree
// at after, unverified and unpublished. head is the queue's tip for a submission on its own, and the unpublished head of
// the batch for a submission merged in a batch (integrateBatch).
func (q *Queue) combine(ctx context.Context, s Submission, entry *QueueEntry, head string) (res *Result, after string, err error) {
	m := q.m
	// History queries go through the repository the trees belong to, not through the
	// integration tree: they must keep working while that tree is damaged (settle
	// below is what repairs it).
	hist := m.st.base
	res = &Result{Agent: s.Agent, Task: s.Task}
	prev := head
	res.Before, res.After = prev, prev

	// 1. The submission must be a commit.
	q.setPhase(entry, "committing")
	theirs, reject, err := q.commitSubmission(ctx, s)
	if err != nil {
		return nil, "", err
	}
	if reject != "" {
		return q.reject(res, reject), "", nil
	}
	if theirs == prev {
		return q.empty(res, "the submission is the integration tip itself"), "", nil
	}
	if ok, err := hist.IsAncestor(ctx, theirs, prev); err != nil {
		return nil, "", err
	} else if ok {
		return q.empty(res, "everything in the submission is already integrated"), "", nil
	}
	mb, err := hist.MergeBase(ctx, prev, theirs)
	if err != nil {
		if gitx.KindOf(err) == gitx.KindNotFound {
			return q.reject(res, "the submission shares no history with the integration branch"), "", nil
		}
		return nil, "", err
	}

	// 1b. What the agent committed itself meets the guards that what we commit for it
	// does: nothing oversized or unexplained enters the project's history.
	if err := m.checkHistory(ctx, hist, mb, theirs); err != nil {
		var tl *TooLargeError
		var nr *NestedRepoError
		switch {
		case errors.As(err, &tl):
			return q.reject(res, tl.Error()), "", nil
		case errors.As(err, &nr):
			return q.reject(res, nr.Error()), "", nil
		}
		return nil, "", err
	}

	// 2. Scope and refused files, judged on what this submission itself changed.
	if (s.EnforceScope && len(s.Scope) > 0) || s.Refuse != nil {
		d, err := hist.Diff(ctx, mb, gitx.DiffOptions{To: theirs, NoPatch: true, MaxFiles: 1 << 20})
		if err != nil {
			return nil, "", err
		}
		// A file list cut at its cap would judge only the files that fit; what it
		// left out would merge unchecked.
		if d.Truncated {
			return q.reject(res, "changed more files than the queue can check against its scope; split the work into smaller submissions"), "", nil
		}
		var files []string
		for _, f := range d.Files {
			files = append(files, f.Path)
			if f.OldPath != "" {
				files = append(files, f.OldPath)
			}
		}
		if s.Refuse != nil {
			out, why := refused(files, s.Refuse)
			if out, why, _, err = changedFromTip(ctx, hist, prev, theirs, out, why); err != nil {
				return nil, "", err
			}
			if len(out) > 0 {
				res.Files = out
				return q.reject(res, fmt.Sprintf("changed %d file(s) it may not change: %s%s", len(out), strings.Join(firstN(why, 8), "; "), moreFiles(out, 8))), "", nil
			}
		}
		if s.EnforceScope && len(s.Scope) > 0 {
			out := allowOutside(OutOfScopeForMerge(files, s.Scope), s.AllowOutside)
			var changes []gitx.FileChange
			if out, _, changes, err = changedFromTip(ctx, hist, prev, theirs, out, nil); err != nil {
				return nil, "", err
			}
			if len(out) > 0 {
				res.Files = out
				return q.reject(res, fmt.Sprintf("changed %d file(s) outside its scope: %s%s", len(out), strings.Join(howChanged(firstN(out, 8), changes), ", "), moreFiles(out, 8))), "", nil
			}
		}
	}

	// 3. Combine, in a tree that is exactly at the tip and clean. settle may have to
	// rebuild the tree, so the handle is taken afterwards, never before.
	q.setPhase(entry, "merging")
	if err := q.settle(ctx, prev); err != nil {
		return nil, "", err
	}
	repo := q.tree.repo
	var out *gitx.MergeResult
	switch q.opts.Strategy {
	case StrategyRebase:
		out, err = repo.Rebase(ctx, gitx.RebaseOptions{Onto: prev, Upstream: mb, Branch: theirs, Author: q.author()})
	default:
		// The merge commit stays in the project's history: name the work it brings
		// in by its own commit message when there is one, not only by who did it.
		// The agent is named by the trailer, not in the subject: its id means nothing
		// to a reader of the project's history.
		msg := "Merge work in progress"
		if subject, _, _ := strings.Cut(strings.TrimSpace(s.Message), "\n"); strings.TrimSpace(subject) != "" {
			msg = "Merge \"" + oneLine(subject, 100) + "\""
		} else if t := oneLine(taskTitle(s.Task), 100); t != "" {
			msg = "Merge \"" + t + "\""
		}
		out, err = repo.Merge(ctx, gitx.MergeOptions{Ref: theirs, Message: q.m.withAgentTrailer(msg, s.Agent), NoFF: q.opts.NoFF || q.unnamedTip(ctx, theirs, s.Message), Author: q.author()})
	}
	if err != nil {
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return nil, "", err
	}
	if out.Conflicted {
		c, cerr := buildConflict(ctx, repo, conflictOpts{agent: s.Agent, task: s.Task, tip: prev, theirs: theirs, mergeBase: mb, landedBy: q.landedBy(ctx, mb, prev)})
		q.setPhase(entry, "rolling back")
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			return nil, "", errors.Join(rerr, cerr)
		}
		if cerr != nil {
			return nil, "", cerr
		}
		res.Outcome, res.Conflict = OutcomeConflict, c
		q.mu.Lock()
		q.stats.Conflicts++
		q.mu.Unlock()
		q.emit(EventConflict, s.Agent, s.Task, map[string]any{"files": c.Files, "hunks": len(c.Hunks), "tip": prev, "theirs": theirs})
		return res, "", nil
	}
	after = out.Head
	if out.UpToDate || after == prev {
		if err := q.settle(ctx, prev); err != nil {
			return nil, "", err
		}
		return q.empty(res, "the merge changed nothing"), "", nil
	}
	res.After = after
	if files, err := q.changedBetween(ctx, prev, after); err == nil {
		res.Files = files
	} else {
		q.setPhase(entry, "rolling back")
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			return nil, "", errors.Join(err, rerr)
		}
		if errors.Is(err, gitx.ErrTooLarge) {
			res.After = prev
			return q.reject(res, "the merge changed more files than the queue can check against its scope; split the work into smaller submissions"), "", nil
		}
		return nil, "", err
	}
	res.Commits, _ = hist.CountCommits(ctx, mb, theirs)

	// 3b. Scope and refused files again, on what integrating actually changed. A
	// rebase replays commit by commit and drops the ones whose change is already on
	// the tip, so the result can change a path the submission's own difference
	// (step 2) does not show: a commit undoing a change that a dropped commit made.
	if why := q.outOfBounds(s, res.Files); why != "" {
		q.setPhase(entry, "rolling back")
		if rerr := q.rollbackTo(ctx, prev); rerr != nil {
			return nil, "", rerr
		}
		res.After = prev
		return q.reject(res, why), "", nil
	}

	return res, after, nil
}

// verifyAndPublish is steps 4 and 5 of integrate for a submission combine merged: it verifies the tree at after and
// moves the integration branch from prev to it, or rolls the tree back to prev.
func (q *Queue) verifyAndPublish(ctx context.Context, s Submission, entry *QueueEntry, res *Result, prev, after string) (*Result, error) {
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
		if vr.OK() && ctx.Err() == nil {
			vr.Wrote = q.verifyWrote(ctx)
		}
		res.Verify = &vr
		if ctx.Err() == nil {
			q.noteVerify(s, vr, q.m.now())
		}
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

// allowOutside drops from out the files allow lets through (nil: none).
func allowOutside(out []string, allow func(string) bool) []string {
	if allow == nil || len(out) == 0 {
		return out
	}
	kept := out[:0:0]
	for _, f := range out {
		if !allow(f) {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// verifyWrote lists the tracked files the verifier left modified in the queue's tree, which was clean at the merge: the
// verifier wrote them. A status that cannot be read lists none.
func (q *Queue) verifyWrote(ctx context.Context) []string {
	st, err := q.tree.repo.StatusWith(ctx, gitx.StatusOptions{Untracked: "no"})
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range append(st.Staged, st.Unstaged...) {
		if !slices.Contains(out, c.Path) {
			out = append(out, c.Path)
		}
	}
	slices.Sort(out)
	return out
}

// outOfBounds returns why the integration changes files may not be changed by s
// ("" when none), by the same rules step 2 of integrate applies to the
// submission's own difference.
func (q *Queue) outOfBounds(s Submission, files []string) string {
	if s.Refuse != nil {
		if out, why := refused(files, s.Refuse); len(out) > 0 {
			return fmt.Sprintf("changed %d file(s) it may not change: %s%s", len(out), strings.Join(firstN(why, 8), "; "), moreFiles(out, 8))
		}
	}
	if s.EnforceScope && len(s.Scope) > 0 {
		if out := allowOutside(OutOfScopeForMerge(files, s.Scope), s.AllowOutside); len(out) > 0 {
			return fmt.Sprintf("changed %d file(s) outside its scope: %s%s", len(out), strings.Join(firstN(out, 8), ", "), moreFiles(out, 8))
		}
	}
	return ""
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

// changedFromTip keeps the files (and their reasons, when why is given) whose content in theirs
// differs from the tip's. A submission that changed a file only to what the integration branch
// already holds, as when a formatter rewrote code the tip had already reformatted, changes
// nothing there when merged, so that file does not count against its scope. It also returns the
// changes from the tip to theirs among files (nil when the list was too long to read whole).
func changedFromTip(ctx context.Context, hist *gitx.Repo, tip, theirs string, files, why []string) ([]string, []string, []gitx.FileChange, error) {
	if len(files) == 0 {
		return files, why, nil, nil
	}
	d, err := hist.Diff(ctx, tip, gitx.DiffOptions{To: theirs, Paths: files, NoPatch: true, MaxFiles: 1 << 20})
	if err != nil {
		return nil, nil, nil, err
	}
	if d.Truncated {
		// An incomplete list cannot show a file to be unchanged: keep them all.
		return files, why, nil, nil
	}
	differ := map[string]bool{}
	for _, f := range d.Files {
		differ[f.Path] = true
		if f.OldPath != "" {
			differ[f.OldPath] = true
		}
	}
	var keep, keepWhy []string
	for i, f := range files {
		if differ[f] {
			keep = append(keep, f)
			if why != nil {
				keepWhy = append(keepWhy, why[i])
			}
		}
	}
	return keep, keepWhy, d.Files, nil
}

// howChanged names each of files with how changes (from the integration tip to the submission) record it:
// "a.txt (deleted)", "b.txt (added)", and a file changed in place by its path alone. A worker that committed a deletion finds no trace of the file in
// its tree or in git status, and cannot tell from the bare path what it is asked to put back.
func howChanged(files []string, changes []gitx.FileChange) []string {
	how := map[string]string{}
	for _, c := range changes {
		switch c.Status {
		case 'D':
			how[c.Path] = " (deleted)"
		case 'A':
			how[c.Path] = " (added)"
		}
	}
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f + how[f]
	}
	return out
}

// refused asks refuse about each file once and returns, sorted by path, the files it
// refused and its reasons for them.
func refused(files []string, refuse func(string) string) (out, why []string) {
	seen := map[string]bool{}
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	for _, f := range sorted {
		if seen[f] {
			continue
		}
		seen[f] = true
		if r := refuse(f); r != "" {
			out, why = append(out, f), append(why, r)
		}
	}
	return out, why
}

// firstN returns a shared prefix of at most n strings; n must be nonnegative.
func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// moreFiles names, by directory, the files a refusal does not list after its first n: "; and 9 more: docs/demo/ (7), web/ (2)".
// A worker that reverts only the files it was shown is refused again for the rest, so it needs to know where they are.
func moreFiles(files []string, n int) string {
	if len(files) <= n {
		return ""
	}
	count := map[string]int{}
	for _, f := range files[n:] {
		dir := filepath.ToSlash(filepath.Dir(f))
		if dir == "." {
			dir = "the root"
		} else {
			dir += "/"
		}
		count[dir]++
	}
	dirs := make([]string, 0, len(count))
	for d := range count {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if count[dirs[i]] != count[dirs[j]] {
			return count[dirs[i]] > count[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})
	parts := make([]string, 0, 6)
	for i, d := range dirs {
		if i == 5 {
			if rest := len(dirs) - 5; rest == 1 {
				parts = append(parts, "1 other directory")
			} else {
				parts = append(parts, fmt.Sprintf("%d other directories", rest))
			}
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", d, count[d]))
	}
	return fmt.Sprintf("; and %d more: %s", len(files)-n, strings.Join(parts, ", "))
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
			msg = "Save work in progress"
			if task := oneLine(taskTitle(s.Task), 100); task != "" {
				msg = task
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

// changedBetween lists, sorted, every path that differs between a and b; it fails
// with gitx.ErrTooLarge rather than return a list cut at its cap.
func (q *Queue) changedBetween(ctx context.Context, a, b string) ([]string, error) {
	return q.m.st.base.ChangedPaths(ctx, a, b)
}

// clearStaleLocks removes the lock files a stopped git command left in the
// integration tree's administrative directory (ORIG_HEAD.lock, HEAD.lock,
// index.lock, ...). git deletes its locks when it is asked to stop, but it installs
// that cleanup only after it has created its first lock (ORIG_HEAD.lock, for a
// merge), so a command that is stopped in between, or that has to be killed, leaves
// the lock behind, and every later merge or reset in the tree then fails on it
// until the file is removed.
//
// Call it only while holding the queue's turn and between commands of the queue.
// The queue is the only user of the integration tree and runs one command at a
// time; a stopped git (and the verifier) has been waited for, with its process
// group, before its call returned, so a lock found then belongs to no running
// process. That holds for the queue's own commands only: a git command a person
// runs by hand inside the integration tree at that moment would lose its lock,
// which is why the tree is not a place to work in.
//
// Only regular files named *.lock directly inside the directory are removed. The
// directory has to be a real directory (not a link) that is an entry of the
// repository's worktrees/ directory, and it is opened once, so that the listing and
// the removals act on that directory and cannot leave it. The user's work tree and
// .git, the agents' trees, and the lock file of the integration branch's ref are
// never touched. Failures are ignored: a lock that stays is reported by the git
// command that meets it.
func (q *Queue) clearStaleLocks() {
	if q.tree == nil {
		return
	}
	admin := q.tree.repo.GitDir()
	if filepath.Dir(admin) != filepath.Join(q.m.st.base.CommonDir(), "worktrees") {
		return
	}
	fi, err := os.Lstat(admin)
	if err != nil || !fi.IsDir() {
		return
	}
	root, err := os.OpenRoot(admin)
	if err != nil {
		return
	}
	defer root.Close()
	if ri, err := root.Stat("."); err != nil || !os.SameFile(fi, ri) {
		return
	}
	dir, err := root.Open(".")
	if err != nil {
		return
	}
	entries, _ := dir.ReadDir(-1) // what was read before an error is still worth acting on
	_ = dir.Close()
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".lock") {
			_ = root.Remove(e.Name())
		}
	}
}

// settle makes sure the integration tree is at commit to and clean, and free of the
// lock files a stopped git left in it.
func (q *Queue) settle(ctx context.Context, to string) error {
	q.clearStaleLocks()
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

// restore resets the integration tree in place to commit to and checks the result.
// Lock files left by a stopped git command are cleared first; otherwise the reset
// would wait for them, fail, and send rollbackTo to rebuilding the whole tree.
func (q *Queue) restore(ctx context.Context, to string) error {
	q.clearStaleLocks()
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
	return q.fastForward(ctx, "")
}

// FastForwardChecked refuses a changed integration tip after acquiring the queue
// turn, so applying a preview cannot include work accepted during authorization.
func (q *Queue) FastForwardChecked(ctx context.Context, expectedTip string) (*FastForwardResult, error) {
	if expectedTip == "" {
		return nil, fmt.Errorf("%w: missing expected integration tip", ErrNotFastForward)
	}
	return q.fastForward(ctx, expectedTip)
}

// fastForward serializes the verified branch move against integration changes.
func (q *Queue) fastForward(ctx context.Context, expectedTip string) (*FastForwardResult, error) {
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
	if expectedTip != "" && tip != expectedTip {
		return nil, fmt.Errorf("%w: integration tip changed after preview", ErrNotFastForward)
	}
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
	} else if _, err := r.Merge(mctx, gitx.MergeOptions{Ref: tip, FFOnly: true, KeepIgnored: true}); err != nil {
		// The status check above leaves ignored files out; a tracked file of the tip
		// that would replace one is refused by git (KeepIgnored), not overwritten.
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

// leadingBoardID matches the board id a submission's task label starts with ("T12: ").
var leadingBoardID = regexp.MustCompile(`^\s*(?:\[T\d+\]\s*|T\d+\s*[:\-–—]\s*)`)

// taskTitle is a submission's task label without its board id, which means nothing
// outside the run that made it.
func taskTitle(label string) string {
	return strings.TrimSpace(leadingBoardID.ReplaceAllString(label, ""))
}
