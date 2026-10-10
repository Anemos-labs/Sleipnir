// Package tools serves the recorded-session, schedule, doctor and update routes of `sleipnir web`: the Sessions view's recorded
// list, prune and delete; the read-only replay and the live following of a session another process writes; the Schedule view's
// jobs, run now, logs and the daemon; the Doctor's probe; the update check and install.
//
// It reads the state directory (session.StateRoot) and never a session's log while it is written by this package: sessions are
// listed and summarised by package session, deleted through session.RemoveRecorded (the session's lock, the salvage of worker
// trees), the schedule is package sched's (its write lock and its daemon lock, shared with `sleipnir daemon`). Commands it runs
// (the doctor's probe, a job run now, an update) go through the runner of the same server (package runner), so they share its cap,
// its process groups, its masking of output and its "run" frames.
//
// Privileged actions need a confirmation (docs/WEB-API.md): a prune with apply (prune:<d16 of the sorted ids>), a delete
// (delete:<d16 of the sorted ids>), adding a job (job.add), editing one (job.edit:<job>), installing an update (update:<version>).
// A request without the X-Confirm header is answered 428 with the scope in X-Confirm-Scope and, where the server decides what is
// confirmed (a prune), the plan as the error's detail.
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/update"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Options configure the tools routes.
type Options struct {
	// Home is the user's home directory (the state directory is session.StateRoot(Home)); empty is the current user's.
	Home string
	// Version and Commit are the program's (update checks); Self is the executable (runs), Env the environment of runs, Cwd the
	// directory of a run or a job that names none. Empty values take the runner's defaults.
	Version, Commit string
	Self            string
	Env             func() []string
	Cwd             string
	// Replay translates a recorded session's log into the page's UI events, in order (the translator of internal/web/translate,
	// wired by the host). Nil: GET /api/recorded/{sid}/events answers 501.
	Replay func(ctx context.Context, dir string) ([]json.RawMessage, error)
	// Follow translates a log another process is writing into UI events for the read-only tab tab, published with publish as the
	// page's "ev" frames, until ctx ends or the session ends (then it returns). Nil: watching answers 501.
	Follow func(ctx context.Context, tab, dir string, publish func(wire.Frame)) error
	// Update configures the release checks (the zero value is the real thing; tests point it at a fake server).
	Update update.Options
	// Now is the clock (tests); default time.Now.
	Now func() time.Time
	// Every is the default interval of the daemon started from the page (30 s).
	Every time.Duration
}

// service is the tools routes of one server.
type service struct {
	srv  *web.Server
	host seam.Host
	o    Options
	runs *runner.Runs

	mu      sync.Mutex
	daemon  *daemon
	jobRuns map[string]string // job id -> run id of a run now in progress
	busy    map[string]bool   // job ids the in-process daemon is running
	watches map[string]*watch // session id -> watch
	replays replayCache
}

// services are the tools services of the servers of this process, for Shutdown.
var services = struct {
	sync.Mutex
	m map[*web.Server]*service
}{m: map[*web.Server]*service{}}

// Register adds the recorded-session, schedule, doctor and update routes to srv, resolving tabs through h; output goes to h.Publish.
func Register(srv *web.Server, h seam.Host, o Options) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every <= 0 {
		o.Every = 30 * time.Second
	}
	if o.Cwd == "" {
		o.Cwd, _ = os.Getwd()
	}
	if o.Self == "" {
		o.Self, _ = os.Executable()
	}
	if o.Update.Agent == "" {
		o.Update.Agent = "sleipnir/" + o.Version
	}
	if o.Update.CachePath == "" {
		home := o.Home
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		o.Update.CachePath = filepath.Join(home, ".sleipnir", "update.json") // where `sleipnir update` keeps it: the user's, not the state's
	}
	s := &service{
		srv: srv, host: h, o: o,
		runs:    runner.For(srv, h, runner.Options{Self: o.Self, Env: o.Env, Cwd: o.Cwd}),
		jobRuns: map[string]string{}, busy: map[string]bool{}, watches: map[string]*watch{},
	}
	services.Lock()
	services.m[srv] = s
	services.Unlock()
	s.registerRecorded()
	s.registerSchedule()
	s.registerDoctor()
}

