package main

// The routes of the session host (CONTRACT.md sections 4, 5, 6, 8, 9, 10 and 11). Every route is behind the envelope of
// internal/web (loopback, token or cookie, Origin, the custom header, JSON bodies within a cap); a route that raises privilege also
// asks for its confirmation here.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// tabRoute registers a route of a tab: the id is checked and looked up (400 bad_request, 404 no_session) before fn runs.
func (h *webHostImpl) tabRoute(srv *web.Server, pattern string, opts web.RouteOpts, fn func(w http.ResponseWriter, r *http.Request, t *webTab)) {
	srv.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !tabIDRE.MatchString(id) {
			web.Error(w, http.StatusBadRequest, "bad_request", "bad session id")
			return
		}
		t := h.tab(id)
		if t == nil {
			web.Error(w, http.StatusNotFound, "no_session", "no such session")
			return
		}
		fn(w, withConfirmGate(srv, w, r), t) // a privilege the action raises is confirmed where it takes effect (web_authorize.go)
	}, opts)
}

// routes registers the host's routes on srv.
func (h *webHostImpl) routes(srv *web.Server) {
	// 4. The stream: one topic for every tab.
	srv.Handle("GET /api/stream", srv.Hub().ServeSSE(func(*http.Request) (string, bool) { return seam.Topic, true }), web.RouteOpts{})

	// 5. Boot.
	srv.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) { ok(w, h.hello(r)) }, web.RouteOpts{})
	srv.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		after := h.hub.LastID(seam.Topic) // read before the snapshots: the stream from here misses nothing
		snaps := []wire.TabSnapshot{}
		for _, t := range h.tabList() {
			if s, err := t.Snapshot(r.Context()); err == nil {
				snaps = append(snaps, s)
			}
		}
		ok(w, map[string]any{"streamAfter": after, "active": h.Active(), "tabs": snaps})
	}, web.RouteOpts{WriteTimeout: snapshotWrite})

	// 6. Tabs.
	srv.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) { ok(w, map[string]any{"tabs": h.Tabs()}) }, web.RouteOpts{})
	srv.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"projects": h.Projects(r.Context())})
	}, web.RouteOpts{})
	srv.HandleFunc("POST /api/sessions", h.newSession, web.RouteOpts{})
	srv.HandleFunc("POST /api/sessions/resume", h.resume, web.RouteOpts{})
	h.tabRoute(srv, "GET /api/sessions/{id}/snapshot", web.RouteOpts{WriteTimeout: snapshotWrite}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		snap, err := t.Snapshot(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, snap)
	})
	h.tabRoute(srv, "PATCH /api/sessions/{id}", web.RouteOpts{MaxBody: 4 << 10}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body struct {
			Name string `json:"name"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Rename(r.Context(), body.Name); err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]any{"tab": t.Summary()})
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/stop", web.RouteOpts{NoBody: true}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		if err := t.Interrupt(r.Context(), "turn"); err != nil {
			if we := (*wire.Error)(nil); errors.As(err, &we) && we.Code == "idle" {
				writeErr(w, werr(http.StatusConflict, "idle", "nothing was running"))
				return
			}
			writeErr(w, err)
			return
		}
		ok(w, okBody)
	})
	h.tabRoute(srv, "DELETE /api/sessions/{id}", web.RouteOpts{NoBody: true, WriteTimeout: closeLimit + time.Minute}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		h.mu.Lock()
		last := len(h.tabs) <= 1
		h.mu.Unlock()
		if last {
			writeErr(w, werr(http.StatusConflict, "last", "the last session cannot be closed: start another first"))
			return
		}
		rep := h.removeTab(t, "closed")
		body := map[string]any{"ok": true}
		if rep != nil {
			body["integration"] = session.Integration{Applied: rep.Applied, Committed: rep.Committed, Branch: rep.Branch, Message: rep.Message, Hint: rep.Hint}
		}
		ok(w, body)
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/restart", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.RestartRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Restart(r.Context(), body); err != nil {
			writeErr(w, err)
			return
		}
		_ = web.WriteJSON(w, http.StatusAccepted, map[string]any{"gen": t.Summary().Gen})
	})

	// 8. Chat.
	h.tabRoute(srv, "POST /api/sessions/{id}/messages", web.RouteOpts{MaxBody: maxMessage}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.MessageRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		key := idemKey("messages/"+t.id, body.ClientID)
		if e, seen := h.idemGet(key); seen {
			_ = web.WriteJSON(w, e.status, e.body)
			return
		}
		res, err := t.Send(r.Context(), body)
		if err != nil {
			writeErr(w, err)
			return
		}
		h.idemPut(key, http.StatusOK, res)
		ok(w, res)
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/command", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.CommandRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.Command(r.Context(), body)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, res)
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/steer", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body struct {
			Text string `json:"text"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Steer(r.Context(), body.Text); err != nil {
			writeErr(w, err)
			return
		}
		ok(w, okBody)
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/interrupt", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body struct {
			Target string `json:"target"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Interrupt(r.Context(), body.Target); err != nil {
			writeErr(w, err)
			return
		}
		ok(w, okBody)
	})
	h.tabRoute(srv, "GET /api/sessions/{id}/slash", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		s, err := t.Slash(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]any{"slash": s})
	})

	// 9. Approvals.
	srv.HandleFunc("GET /api/questions", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"questions": h.Questions()})
	}, web.RouteOpts{})
	srv.HandleFunc("POST /api/questions/{qid}/answer", func(w http.ResponseWriter, r *http.Request) {
		qid := r.PathValue("qid")
		if !qidRE.MatchString(qid) {
			web.Error(w, http.StatusBadRequest, "bad_request", "bad question id")
			return
		}
		var body wire.AnswerRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		tab := h.bridge.TabOf(qid)
		if body.Choice == 4 && tab != "" {
			if t := h.tab(tab); t != nil {
				t.noteTestsPreset()
			}
		}
		res, err := h.bridge.Answer(r.Context(), qid, body)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, res)
	}, web.RouteOpts{MaxBody: 16 << 10})

	// 10. Goal.
	h.tabRoute(srv, "POST /api/sessions/{id}/goal", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.GoalRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Goal(r.Context(), body); err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]any{"ok": true, "state": t.goalState()})
	})

	// 11. Session settings.
	h.tabRoute(srv, "POST /api/sessions/{id}/mode", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.ModeRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.SetMode(r.Context(), body); err != nil {
			writeErr(w, err)
			return
		}
		ok(w, okBody)
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/model", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.ModelRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		restarted, err := t.setModel(r.Context(), body)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]any{"ok": true, "restarted": restarted})
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/effort", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.EffortRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.SetEffort(r.Context(), body)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, res)
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/budget", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.BudgetRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		paused, err := t.budgetRoute(r.Context(), body)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]any{"ok": true, "paused": paused})
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/compact", web.RouteOpts{WriteTimeout: 10 * time.Minute}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.CompactRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		from, to, err := t.CompactNow(r.Context(), body.Focus)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]int{"from": from, "to": to})
	})
	h.tabRoute(srv, "PATCH /api/sessions/{id}/launch", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.LaunchPatch
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.StageLaunch(r.Context(), body); err != nil {
			writeErr(w, err)
			return
		}
		ok(w, okBody)
	})
	h.tabRoute(srv, "GET /api/sessions/{id}/rules", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		rules, err := t.Rules(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]any{"rules": rules})
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/rules", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.RuleRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.AddRule(r.Context(), body)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, res)
	})
	h.tabRoute(srv, "POST /api/sessions/{id}/rules/remove", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body wire.RuleRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.RemoveRule(r.Context(), body)
		if err != nil {
			writeErr(w, err)
			return
		}
		ok(w, res)
	})
}

// newSession handles POST /api/sessions: the New session dialog.
func (h *webHostImpl) newSession(w http.ResponseWriter, r *http.Request) {
	var body newSessionRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	key := idemKey("sessions", body.ClientID)
	if e, seen := h.idemGet(key); seen {
		_ = web.WriteJSON(w, e.status, e.body)
		return
	}
	h.mu.Lock()
	full := len(h.tabs) >= maxTabs
	h.mu.Unlock()
	if full {
		writeErr(w, werr(http.StatusConflict, "limit", "16 sessions are open: close one first"))
		return
	}
	if body.Cwd == "" || !filepath.IsAbs(body.Cwd) {
		writeErr(w, werr(http.StatusForbidden, "not_a_project", "start a session in one of the listed projects"))
		return
	}
	dir := filepath.Clean(body.Cwd)
	if _, ok := h.isProject(r.Context(), dir); !ok {
		writeErr(w, werr(http.StatusForbidden, "not_a_project", "start a session in one of the listed projects"))
		return
	}
	body.Cwd = dir
	if body.Name != "" {
		if err := validName(strings.TrimSpace(body.Name)); err != nil {
			writeErr(w, err)
			return
		}
	}
	if e := strings.TrimSpace(body.Effort); e != "" {
		found := false
		for _, l := range effortLevels {
			found = found || l == e
		}
		if !found {
			writeErr(w, werr(http.StatusBadRequest, "bad_flags", "the effort is default, none, minimal, low, medium, high, xhigh or max"))
			return
		}
	}
	if len(body.GoalText) > 4*maxGoalLen {
		writeErr(w, werr(http.StatusBadRequest, "bad_flags", "the first goal is longer than 4,000 characters"))
		return
	}
	args := argsFor(body.NewSessionRequest)
	if body.Swarm == nil {
		args = append(args, "--swarm", strconv.Itoa(h.d.Workers))
	}
	if !body.Mailman && h.defaults().Mailman {
		args = append(args, "--mailman=false") // unchecked against a default of on (PARITY A4)
	}
	if body.Resume != "" {
		if !session.ValidID(body.Resume) {
			writeErr(w, werr(http.StatusNotFound, "no_session", "there is no such recorded session"))
			return
		}
		home, _ := os.UserHomeDir()
		if _, err := session.ResolveResume(home, rootOf(dir), body.Resume); err != nil {
			writeErr(w, werr(http.StatusConflict, "not_resumable", clip(err.Error(), 400)))
			return
		}
		if t := h.hosting(body.Resume); t != nil {
			web.ErrorDetail(w, http.StatusConflict, "hosted", "that session is open in a tab: switch to it", map[string]any{"tab": t.Summary()})
			return
		}
		args = append(args, "--resume", body.Resume)
	}
	f, err := parseChatFlags(args)
	if err != nil {
		writeErr(w, werr(http.StatusBadRequest, "bad_flags", clip(err.Error(), 400)))
		return
	}
	if body.Model != "" {
		if err := h.checkModel(body.Model); err != nil {
			writeErr(w, err)
			return
		}
	}
	// What the session raises above the server's own command line needs the person's confirmation (web_authorize.go); trusting the
	// project's files shows them first (409 trust_required with the challenge).
	base := baselineOfFlags(chatFlags{mode: h.d.Mode, verify: h.d.Verify, allow: h.d.Allow}, cleanDir(h.d.Cwd))
	base.trusted = h.d.TrustProject && dir == cleanDir(h.d.Cwd)
	note, okay := h.gateNewSession(w, withConfirmGate(h.srv, w, r), raised(f, base, dir))
	if !okay {
		return
	}
	t, err := h.addTab(strings.TrimSpace(body.Name), dir, h.base)
	if err != nil {
		writeErr(w, err)
		return
	}
	spec := startSpec{args: args, name: strings.TrimSpace(body.Name), goalText: body.GoalText, effort: strings.TrimSpace(body.Effort), note: note}
	h.track(func() { _ = t.startGen(spec) })
	res := map[string]any{"tab": t.Summary()}
	h.idemPut(key, http.StatusCreated, res)
	_ = web.WriteJSON(w, http.StatusCreated, res)
}

// resumeRequest is the body of POST /api/sessions/resume: wire.ResumeRequest and the page's idempotency key.
type resumeRequest struct {
	wire.ResumeRequest
	ClientID string `json:"clientId,omitempty"`
}

// hosting is the tab whose session is the one named, or nil.
func (h *webHostImpl) hosting(sid string) *webTab {
	for _, t := range h.tabList() {
		if t.Summary().SID == sid {
			return t
		}
	}
	return nil
}

// resume handles POST /api/sessions/resume: a recorded session ("latest" of a project, or an id) continues in a new tab.
func (h *webHostImpl) resume(w http.ResponseWriter, r *http.Request) {
	var body resumeRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	key := idemKey("resume", body.ClientID)
	if e, seen := h.idemGet(key); seen {
		_ = web.WriteJSON(w, e.status, e.body)
		return
	}
	from := strings.TrimSpace(body.From)
	if from == "" {
		from = "latest"
	}
	if body.Name != "" {
		if err := validName(strings.TrimSpace(body.Name)); err != nil {
			writeErr(w, err)
			return
		}
	}
	cwd := h.d.Cwd
	if body.Cwd != "" {
		if !filepath.IsAbs(body.Cwd) {
			writeErr(w, werr(http.StatusForbidden, "not_a_project", "resume in one of the listed projects"))
			return
		}
		if _, ok := h.isProject(r.Context(), filepath.Clean(body.Cwd)); !ok {
			writeErr(w, werr(http.StatusForbidden, "not_a_project", "resume in one of the listed projects"))
			return
		}
		cwd = filepath.Clean(body.Cwd)
	}
	home, _ := os.UserHomeDir()
	if from != "latest" {
		if !session.ValidID(from) {
			writeErr(w, werr(http.StatusNotFound, "no_session", "there is no such recorded session"))
			return
		}
		if !fileExists(eventsPath(filepath.Join(session.SessionsDir(home), from))) {
			writeErr(w, werr(http.StatusNotFound, "no_session", "there is no such recorded session"))
			return
		}
	}
	dir, err := session.ResolveResume(home, rootOf(cwd), from)
	if err != nil {
		code, status := "not_resumable", http.StatusConflict
		if from == "latest" {
			code, status = "no_session", http.StatusNotFound
		}
		writeErr(w, werr(status, code, clip(err.Error(), 400)))
		return
	}
	sid := filepath.Base(dir)
	if t := h.hosting(sid); t != nil {
		web.ErrorDetail(w, http.StatusConflict, "hosted", "that session is open in a tab: switch to it", map[string]any{"tab": t.Summary()})
		return
	}
	if session.Locked(dir) {
		writeErr(w, werr(http.StatusConflict, "locked", "another process is using that session"))
		return
	}
	if body.Cwd == "" {
		if sum := session.Summarize(eventsPath(dir)); sum.Cwd != "" {
			if fi, err := os.Stat(sum.Cwd); err == nil && fi.IsDir() {
				cwd = sum.Cwd
			}
		}
	}
	d := h.d
	d.Cwd = cwd
	args := chatArgsOf(d, sid)
	if _, err := parseChatFlags(args); err != nil {
		writeErr(w, werr(http.StatusBadRequest, "bad_flags", clip(err.Error(), 400)))
		return
	}
	t, err := h.addTab(strings.TrimSpace(body.Name), cwd, h.base)
	if err != nil {
		writeErr(w, err)
		return
	}
	spec := startSpec{args: args, name: strings.TrimSpace(body.Name)}
	h.track(func() { _ = t.startGen(spec) })
	res := map[string]any{"tab": t.Summary()}
	h.idemPut(key, http.StatusCreated, res)
	_ = web.WriteJSON(w, http.StatusCreated, res)
}

// checkModel says whether a model can be used, as CheckModel of a live session says it (422 model); with no live session it is
// left to the start, which says why in the tab.
func (h *webHostImpl) checkModel(ref string) error {
	if len(ref) > 300 {
		return werr(http.StatusUnprocessableEntity, "model", "the model name is too long")
	}
	for _, t := range h.tabList() {
		if s := t.session(); s != nil {
			if _, err := s.CheckModel(ref); err != nil {
				return werr(http.StatusUnprocessableEntity, "model", clip(err.Error(), 400))
			}
			return nil
		}
	}
	return nil
}

// noteTestsPreset records that the rules the tests preset adds come from it (the fourth answer of a question).
func (t *webTab) noteTestsPreset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range config.ExpandAllow([]string{config.TestsPreset}) {
		if pr, err := perm.ParseRule(perm.Allow, r); err == nil {
			if _, seen := t.origins["allow "+pr.String()]; !seen {
				t.origins["allow "+pr.String()] = "the tests preset"
			}
		}
	}
}
