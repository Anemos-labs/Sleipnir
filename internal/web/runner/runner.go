// Package runner serves the command runner of `sleipnir web`: every `sleipnir` command of the CLI spec
// (internal/web/clispec) run from a form, its output streamed to the page as "run" frames, and stopped on request.
//
// # How a command runs
//
// A run is a child process of this program's own executable, `sleipnir <path...> <flags...> <positionals...>`, built from the spec
// (never through a shell, never with a key in its arguments), in the directory of the tab the request names (else the server's
// directory). Its standard input is closed; it starts in a process group of its own, which a cancel ends (SIGTERM, then SIGKILL
// after five seconds) and which dies with the server on Linux. Its environment is the server's, from which the provider keys were
// taken at start (harden.MoveKeys); the commands that call a provider (mode "net") get the held keys on an inherited pipe they read
// at their start (sched.PassKeys), as a scheduled job does, never in their environment, which every same-user process can read on
// Linux. A command whose arguments name an endpoint or a key variable of their own (--base-url, --api-key-env, --policy-host,
// --policy-key-env, --allow-insecure-http) gets no key at all and needs a confirmation. Each command's mode decides: "run" and "net"
// run, "priv" needs a confirmation for the exact argument vector, "server" runs until it is stopped, "tty_only" is refused with the
// sentence that names the page's equivalent; the flags that widen what a run may do (--allow, --trust-project, --verify,
// --no-net-isolation, --pass-env, --set-env, a bypass or yolo mode, a listen address that is not loopback) make it "priv".
//
// # Output
//
// Standard output and standard error are read line by line (a line is cut at 4 KiB), made safe for display
// (tools.SanitizeForTerminal) and masked (the held keys, the run token, and credential-shaped strings), and sent in "run" frames
// every 50 ms: {id, lines: [{k: "out"|"err", t}]}. A run sends and keeps its first 20,000 lines and 4 MiB of text; a line past
// either limit is neither sent nor kept, and a last line says how many were left out. The end is {id, result: {exit, ms, card,
// canceled}} (critical: a slow page may miss lines, never the end). A run keeps the lines it sent for GET /api/runs/{run}/output, so
// that a page that comes back to a run it kept going can show what it missed. At most four runs go at once; a run ends after an
// hour (servers excepted).
//
// # Confirmation of privileged commands
//
// A "priv" run needs an X-Confirm id for the scope "run:<d16>", d16 being the first 16 hex digits of the SHA-256 of the argument
// vector (without the program's name) as JSON (JSON.stringify of the array). A request without one is answered 428 with the scope in
// the X-Confirm-Scope header and {scope, argv, cmdline} as the error's detail: the page shows the command, asks, obtains an id for
// that scope (POST /api/confirm) and repeats the request with it.
package runner

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/clispec"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
)

// Options configure the runner: the executable to run and the environment of its commands. The limits are for tests; a zero limit
// takes the default shown.
type Options struct {
	// Self is the executable of the runs (this program). Default: os.Executable.
	Self string
	// Env is the environment of the runs, before the held keys are put back for "net" commands. Default: os.Environ.
	Env func() []string
	// Cwd is the directory of a run whose request names no tab. Default: the working directory.
	Cwd string

	MaxRuns      int           // runs at once (4)
	MaxLines     int           // lines kept and sent per run (20,000)
	MaxBytes     int           // text kept and sent per run (4 MiB)
	MaxLineBytes int           // bytes per line (4 KiB)
	Timeout      time.Duration // the limit of one run, servers excepted (1 hour)
	Grace        time.Duration // between SIGTERM and SIGKILL (5 s)
	Batch        time.Duration // how long lines wait to be sent together (50 ms)
	Keep         int           // finished runs listed and kept for reattaching (20)
}

// withDefaults fills the zero fields.
func (o Options) withDefaults() Options {
	if o.Self == "" {
		o.Self, _ = os.Executable()
	}
	if o.Env == nil {
		o.Env = os.Environ
	}
	if o.Cwd == "" {
		o.Cwd, _ = os.Getwd()
	}
	if o.MaxRuns <= 0 {
		o.MaxRuns = 4
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 4 << 20
	}
	if o.MaxLines <= 0 {
		o.MaxLines = 20000
	}
	if o.MaxLineBytes <= 0 {
		o.MaxLineBytes = 4 << 10
	}
	if o.Timeout <= 0 {
		o.Timeout = time.Hour
	}
	if o.Grace <= 0 {
		o.Grace = 5 * time.Second
	}
	if o.Batch <= 0 {
		o.Batch = 50 * time.Millisecond
	}
	if o.Keep <= 0 {
		o.Keep = 20
	}
	return o
}

// engines are the run sets of the servers of this process: the runner's routes and the tools' routes (doctor, run now, update)
// share one per server, so that the cap of four counts them all and GET /api/runs lists them all.
var engines = struct {
	sync.Mutex
	m map[*web.Server]*Runs
}{m: map[*web.Server]*Runs{}}

// For returns the run set of srv, made with h and o on the first call (later calls return it as it is).
func For(srv *web.Server, h seam.Host, o Options) *Runs {
	engines.Lock()
	defer engines.Unlock()
	if r, ok := engines.m[srv]; ok {
		return r
	}
	r := newRuns(srv, h, o.withDefaults())
	engines.m[srv] = r
	return r
}

