package webtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// RouteOptions configure RegisterRoutes.
type RouteOptions struct {
	// Version is the program version the hello reports (default v0.9.0).
	Version string
	// Addr gives the address pages should show as the server's (default 127.0.0.1:6969).
	Addr func() string
}

// D16 is the first 16 hex characters of the SHA-256 of the JSON encoding of v (maps with sorted keys, no spaces): the digest of
// the confirmation scopes of CONTRACT.md section 20.
func D16(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// PublishFrame hands a frame to the hub of the page's stream as the real host does (OWNERSHIP.md 5.3): the frame's type is the SSE
// event name, its data the body, its class the hub's.
func PublishFrame(hub *web.Hub, f wire.Frame) (uint64, error) {
	b, err := json.Marshal(f.Data)
	if err != nil {
		return 0, err
	}
	return hub.Publish(seam.Topic, web.Event{Type: f.Type, Data: b, Critical: f.Critical, Coalescable: f.Coalescable, Key: f.Key})
}

// router registers the fake routes of one server.
type router struct {
	srv  *web.Server
	host *Host
	opts RouteOptions
}

// RegisterRoutes adds the fake routes to srv: the routes of CONTRACT.md that the live session needs, answered by the fake host (hello,
// the stream, tabs, snapshots, messages, approvals, session settings), fixed contract-shaped answers for the pages (models,
// providers, permissions, trust, MCP, skills, configuration, schedule, the Workspace, recorded sessions, the CLI spec, runs), the
// control routes /api/_fake/*, and a 501 not_implemented for any other mutating route under /api/. Frames the host publishes reach
// the hub. Call it from web.Config.Routes.
func RegisterRoutes(srv *web.Server, h *Host, o RouteOptions) {
	if o.Version == "" {
		o.Version = "v0.9.0"
	}
	if o.Addr == nil {
		o.Addr = func() string { return web.DefaultAddr }
	}
	h.SetPublisher(func(f wire.Frame) { _, _ = PublishFrame(srv.Hub(), f) })
	r := &router{srv: srv, host: h, opts: o}
	r.live()
	r.pages()
	r.workspace()
	r.control()
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		srv.HandleFunc(m+" /api/", func(w http.ResponseWriter, req *http.Request) {
			web.Error(w, http.StatusNotImplemented, "not_implemented", "the fake server does not implement "+req.Method+" "+req.URL.Path)
		}, web.RouteOpts{})
	}
}

// fail writes an error from a fake: a *wire.Error as its status, code and detail, anything else as an internal error.
func fail(w http.ResponseWriter, err error) {
	var we *wire.Error
	if errors.As(err, &we) {
		web.ErrorDetail(w, we.Status, we.Code, we.Msg, we.Detail)
		return
	}
	web.Error(w, http.StatusInternalServerError, "internal", "internal error")
}

// ok writes a 200 JSON body.
func ok(w http.ResponseWriter, v any) { _ = web.WriteJSON(w, http.StatusOK, v) }

// okBody is the answer of an action that returns nothing.
var okBody = map[string]bool{"ok": true}

// get registers an authenticated GET route whose answer is computed from the request.
func (rt *router) get(pattern string, fn func(r *http.Request) (any, error)) {
	rt.srv.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r)
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, v)
	}, web.RouteOpts{})
}

// tabRoute registers a route on a tab: the id is checked and looked up (400 bad_request, 404 no_session) before fn runs.
func (rt *router) tabRoute(pattern string, opts web.RouteOpts, fn func(w http.ResponseWriter, r *http.Request, t *Tab)) {
	rt.srv.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validTabID(id) {
			web.Error(w, http.StatusBadRequest, "bad_request", "bad session id")
			return
		}
		t := rt.host.FakeTab(id)
		if t == nil {
			web.Error(w, http.StatusNotFound, "no_session", "no such session")
			return
		}
		fn(w, r, t)
	}, opts)
}

