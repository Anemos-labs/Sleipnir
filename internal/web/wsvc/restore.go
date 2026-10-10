package wsvc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// restoreRec is the latest restore of a session while it can be undone: the checkpoint
// put back, the files written, when, the safety checkpoint's id (the undo capture, as
// the page names it) and the capture's id in the store.
type restoreRec struct {
	dir, to, cp string
	files       []string
	at          float64
	when        time.Time
	safety      string
	undo        string
}

// restoreLabelRE reads the label the store gives a restore's undo capture.
var restoreLabelRE = regexp.MustCompile(`^restore (cp_\d+)$`)

// latestRestore is the latest restore of a session that can still be undone: the one
// this process made, or after a restart the newest restore capture in the store.
func (s *service) latestRestore(sess *session.Session) *restoreRec {
	s.mu.Lock()
	rec := s.restores[sess.Dir]
	s.mu.Unlock()
	if rec != nil {
		return rec
	}
	undos := sess.Ckpt.Undos()
	for i := len(undos) - 1; i >= 0; i-- {
		u := undos[i]
		m := restoreLabelRE.FindStringSubmatch(u.Label)
		if m == nil {
			continue
		}
		rec = &restoreRec{dir: sess.Dir, to: uiID(m[1]), cp: m[1], files: u.Files, when: u.Time,
			at: float64(u.Time.UnixMilli()) / 1000, safety: uiID(m[1]) + "s", undo: u.ID}
		s.mu.Lock()
		s.restores[sess.Dir] = rec
		s.mu.Unlock()
		return rec
	}
	return nil
}

// infoOf finds a checkpoint's description.
func infoOf(sess *session.Session, cp string) (checkpoint.Info, bool) {
	for _, i := range sess.Ckpt.Infos() {
		if i.ID == cp {
			return i, true
		}
	}
	return checkpoint.Info{}, false
}

