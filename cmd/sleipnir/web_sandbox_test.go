package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// GET /api/sandbox says whether a sandbox can start here, and why not, asking the engine once for a while.
func TestWebSandboxStatusSaysWhyNot(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)

	calls := 0
	r.h.sandbox.check = func(context.Context) error { calls++; return errors.New("the docker command is not installed") }
	var st wire.SandboxStatus
	r.json(http.MethodGet, "/api/sandbox", nil, http.StatusOK, &st)
	if st.Available || st.Why != "the docker command is not installed" {
		t.Errorf("status = %+v, want unavailable with the engine's reason", st)
	}
	r.json(http.MethodGet, "/api/sandbox", nil, http.StatusOK, &st)
	if calls != 1 {
		t.Errorf("the engine was asked %d times, want once while the answer is fresh", calls)
	}

	r.h.sandbox.check, r.h.sandbox.at = func(context.Context) error { return nil }, time.Time{}
	st = wire.SandboxStatus{}
	r.json(http.MethodGet, "/api/sandbox", nil, http.StatusOK, &st)
	if !st.Available || st.Why != "" {
		t.Errorf("status = %+v, want available", st)
	}
}

// A clone's progress is followed by the id the page chose: git's lines and their share while it runs, the inventory after, and
// done once it answered; an unknown or malformed id is refused.
func TestWebScanCloneProgress(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)

	k := r.h.clones.begin("abc-1")
	k.line("Receiving objects:  42% (420/1000), 1.20 MiB | 2.00 MiB/s")
	var p wire.CloneProgress
	r.json(http.MethodGet, "/api/scan/clone/progress?id=abc-1", nil, http.StatusOK, &p)
	if p.Phase != "clone" || p.Percent != 42 || p.Done {
		t.Errorf("progress = %+v, want the clone at 42%%", p)
	}
	k.step("inventory", "Taking the inventory of the repository")
	k.end()
	r.json(http.MethodGet, "/api/scan/clone/progress?id=abc-1", nil, http.StatusOK, &p)
	if p.Phase != "inventory" || p.Percent != -1 || !p.Done {
		t.Errorf("progress = %+v, want the inventory, done", p)
	}
	if code, _ := r.do(http.MethodGet, "/api/scan/clone/progress?id=nope", nil); code != http.StatusNotFound {
		t.Errorf("an unknown id answered %d, want 404", code)
	}
	if code, _ := r.do(http.MethodGet, "/api/scan/clone/progress?id=a/b", nil); code != http.StatusBadRequest {
		t.Errorf("a malformed id answered %d, want 400", code)
	}
	if code, _ := r.do(http.MethodPost, "/api/scan/clone", map[string]any{"url": "https://github.com/o/r", "progress": "bad id!"}); code != http.StatusBadRequest {
		t.Errorf("a malformed progress id answered %d, want 400", code)
	}
	var none cloneTracker
	none.begin("").line("ignored") // no id: nothing is kept
	if len(none.runs) != 0 {
		t.Errorf("a clone without an id was tracked")
	}
}
