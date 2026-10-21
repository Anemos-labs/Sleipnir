package main

// The saved teams (loadouts) of `sleipnir web` (docs/WEB-API.md § Saved teams): list them for a folder, save one, remove one,
// check one pasted or picked from a file, and the running team of a tab as it would be saved. The files are those --team reads
// (internal/teamfile); the chat's /team works on the same files.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/teamfile"
	"github.com/anemos-labs/sleipnir/internal/web"
)

// teamsBodyMax bounds a request that carries a team: the file's own limit and room for the envelope.
const teamsBodyMax = teamfile.MaxBytes + 4<<10

// teamsList is GET /api/teams: the saved teams of the folder's project and of the user, and the two directories they are kept in
// ("" for a folder outside a project).
type teamsList struct {
	Teams   []teamfile.Entry `json:"teams"`
	Project string           `json:"project"`
	User    string           `json:"user"`
}

// teamsDir is the folder a teams request is about: ?dir= (or the body's dir), else the folder the server was started in. A folder
// the server does not know as a project (Projects: its own, the configured ones, those of its sessions and recorded sessions) is
// never looked at: its request sees only the user's teams (teamfile.NoProject), and the page reads or writes no team file in a
// folder it merely names.
func (h *webHostImpl) teamsDir(ctx context.Context, dir string) string {
	if strings.TrimSpace(dir) == "" {
		return h.d.Cwd
	}
	want := cleanDir(dir)
	for _, d := range h.projectDirs(ctx) {
		if d == want {
			return d
		}
	}
	return teamfile.NoProject
}

// teamsRoutes registers the saved-team routes on srv.
func (h *webHostImpl) teamsRoutes(srv *web.Server) {
	srv.HandleFunc("GET /api/teams", func(w http.ResponseWriter, r *http.Request) {
		dir := h.teamsDir(r.Context(), r.URL.Query().Get("dir"))
		project, user := teamfile.Dirs(dir, h.base.Home)
		list := teamfile.List(dir, h.base.Home)
		if list == nil {
			list = []teamfile.Entry{}
		}
		ok(w, teamsList{Teams: list, Project: project, User: user})
	}, web.RouteOpts{})
	srv.HandleFunc("POST /api/teams/check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		tf, err := teamfile.Parse([]byte(strings.TrimSpace(body.Text)))
		if err != nil {
			writeErr(w, werr(http.StatusUnprocessableEntity, "team", "not a team: "+clip(err.Error(), 400)))
			return
		}
		ok(w, map[string]any{"team": tf})
	}, web.RouteOpts{MaxBody: teamsBodyMax})
	srv.HandleFunc("PUT /api/teams/{name}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Dir       string         `json:"dir"`
			Scope     string         `json:"scope"`
			Overwrite bool           `json:"overwrite"`
			Team      *teamfile.File `json:"team"`
		}
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if body.Team == nil {
			writeErr(w, werr(http.StatusBadRequest, "bad_request", "the body names the team: {\"team\": {\"slots\": [...]}}"))
			return
		}
		name := r.PathValue("name")
		dir := h.teamsDir(r.Context(), body.Dir)
		p, err := teamfile.Save(dir, h.base.Home, body.Scope, name, body.Team, body.Overwrite)
		switch {
		case errors.Is(err, teamfile.ErrExists):
			writeErr(w, werr(http.StatusConflict, "exists", "a team named "+name+" is saved there already: replace it, or choose another name"))
			return
		case err != nil:
			writeErr(w, werr(http.StatusUnprocessableEntity, "team", clip(err.Error(), 400)))
			return
		}
		scope := body.Scope
		if scope == "" {
			scope = teamfile.ScopeUser
		}
		ok(w, map[string]any{"entry": teamfile.Entry{Name: name, Scope: scope, Path: p, File: body.Team}})
	}, web.RouteOpts{MaxBody: teamsBodyMax})
	srv.HandleFunc("DELETE /api/teams/{name}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if err := teamfile.Remove(h.teamsDir(r.Context(), q.Get("dir")), h.base.Home, q.Get("scope"), r.PathValue("name")); err != nil {
			writeErr(w, werr(http.StatusNotFound, "no_team", clip(err.Error(), 400)))
			return
		}
		ok(w, okBody)
	}, web.RouteOpts{NoBody: true})
	h.tabRoute(srv, "GET /api/sessions/{id}/team", web.RouteOpts{}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		s := t.session()
		if s == nil {
			writeErr(w, werr(http.StatusConflict, "starting", "the session has not started: try again when it has"))
			return
		}
		t.sessMu.Lock()
		slots, models := s.Loadout()
		t.sessMu.Unlock()
		if len(slots) == 0 {
			writeErr(w, werr(http.StatusConflict, "single", "a single agent has no team to save: start a team first"))
			return
		}
		ok(w, map[string]any{"team": teamfile.FromTeam("", slots, models), "dir": s.Cwd()})
	})
}
