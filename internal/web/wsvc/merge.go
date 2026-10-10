package wsvc

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The isolated team's side of the Workspace: its worktrees, its merge
// queue, the output of each verification gate, and applying the verified work now.

// errNotIsolated is the refusal of an isolated team's route on a shared-tree session.
var errNotIsolated = werr(http.StatusConflict, "not_isolated", "no worktree isolation in this run: work is written to the checkout directly")

// maxTreeFiles bounds the changed files listed for one worktree.
const maxTreeFiles = 200

// handleWorktrees answers GET /api/sessions/{id}/ws/worktrees: one row per worker tree
// (agent, branch, base, head, whether it has uncommitted work, the files it changed).
func (s *service) handleWorktrees(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	m := sess.Worktrees()
	if m == nil {
		fail(w, errNotIsolated)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	out := []wire.Worktree{}
	for _, t := range m.Trees() {
		wt := wire.Worktree{Agent: t.Agent, Path: t.Path, Branch: t.Branch, Base: t.Base}
		if h, err := t.Head(ctx); err == nil {
			wt.Head = h
		}
		if d, err := t.Dirty(ctx); err == nil {
			wt.Dirty = d
		}
		if files, err := t.Changed(ctx); err == nil {
			if len(files) > maxTreeFiles {
				files = files[:maxTreeFiles]
			}
			wt.Files = files
		}
		out = append(out, wt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	_ = web.WriteJSON(w, http.StatusOK, map[string]any{"worktrees": out})
}

// handleQueue answers GET /api/sessions/{id}/ws/queue: the merge queue now.
func (s *service) handleQueue(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	q := sess.MergeQueue()
	if q == nil {
		fail(w, errNotIsolated)
		return
	}
	st := q.Status()
	out := wire.MergeQueueStatus{Branch: st.Branch, Tip: st.Tip, Healthy: st.Healthy, Waiting: []string{}, Landed: []wire.LandedEntry{},
		Verify: sess.Options().Verify}
	if st.Active != nil {
		out.Active, out.Phase = taskID(st.Active.Task), st.Active.Phase
	}
	for _, e := range st.Waiting {
		out.Waiting = append(out.Waiting, taskID(e.Task))
	}
	for _, l := range st.Landed {
		out.Landed = append(out.Landed, wire.LandedEntry{Agent: l.Agent, Task: taskID(l.Task), Commit: l.Commit, Files: nonNil(l.Files)})
	}
	_ = web.WriteJSON(w, http.StatusOK, out)
}

// handleVerify answers GET /api/sessions/{id}/ws/verify/{task}: every run of the task's
// verification gate, oldest first, with the output each kept (the tail); the top fields
// are the last run's.
func (s *service) handleVerify(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	task := r.PathValue("task")
	if !taskIDRE.MatchString(task) {
		fail(w, werr(http.StatusBadRequest, "bad_request", "that is not a task id"))
		return
	}
	logs := s.logOf(sess.Dir)
	logs.refresh()
	var runs []wire.VerifyRun
	for _, g := range logs.runsOf(task) {
		if g.gate == "merge" && sess.MergeQueue() != nil {
			continue // the queue itself has these, with the output of the passing ones too
		}
		out := g.output
		if !g.inline {
			out = blobText(sess, g.output)
		}
		runs = append(runs, wire.VerifyRun{Cmd: g.cmd, Exit: g.exit, OK: g.ok, TimedOut: g.timedOut, Ms: g.ms, At: g.at.UnixMilli(),
			Agent: g.agent, Gate: g.gate, Out: out, Truncated: g.bytes > len(out)})
	}
	if q := sess.MergeQueue(); q != nil {
		for _, v := range q.Verifications("") {
			if taskID(v.Task) != task {
				continue
			}
			res := v.Result
			runs = append(runs, wire.VerifyRun{Cmd: res.Cmd, Exit: res.ExitCode, OK: res.OK(), TimedOut: res.TimedOut, Ms: res.Duration.Milliseconds(),
				At: v.At.UnixMilli(), Agent: v.Agent, Gate: "merge", Out: res.Output, Truncated: res.Truncated})
		}
	}
	if len(runs) == 0 {
		fail(w, werr(http.StatusNotFound, "not_found", "no verification of "+task+" has run"))
		return
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].At < runs[j].At })
	for i := range runs {
		runs[i].Attempt = i + 1
	}
	last := runs[len(runs)-1]
	_ = web.WriteJSON(w, http.StatusOK, wire.VerifyOutput{Task: task, Cmd: last.Cmd, ExitCode: last.Exit, TimedOut: last.TimedOut, Output: last.Out, Runs: runs})
}

// blobText reads a gate run's output from the session's blob store ("" when it is gone).
func blobText(sess *session.Session, h string) string {
	if h == "" || sess.Blobs == nil {
		return ""
	}
	b, err := sess.Blobs.Get(core.Hash(h))
	if err != nil || len(b) > 1<<20 {
		return ""
	}
	return string(b)
}

// handleAccept answers POST /api/sessions/{id}/ws/accept: applies the isolated team's
// verified work to the checkout now, as commits on the person's branch (the default) or
// as uncommitted edits (mode "edits"). A dry run reports the files, the tasks and whether
// commits are possible, needs no confirmation and changes nothing; the application needs
// the dry run's scope (accept:<tab>:<d16>) and no running turn.
func (s *service) handleAccept(w http.ResponseWriter, r *http.Request) {
	acc, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	var req wire.AcceptRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	var commits bool
	switch req.Mode {
	case "", "commits":
		commits = true
	case "edits":
	default:
		fail(w, werr(http.StatusBadRequest, "bad_request", "mode is commits or edits"))
		return
	}
	if len(req.Message) > 4000 || strings.ContainsRune(req.Message, 0) {
		fail(w, werr(http.StatusBadRequest, "bad_request", "the commit message is at most 4,000 characters"))
		return
	}
	if !sess.WorktreeIsolation() {
		fail(w, errNotIsolated)
		return
	}
	mode := map[bool]string{true: "commits", false: "edits"}[commits]
	// The dry run, always: it is the answer to a dry run, and what a confirmation binds.
	dry, err := sess.ApplyVerified(r.Context(), swarm.AcceptOptions{Commits: commits, DryRun: true})
	if err != nil {
		fail(w, acceptError(err, dry))
		return
	}
	shown := acceptResult(dry, acc.TabID(), mode)
	if req.DryRun {
		_ = web.WriteJSON(w, http.StatusOK, shown)
		return
	}
	if req.Scope != "" && req.Scope != shown.Scope {
		fail(w, acceptChanged(shown))
		return
	}
	if !s.srv.RequireConfirm(w, r, shown.Scope) {
		return
	}
	opts := swarm.AcceptOptions{Commits: commits, Message: req.Message, ExpectTip: dry.Tip, ExpectFrom: dry.From}
	var rep *swarm.AcceptReport
	err = s.exclusive(r.Context(), acc, func(sess *session.Session) error {
		var err error
		rep, err = sess.ApplyVerified(r.Context(), opts)
		if errors.Is(err, swarm.ErrAcceptChanged) {
			now, derr := sess.ApplyVerified(r.Context(), swarm.AcceptOptions{Commits: commits, DryRun: true})
			if derr != nil {
				return derr
			}
			return acceptChanged(acceptResult(now, acc.TabID(), mode))
		}
		if err == nil {
			s.logAction(sess, "accept", map[string]any{"mode": mode, "files": len(rep.Files), "commit": rep.Commit})
		}
		return err
	})
	if err != nil {
		fail(w, acceptError(err, rep))
		return
	}
	if rep.Message != "" {
		acc.Emit(&wire.Say{Who: "sys", Glyph: "✓", Text: rep.Message})
	}
	_ = web.WriteJSON(w, http.StatusOK, acceptResult(rep, acc.TabID(), mode))
}

// acceptResult is the wire form of an application's report. A dry run's carries the scope
// that confirms exactly it: accept:<tab>:<d16 of the mode, the integration tip, the
// checkout's position, the files and the tasks>.
func acceptResult(rep *swarm.AcceptReport, tab, mode string) wire.AcceptResult {
	out := wire.AcceptResult{Commit: rep.Commit, Files: nonNil(rep.Files), Branch: rep.Checkout, Tasks: rep.Tasks, Applied: rep.Applied,
		Committed: rep.Committed, Waiting: rep.Waiting, CanCommit: rep.CanCommit, Message: rep.Message, DryRun: rep.DryRun}
	if rep.CommitBlocked != nil {
		out.CommitBlocked = clip(rep.CommitBlocked.Error(), 400)
	}
	if rep.DryRun {
		out.Scope = "accept:" + tab + ":" + d16(map[string]string{"mode": mode, "tip": rep.Tip, "from": rep.From,
			"files": strings.Join(rep.Files, "\x00"), "tasks": strings.Join(rep.Tasks, "\x00")})
	}
	return out
}

// acceptChanged is the refusal of a confirmed application whose work is not the one
// confirmed: 409 changed, with the dry run as it is now (and its scope) in the detail.
func acceptChanged(now wire.AcceptResult) error {
	return &wire.Error{Status: http.StatusConflict, Code: "changed",
		Msg: "the verified work changed since it was shown: look at it again and confirm", Detail: now}
}

// acceptError maps Accept's refusals to the errors of the API (409 not_isolated, nothing,
// dirty, moved); a failed application is 409 conflict with the report's message.
func acceptError(err error, rep *swarm.AcceptReport) error {
	var we *wire.Error
	switch {
	case errors.As(err, &we):
		return err
	case errors.Is(err, session.ErrNotIsolated):
		return errNotIsolated
	case errors.Is(err, swarm.ErrNothingToAccept):
		return werr(http.StatusConflict, "nothing", "nothing verified is waiting: the checkout already has it")
	case errors.Is(err, swarm.ErrCheckoutDirty):
		return &wire.Error{Status: http.StatusConflict, Code: "dirty", Msg: clip(err.Error(), 400)}
	case errors.Is(err, swarm.ErrBranchMoved):
		return &wire.Error{Status: http.StatusConflict, Code: "moved", Msg: clip(err.Error(), 400)}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	msg := "the verified work could not be applied"
	if rep != nil && rep.Message != "" {
		msg = rep.Message
	}
	return &wire.Error{Status: http.StatusConflict, Code: "conflict", Msg: clip(msg, 500)}
}

// clip cuts a message for a toast.
func clip(s string, n int) string {
	s = strings.TrimPrefix(s, "swarm: ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
