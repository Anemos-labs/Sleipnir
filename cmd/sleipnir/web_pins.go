package main

// Pinned sessions of `sleipnir web`: a person pins a tab's session (PUT /api/sessions/{id}/pin) and the server opens it again, in a
// tab of its own, each time it starts (internal/session/pins.go keeps the list). A pinned session reopens the way the Resume dialog
// resumes it: with the server's own flags, the team its last start had and the verify command it ran with. What the person
// confirmed for the tab when they pinned it is all a reopen may use: a verify command other than the pinned one, or trust in a
// folder other than the server's, is left for the person to confirm from the page, and the session is not reopened by itself.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// pinRequest is the body of PUT /api/sessions/{id}/pin.
type pinRequest struct {
	Pinned bool `json:"pinned"`
}

// pinRoutes adds the pin route of a tab.
func (h *webHostImpl) pinRoutes(srv *web.Server) {
	h.tabRoute(srv, "PUT /api/sessions/{id}/pin", web.RouteOpts{MaxBody: 1 << 10}, func(w http.ResponseWriter, r *http.Request, t *webTab) {
		var body pinRequest
		if !web.DecodeJSON(w, r, &body) {
			return
		}
		if err := t.SetPinned(r.Context(), body.Pinned); err != nil {
			writeErr(w, err)
			return
		}
		ok(w, map[string]any{"tab": t.Summary()})
	})
}

// pinHome is the home directory whose state directory holds the pin list.
func pinHome() string {
	home, _ := os.UserHomeDir()
	return home
}

// pinOfLocked is the pin of the tab's session as it runs now: its id, its name, its folder and its verify command. Under t.mu.
func (t *webTab) pinOfLocked(s *session.Session) session.Pin {
	p := session.Pin{ID: s.ID, Name: t.name, Cwd: t.cwdLocked(), At: time.Now().UTC()}
	if f, err := parseChatFlags(t.args); err == nil {
		p.Verify = f.verify
	}
	return p
}

// SetPinned pins the tab's session (on) or unpins it. The tab's session must have started; the change is announced to the pages.
func (t *webTab) SetPinned(ctx context.Context, on bool) error {
	s := t.readySession(ctx)
	if s == nil {
		return werr(http.StatusConflict, "not_ready", "the session has not started")
	}
	home := pinHome()
	t.mu.Lock()
	p := t.pinOfLocked(s)
	t.mu.Unlock()
	if on {
		if err := session.SetPin(home, p); err != nil {
			return werr(http.StatusConflict, "pin", clip(err.Error(), 300))
		}
	} else if _, err := session.Unpin(home, s.ID); err != nil {
		return werr(http.StatusInternalServerError, "pin", clip(err.Error(), 300))
	}
	t.mu.Lock()
	t.pinned, t.pinnedSID = on, s.ID
	sum := t.summaryLocked()
	t.mu.Unlock()
	if on {
		webAction(s, "pin", "reopened when the server starts")
	} else {
		webAction(s, "unpin", "")
	}
	t.h.Publish(wire.Frame{Type: "tab", Data: wire.TabFrame{Op: "update", Tab: sum}, Critical: true})
	return nil
}

// notePinned reads whether the session that has just started in the tab is pinned. A tab that was pinned and continues in another
// session (a restart that starts a new one) moves its pin to it, so that the pin follows the tab.
func (t *webTab) notePinned(s *session.Session) {
	home := pinHome()
	t.mu.Lock()
	was, prev := t.pinned, t.pinnedSID
	p := t.pinOfLocked(s)
	t.mu.Unlock()
	pinned := session.Pinned(home, s.ID)
	if !pinned && was && prev != "" && prev != s.ID && session.Pinned(home, prev) {
		if err := session.RepinSession(home, prev, p); err == nil {
			pinned = true
		} else {
			t.sys("◇", "the pin of this session could not move to its new session: "+clip(err.Error(), 200))
		}
	}
	t.mu.Lock()
	t.pinned, t.pinnedSID = pinned, s.ID
	t.mu.Unlock()
}

