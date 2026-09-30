package swarm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/gitx"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/workspace"
)

// Worktree isolation (swarm.isolation = "worktree").
//
// Every worker that may write gets a git worktree of its own (workspace.Manager) at
// the integration tip, and its tools work there: Env.Cwd and Env.Root are the tree.
// The prompt bytes do not depend on it: nothing the model is shown before its own
// tool results names a directory, and the tools print paths relative to the working
// directory, which are the project's paths because a tree is a checkout of the
// repository. Read-only roles and the manager keep using the shared checkout; the
// manager does not edit files in an isolated run (see Leases).
//
// "Done" then has one more step. After the gate (evidence, verifier in the worker's
// tree) the harness commits the tree and submits it to the merge queue
// (workspace.Queue), which merges it onto the integration tip, runs the verifier on
// the result and moves the integration branch only if it passes:
//
//	merged        the task proceeds to review, as in a shared tree
//	empty         nothing to merge (the tree has no changes): reported, and it proceeds
//	conflict      back to the worker with the conflict's summary and hunks; the
//	              integration tip is merged into its tree so the markers are there
//	verify_failed back to the worker with the verifier's output; its tree now holds
//	              the merged state, so the failure reproduces there
//	rejected      back to the worker with the reason (out of scope, oversize, a
//	              nested repository)
//
// Bounces are bounded like other rework (Config.MaxAttempts): after that many the
// task returns to the pool and its worker is stopped. A worker sees other agents'
// work only when it is merged: new workers start from the integration tip, a reused
// or resumed worker's tree is brought up to it, and a bounce does the same. Nobody
// sees another's uncommitted files: trees are separate directories, and the
// permission engine confines every writer to its own (perm.Engine.Confine).
//
// At the end of the run the integration branch holds only verified commits. Finish
// applies its result to the user's checkout, by default as a patch to the working
// tree (uncommitted edits, exactly what a shared-tree run leaves), or as commits on
// the user's branch (Isolation.Commit). When that fails (the user edited the same
// files meanwhile) the branch is kept and the report says how to get the result.

// Isolation is the workspace side of an isolated swarm.
type Isolation struct {
	// Manager creates the trees; its Repo is the user's repository.
	Manager *workspace.Manager
	// Queue integrates finished trees onto the integration branch.
	Queue *workspace.Queue
	// Commit makes Finish move the user's branch to the integration tip (commits)
	// instead of applying the result as a patch to the working tree.
	Commit bool
	// Subdir is the directory the session works in relative to the repository root
	// (slash-separated, "" for the root itself). A tree is a checkout of the whole
	// repository; a session started in a subdirectory keeps its writers in the same
	// subdirectory of their trees.
	Subdir string
	// Checkpoints builds the checkpoint hooks of one tree: the snapshotter the agent's
	// tools call before a write and the function told after it. Nil keeps the
	// session's shared ones (which are rooted at the shared checkout).
	Checkpoints func(agent, dir string) (tools.Snapshotter, func(agent, path string))
}

// isolationCard is appended to the assignment in the private notes of a worker that
// has a tree of its own (never to a shared layer, and it names no path: the bytes are
// the same for every worker). It says what the harness does with the work, so the
// messages that come back are not a surprise.
const isolationCard = "\nIsolation: your working directory is a private git worktree of the project. " +
	"Other agents cannot see your uncommitted files and you do not see theirs until they are merged. " +
	"When you call task done the harness commits your work, merges it into the shared integration branch and runs the verifier on the result; " +
	"if that conflicts, fails, or leaves your scope, the task comes back to you with the details and your tree is brought up to date. " +
	"If you need work another agent has merged, block your task and say what you need: when the manager resumes it your tree is brought up to date."

// mergeRec is the harness's record that a task's work is in the integration branch.
type mergeRec struct {
	rev    uint64 // the assignment it belongs to
	commit string // the integration commit
	empty  bool   // there was nothing to merge
}

// isolated reports whether writers get worktrees of their own.
func (s *Swarm) isolated() bool {
	iso := s.deps.Isolation
	return iso != nil && iso.Manager != nil && iso.Queue != nil
}