// validTabID reports whether s is a tab id ([a-z0-9-]{1,40}).
func validTabID(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// live registers the routes of the live session: hello, the stream, tabs, chat, approvals, goal and session settings.
func (rt *router) live() {
	srv, h := rt.srv, rt.host
	rt.get("/api/hello", func(*http.Request) (any, error) {
		return wire.Hello{
			Boot: Boot, Now: StartedAt + int64(Now*1000), Tabs: h.Tabs(), Active: h.Active(), Version: rt.opts.Version,
			Server:      wire.ServerInfo{Addr: rt.opts.Addr(), Loopback: true, Version: rt.opts.Version},
			Limits:      wire.Limits{MaxBody: int(web.DefaultMaxBody), MaxMessage: 256 << 10, MaxQuestions: 64},
			UI:          wire.UIInfo{Version: "fake"},
			StreamAfter: srv.Hub().LastID(seam.Topic),
		}, nil
	})
	srv.Handle("GET /api/stream", srv.Hub().ServeSSE(func(*http.Request) (string, bool) { return seam.Topic, true }), web.RouteOpts{})
	rt.get("/api/sessions", func(*http.Request) (any, error) { return map[string]any{"tabs": h.Tabs()}, nil })
	rt.get("/api/projects", func(r *http.Request) (any, error) { return map[string]any{"projects": h.Projects(r.Context())}, nil })

	srv.HandleFunc("POST /api/sessions", rt.newSession, web.RouteOpts{})
	srv.HandleFunc("POST /api/sessions/resume", rt.resume, web.RouteOpts{})
	rt.tabRoute("GET /api/sessions/{id}/snapshot", web.RouteOpts{WriteTimeout: 60 * time.Second}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		snap, err := t.Snapshot(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, snap)
	})
	rt.tabRoute("PATCH /api/sessions/{id}", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body struct {
			Name string `json:"name"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Rename(r.Context(), body.Name); err != nil {
			fail(w, err)
			return
		}
		ok(w, map[string]any{"tab": t.Summary()})
	})
	rt.tabRoute("POST /api/sessions/{id}/stop", web.RouteOpts{NoBody: true}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		if err := t.Interrupt(r.Context(), "turn"); err != nil {
			fail(w, err)
			return
		}
		ok(w, okBody)
	})
	rt.tabRoute("DELETE /api/sessions/{id}", web.RouteOpts{NoBody: true}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		if err := h.CloseTab(t.Summary().ID); err != nil {
			fail(w, err)
			return
		}
		ok(w, okBody)
	})
	rt.tabRoute("POST /api/sessions/{id}/restart", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.RestartRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		id := t.Summary().ID
		if raises(body.Flags) && !rt.srv.RequireConfirm(w, r, "restart:"+id+":"+D16(body.Flags)) {
			return
		}
		if err := t.Restart(r.Context(), body); err != nil {
			fail(w, err)
			return
		}
		_ = web.WriteJSON(w, http.StatusAccepted, map[string]any{"gen": t.Summary().Gen})
	})

	rt.tabRoute("POST /api/sessions/{id}/messages", web.RouteOpts{MaxBody: 256 << 10}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.MessageRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.Send(r.Context(), body)
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, res)
	})
	rt.tabRoute("POST /api/sessions/{id}/command", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.CommandRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.Command(r.Context(), body)
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, res)
	})
	rt.tabRoute("POST /api/sessions/{id}/steer", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body struct {
			Text string `json:"text"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Steer(r.Context(), body.Text); err != nil {
			fail(w, err)
			return
		}
		ok(w, okBody)
	})
	rt.tabRoute("POST /api/sessions/{id}/interrupt", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body struct {
			Target string `json:"target"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Interrupt(r.Context(), firstNonEmpty(body.Target, "turn")); err != nil {
			fail(w, err)
			return
		}
		ok(w, okBody)
	})
	rt.tabRoute("GET /api/sessions/{id}/slash", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		s, err := t.Slash(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, map[string]any{"slash": s})
	})
	rt.tabRoute("POST /api/sessions/{id}/goal", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.GoalRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Goal(r.Context(), body); err != nil {
			fail(w, err)
			return
		}
		ok(w, map[string]any{"ok": true, "state": t.goalState()})
	})
	rt.tabRoute("POST /api/sessions/{id}/mode", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.ModeRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if (body.Mode == "bypass" || body.Mode == "yolo") && !rt.srv.RequireConfirm(w, r, "mode:"+body.Mode+":"+t.Summary().ID) {
			return
		}
		if err := t.SetMode(r.Context(), body); err != nil {
			fail(w, err)
			return
		}
		ok(w, okBody)
	})
	rt.tabRoute("POST /api/sessions/{id}/model", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.ModelRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.SetModel(r.Context(), body); err != nil {
			fail(w, err)
			return
		}
		ok(w, map[string]any{"ok": true, "restarted": false})
	})
	rt.tabRoute("POST /api/sessions/{id}/effort", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.EffortRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.SetEffort(r.Context(), body)
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, res)
	})
	rt.tabRoute("POST /api/sessions/{id}/budget", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.BudgetRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.SetBudget(r.Context(), body); err != nil {
			fail(w, err)
			return
		}
		ok(w, map[string]any{"ok": true, "paused": false})
	})
	rt.tabRoute("POST /api/sessions/{id}/compact", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.CompactRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.Compact(r.Context(), body); err != nil {
			fail(w, err)
			return
		}
		ok(w, map[string]int{"from": 41000, "to": 12000})
	})
	rt.tabRoute("PATCH /api/sessions/{id}/launch", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.LaunchPatch
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.StageLaunch(r.Context(), body); err != nil {
			fail(w, err)
			return
		}
		ok(w, okBody)
	})
	rt.tabRoute("GET /api/sessions/{id}/rules", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		rules, err := t.Rules(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, map[string]any{"rules": rules})
	})
	rt.tabRoute("POST /api/sessions/{id}/rules", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.RuleRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.AddRule(r.Context(), body)
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, res)
	})
	rt.tabRoute("POST /api/sessions/{id}/rules/remove", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.RuleRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		res, err := t.RemoveRule(r.Context(), body)
		if err != nil {
			fail(w, err)
			return
		}
		ok(w, res)
	})
	rt.tabRoute("POST /api/sessions/{id}/permissions/check", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.PermCheckRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		switch body.Tool {
		case "Bash", "Edit", "Read":
		default:
			web.Error(w, http.StatusBadRequest, "bad_tool", "tool must be Bash, Edit or Read")
			return
		}
		v := wire.PermVerdict{D: "ask", Why: "no rule matches: the default mode asks", Cls: "warm"}
		if strings.HasPrefix(body.Arg, "go test") {
			v = wire.PermVerdict{D: "allowed", Why: "Bash(go test:*) from --allow flag", Cls: "ok"}
		}
		if strings.Contains(body.Arg, ".env") {
			v = wire.PermVerdict{D: "refused", Why: "Read(./.env) from project config", Cls: "err"}
		}
		ok(w, v)
	})

	rt.get("/api/questions", func(*http.Request) (any, error) { return map[string]any{"questions": h.Questions()}, nil })
	srv.HandleFunc("POST /api/questions/{qid}/answer", rt.answer, web.RouteOpts{})
}

// goalState is the standing goal's state for the goal route's answer.
func (t *Tab) goalState() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return firstNonEmpty(t.goal, "cleared")
}

// raises reports whether restart flags raise privileges (a permissive mode, trusting the project).
func raises(flags []string) bool {
	for i, f := range flags {
		if f == "--trust-project" || ((f == "--mode" || f == "-mode") && i+1 < len(flags) && (flags[i+1] == "bypass" || flags[i+1] == "yolo")) ||
			f == "--mode=bypass" || f == "--mode=yolo" {
			return true
		}
	}
	return false
}

// newSession handles POST /api/sessions: the directory must be one of the projects, an untrusted project asks for the trust step.
func (rt *router) newSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		wire.NewSessionRequest
		Resume   string `json:"resume,omitempty"`
		ClientID string `json:"clientId,omitempty"`
	}
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	var proj *wire.Project
	for _, p := range rt.host.Projects(r.Context()) {
		if p.Dir == body.Cwd {
			p := p
			proj = &p
		}
	}
	if proj == nil {
		web.Error(w, http.StatusForbidden, "not_a_project", "start a session in one of the listed projects")
		return
	}
	if body.TrustProject && proj.Trust != "trusted" {
		digest := D16(map[string]string{"dir": proj.Dir, "files": fmt.Sprint(proj.Files)})
		scope := "trust:" + D16(map[string]string{"dir": proj.Dir, "digest": digest})
		if r.Header.Get(web.ConfirmHeader) == "" {
			web.ErrorDetail(w, http.StatusConflict, "trust_required", "trust the files of this project first", wire.TrustChallenge{
				Dir: proj.Dir, Digest: digest, Confirm: rt.srv.IssueConfirm(r, scope), Scope: scope,
				Files: []wire.TrustFile{{Path: "AGENTS.md", Kind: "instructions", Bytes: 1840, Hash: "a1b2c3d4"}},
			})
			return
		}
		if !rt.srv.RequireConfirm(w, r, scope) {
			return
		}
	}
	t, err := rt.host.AddTab(body.Name, body.Cwd)
	if err != nil {
		fail(w, err)
		return
	}
	if body.GoalText != "" {
		_ = t.Goal(r.Context(), wire.GoalRequest{Action: "set", Text: body.GoalText})
	}
	_ = web.WriteJSON(w, http.StatusCreated, map[string]any{"tab": t.Summary()})
}

// resume handles POST /api/sessions/resume: a recorded session, or "latest", in a new tab.
func (rt *router) resume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		wire.ResumeRequest
		ClientID string `json:"clientId,omitempty"`
	}
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	rec := Recorded()
	var found *wire.RecordedSession
	for i := range rec {
		if body.From == "latest" && i == 0 || rec[i].ID == body.From {
			found = &rec[i]
			break
		}
	}
	switch {
	case found == nil:
		web.Error(w, http.StatusNotFound, "no_session", "no such recorded session")
		return
	case !found.Resumable:
		web.Error(w, http.StatusConflict, "not_resumable", "that session cannot be resumed: its log has no conversation")
		return
	}
	t, err := rt.host.AddTab(firstNonEmpty(body.Name, found.First), firstNonEmpty(body.Cwd, found.Cwd, Cwd))
	if err != nil {
		fail(w, err)
		return
	}
	t.Emit(&wire.Say{Who: "sys", Glyph: "↺", Text: "resumed " + found.ID})
	_ = web.WriteJSON(w, http.StatusCreated, map[string]any{"tab": t.Summary()})
}

// answer handles POST /api/questions/{qid}/answer.
func (rt *router) answer(w http.ResponseWriter, r *http.Request) {
	qid := r.PathValue("qid")
	var body wire.AnswerRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	if !validQID(qid) {
		web.Error(w, http.StatusBadRequest, "bad_request", "bad question id")
		return
	}
	for _, t := range rt.host.tabList() {
		if t.hasQuestion(qid) {
			res, err := t.Answer(qid, body)
			if err != nil {
				fail(w, err)
				return
			}
			ok(w, res)
			return
		}
		if t.wasAnswered(qid) {
			web.Error(w, http.StatusConflict, "answered", "that question was answered")
			return
		}
	}
	web.Error(w, http.StatusNotFound, "no_question", "no such question")
}

// validQID reports whether s has the form of a question id: q_ and 26 lower-case base32 characters.
func validQID(s string) bool {
	if len(s) != 28 || !strings.HasPrefix(s, "q_") {
		return false
	}
	for _, c := range s[2:] {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// hasQuestion reports whether the tab has the question open.
func (t *Tab) hasQuestion(qid string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, o := range t.open {
		if o.q.ID == qid {
			return true
		}
	}
	return false
}

// wasAnswered reports whether the tab's journal holds an answer to the question.
func (t *Tab) wasAnswered(qid string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	needle := `"qid":"` + qid + `"`
	for _, e := range t.journal {
		if strings.Contains(string(e), needle) {
			return true
		}
	}
	return false
}

// pages registers the fixed answers of the pages that are not about the live session.
func (rt *router) pages() {
	srv := rt.srv
	rt.get("/api/cli", func(*http.Request) (any, error) { return CLISpec(), nil })
	rt.get("/api/recorded", func(*http.Request) (any, error) {
		return map[string]any{"recorded": Recorded(), "mb": 1.7}, nil
	})
	rt.get("/api/recorded/{sid}/events", func(r *http.Request) (any, error) {
		for _, s := range Recorded() {
			if s.ID == r.PathValue("sid") {
				evs := []json.RawMessage{}
				for _, e := range Shop().History {
					evs = append(evs, raw(e))
				}
				return map[string]any{"events": evs, "next": ""}, nil
			}
		}
		return nil, werr(404, "not_found", "no such recorded session")
	})
	srv.HandleFunc("POST /api/recorded/prune", func(w http.ResponseWriter, r *http.Request) {
		var body wire.PruneRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		list := Recorded()[1:]
		if body.Apply {
			ids := []string{}
			for _, s := range list {
				ids = append(ids, s.ID)
			}
			sort.Strings(ids)
			if !srv.RequireConfirm(w, r, "prune:"+D16(ids)) {
				return
			}
		}
		ok(w, wire.PrunePlan{List: list, MB: 0.5, Applied: body.Apply})
	}, web.RouteOpts{})
	rt.get("/api/models", func(*http.Request) (any, error) { return Models(), nil })
	srv.HandleFunc("POST /api/models/fav", func(w http.ResponseWriter, r *http.Request) {
		var body wire.FavRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		ok(w, okBody)
	}, web.RouteOpts{})
	rt.get("/api/providers", func(*http.Request) (any, error) { return Providers(), nil })
	srv.HandleFunc("POST /api/providers/recheck", func(w http.ResponseWriter, r *http.Request) { ok(w, Providers()) }, web.RouteOpts{NoBody: true})
	srv.HandleFunc("POST /api/providers/{name}/signout", func(w http.ResponseWriter, r *http.Request) {
		for _, p := range Providers().Providers {
			if p.ID == r.PathValue("name") {
				p.Key, p.State, p.SignedIn, p.Who = "none", "no key", false, ""
				ok(w, p)
				return
			}
		}
		web.Error(w, http.StatusNotFound, "not_found", "no such provider")
	}, web.RouteOpts{NoBody: true})

	tabView := func(pattern string, view func(t *Tab) any) {
		rt.tabRoute("GET /api/sessions/{id}/"+pattern, web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) { ok(w, view(t)) })
	}
	tabView("permissions", func(t *Tab) any {
		v := Permissions(firstNonEmpty(deref(t.metaFull().Mode), "default"))
		t.mu.Lock()
		v.Session = append([]wire.Rule{}, t.rules...)
		t.mu.Unlock()
		return v
	})
	tabView("trust", func(*Tab) any { return Trust() })
	tabView("mcp", func(*Tab) any { return MCP() })
	tabView("skills", func(*Tab) any { return Skills() })
	tabView("config", func(*Tab) any { return Config() })
	rt.get("/api/trust/challenge", func(r *http.Request) (any, error) {
		dir := r.URL.Query().Get("dir")
		for _, p := range Projects() {
			if p.Dir == dir {
				digest := D16(map[string]string{"dir": p.Dir, "files": fmt.Sprint(p.Files)})
				scope := "trust:" + D16(map[string]string{"dir": p.Dir, "digest": digest})
				return wire.TrustChallenge{Dir: p.Dir, Digest: digest, Scope: scope, Confirm: srv.IssueConfirm(r, scope),
					Files: []wire.TrustFile{{Path: "AGENTS.md", Kind: "instructions", Bytes: 1840, Hash: "a1b2c3d4"}}}, nil
			}
		}
		return nil, werr(403, "not_a_project", "start a session in one of the listed projects")
	})
	srv.HandleFunc("POST /api/trust", func(w http.ResponseWriter, r *http.Request) {
		var body wire.TrustRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		ok(w, okBody)
	}, web.RouteOpts{})

	rt.get("/api/schedule", func(*http.Request) (any, error) { return Schedule(), nil })
	rt.get("/api/schedule/next", func(r *http.Request) (any, error) {
		if c := r.URL.Query().Get("cron"); len(strings.Fields(c)) == 5 {
			return wire.CronCheck{OK: true, Next: "2026-10-10 07:00"}, nil
		}
		return wire.CronCheck{OK: false, Err: "the cron expression is not valid"}, nil
	})
	rt.get("/api/schedule/jobs/{job}/log", func(r *http.Request) (any, error) {
		for _, l := range Schedule().Logs {
			if l.Job == r.PathValue("job") {
				return l, nil
			}
		}
		return nil, werr(404, "not_found", "no such job")
	})
	rt.get("/api/doctor/endpoints", func(*http.Request) (any, error) { return map[string]any{"endpoints": DoctorEndpoints()}, nil })
	rt.get("/api/update", func(*http.Request) (any, error) { return Update(), nil })
	rt.get("/api/runs", func(*http.Request) (any, error) { return map[string]any{"runs": []wire.RunInfo{}}, nil })
	srv.HandleFunc("POST /api/runs", rt.startRun, web.RouteOpts{})
}

// deref returns the string a pointer points to, or "".
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// startRun handles POST /api/runs: it answers 202 and, shortly after, publishes the canned output of the run as run frames.
func (rt *router) startRun(w http.ResponseWriter, r *http.Request) {
	var body wire.RunRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	if len(body.Path) == 0 {
		web.Error(w, http.StatusBadRequest, "bad_flags", "a command is required")
		return
	}
	var cmd *wire.CLICommand
	for i, c := range CLISpec().Commands {
		if strings.Join(c.Path, " ") == strings.Join(body.Path, " ") {
			cmd = &CLISpec().Commands[i]
		}
	}
	switch {
	case cmd == nil:
		web.Error(w, http.StatusBadRequest, "bad_flags", "unknown command")
		return
	case cmd.Mode == "tty_only":
		web.Error(w, http.StatusForbidden, "tty_only", cmd.Why)
		return
	case cmd.Mode == "priv" && !rt.srv.RequireConfirm(w, r, "run:"+D16(append(append([]string{}, body.Path...), fmt.Sprint(body.Flags)))):
		return
	}
	id := "r_" + strings.Repeat("a", 15) + string(rune('a'+rt.host.nextRun()%26))
	cmdline := "sleipnir " + strings.Join(body.Path, " ")
	_ = web.WriteJSON(w, http.StatusAccepted, wire.RunStarted{ID: id, Cmdline: cmdline})
	host := rt.host
	time.AfterFunc(60*time.Millisecond, func() {
		host.Publish(wire.Frame{Type: "run", Data: wire.RunFrame{ID: id, Lines: []wire.RunLine{{K: "head", T: "$ " + cmdline}, {K: "out", T: "(fake) " + cmd.Summary}, {K: "ok", T: "done"}}}})
		host.Publish(wire.Frame{Type: "run", Data: wire.RunFrame{ID: id, Result: &wire.RunResult{Exit: 0, Ms: 120, Card: &wire.RunCard{Title: cmdline, Rows: [][2]string{{"exit", "0"}}}}}})
	})
}

// nextRun counts the runs started.
func (h *Host) nextRun() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs++
	return h.runs
}

// workspace registers the Workspace routes with fixed answers.
func (rt *router) workspace() {
	rt.tabRoute("GET /api/sessions/{id}/ws/index", web.RouteOpts{WriteTimeout: 60 * time.Second}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		w.Header().Set("ETag", `"v1"`)
		ok(w, WsIndex())
	})
	rt.tabRoute("GET /api/sessions/{id}/ws/file", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		p := r.URL.Query().Get("path")
		if !cleanRel(p) {
			web.Error(w, http.StatusBadRequest, "bad_path", "a project-relative path is needed")
			return
		}
		if p == ".env" {
			web.Error(w, http.StatusForbidden, "denied", "agents may not read this file, and the page does not show it")
			return
		}
		ok(w, WsContent(p, r.URL.Query().Get("at")))
	})
	rt.tabRoute("GET /api/sessions/{id}/ws/diff", web.RouteOpts{WriteTimeout: 60 * time.Second}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		p := r.URL.Query().Get("path")
		if !cleanRel(p) {
			web.Error(w, http.StatusBadRequest, "bad_path", "a project-relative path is needed")
			return
		}
		ok(w, WsDiff(p, r.URL.Query().Get("from"), r.URL.Query().Get("to")))
	})
	rt.tabRoute("GET /api/sessions/{id}/complete", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		prefix := strings.ToLower(r.URL.Query().Get("prefix"))
		paths := []string{}
		for _, f := range WsIndex().Tree {
			if f.Protected == nil && strings.Contains(strings.ToLower(f.Path), prefix) {
				paths = append(paths, f.Path)
			}
		}
		ok(w, map[string]any{"paths": paths})
	})
	rt.tabRoute("PUT /api/sessions/{id}/ws/reviewed", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
		var body wire.ReviewedRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if !cleanRel(body.Path) {
			web.Error(w, http.StatusBadRequest, "bad_path", "a project-relative path is needed")
			return
		}
		ok(w, okBody)
	})
	for _, p := range []string{"worktrees", "queue"} {
		rt.tabRoute("GET /api/sessions/{id}/ws/"+p, web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *Tab) {
			web.Error(w, http.StatusConflict, "not_isolated", "this team does not use worktrees")
		})
	}
}

// cleanRel reports whether p is a project-relative path with no dot or dot-dot elements, no backslash or NUL and no leading slash.
func cleanRel(p string) bool {
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, e := range strings.Split(p, "/") {
		if e == "" || e == "." || e == ".." {
			return false
		}
	}
	return true
}

// control registers the /api/_fake routes that drive the canned session.
func (rt *router) control() {
	step := func(name string, do func(t *Tab) any) {
		rt.srv.HandleFunc("POST /api/_fake/"+name, func(w http.ResponseWriter, r *http.Request) {
			t := rt.host.FakeTab(TabID)
			if t == nil {
				web.Error(w, http.StatusNotFound, "no_session", "the canned tab is not open")
				return
			}
			ok(w, do(t))
		}, web.RouteOpts{NoBody: true})
	}
	step("step", func(t *Tab) any { return map[string]any{"stepped": t.Step(), "remaining": t.Remaining()} })
	step("play", func(t *Tab) any {
		n := 0
		for t.Step() {
			n++
		}
		return map[string]any{"played": n, "remaining": 0}
	})
	step("reset", func(t *Tab) any { t.Reset(); return map[string]any{"remaining": t.Remaining()} })
	step("ask", func(t *Tab) any { t.Ask(); return okBody })
}
