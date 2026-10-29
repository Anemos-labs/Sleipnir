package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/scan"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// scanAction is POST /api/sessions/{id}/scan: start a scan (the goal /scan sets, queued like any goal), write a finding up as
// an issue, open the issue the person approved, or set the goal that fixes a finding.
func (t *webTab) scanAction(ctx context.Context, req wire.ScanRequest) (map[string]any, error) {
	s := t.readySession(ctx)
	if s == nil {
		return nil, t.noSession()
	}
	if msg, off := s.SlashOff("/scan"); off {
		return nil, werr(http.StatusConflict, "plugin_off", msg)
	}
	switch req.Action {
	case "start":
		text, err := s.StartScan(ctx, req.Depth, req.Focus)
		if err != nil {
			return nil, werr(http.StatusBadRequest, "bad_scan", err.Error())
		}
		queued, err := t.goalAction(ctx, s, wire.GoalRequest{Action: "set", Text: text}, true)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "queued": queued}, nil
	case "issue-draft":
		title, body, err := s.ScanIssue(req.Slug, req.Lead)
		if err != nil {
			return nil, werr(http.StatusBadRequest, "bad_finding", err.Error())
		}
		return map[string]any{"ok": true, "title": title, "body": body}, nil
	case "issue":
		u, err := s.OpenScanIssue(ctx, req.Title, req.Body)
		if err != nil {
			return nil, werr(http.StatusBadGateway, "issue_failed", err.Error())
		}
		return map[string]any{"ok": true, "url": u}, nil
	case "fix":
		text, err := s.ScanFix(req.Slug, req.Lead)
		if err != nil {
			return nil, werr(http.StatusBadRequest, "bad_finding", err.Error())
		}
		queued, err := t.goalAction(ctx, s, wire.GoalRequest{Action: "set", Text: text, AutoFix: req.AutoFix, AutoMerge: req.AutoMerge}, true)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "queued": queued}, nil
	}
	return nil, werr(http.StatusBadRequest, "bad_request", "the action is start, issue-draft, issue or fix")
}

// scanClonesDir is where the page clones the repositories it scans: a folder of their own, beside the person's projects and
// outside the harness's state.
func scanClonesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("the home directory is unknown, so there is nowhere to clone to")
	}
	return filepath.Join(home, "sleipnir-scans"), nil
}

// scanClone is POST /api/scan/clone: clone a repository (hooks and symbolic links off, https and ssh only) into a folder of
// its own and create the scan there. The page then starts a sandboxed session in that folder with the scan's goal: the
// person sees the New session dialog filled in and starts it.
func (h *webHostImpl) scanClone(w http.ResponseWriter, r *http.Request) {
	var req wire.ScanCloneRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	if err := scan.CheckURL(req.URL); err != nil {
		writeErr(w, werr(http.StatusBadRequest, "bad_url", err.Error()))
		return
	}
	if req.Depth == "" {
		req.Depth = scan.DefaultDepth
	}
	if _, ok := scan.DepthByID(req.Depth); !ok {
		writeErr(w, werr(http.StatusBadRequest, "bad_depth", "the depth is survey, standard or deep"))
		return
	}
	base, err := scanClonesDir()
	if err != nil {
		writeErr(w, werr(http.StatusInternalServerError, "internal", err.Error()))
		return
	}
	dir := filepath.Join(base, scan.CloneName(req.URL))
	for n := 2; ; n++ {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			break
		}
		dir = filepath.Join(base, scan.CloneName(req.URL)+"-"+strconv.Itoa(n))
	}
	if err := scan.Clone(r.Context(), req.URL, dir); err != nil {
		writeErr(w, werr(http.StatusBadGateway, "clone_failed", err.Error()))
		return
	}
	repo, err := gitx.Open(dir, gitx.WithMaxOutput(256<<20), gitx.WithTimeout(5*time.Minute))
	if err != nil {
		writeErr(w, werr(http.StatusBadGateway, "clone_failed", err.Error()))
		return
	}
	run, err := scan.Start(r.Context(), scan.NewStore(dir), repo, req.Depth, "")
	if err != nil {
		writeErr(w, werr(http.StatusBadRequest, "bad_scan", "cloned into "+dir+", but the scan was not created: "+err.Error()))
		return
	}
	ok(w, map[string]any{"ok": true, "dir": dir, "slug": run.Slug, "team": run.Team(), "shards": len(run.Shards),
		"goal": scan.Objective(run, filepath.Join(dir, filepath.FromSlash(scan.BriefPath(run.Slug))))})
}

// scanExports are the formats a scan's report is exported in: the file name and the content type of each. The page is the
// report laid out for print, which the browser saves as a PDF.
var scanExports = map[string][2]string{
	"json":  {scan.FindingsFile, "application/json"},
	"csv":   {scan.CSVFile, "text/csv; charset=utf-8"},
	"html":  {scan.HTMLFile, "text/html; charset=utf-8"},
	"md":    {scan.ReportFile, "text/markdown; charset=utf-8"},
	"sarif": {scan.SARIFFile, "application/sarif+json"},
}

// scanExport is GET /api/sessions/{id}/scan/{slug}/export/{format}: the scan's report in one format, rendered from its ledger as
// it is now (never a file read from the project), as a download except for the printable page.
func (t *webTab) scanExport(w http.ResponseWriter, r *http.Request) {
	s := t.session()
	if s == nil {
		writeErr(w, t.noSession())
		return
	}
	format := r.PathValue("format")
	kind, known := scanExports[format]
	if !known {
		writeErr(w, werr(http.StatusNotFound, "bad_format", "the format is json, csv, html, md or sarif"))
		return
	}
	data, slug, err := s.ScanExport(r.PathValue("slug"), format)
	if err != nil {
		writeErr(w, werr(http.StatusNotFound, "no_scan", err.Error()))
		return
	}
	w.Header().Set("Content-Type", kind[1])
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if format == "html" {
		// the page is the report and nothing else: no script runs in it, and it loads nothing
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="`+slug+"-"+kind[0]+`"`)
	}
	_, _ = w.Write(data)
}