// gitTimeout bounds the git work the swarm starts on its own (creating or updating a
// tree); merges have their own bounds inside the queue.
const gitTimeout = 3 * time.Minute

func (s *Swarm) runCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rootCtx != nil {
		return s.rootCtx
	}
	return context.Background()
}

// createTree makes a worker's tree at the integration tip.
func (s *Swarm) createTree(id string) (*workspace.Tree, error) {
	ctx, cancel := context.WithTimeout(s.runCtx(), gitTimeout)
	defer cancel()
	iso := s.deps.Isolation
	return iso.Manager.Create(ctx, id, workspace.CreateOptions{Base: iso.Queue.Tip()})
}

// dropTree removes a tree that was never used (its worker could not be started).
func (s *Swarm) dropTree(t *workspace.Tree) {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	_ = t.Remove(ctx, true)
}

// bindTree tells the guards which tree is the agent's: the lease guard confines its
// writes to it, and a permission engine that can (perm.Engine) confines everything it
// touches inside the workspace.
func (s *Swarm) bindTree(m *member) {
	if m.tree == nil {
		return
	}
	s.mu.Lock()
	s.treeAgents[m.id] = true
	s.mu.Unlock()
	s.Leases.BindTree(m.id, m.tree.Path)
	if c, ok := s.deps.Perm.(interface{ Confine(agent, dir string) }); ok {
		c.Confine(m.id, m.tree.Path)
	}
}

// hadTree reports whether an agent ever had a tree of its own in this run (its work
// is then merged before review, and its task is accepted only once it is).
func (s *Swarm) hadTree(agent string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.treeAgents[agent]
}

// afterResume brings an isolated worker's tree up to the integration tip when its
// blocked task is resumed (by itself or by the manager): the natural moment to pick
// up what it was waiting for. It returns text for the resuming caller.
func (s *Swarm) afterResume(ctx context.Context, caller string, byManager bool, taskID string) string {
	t, ok := s.Board.Snapshot().Task(taskID)
	if !ok || t.Owner == "" {
		return ""
	}
	om := s.get(t.Owner)
	if om == nil || om.tree == nil {
		return ""
	}
	if byManager && om.isActive() {
		return "; its worker is running and keeps its tree (it picks up merged work when it next resumes)"
	}
	note, err := s.syncTree(ctx, om)
	msg := "your tree now includes the work merged so far"
	switch {
	case err != nil:
		msg = "the harness could not bring the merged work into your tree (" + cleanText(err.Error(), 160) + ")"
	case note != "":
		msg = note
	}
	if byManager {
		s.notify(om.id, "info", fmt.Sprintf("%s was resumed by the manager: %s.", taskID, strings.TrimSuffix(msg, ".")))
		return "; its worker's tree was brought up to date"
	}
	return ": " + msg
}

func (s *Swarm) unbindTree(m *member) {
	if m.tree == nil {
		return
	}
	s.Leases.UnbindTree(m.id)
	if c, ok := s.deps.Perm.(interface{ Unconfine(agent string) }); ok {
		c.Unconfine(m.id)
	}
}

// retireTree lets go of a departing member's tree: it is removed when nothing would
// be lost (its work is merged and it holds no uncommitted change); otherwise it stays
// where it is, and Finish reports it.
func (s *Swarm) retireTree(m *member) {
	if m.tree == nil {
		return
	}
	s.unbindTree(m)
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	_ = m.tree.Remove(ctx, false)
}

// syncTree brings the integration tip into a member's tree, so it sees the work that
// has been merged since. It returns a note for the worker when the tree is left in a
// conflicted state (the markers are in its files) and the error when git failed.
func (s *Swarm) syncTree(ctx context.Context, m *member) (string, error) {
	if m.tree == nil {
		return "", nil
	}
	tip := s.deps.Isolation.Queue.Tip()
	uctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	c, err := m.tree.Update(uctx, tip)
	if err != nil {
		return "", err
	}
	if c != nil {
		return "Merging the work that has landed since you started left conflicts in your tree: " + conflictHunks(c), nil
	}
	return "", nil
}

