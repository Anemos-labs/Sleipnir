package main

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/research"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// researchAction is POST /api/sessions/{id}/research: start a run (the goal /research sets, queued like any goal) or export a
// run's wiki into a directory of the project.
func (t *webTab) researchAction(ctx context.Context, req wire.ResearchRequest) (map[string]any, error) {
	s := t.readySession(ctx)
	if s == nil {
		return nil, errNoSession
	}
	if msg, off := s.SlashOff("/research"); off {
		return nil, werr(http.StatusConflict, "plugin_off", msg)
	}
	switch req.Action {
	case "start":
		text, err := s.ResearchStart(req.Question, req.Depth)
		if err != nil {
			return nil, werr(http.StatusBadRequest, "bad_research", err.Error())
		}
		queued, err := t.goalAction(ctx, s, wire.GoalRequest{Action: "set", Text: text}, true)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "queued": queued}, nil
	case "export":
		dst, err := s.ResearchExport(req.Slug, req.Dir)
		if err != nil {
			return nil, werr(http.StatusBadRequest, "bad_export", "export: "+err.Error())
		}
		return map[string]any{"ok": true, "dir": dst}, nil
	}
	return nil, werr(http.StatusBadRequest, "bad_request", "the action is start or export")
}

// researchTypes are the content types of the files a wiki has.
var researchTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
}

// serveResearch is GET /research/{id}/{slug}/{file...}: a file of a run's wiki, rendered from the run's ledger as it is now. The
// files are the renderer's own (research.Render), never a file read from the project as it is, so what the page serves under its
// origin is what the renderer escaped.
func (t *webTab) serveResearch(w http.ResponseWriter, r *http.Request) {
	s := t.session()
	slug, file := r.PathValue("slug"), r.PathValue("file")
	if s == nil || s.ResearchStore() == nil || !research.ValidSlug(slug) {
		web.Error(w, http.StatusNotFound, "not_found", "no such research run")
		return
	}
	if file == "" {
		file = "index.html"
	}
	file = path.Clean("/" + file)[1:]
	site, err := s.ResearchSite(slug)
	if err != nil {
		web.Error(w, http.StatusNotFound, "not_found", "no such research run")
		return
	}
	body, found := site.Files[file]
	ctype := researchTypes[strings.ToLower(path.Ext(file))]
	if !found || ctype == "" {
		web.Error(w, http.StatusNotFound, "not_found", "no such page in this wiki")
		return
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(body)
}