// Shutdown stops what the tools routes of srv started: the daemon of the page, the watched sessions, and every run of the server's
// runner; it waits for them at most until ctx ends. The server's owner calls it when the server stops.
func Shutdown(ctx context.Context, srv *web.Server) {
	services.Lock()
	s := services.m[srv]
	delete(services.m, srv)
	services.Unlock()
	if s != nil {
		s.stopDaemon(ctx)
		s.stopWatches()
	}
	runner.Shutdown(ctx, srv)
}

// home is the home directory the state directory is resolved against.
func (s *service) home() string { return s.o.Home }

// store is the schedule's job file.
func (s *service) store() sched.Store {
	return sched.Store{Path: filepath.Join(session.StateRoot(s.home()), "schedule.json")}
}

// hostedSessions is what a host that knows more than its tabs' summaries tells: every session id a tab holds or is about to hold
// while it starts or restarts (a summary names none in that gap, and the session's lock is free).
type hostedSessions interface {
	HostedSessions() map[string]string
}

// hostedSIDs are the session ids of the live tabs, with the tab of each: a recorded listing leaves them out, a prune keeps them, a
// delete refuses them. A tab that restarts counts for the session it had and the one it resumes.
func (s *service) hostedSIDs() map[string]string {
	out := map[string]string{}
	if s.host == nil {
		return out
	}
	for _, t := range s.host.Tabs() {
		if t.SID != "" {
			out[t.SID] = t.ID
		}
	}
	if hs, ok := s.host.(hostedSessions); ok {
		for sid, tab := range hs.HostedSessions() {
			out[sid] = tab
		}
	}
	return out
}

// publish sends a frame to every page.
func (s *service) publish(f wire.Frame) {
	if s.host != nil {
		s.host.Publish(f)
	}
}

// werr is a route error.
func werr(status int, code, msg string) error {
	return &wire.Error{Status: status, Code: code, Msg: msg}
}

// werrDetail is a route error with detail.
func werrDetail(status int, code, msg string, detail any) error {
	return &wire.Error{Status: status, Code: code, Msg: msg, Detail: detail}
}

// ok writes {"ok": true}.
func ok(w http.ResponseWriter) { _ = web.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

// captured is a ResponseWriter that keeps what a check wrote, for a route that answers in its own words (a prune whose plan changed).
type captured struct {
	h      http.Header
	status int
}

// Header returns the captured headers.
func (c *captured) Header() http.Header { return c.h }

// Write discards the body.
func (c *captured) Write(b []byte) (int, error) { return len(b), nil }

// WriteHeader keeps the status.
func (c *captured) WriteHeader(status int) { c.status = status }

// confirmOr checks the X-Confirm header against scope. Without the header it answers 428 with the scope (in X-Confirm-Scope and in
// the detail, with what is confirmed); with one that does not fit it answers 409 changed (the page's confirmation was for another
// plan, or expired) with the same detail, so that the page can show the plan as it is now and ask again.
func (s *service) confirmOr(w http.ResponseWriter, r *http.Request, scope string, detail map[string]any) bool {
	detail["scope"] = scope
	if r.Header.Get(web.ConfirmHeader) == "" {
		w.Header().Set("X-Confirm-Scope", scope)
		web.ErrorDetail(w, http.StatusPreconditionRequired, "confirm_required", "this needs a confirmation: confirm it, then send it again with the confirmation", detail)
		return false
	}
	c := &captured{h: http.Header{}}
	if s.srv.RequireConfirm(c, r, scope) {
		return true
	}
	w.Header().Set("X-Confirm-Scope", scope)
	web.ErrorDetail(w, http.StatusConflict, "changed", "what would be deleted changed, or the confirmation is no longer valid: look at the list again and confirm it", detail)
	return false
}