// integration is what one submission to the merge queue came to.
type integration struct {
	ok          bool   // the work is in the integration branch (or there was nothing to merge)
	evidence    string // what to add to the task's evidence
	bounce      string // the task goes back to its worker with this
	infra       error  // the merge could not run: nothing is known about the work
	interrupted bool   // the swarm is stopping
}

// integrate commits an agent's tree and submits it to the merge queue.
func (s *Swarm) integrate(ctx context.Context, m *member, t Task) integration {
	iso := s.deps.Isolation
	subject := t.ID + ": " + cleanText(t.Title, 100)

	// Keep the watchdog from taking a worker that waits its turn in the queue for a
	// stuck one: the queue is serial and each merge may run the verifier.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				m.touch(s)
			}
		}
	}()

	if _, err := m.tree.Commit(ctx, subject); err != nil {
		return s.notCommitted(ctx, err)
	}
	res, err := iso.Queue.Submit(ctx, workspace.Submission{
		Agent: m.id, Tree: m.tree, Task: subject, VerifyCmd: s.cfg.VerifyCmd, Message: subject,
		Scope: t.Files, EnforceScope: len(t.Files) > 0,
	})
	if err != nil {
		if ctx.Err() != nil {
			return integration{interrupted: true}
		}
		s.emitAs(m.id, events.TypeTaskMerge, map[string]any{"task": t.ID, "outcome": "error", "error": cleanText(err.Error(), 300)})
		return integration{infra: err}
	}
	data := map[string]any{"task": t.ID, "outcome": string(res.Outcome), "commit": res.After, "files": firstN(res.Files, 30)}
	if res.Reason != "" {
		data["reason"] = cleanText(res.Reason, 300)
	}
	s.emitAs(m.id, events.TypeTaskMerge, data)

	switch res.Outcome {
	case workspace.OutcomeMerged:
		s.recordMerge(t, mergeRec{rev: t.Rev, commit: res.After})
		note := "merged into integration " + short(res.After)
		if res.Verify != nil && res.Verify.OK() {
			note += " (verified)"
		}
		return integration{ok: true, evidence: note}
	case workspace.OutcomeEmpty:
		s.recordMerge(t, mergeRec{rev: t.Rev, commit: res.After, empty: true})
		return integration{ok: true, evidence: "nothing to merge: the tree has no changes"}
	case workspace.OutcomeConflict:
		return integration{bounce: s.conflictBounce(ctx, m, res.Conflict)}
	case workspace.OutcomeVerifyFailed:
		if v := res.Verify; v != nil && (v.Err != nil || v.TimedOut) {
			// The verifier could not give a verdict: nothing is known about the work, and it
			// is not a failed test (the queue rolled the merge back either way).
			why := "it could not run"
			if v.TimedOut {
				why = "it timed out"
			} else if v.Err != nil {
				why = cleanText(v.Err.Error(), 120)
			}
			return integration{infra: fmt.Errorf("verification of the merged result did not finish: %s", why)}
		}
		return integration{bounce: s.verifyBounce(ctx, m, res.Verify)}
	default:
		reason := cleanText(res.Reason, 400)
		return integration{bounce: "Not done: the merge queue refused your work: " + reason +
			". Fix that in your tree (revert files outside your scope, or ask the manager to widen it), then call task done again."}
	}
}

// notCommitted maps a refusal of Tree.Commit onto what the worker is told.
func (s *Swarm) notCommitted(ctx context.Context, err error) integration {
	var tl *workspace.TooLargeError
	var nr *workspace.NestedRepoError
	switch {
	case ctx.Err() != nil:
		return integration{interrupted: true}
	case errors.As(err, &tl), errors.As(err, &nr):
		return integration{bounce: "Not done: your tree cannot be recorded: " + cleanText(err.Error(), 500)}
	case errors.Is(err, gitx.ErrConflict):
		return integration{bounce: "Not done: your tree still has unresolved conflict markers (" + cleanText(err.Error(), 300) +
			"). Resolve them, run your checks, then call task done again."}
	}
	return integration{infra: err}
}