// renamePin keeps a pinned session's name current in the pin list.
func (t *webTab) renamePin(s *session.Session) {
	t.mu.Lock()
	pinned := t.pinned
	p := t.pinOfLocked(s)
	t.mu.Unlock()
	if pinned {
		_ = session.SetPin(pinHome(), p)
	}
}

// unpinClosed removes the pin of a tab the person closed: closing a pinned tab is how a person says it should not come back.
func (t *webTab) unpinClosed() {
	t.mu.Lock()
	pinned, sid := t.pinned, t.pinnedSID
	t.mu.Unlock()
	if pinned && sid != "" {
		_, _ = session.Unpin(pinHome(), sid)
	}
}

// reopenPinned opens the pinned sessions, in pin order, each in a tab of its own; it runs once, when the server starts. A pinned
// session that cannot reopen by itself is named on the server's terminal with the reason, and stays pinned (the Resume dialog lists
// it as pinned) unless it is no longer recorded.
func (h *webHostImpl) reopenPinned() {
	home := pinHome()
	pins, err := session.LoadPins(home)
	if err != nil {
		h.logf("the pinned sessions could not be read: %s", clip(err.Error(), 300))
		return
	}
	for _, p := range pins {
		if h.ctx.Err() != nil {
			return
		}
		if why := h.reopenPin(home, p); why != "" {
			label := p.ID
			if n := strings.TrimSpace(p.Name); n != "" && validName(n) == nil {
				label = n + " (" + p.ID + ")"
			}
			h.logf("pinned session %s was not reopened: %s", label, why)
		}
	}
}

// reopenPin opens one pinned session in a new tab and starts it; it returns why it did not ("" when it did, or when a tab holds the
// session already).
func (h *webHostImpl) reopenPin(home string, p session.Pin) string {
	dir := filepath.Join(session.SessionsDir(home), p.ID)
	if !fileExists(eventsPath(dir)) {
		_, _ = session.Unpin(home, p.ID)
		return "it is no longer recorded, so it was unpinned"
	}
	if h.holder(p.ID, nil) != nil {
		return ""
	}
	if session.Locked(dir) {
		return "another process is using it"
	}
	cwd := h.d.Cwd
	if sum := session.Summarize(eventsPath(dir)); sum.Cwd != "" {
		if fi, err := os.Stat(sum.Cwd); err == nil && fi.IsDir() {
			cwd = sum.Cwd
		}
	}
	if _, err := session.ResolveResume(home, rootOf(cwd), p.ID); err != nil {
		return clip(err.Error(), 300)
	}
	d := h.d
	d.Cwd = cwd
	last := session.LatestStart(dir)
	if last.Verify != "" {
		d.Verify = last.Verify
	}
	if len(last.Team) > 0 {
		d.Team, d.Workers = last.Team, len(last.Team)
	}
	if d.TrustProject && cleanDir(cwd) != cleanDir(h.d.Cwd) {
		// --trust-project is for the server's folder: in another one the session goes by what the person remembered trusting
		d.TrustProject = false
	}
	args := chatArgsOf(d, p.ID)
	f, err := parseChatFlags(args)
	if err != nil {
		return clip(err.Error(), 300)
	}
	pv := raised(f, h.serverBaseline(), cleanDir(cwd))
	if pv.Verify != "" && pv.Verify == p.Verify {
		pv.Verify = "" // the person ran it with this verify command when they pinned it
	}
	if reasons := pv.reasons(); len(reasons) > 0 {
		return "it needs your confirmation (" + clip(strings.Join(reasons, "; "), 300) + "): resume it from the page"
	}
	name := strings.TrimSpace(p.Name)
	if validName(name) != nil {
		name = ""
	}
	t, err := h.addTab(name, cwd, h.base)
	if err != nil {
		return clip(err.Error(), 300)
	}
	h.track(func() {
		if err := t.startGen(startSpec{args: args, note: "reopened because this session is pinned"}); err != nil && h.ctx.Err() == nil {
			h.logf("pinned session %s did not start: %s", p.ID, clip(err.Error(), 300))
		}
	})
	return ""
}