// Shutdown stops every run of srv (its processes are ended as a cancel ends them) and waits for them, at most until ctx ends. The
// server's owner calls it when the server stops.
func Shutdown(ctx context.Context, srv *web.Server) {
	engines.Lock()
	r := engines.m[srv]
	delete(engines.m, srv)
	engines.Unlock()
	if r != nil {
		r.Close(ctx)
	}
}

// runIDRE is the shape of a run id.
var runIDRE = regexp.MustCompile(`^r_[a-z2-7]{16}$`)

// runRequest is wire.RunRequest with Keep: the page lets the run go on when it leaves the runner.
type runRequest struct {
	Path  []string          `json:"path"`
	Pos   map[string]string `json:"pos,omitempty"`
	Flags map[string]any    `json:"flags,omitempty"`
	Tab   string            `json:"tab,omitempty"`
	Keep  bool              `json:"keep,omitempty"`
}

// Register adds the runner routes to srv, resolving tabs through h; output goes to h.Publish.
//
//	GET    /api/cli                     the CLI spec
//	POST   /api/runs                    start a run (RunRequest, plus keep) → 202 RunStarted
//	GET    /api/runs                    the running and recent runs
//	DELETE /api/runs/{run}              stop a run
//	GET    /api/runs/{run}/output?from= the lines a run kept, from a line number on
func Register(srv *web.Server, h seam.Host, o Options) {
	r := For(srv, h, o)
	srv.HandleFunc("GET /api/cli", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_ = web.WriteJSON(w, http.StatusOK, rawJSON(clispec.JSON()))
	}, web.RouteOpts{})
	srv.HandleFunc("POST /api/runs", r.handleStart, web.RouteOpts{})
	srv.HandleFunc("GET /api/runs", func(w http.ResponseWriter, _ *http.Request) {
		_ = web.WriteJSON(w, http.StatusOK, map[string]any{"runs": r.List()})
	}, web.RouteOpts{})
	srv.HandleFunc("DELETE /api/runs/{run}", func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("run")
		if !runIDRE.MatchString(id) {
			web.Error(w, http.StatusBadRequest, "bad_request", "that is not a run id")
			return
		}
		if !r.Cancel(id) {
			web.Error(w, http.StatusNotFound, "not_found", "no such run")
			return
		}
		_ = web.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}, web.RouteOpts{NoBody: true})
	srv.HandleFunc("GET /api/runs/{run}/output", func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("run")
		if !runIDRE.MatchString(id) {
			web.Error(w, http.StatusBadRequest, "bad_request", "that is not a run id")
			return
		}
		from := 0
		if v := req.URL.Query().Get("from"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				web.Error(w, http.StatusBadRequest, "bad_request", "from must be a line number")
				return
			}
			from = n
		}
		out, ok := r.Output(id, from)
		if !ok {
			web.Error(w, http.StatusNotFound, "not_found", "no such run, or its output is no longer kept")
			return
		}
		_ = web.WriteJSON(w, http.StatusOK, out)
	}, web.RouteOpts{})
}

// rawJSON is JSON that is written as it is (WriteJSON escapes its HTML characters).
type rawJSON []byte

// MarshalJSON returns the bytes.
func (j rawJSON) MarshalJSON() ([]byte, error) { return j, nil }

// handleStart is POST /api/runs.
func (r *Runs) handleStart(w http.ResponseWriter, req *http.Request) {
	var body runRequest
	if !web.DecodeJSON(w, req, &body) {
		return
	}
	plan, err := r.Plan(req.Context(), body.Path, body.Pos, body.Flags, body.Tab)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	if plan.Mode == "priv" && !r.confirm(w, req, plan) {
		return
	}
	started, err := r.Start(Spec{
		Path: plan.Path, Flags: body.Flags, Args: plan.Args, Dir: plan.Dir, Net: plan.Net, Cmdline: plan.Cmdline,
		Keep: body.Keep, Server: plan.Server,
	})
	if err != nil {
		web.WriteError(w, err)
		return
	}
	web.Logf(req, "run %s started: sleipnir %s (%s)", started.ID, joinPath(plan.Path), plan.Mode)
	_ = web.WriteJSON(w, http.StatusAccepted, started)
}

// confirm checks the confirmation of a privileged run; without one it answers 428 with the scope and what is to be confirmed.
func (r *Runs) confirm(w http.ResponseWriter, req *http.Request, plan *Plan) bool {
	scope := "run:" + D16(plan.Args)
	if req.Header.Get(web.ConfirmHeader) == "" {
		w.Header().Set("X-Confirm-Scope", scope)
		web.ErrorDetail(w, http.StatusPreconditionRequired, "confirm_required",
			"this command changes your settings or files: confirm it, then send it again with the confirmation",
			map[string]any{"scope": scope, "argv": plan.Args, "cmdline": plan.Cmdline, "reasons": plan.Reasons})
		return false
	}
	return r.srv.RequireConfirm(w, req, scope)
}

// List returns the running runs and the recent ones, newest first.
func (r *Runs) List() []RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RunInfo, 0, len(r.runs))
	for _, x := range r.runs {
		out = append(out, x.info())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].started.After(out[j].started) })
	return out
}