// conflictBounce is what a worker is told when its work conflicts with what was
// merged before it. The integration tip is merged into its tree so the conflict is
// there to resolve, and the hunks shown are the ones in its files.
func (s *Swarm) conflictBounce(ctx context.Context, m *member, qc *workspace.Conflict) string {
	var sb strings.Builder
	sb.WriteString("Not done: your changes conflict with work that was already merged. ")
	uctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	upd, err := m.tree.Update(uctx, s.deps.Isolation.Queue.Tip())
	switch {
	case err != nil:
		sb.WriteString("The harness could not bring the integrated work into your tree (" + cleanText(err.Error(), 160) + "). ")
	case upd != nil:
		sb.WriteString("The harness has merged the integration branch into your tree, so the conflict is there to resolve: ")
		sb.WriteString(conflictHunks(upd))
		sb.WriteString(" ")
	default:
		sb.WriteString("The integration branch has now been merged into your tree without conflicts: run your checks and call task done again. ")
		return sb.String()
	}
	if qc != nil {
		sb.WriteString(cleanBlock(qc.Suggest, 700))
	}
	return sb.String()
}

// conflictHunks renders the conflicting regions of a tree in the conflicted state
// Tree.Update leaves: which files, and the first few hunks with both versions. In the
// markers "ours" is the worker's own version and "theirs" what was integrated.
func conflictHunks(c *workspace.Conflict) string {
	if c == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d file(s): %s. In the markers (<<<<<<< ours, =======, >>>>>>> theirs) ours is your version and theirs is the integrated one.", len(c.Files), cleanText(strings.Join(firstN(c.Files, 8), ", "), 240))
	for i, h := range c.Hunks {
		if i == 3 {
			fmt.Fprintf(&sb, "\n(+%d more conflicting region(s): see the markers)", len(c.Hunks)-i)
			break
		}
		fmt.Fprintf(&sb, "\n%s:%d\n<<<<<<< ours\n%s\n=======\n%s\n>>>>>>> theirs", cleanText(h.File, 120), h.Line, truncRunes(h.Ours, 500), truncRunes(h.Theirs, 500))
	}
	return sb.String()
}

// verifyBounce is what a worker is told when its work merged cleanly but the merged
// result fails the verifier. Its tree is brought to the merged state, where the
// failure reproduces.
func (s *Swarm) verifyBounce(ctx context.Context, m *member, vr *workspace.VerifyResult) string {
	cmd, code, out := cleanText(s.cfg.VerifyCmd, 80), -1, ""
	if vr != nil {
		cmd, code, out = cleanText(vr.Cmd, 80), vr.ExitCode, vr.Output
	}
	uctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	state := "The integration branch is now merged into your tree: reproduce the failure there, fix it, and call task done again."
	if upd, err := m.tree.Update(uctx, s.deps.Isolation.Queue.Tip()); err != nil {
		state = "The harness could not bring the integration branch into your tree (" + cleanText(err.Error(), 160) + "); fix what the output points at and call task done again."
	} else if upd != nil {
		state = "The integration branch is now merged into your tree and conflicts with your changes: " + conflictHunks(upd)
	}
	return fmt.Sprintf("Not done: your work merged cleanly with what other agents landed, but verification `%s` failed on the merged result (exit %d). %s\n%s", cmd, code, state, tailText(out, 2000))
}

func (s *Swarm) recordMerge(t Task, r mergeRec) {
	s.mu.Lock()
	s.merged[t.ID] = r
	s.mu.Unlock()
}

// mergedFor reports whether the task's current assignment is in the integration
// branch (or had nothing to merge). "accept" requires it in an isolated run: the
// merge queue's verification of the merged result is what stands in for the verifier
// run in the shared tree.
func (s *Swarm) mergedFor(t Task) (mergeRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.merged[t.ID]
	return r, ok && r.rev == t.Rev
}

// countBounce counts the times this assignment came back from the merge queue.
func (s *Swarm) countBounce(t Task) int {
	key := fmt.Sprintf("%s#%d", t.ID, t.Rev)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bounces[key]++
	return s.bounces[key]
}