// handleRestore answers POST /api/sessions/{id}/ws/restore: with dryRun the preview of
// /rewind ID (what each file would become, and its lines); without, the restore itself,
// confirmed with the preview's scope (restore:<tab>:<cid>:<d16>), refused while a turn runs and refused whole when a
// file was edited since its last recorded write (force is not offered). The files it
// overwrites are captured first (the safety checkpoint <cid>s), so the restore can be
// undone; the agents are told to read the files again.
func (s *service) handleRestore(w http.ResponseWriter, r *http.Request) {
	acc, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	var req wire.RestoreRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	cp, ok := storeID(req.ID)
	if !ok {
		fail(w, werr(http.StatusNotFound, "no_checkpoint", "there is no such checkpoint"))
		return
	}
	if req.DryRun {
		release, err := s.acquire(r.Context())
		if err != nil {
			return
		}
		defer release()
		plan, _, err := s.preview(sess, acc.TabID(), cp)
		if err != nil {
			fail(w, err)
			return
		}
		_ = web.WriteJSON(w, http.StatusOK, plan)
		return
	}
	// The confirmation is of one plan: the one the person previewed. When the files or the
	// checkpoints changed since, the plan's scope is another, and the answer is the new plan.
	release, err := s.acquire(r.Context())
	if err != nil {
		return
	}
	shown, _, err := s.preview(sess, acc.TabID(), cp)
	release()
	if err != nil {
		fail(w, err)
		return
	}
	if req.Scope != "" && req.Scope != shown.Scope {
		fail(w, planChanged(shown))
		return
	}
	if !s.srv.RequireConfirm(w, r, shown.Scope) {
		return
	}
	var plan wire.RestorePlan
	var note string
	var evs []wire.Event
	err = s.exclusive(r.Context(), acc, func(sess *session.Session) error {
		pre, conflicts, err := s.preview(sess, acc.TabID(), cp)
		if err != nil {
			return err
		}
		if pre.Scope != shown.Scope {
			return planChanged(pre)
		}
		if len(conflicts) > 0 {
			return &wire.Error{Status: http.StatusConflict, Code: "conflict",
				Msg:    "files were edited after the agents' last write: a restore would destroy that, so nothing was restored",
				Detail: map[string]any{"files": conflicts}}
		}
		rep, undo, err := sess.Ckpt.RestoreWithUndo(cp)
		if err != nil && !rep.OK() && len(rep.Files) == 0 {
			return err
		}
		plan = planOf(pre, rep)
		plan.Applied = true
		note = checkpoint.RewindNote(rep, "the web workspace")
		if undo == "" {
			return nil
		}
		info, _ := infoOf(sess, cp)
		now := s.now()
		rec := &restoreRec{dir: sess.Dir, to: uiID(cp), cp: cp, at: float64(now.UnixMilli()) / 1000, when: now, safety: uiID(cp) + "s", undo: undo}
		for _, f := range rep.Files {
			if f.Outcome == checkpoint.OutcomeDone && f.Action != checkpoint.ActionRmdir {
				rec.files = append(rec.files, f.Path)
			}
		}
		sort.Strings(rec.files)
		plan.Safety = rec.safety
		s.mu.Lock()
		s.restores[sess.Dir] = rec
		s.mu.Unlock()
		evs = append(evs, &wire.Ckpt{CID: rec.safety, TS: clock(now), Files: len(rec.files), Note: "before restoring " + rec.to, Safety: true},
			&wire.Say{Who: "sys", Glyph: "↺", Text: "restored the files of " + rec.to + " (" + count(len(rec.files), "file") + "): " +
				info.Label + " · safety checkpoint " + rec.safety + " taken first"})
		if sess.Log != nil {
			_, _ = sess.Log.Emit("", "checkpoint.restore", map[string]any{"id": cp, "files": rec.files, "safety": rec.safety})
		}
		s.logAction(sess, "restore", map[string]any{"id": rec.to, "files": len(rec.files)})
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	acc.Emit(evs...)
	if note != "" {
		acc.Notify(note)
	}
	_ = web.WriteJSON(w, http.StatusOK, plan)
}

// planChanged is the refusal of a confirmed restore whose plan is not the one confirmed:
// 409 changed, with the plan as it is now (and its scope) in the detail.
func planChanged(plan wire.RestorePlan) error {
	return &wire.Error{Status: http.StatusConflict, Code: "changed",
		Msg: "the files changed since the preview: look at the new plan and confirm it again", Detail: plan}
}

// preview is the dry run of a restore as a plan, with the files a restore would refuse
// because they were edited since (their outcome is conflict); it refuses a checkpoint
// with nothing to put back (409 nothing). The plan's scope binds a confirmation to it:
// restore:<tab>:<cid>:<d16 of every file's path, action, outcome and current content>.
func (s *service) preview(sess *session.Session, tab, cp string) (wire.RestorePlan, []string, error) {
	rep, err := sess.Ckpt.Restore(cp, checkpoint.RestoreOpts{DryRun: true})
	if errors.Is(err, checkpoint.ErrUnknownCheckpoint) {
		return wire.RestorePlan{}, nil, werr(http.StatusNotFound, "no_checkpoint", "there is no such checkpoint")
	}
	if err != nil {
		return wire.RestorePlan{}, nil, err
	}
	info, _ := infoOf(sess, cp)
	plan := wire.RestorePlan{ID: uiID(cp), Label: info.Label, Time: clock(info.Time), Files: []wire.RestoreFile{}, Summary: rep.Summary()}
	var conflicts []string
	planned := 0
	for _, f := range rep.Files {
		rf := wire.RestoreFile{Path: f.Path, Action: string(f.Action), Outcome: string(f.Outcome), To: actionWords(f.Action), Reason: f.Detail}
		if f.Outcome == checkpoint.OutcomePlanned && f.Action != checkpoint.ActionRmdir {
			planned++
			rf.Added, rf.Removed = linesBack(sess, cp, f.Path)
			if secretCarrier(f.Path) && f.Action != checkpoint.ActionDelete {
				// the page never shows this file's values: say that the whole file, its
				// secrets included, goes back
				rf.Reason = strings.TrimSpace(rf.Reason + " it holds secrets: the values it held then come back too")
			}
		}
		if f.Outcome == checkpoint.OutcomeConflict {
			conflicts = append(conflicts, f.Path)
		}
		plan.Files = append(plan.Files, rf)
	}
	if planned == 0 && len(conflicts) == 0 {
		return plan, nil, werr(http.StatusConflict, "nothing", uiID(cp)+" has nothing to put back")
	}
	plan.Scope = "restore:" + tab + ":" + uiID(cp) + ":" + planDigest(sess, cp, rep)
	return plan, conflicts, nil
}

// planDigest fingerprints what a restore would do: for every file of the dry run its
// path, action and outcome, and the checksum of what the file holds now.
func planDigest(sess *session.Session, cp string, rep checkpoint.RestoreReport) string {
	items := make([][4]string, 0, len(rep.Files))
	for _, f := range rep.Files {
		items = append(items, [4]string{f.Path, string(f.Action), string(f.Outcome), nowSum(sess, f.Path)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i][0] < items[j][0] })
	b, _ := json.Marshal(struct {
		CP    string      `json:"cp"`
		Files [][4]string `json:"files"`
	}{cp, items})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// nowSum is the checksum of a project file now ("absent", or the kind of failure for
// something that is not a readable regular file).
func nowSum(sess *session.Session, rel string) string {
	data, ok, err := sess.Ckpt.CurrentContent(rel)
	switch {
	case err != nil:
		return "unreadable:" + err.Error()
	case !ok:
		return "absent"
	}
	return string(core.HashBytes(data))
}

// planOf is a preview with the outcomes of the restore that was made.
func planOf(pre wire.RestorePlan, rep checkpoint.RestoreReport) wire.RestorePlan {
	out := pre
	out.Summary = rep.Summary()
	byPath := map[string]checkpoint.FileResult{}
	for _, f := range rep.Files {
		byPath[f.Path] = f
	}
	out.Files = nil
	for _, f := range pre.Files {
		if res, ok := byPath[f.Path]; ok {
			f.Outcome, f.Reason = string(res.Outcome), res.Detail
		}
		out.Files = append(out.Files, f)
	}
	return out
}

// actionWords says what a restore does to a file, as the Restore dialog words it.
func actionWords(a checkpoint.Action) string {
	switch a {
	case checkpoint.ActionDelete, checkpoint.ActionRmdir:
		return "removed"
	case checkpoint.ActionNone:
		return "unchanged"
	}
	return "put back"
}

// linesBack counts the lines a restore of a file adds and removes (from now to the
// checkpoint's state); zero for content that is not text.
func linesBack(sess *session.Session, cp, rel string) (added, removed int) {
	now, _, err1 := sess.Ckpt.CurrentContent(rel)
	then, _, err2 := sess.Ckpt.ContentAt(cp, rel)
	if err1 != nil || err2 != nil || len(now) > maxText || len(then) > maxText || checkpoint.IsBinary(now) || checkpoint.IsBinary(then) {
		return 0, 0
	}
	d := checkpoint.DiffLines(now, then)
	return d.Added, d.Removed
}

// count words a number of things ("1 file", "3 files").
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return itoa(n) + " " + thing + "s"
}

// itoa formats an int.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	if neg {
		return "-" + string(d)
	}
	return string(d)
}

