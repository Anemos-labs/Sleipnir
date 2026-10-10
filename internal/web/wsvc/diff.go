package wsvc

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// handleFile answers GET /api/sessions/{id}/ws/file?path=&at=: the file's text at a point
// (live by default) with who wrote each line.
func (s *service) handleFile(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	rel, err := s.readable(sess, r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	at, err := parsePoint(r.URL.Query().Get("at"), "live")
	if err != nil {
		fail(w, err)
		return
	}
	release, err := s.acquire(r.Context())
	if err != nil {
		return
	}
	defer release()
	out, err := s.fileAt(r.Context(), sess, rel, at)
	if err != nil {
		fail(w, err)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, out)
}

// fileAt reads a file at a point with its authorship.
func (s *service) fileAt(ctx context.Context, sess *session.Session, rel string, at point) (wire.WsContent, error) {
	out := wire.WsContent{Path: rel, At: at.name(), Blame: []wire.BlameRun{}}
	id := at.cp
	if at.base {
		list := sess.Ckpt.Infos()
		if len(list) > 0 {
			id = list[0].ID
		}
	}
	a, err := sess.Ckpt.AuthorshipAt(id, rel)
	switch {
	case errors.Is(err, checkpoint.ErrUnknownCheckpoint):
		return out, werr(http.StatusNotFound, "no_checkpoint", "there is no such checkpoint")
	case errors.Is(err, checkpoint.ErrOutsideRoot):
		return out, errBadPath
	case errors.Is(err, checkpoint.ErrNotRegular), errors.Is(err, checkpoint.ErrNotSaved), errors.Is(err, checkpoint.ErrTooLarge):
		out.Exists, out.Binary = true, true
		return out, nil
	case err != nil:
		return out, err
	}
	out.Exists = a.Exists
	if !a.Exists {
		return out, errNoFile
	}
	out.Size = int64(len(a.Content))
	if a.Binary {
		out.Binary = true
		return out, nil
	}
	text := a.Content
	if len(text) > maxText {
		text, out.Truncated = trimToRune(text[:maxText]), true
	}
	out.Text = string(text)
	if secretCarrier(rel) {
		out.Text = maskSecrets(out.Text)
	}
	out.Exact = a.Exact
	runs := s.blameRuns(sess, rel, a.Lines)
	if sess.WorktreeIsolation() {
		if gr, exact, ok := s.gitBlame(ctx, sess, rel, a.Content); ok {
			runs, out.Exact = gr, exact
		}
	}
	if out.Truncated { // runs past the text sent are of no use to the page
		n := strings.Count(out.Text, "\n") + 1
		runs = cutRuns(runs, n)
	}
	out.Blame = runs
	return out, nil
}

// trimToRune drops a partial UTF-8 character at the end of b.
func trimToRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// blameRuns turns per-line authors into runs, with the task each author was on when it
// wrote the file in that checkpoint.
func (s *service) blameRuns(sess *session.Session, rel string, lines []checkpoint.LineAuthor) []wire.BlameRun {
	logs := s.logOf(sess.Dir)
	taskOf := map[string]string{}
	writes := sess.Ckpt.Writes(rel)
	var out []wire.BlameRun
	for i, l := range lines {
		ag, id := l.Agent, ""
		if ag == "" {
			ag = "-"
		} else {
			id = uiID(l.Checkpoint)
		}
		if n := len(out); n > 0 && out[n-1].Ag == ag && out[n-1].ID == id {
			out[n-1].Count++
			continue
		}
		task := ""
		if ag != "-" && ag != checkpoint.PersonAgent {
			key := ag + "\x00" + l.Checkpoint
			t, ok := taskOf[key]
			if !ok {
				var at time.Time
				for _, w := range writes {
					if w.Agent == ag && w.Checkpoint == l.Checkpoint {
						at = w.At
					}
				}
				t = logs.taskAt(ag, at)
				taskOf[key] = t
			}
			task = t
		}
		out = append(out, wire.BlameRun{Line: i + 1, Count: 1, Ag: ag, ID: id, Task: task})
	}
	if out == nil {
		out = []wire.BlameRun{}
	}
	return out
}

// cutRuns keeps the runs of the first n lines.
func cutRuns(runs []wire.BlameRun, n int) []wire.BlameRun {
	var out []wire.BlameRun
	for _, r := range runs {
		if r.Line > n {
			break
		}
		if r.Line+r.Count-1 > n {
			r.Count = n - r.Line + 1
		}
		out = append(out, r)
	}
	if out == nil {
		out = []wire.BlameRun{}
	}
	return out
}

// gitBlame is the authorship of an isolated team's file from git: the content (the
// checkout's file at the point asked) blamed against the history of the integration
// branch, whose commits each agent made in its own tree. Lines no commit introduced, or
// commits by no agent, have no author ("-"); the result is exact when every line has a
// commit.
func (s *service) gitBlame(ctx context.Context, sess *session.Session, rel string, content []byte) ([]wire.BlameRun, bool, bool) {
	q := sess.MergeQueue()
	m := sess.Worktrees()
	if q == nil || m == nil || m.Repo == nil {
		return nil, false, false
	}
	repoRel := rel
	if sub := subdirOf(sess); sub != "" {
		repoRel = sub + "/" + rel
	}
	lines, err := m.Repo.BlameContents(ctx, q.Branch(), repoRel, content)
	if err != nil {
		return nil, false, false
	}
	exact := true
	var out []wire.BlameRun
	for _, l := range lines {
		ag, id, task := "-", "", ""
		if l.Commit == gitx.ZeroCommit {
			exact = false
		} else if strings.HasSuffix(l.Email, "@sleipnir.invalid") && !strings.HasPrefix(l.Email, "queue@") {
			ag = strings.TrimSuffix(l.Email, "@sleipnir.invalid")
			id = shortSHA(l.Commit)
			task = taskID(l.Summary)
			if task == l.Summary {
				task = ""
			}
		}
		if n := len(out); n > 0 && out[n-1].Ag == ag && out[n-1].ID == id && out[n-1].Line+out[n-1].Count == l.Line {
			out[n-1].Count++
			continue
		}
		out = append(out, wire.BlameRun{Line: l.Line, Count: 1, Ag: ag, ID: id, Task: task})
	}
	if out == nil {
		out = []wire.BlameRun{}
	}
	return out, exact, true
}

// subdirOf is the session's root relative to its repository ("" at the top).
func subdirOf(sess *session.Session) string {
	m := sess.Worktrees()
	if m == nil || m.Repo == nil {
		return ""
	}
	sub, err := filepath.Rel(m.Repo.Root(), resolved(sess.Root()))
	if err != nil || sub == "." {
		return ""
	}
	return filepath.ToSlash(sub)
}

// shortSHA is the first eight characters of a commit name.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// ---- diffs ----------------------------------------------------------------------------------

// handleDiff answers GET /api/sessions/{id}/ws/diff?path=&from=&to= (from base and to live
// by default).
func (s *service) handleDiff(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rel, err := s.readable(sess, q.Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	from, err := parsePoint(q.Get("from"), "base")
	if err != nil {
		fail(w, err)
		return
	}
	to, err := parsePoint(q.Get("to"), "live")
	if err != nil {
		fail(w, err)
		return
	}
	release, err := s.acquire(r.Context())
	if err != nil {
		return
	}
	defer release()
	d, _, err := s.diffOf(sess, rel, from, to)
	if err != nil {
		fail(w, err)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, d.out)
}

// fileDiff is a diff and the contents it was computed from.
type fileDiff struct {
	out      wire.WsDiff
	old, new []byte
	newOK    bool // the new side exists
	ld       checkpoint.LineDiff
}

// diffOf computes the diff of a file between two points.
func (s *service) diffOf(sess *session.Session, rel string, from, to point) (fileDiff, bool, error) {
	fd := fileDiff{out: wire.WsDiff{Path: rel, From: from.name(), To: to.name(), Hunks: []wire.Hunk{}}}
	oldData, oldOK, err := content(sess, from, rel)
	if err != nil && !isContentErr(err) {
		return fd, false, err
	}
	oldBad := err != nil
	newData, newOK, err := content(sess, to, rel)
	if err != nil && !isContentErr(err) {
		return fd, false, err
	}
	newBad := err != nil
	if !oldOK && !newOK {
		return fd, false, errNoFile
	}
	fd.old, fd.new, fd.newOK = oldData, newData, newOK
	switch {
	case oldBad || newBad || checkpoint.IsBinary(oldData) || checkpoint.IsBinary(newData):
		fd.out.Binary = true
		return fd, false, nil
	case len(oldData) > maxText || len(newData) > maxText:
		fd.out.Truncated = true
		return fd, false, nil
	}
	if secretCarrier(rel) {
		oldData, newData = []byte(maskSecrets(string(oldData))), []byte(maskSecrets(string(newData)))
	}
	fd.ld = checkpoint.DiffLines(oldData, newData)
	fd.out.Added, fd.out.Removed, fd.out.Truncated = fd.ld.Added, fd.ld.Removed, fd.ld.Truncated
	for _, h := range fd.ld.Hunks {
		wh := wire.Hunk{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines, Lines: make([]wire.HunkLine, 0, len(h.Lines))}
		for _, l := range h.Lines {
			wh.Lines = append(wh.Lines, wire.HunkLine{T: string(rune(l.Op)), S: l.Text})
		}
		wh.Section = section(oldData, h.OldStart)
		fd.out.Hunks = append(fd.out.Hunks, wh)
	}
	return fd, true, nil
}

// isContentErr reports a content that exists but cannot be shown as text.
func isContentErr(err error) bool {
	return errors.Is(err, checkpoint.ErrNotRegular) || errors.Is(err, checkpoint.ErrNotSaved) || errors.Is(err, checkpoint.ErrTooLarge)
}

// section is the hunk header's context, as git's default: the nearest line above the
// hunk that starts with a letter, '_' or '$', cut at 80 bytes.
func section(old []byte, oldStart int) string {
	lines := bytes.Split(old, []byte("\n"))
	for i := min(oldStart-2, len(lines)-1); i >= 0; i-- {
		l := lines[i]
		if len(l) > 0 && (l[0] == '_' || l[0] == '$' || l[0] >= 'a' && l[0] <= 'z' || l[0] >= 'A' && l[0] <= 'Z') {
			s := strings.TrimRight(string(trimToRune(l[:min(len(l), 80)])), " \t\r")
			return s
		}
	}
	return ""
}

// ---- reverting a hunk -----------------------------------------------------------------------

// revertRec is a hunk revert that can be undone.
type revertRec struct {
	id, dir, path, key string
	undo               string
	at                 float64
}

// revertsOf lists a session's reverts, oldest first.
func (s *service) revertsOf(sess *session.Session) []wire.WsRevert {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []wire.WsRevert
	for _, r := range s.reverts {
		if r.dir == sess.Dir {
			out = append(out, wire.WsRevert{ID: r.id, Path: r.path, Key: r.key, At: r.at})
		}
	}
	sortReverts(out)
	return out
}

// sortReverts orders reverts by time, then id.
func sortReverts(r []wire.WsRevert) {
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && (r[j].At < r[j-1].At || r[j].At == r[j-1].At && r[j].ID < r[j-1].ID); j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}

// parseKey reads a hunk key "oldStart:newStart".
func parseKey(k string) (int, int, bool) {
	a, b, ok := strings.Cut(k, ":")
	if !ok || len(k) > 24 {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(b)
	return x, y, err1 == nil && err2 == nil && x >= 0 && y >= 0
}

// handleRevert answers POST /api/sessions/{id}/ws/revert: the server takes the hunk from
// its own diff of from..to (the page sends its key, never patch text), puts the old lines
// back in the live file where the hunk's new lines are, records the write as the
// person's (Before, the write, After: /rewind sees it) with an undo capture, and tells
// the agent that last wrote the file. Confirmed with revert:<id>:<d16 of path,key,from,to>.
func (s *service) handleRevert(w http.ResponseWriter, r *http.Request) {
	acc, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	var req wire.RevertRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	rel, err := s.readable(sess, req.Path)
	if err != nil {
		fail(w, err)
		return
	}
	if _, _, ok := parseKey(req.Key); !ok {
		fail(w, werr(http.StatusBadRequest, "bad_request", "a hunk key is oldStart:newStart"))
		return
	}
	from, err := parsePoint(req.From, "base")
	if err != nil {
		fail(w, err)
		return
	}
	to, err := parsePoint(req.To, "live")
	if err != nil {
		fail(w, err)
		return
	}
	scope := "revert:" + acc.TabID() + ":" + d16(map[string]string{"path": req.Path, "key": req.Key, "from": req.From, "to": req.To})
	if !s.srv.RequireConfirm(w, r, scope) {
		return
	}
	prev, _ := sess.LastWriter(rel) // the agent to tell, before the revert makes the person the last writer
	var rec *revertRec
	err = s.exclusive(r.Context(), acc, func(sess *session.Session) error {
		var err error
		rec, err = s.revert(sess, rel, req.Key, from, to)
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	acc.Emit(&wire.Say{Who: "sys", Glyph: "↺", Text: "reverted a hunk of " + rel})
	if prev != "" && prev != checkpoint.PersonAgent {
		acc.Notify("Notice from the harness: the person reverted part of your change to " + rel + " (lines around " + req.Key + "). Read the file again before you edit it.")
	}
	s.logAction(sess, "revert", map[string]any{"path": rel, "key": req.Key, "from": from.name(), "to": to.name()})
	_ = web.WriteJSON(w, http.StatusOK, wire.WsRevert{ID: rec.id, Path: rec.path, Key: rec.key, At: rec.at})
}

// revert reverses one hunk of the diff of rel between two points in the live file.
func (s *service) revert(sess *session.Session, rel, key string, from, to point) (*revertRec, error) {
	fd, ok, err := s.diffOf(sess, rel, from, to)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, werr(http.StatusUnprocessableEntity, "conflict", "this file has no line diff to revert (binary or too large)")
	}
	var hunk *checkpoint.Hunk
	for i, h := range fd.ld.Hunks {
		if strconv.Itoa(h.OldStart)+":"+strconv.Itoa(h.NewStart) == key {
			hunk = &fd.ld.Hunks[i]
		}
	}
	if hunk == nil {
		return nil, werr(http.StatusConflict, "changed", "the file changed since the diff was drawn: that hunk is not in it now")
	}
	live, exists, err := sess.Ckpt.CurrentContent(rel)
	if err != nil {
		return nil, werr(http.StatusUnprocessableEntity, "conflict", "the file cannot be read as text now")
	}
	if !exists {
		return nil, werr(http.StatusUnprocessableEntity, "conflict", "the file does not exist now")
	}
	next, ok := reverseHunk(fd.old, fd.new, live, *hunk)
	if !ok {
		return nil, werr(http.StatusUnprocessableEntity, "conflict", "the hunk does not apply to the file as it is now")
	}
	undo, err := sess.Ckpt.WriteFile(checkpoint.PersonAgent, rel, next, core.HashBytes(live))
	switch {
	case errors.Is(err, checkpoint.ErrChanged):
		return nil, werr(http.StatusConflict, "changed", "the file changed while the hunk was being reverted; nothing was written")
	case errors.Is(err, checkpoint.ErrOutsideRoot):
		return nil, errBadPath
	case err != nil:
		return nil, err
	}
	id, err := randomID("v_")
	if err != nil {
		_, _ = sess.Ckpt.RestoreUndo(undo)
		return nil, err
	}
	rec := &revertRec{id: id, dir: sess.Dir, path: rel, key: key, undo: undo, at: float64(s.now().UnixMilli()) / 1000}
	s.mu.Lock()
	s.reverts[id] = rec
	s.mu.Unlock()
	return rec, nil
}

// reverseHunk puts the old lines of a hunk back in live where its new lines are: at the
// hunk's position when they are there (always, when live is the diff's new side), else
// where its new lines (context included) occur in live nearest to that position. false
// when they do not occur, or occur at two places equally near.
func reverseHunk(old, new, live []byte, h checkpoint.Hunk) ([]byte, bool) {
	oldLines, newLines, liveLines := keepLines(old), keepLines(new), keepLines(live)
	oStart, nStart := h.OldStart-1, h.NewStart-1
	if h.OldLines == 0 {
		oStart = h.OldStart
	}
	if h.NewLines == 0 {
		nStart = h.NewStart
	}
	if oStart < 0 || nStart < 0 || oStart+h.OldLines > len(oldLines) || nStart+h.NewLines > len(newLines) {
		return nil, false
	}
	want := newLines[nStart : nStart+h.NewLines]
	repl := oldLines[oStart : oStart+h.OldLines]
	at := -1
	if matchAt(liveLines, want, nStart) {
		at = nStart
	}
	if at < 0 && len(want) > 0 {
		best := -1
		for i := 0; i+len(want) <= len(liveLines); i++ {
			if !matchAt(liveLines, want, i) {
				continue
			}
			if best >= 0 && abs(i-nStart) == abs(best-nStart) {
				return nil, false // two places are as likely: refuse rather than guess
			}
			if best < 0 || abs(i-nStart) < abs(best-nStart) {
				best = i
			}
		}
		at = best
	}
	if at < 0 {
		return nil, false
	}
	var out bytes.Buffer
	for _, l := range liveLines[:at] {
		out.WriteString(l)
	}
	for _, l := range repl {
		out.WriteString(l)
	}
	for _, l := range liveLines[at+len(want):] {
		out.WriteString(l)
	}
	return out.Bytes(), true
}

// matchAt reports whether lines holds want at position i.
func matchAt(lines, want []string, i int) bool {
	if i < 0 || i+len(want) > len(lines) {
		return false
	}
	for k, w := range want {
		if lines[i+k] != w {
			return false
		}
	}
	return true
}

// keepLines splits text into lines that keep their terminators.
func keepLines(b []byte) []string {
	var out []string
	s := string(b)
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// abs is the absolute value of an int.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// handleRevertUndo answers POST /api/sessions/{id}/ws/revert/{rid}/undo: the reverted
// lines come back when the file has not changed since the revert.
func (s *service) handleRevertUndo(w http.ResponseWriter, r *http.Request) {
	acc, _, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	rid := r.PathValue("rid")
	if !revIDRE.MatchString(rid) {
		fail(w, werr(http.StatusBadRequest, "bad_request", "that is not a revert id"))
		return
	}
	s.mu.Lock()
	rec := s.reverts[rid]
	s.mu.Unlock()
	if rec == nil {
		fail(w, werr(http.StatusNotFound, "not_found", "that revert cannot be undone any more"))
		return
	}
	err := s.exclusive(r.Context(), acc, func(sess *session.Session) error {
		if sess.Dir != rec.dir {
			return werr(http.StatusNotFound, "not_found", "that revert belongs to an earlier run of this session")
		}
		_, err := sess.Ckpt.RestoreUndo(rec.undo)
		switch {
		case errors.Is(err, checkpoint.ErrUndoConflict):
			return werr(http.StatusConflict, "changed", "the file changed since the revert: the undo would overwrite that, so nothing was written")
		case errors.Is(err, checkpoint.ErrUnknownUndo):
			return werr(http.StatusNotFound, "not_found", "that revert cannot be undone any more")
		case err != nil:
			return err
		}
		s.mu.Lock()
		delete(s.reverts, rid)
		s.mu.Unlock()
		s.logAction(sess, "revert.undo", map[string]any{"path": rec.path, "key": rec.key})
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	acc.Emit(&wire.Say{Who: "sys", Glyph: "↺", Text: "the revert of a hunk of " + rec.path + " was undone"})
	acc.Notify("Notice from the harness: the person undid their revert of part of " + rec.path + ". Read the file again before you edit it.")
	_ = web.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// logAction writes a web.action event to the session's log: what the person did, no file
// content.
func (s *service) logAction(sess *session.Session, action string, detail map[string]any) {
	if sess.Log == nil {
		return
	}
	_, _ = sess.Log.Emit("", "web.action", map[string]any{"action": action, "detail": detail})
}