// giveUpMerge returns a task whose work keeps failing to merge to the pool and stops
// its worker; the manager hears of it once.
func (s *Swarm) giveUpMerge(m *member, t Task, why string) string {
	line := ""
	if nt, applied := s.Board.Requeue(m.id, t.ID, t.Rev, "its work could not be merged: "+why, true, s.cfg.MaxAttempts); applied {
		line = s.requeueLine(m.id, nt, "its work could not be merged: "+why)
	}
	s.stopRun(m, "its work could not be merged", false)
	if line != "" {
		s.notifyManager(line)
	}
	return fmt.Sprintf("Not done: your work could not be merged after %d attempts. The task returns to the manager; stop now.", s.cfg.MaxAttempts)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- the end of the run ----------------------------------------------------------

// IntegrationReport says what became of the integration branch's result.
type IntegrationReport struct {
	// Branch is the integration branch, Base the commit the session's trees started
	// from and Tip the last verified integration commit.
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Tip    string `json:"tip"`
	// Files are the paths the result changes: what this application changed for
	// Integrate, everything the run changed for Finish.
	Files []string `json:"files,omitempty"`
	// Applied is true when the result is in the user's checkout; Committed says it
	// arrived as commits on the user's branch rather than as uncommitted edits.
	Applied   bool `json:"applied"`
	Committed bool `json:"committed,omitempty"`
	// Message is one paragraph for the person. Hint, when the result was not applied,
	// is one command that gets it.
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	// Kept lists worker trees that still hold work that is merged nowhere; the next
	// session's cleanup commits it onto their branches and removes the directories.
	Kept []string `json:"kept,omitempty"`
	// BranchKept says the integration branch (and the base branch it starts from) stay
	// in the repository. Once the result is in the checkout they are deleted: their
	// commits are there, or on the user's branch, and nothing is lost.
	BranchKept bool `json:"branch_kept,omitempty"`
}

// News reports whether this application put something new into the checkout, or
// failed to: the cases the person should hear about (an application that found
// nothing to do is not news).
func (r *IntegrationReport) News() bool {
	return r != nil && (!r.Applied || len(r.Files) > 0)
}

// applyState serialises applications of the result (a chat session applies whenever
// the manager stops and again at the end of the session) and remembers the
// integration commit the user's checkout already has, so the next application is only
// the difference, and every path applied so far, for the account at the end.
type applyState struct {
	mu        sync.Mutex
	applied   string
	files     map[string]bool
	committed string // the user's branch, once the result was committed onto it
	warned    string // the last failure the person was told of (it is not repeated)
}

// Integrate puts the integration branch's verified work that the user's checkout
// does not have yet into it: as a patch to the working tree (uncommitted edits, as a
// shared-tree run leaves them), or, with Isolation.Commit, as commits on the user's
// branch (a fast-forward, only when the work tree is clean and the branch has not
// moved). It is idempotent and incremental: it applies what changed since the last
// call. If applying fails (the user edited the same files meanwhile) nothing is
// changed, the branch stays, and the report says how to get the result.
func (s *Swarm) Integrate(ctx context.Context) *IntegrationReport {
	if !s.isolated() {
		return nil
	}
	iso := s.deps.Isolation
	q := iso.Queue
	s.apply.mu.Lock()
	defer s.apply.mu.Unlock()

	rep := &IntegrationReport{Branch: q.Branch(), Base: q.Base(), Tip: q.Tip()}
	from := s.apply.applied
	if from == "" {
		from = rep.Base
	}
	if rep.Tip == from {
		rep.Applied = true
		rep.Message = "Nothing new to apply: your checkout already has everything the integration branch holds."
		if rep.Tip == rep.Base {
			rep.Message = "The agents made no changes that reached the integration branch."
		}
		return rep
	}
	repo := iso.Manager.Repository()
	d, err := repo.Diff(ctx, from, gitx.DiffOptions{To: rep.Tip, Renames: true, MaxFiles: 1 << 20, MaxPatchBytes: 64 << 20})
	if err != nil {
		return s.notApplied(rep, iso, "could not compute the result: "+cleanText(err.Error(), 200))
	}
	for _, f := range d.Files {
		rep.Files = append(rep.Files, f.Path)
	}
	sort.Strings(rep.Files)
	if iso.Commit {
		return s.applyCommits(ctx, rep, iso)
	}
	if d.Empty() { // commits that add up to no change
		s.apply.applied = rep.Tip
		rep.Applied = true
		rep.Message = "The integration branch's commits change nothing in your checkout."
		return rep
	}
	if d.Truncated {
		return s.notApplied(rep, iso, "the result is too large to apply as one patch")
	}
	if err := repo.ApplyWith(ctx, d.Patch, gitx.ApplyOptions{Check: true}); err != nil {
		return s.notApplied(rep, iso, "it does not apply to your working tree now: "+firstLineOf(err.Error(), 200))
	}
	// A rewind must be able to undo what is about to happen to the user's files.
	paths := map[string]bool{}
	for _, f := range d.Files {
		paths[f.Path] = true
		if f.OldPath != "" {
			paths[f.OldPath] = true
		}
	}
	var abs []string
	for p := range paths {
		abs = append(abs, filepath.Join(repo.Root(), filepath.FromSlash(p)))
	}
	sort.Strings(abs)
	if s.deps.Snap != nil {
		for _, p := range abs {
			if err := s.deps.Snap.Before("integration", p); err != nil {
				return s.notApplied(rep, iso, "could not record a checkpoint before changing "+cleanText(filepath.Base(p), 80)+": "+cleanText(err.Error(), 160))
			}
		}
	}
	if err := repo.ApplyWith(ctx, d.Patch, gitx.ApplyOptions{}); err != nil {
		return s.notApplied(rep, iso, "applying it failed: "+firstLineOf(err.Error(), 200))
	}
	if s.deps.OnWrite != nil {
		for _, p := range abs {
			s.deps.OnWrite("integration", p)
		}
	}
	s.noteApplied(rep, "")
	rep.Applied = true
	rep.Message = appliedMessage(rep, rep.Files, "")
	s.emit(events.TypeSwarmIntegration, map[string]any{"branch": rep.Branch, "tip": rep.Tip, "applied": true, "files": firstN(rep.Files, 50)})
	return rep
}

// applyCommits moves the user's branch to the integration tip.
func (s *Swarm) applyCommits(ctx context.Context, rep *IntegrationReport, iso *Isolation) *IntegrationReport {
	res, err := iso.Queue.FastForward(ctx)
	if err != nil {
		return s.notApplied(rep, iso, firstLineOf(err.Error(), 300))
	}
	s.noteApplied(rep, res.Branch)
	rep.Applied, rep.Committed = true, true
	rep.Message = appliedMessage(rep, rep.Files, res.Branch)
	s.emit(events.TypeSwarmIntegration, map[string]any{"branch": rep.Branch, "tip": rep.Tip, "applied": true, "committed": true, "files": firstN(rep.Files, 50)})
	return rep
}

// noteApplied records that the user's checkout now has the integration branch up to
// rep.Tip (the caller holds apply.mu). branch is the user's branch when the result
// arrived as commits.
func (s *Swarm) noteApplied(rep *IntegrationReport, branch string) {
	s.apply.applied = rep.Tip
	if s.apply.files == nil {
		s.apply.files = map[string]bool{}
	}
	for _, f := range rep.Files {
		s.apply.files[f] = true
	}
	if branch != "" {
		s.apply.committed = branch
	}
}

// appliedMessage says what reached the checkout. branch is the user's branch when the
// result was committed onto it.
func appliedMessage(rep *IntegrationReport, files []string, branch string) string {
	if branch != "" {
		return fmt.Sprintf("Moved your branch %s to the integration tip %s: %d file(s) changed, as commits (branch %s).", branch, short(rep.Tip), len(files), rep.Branch)
	}
	return fmt.Sprintf("Applied %d file(s) to your working tree as uncommitted changes: %s. They are the verified result of the merge queue (branch %s).",
		len(files), listFiles(files), rep.Branch)
}

// notApplied fills in a report for a result that stays on its branch (the caller
// holds apply.mu). The hint starts from what the checkout already has, so a result
// applied in part is not applied twice.
func (s *Swarm) notApplied(rep *IntegrationReport, iso *Isolation, why string) *IntegrationReport {
	rep.BranchKept = true
	from := s.apply.applied
	if from == "" {
		from = rep.Base
	}
	if iso.Commit {
		rep.Hint = "git merge " + rep.Branch
	} else {
		rep.Hint = fmt.Sprintf("git diff --binary %s %s | git apply --3way", from, rep.Branch)
	}
	rep.Message = fmt.Sprintf("The result was NOT applied to your checkout (%s). It is safe on branch %s (%d file(s)). To get it: %s",
		why, rep.Branch, len(rep.Files), rep.Hint)
	s.emit(events.TypeSwarmIntegration, map[string]any{"branch": rep.Branch, "tip": rep.Tip, "applied": false, "reason": why})
	return rep
}

// applyMerged applies what has been merged so far when the manager stops (its run
// ended, or a wake run of an interactive session did): the person then finds the
// files in their checkout when the manager reports. It never blocks the run's result
// on a stopped context: the end of the session applies whatever is left. The person
// hears of new files and of failures, not of a check that found nothing to do.
func (s *Swarm) applyMerged(m *member) {
	if !s.isolated() || s.isClosed() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*gitTimeout)
	defer cancel()
	rep := s.Integrate(ctx)
	if !rep.News() {
		return
	}
	level := "integrate"
	if !rep.Applied {
		level = "warn"
		s.apply.mu.Lock()
		repeat := s.apply.warned == rep.Message
		s.apply.warned = rep.Message
		s.apply.mu.Unlock()
		if repeat {
			return // the same failure as last time: it has been said
		}
	}
	s.managerNotice(m, level, rep.Message)
}