// handleRestoreUndo answers POST /api/sessions/{id}/ws/restore/undo, confirmed with
// restore.undo:<tab>: the files of the latest restore go back to what the safety
// checkpoint captured, when none changed since the restore.
func (s *service) handleRestoreUndo(w http.ResponseWriter, r *http.Request) {
	acc, _, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	if !s.srv.RequireConfirm(w, r, "restore.undo:"+acc.TabID()) {
		return
	}
	var plan wire.RestorePlan
	var rec *restoreRec
	var note string
	err := s.exclusive(r.Context(), acc, func(sess *session.Session) error {
		rec = s.latestRestore(sess)
		if rec == nil {
			return werr(http.StatusConflict, "nothing", "there is no restore to undo")
		}
		rep, err := sess.Ckpt.RestoreUndo(rec.undo)
		switch {
		case errors.Is(err, checkpoint.ErrUndoConflict):
			var changed []string
			for _, f := range rep.Files {
				if f.Outcome == checkpoint.OutcomeConflict || f.Outcome == checkpoint.OutcomeUnrestorable {
					changed = append(changed, f.Path)
				}
			}
			return &wire.Error{Status: http.StatusConflict, Code: "changed",
				Msg: "files changed after the restore: the undo would overwrite that, so nothing was written", Detail: map[string]any{"files": changed}}
		case errors.Is(err, checkpoint.ErrUnknownUndo):
			s.mu.Lock()
			delete(s.restores, sess.Dir)
			s.mu.Unlock()
			return werr(http.StatusConflict, "nothing", "there is no restore to undo")
		case err != nil && len(rep.Files) == 0:
			return err
		}
		s.mu.Lock()
		delete(s.restores, sess.Dir)
		s.mu.Unlock()
		plan = wire.RestorePlan{ID: rec.to, Applied: true, Safety: rec.safety, Summary: strings.Replace(rep.Summary(), "checkpoint "+rep.ID, "the restore of "+rec.to, 1), Files: []wire.RestoreFile{}}
		var files []string
		for _, f := range rep.Files {
			plan.Files = append(plan.Files, wire.RestoreFile{Path: f.Path, Action: string(f.Action), Outcome: string(f.Outcome), To: actionWords(f.Action), Reason: f.Detail})
			if f.Outcome == checkpoint.OutcomeDone && f.Action != checkpoint.ActionRmdir {
				files = append(files, f.Path)
			}
		}
		if sess.Log != nil {
			_, _ = sess.Log.Emit("", "checkpoint.restore", map[string]any{"id": rec.cp, "files": files, "safety": rec.safety, "undo_of": rec.to})
		}
		s.logAction(sess, "restore.undo", map[string]any{"id": rec.to, "files": len(files)})
		if len(files) > 0 {
			note = "Notice from the harness: the person undid the rewind to checkpoint " + rec.cp + ": these files hold again what they held before it: " +
				strings.Join(files, ", ") + ". Read them again before you rely on what you remember of them."
		}
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	acc.Emit(&wire.Say{Who: "sys", Glyph: "↺", Text: "restore of " + rec.to + " undone: the files are as the safety checkpoint " + rec.safety + " had them"})
	if note != "" {
		acc.Notify(note)
	}
	_ = web.WriteJSON(w, http.StatusOK, plan)
}

// handleReviewed answers PUT /api/sessions/{id}/ws/reviewed: marks (on) or unmarks a file
// as reviewed at a checkpoint, kept in the session's sidecar (web.json).
func (s *service) handleReviewed(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	var req wire.ReviewedRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	rel, err := cleanRel(req.Path)
	if err != nil {
		fail(w, err)
		return
	}
	if len(req.Cp) > 64 || strings.ContainsAny(req.Cp, "\x00\n\r") {
		fail(w, werr(http.StatusBadRequest, "bad_request", "cp names a checkpoint (c07) or live"))
		return
	}
	err = session.UpdateMeta(sess.Dir, func(m *session.Meta) {
		if req.On {
			if m.Reviewed == nil {
				m.Reviewed = map[string]string{}
			}
			m.Reviewed[rel] = req.Cp
		} else {
			delete(m.Reviewed, rel)
		}
	})
	if err != nil {
		fail(w, werr(http.StatusInternalServerError, "internal", "the reviewed mark could not be saved"))
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