func listFiles(files []string) string {
	shown := firstN(files, 8)
	txt := cleanText(strings.Join(shown, ", "), 300)
	if len(files) > len(shown) {
		txt += fmt.Sprintf(" and %d more", len(files)-len(shown))
	}
	return txt
}

func firstLineOf(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return cleanText(s, n)
}

// Finish ends an isolated run: it stops the swarm, applies the result (Integrate),
// removes the trees that hold nothing unmerged, and closes the merge queue. When the
// result reached the checkout the integration and base branches are deleted too
// (their commits are in the checkout, or on the user's branch); otherwise they stay.
// It is idempotent and returns nil for a swarm that is not isolated.
func (s *Swarm) Finish(ctx context.Context) *IntegrationReport {
	s.Shutdown()
	if !s.isolated() {
		return nil
	}
	s.finishMu.Lock()
	defer s.finishMu.Unlock()
	if s.finished != nil {
		return s.finished
	}
	iso := s.deps.Isolation
	rep := s.Integrate(ctx)
	if rep.Applied {
		// The account of the whole run, not of the last application.
		s.apply.mu.Lock()
		all, branch := slices.Sorted(maps.Keys(s.apply.files)), s.apply.committed
		s.apply.mu.Unlock()
		if len(all) > 0 {
			rep.Files, rep.Committed = all, branch != ""
			rep.Message = appliedMessage(rep, all, branch)
		}
	}
	for _, t := range iso.Manager.Trees() {
		s.unbindTreeByAgent(t.Agent)
		if err := t.Remove(ctx, false); err != nil {
			rep.Kept = append(rep.Kept, fmt.Sprintf("%s (branch %s)", t.Agent, t.Branch))
		}
	}
	_ = iso.Queue.Close(ctx)
	if rep.Applied && len(rep.Kept) == 0 {
		repo := iso.Manager.Repository()
		for _, b := range []string{rep.Branch, strings.TrimSuffix(rep.Branch, "_integration") + "_base"} {
			_ = repo.DeleteBranch(ctx, b)
		}
	} else if len(rep.Kept) > 0 {
		rep.BranchKept = true
		rep.Message += fmt.Sprintf(" %d worker tree(s) still hold work that was never merged and were kept: %s. The next session in this repository commits that work onto their branches and removes the directories.",
			len(rep.Kept), cleanText(strings.Join(rep.Kept, ", "), 300))
	}
	s.finished = rep
	return rep
}

func (s *Swarm) unbindTreeByAgent(id string) {
	s.Leases.UnbindTree(id)
	if c, ok := s.deps.Perm.(interface{ Unconfine(agent string) }); ok {
		c.Unconfine(id)
	}
}
