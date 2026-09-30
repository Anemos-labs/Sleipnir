package env

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/rl"
)

// The rollout server exposes the environment to trainers (verl, OpenRLHF,
// SkyRL, online GRPO loops) as a black box:
//
//	POST /v1/rollouts   run G samples of a task, streaming NDJSON
//	GET  /v1/runs/{id}  status, manifest and summary of a run
//	GET  /v1/runs/{id}/episodes/{task}/{sample}   one finished episode
//	GET  /healthz       liveness (no authentication)
//
// SECURITY. A rollout request is remote code execution by design: the task it
// carries names setup and verifier commands the server runs, and repositories it
// reads. The server therefore requires a bearer token (compared in constant time)
// unless it is bound to a loopback address, refuses to bind anywhere else
// without one, limits request sizes and concurrency, and can confine inline
// tasks to a set of repository roots. None of that makes it safe to expose to
// untrusted parties: put it behind TLS and a trust boundary you control, and run
// its commands in a container through the Sandbox hook.

// ServeOptions configures the rollout server.
type ServeOptions struct {
	// Root is the directory that holds one subdirectory per run (required).
	Root string
	// Token is the bearer token clients must present. It may be empty only when
	// the listener is on a loopback address.
	Token string
	// Tasks is the registry addressed by {"task": "<id>"} requests.
	Tasks []rl.Task
	// MaxBodyBytes bounds a request body (default 8 MiB).
	MaxBodyBytes int64
	// MaxGroup bounds the group size of one request (default 64).
	MaxGroup int
	// MaxRuns bounds concurrent rollout requests; further ones get 429 (default 2).
	MaxRuns int
	// Concurrency is the number of rollouts in flight within one request; 0 keeps
	// the Runner's setting.
	Concurrency int
	// RepoRoots, when non-empty, confines inline tasks to repositories located
	// under one of these directories (and forbids URL repositories). Tasks from
	// the registry are trusted and exempt.
	RepoRoots []string
	// RewardsDir and NewScorer implement the "rewards" request field: the field
	// names a file relative to RewardsDir, which must resolve inside it (no "..",
	// no symlinks out), and NewScorer turns it into a Scorer for that request.
	RewardsDir string
	NewScorer  func(rewardsPath string) (Scorer, error)
	// ShutdownTimeout bounds graceful shutdown of in-flight requests (default 30 s).
	ShutdownTimeout time.Duration
	// Logf receives diagnostics; nil discards them.
	Logf func(format string, args ...any)
	// Listener, when set, is used instead of listening on the address.
	Listener net.Listener
	// OnListen is called once with the address the server listens on.
	OnListen func(net.Addr)
}

// ErrNeedToken is returned by Serve when asked to listen on a non-loopback
// address without a token.
var ErrNeedToken = errors.New("refusing to listen on a non-loopback address without an authentication token")

// Serve runs the rollout server on addr until ctx ends, then shuts down
// gracefully: it stops accepting requests, cancels the runs in flight (their
// manifests and summaries are still written) and waits for them.
func Serve(ctx context.Context, addr string, r *Runner, opts ServeOptions) error {
	s, err := NewServer(r, opts)
	if err != nil {
		return err
	}
	ln := opts.Listener
	if ln == nil {
		if opts.Token == "" && !loopbackAddr(addr) {
			return fmt.Errorf("%w (%s)", ErrNeedToken, addr)
		}
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
	} else if opts.Token == "" && !loopbackAddr(ln.Addr().String()) {
		return fmt.Errorf("%w (%s)", ErrNeedToken, ln.Addr())
	}
	if opts.OnListen != nil {
		opts.OnListen(ln.Addr())
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // bounds slow request bodies; responses stream without a write timeout
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		s.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	timeout := opts.ShutdownTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// Cancel the runs first: their streams end promptly, which lets Shutdown
	// finish; each run still writes its manifest and summary.
	s.stop()
	sctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err = srv.Shutdown(sctx)
	s.Close()
	if err != nil {
		_ = srv.Close()
	}
	if e := <-errCh; e != nil && !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	return err
}

// loopbackAddr reports whether every address a listen string can bind to is a
// loopback address. ":8080" and "0.0.0.0:8080" are not.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// Server is the rollout server's request handler and run registry.
type Server struct {
	r     *Runner
	o     ServeOptions
	tasks map[string]rl.Task
	sem   chan struct{}
	roots []string
	token [32]byte

	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu     sync.Mutex
	closed bool
	active map[string]*activeRun
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewServer validates options and returns a Server; Handler exposes it over
// HTTP (Serve does the listening). It is separate so tests can use httptest.
func NewServer(r *Runner, o ServeOptions) (*Server, error) {
	if r == nil || r.Harness == nil || r.Workspaces == nil {
		return nil, errors.New("Serve needs a Runner with a Harness and Workspaces")
	}
	if o.Root == "" {
		return nil, errors.New("ServeOptions.Root is required")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	o.Root = root
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 8 << 20
	}
	if o.MaxGroup <= 0 {
		o.MaxGroup = 64
	}
	if o.MaxRuns <= 0 {
		o.MaxRuns = 2
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	s := &Server{r: r, o: o, tasks: map[string]rl.Task{}, sem: make(chan struct{}, o.MaxRuns), active: map[string]*activeRun{}}
	if err := ValidateTasks(o.Tasks); err != nil {
		return nil, fmt.Errorf("task registry: %w", err)
	}
	for _, t := range o.Tasks {
		s.tasks[t.ID] = t
	}
	for _, d := range o.RepoRoots {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		s.roots = append(s.roots, abs)
	}
	s.token = sha256.Sum256([]byte(o.Token))
	s.baseCtx, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

// stop refuses new runs and cancels the active ones.
func (s *Server) stop() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
}

// Close cancels every active run and waits for them to finish writing.
func (s *Server) Close() {
	s.stop()
	s.wg.Wait()
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/rollouts", s.handleRollouts)
	mux.HandleFunc("/v1/runs/", s.handleRuns)
	return s.recoverer(s.auth(mux))
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				s.o.Logf("env: server panic on %s %s: %v", r.Method, r.URL.Path, p)
				httpError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// auth enforces the bearer token on everything except /healthz. Both sides are
// hashed before the constant-time comparison so the token's length is not
// revealed either.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.o.Token == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sleipnir-rl"`)
			httpError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		got := sha256.Sum256([]byte(strings.TrimSpace(h[len(prefix):])))
		if subtle.ConstantTimeCompare(got[:], s.token[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sleipnir-rl"`)
			httpError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---- POST /v1/rollouts ----

// RolloutRequest is the body of POST /v1/rollouts.
type RolloutRequest struct {
	// Task is a task id (a JSON string, looked up in the registry) or an inline
	// task object.
	Task   json.RawMessage `json:"task"`
	Policy PolicyRequest   `json:"policy"`
	// Group is the number of samples (default 1).
	Group int `json:"group"`
	// Swarm is true, false or {"agents": N}.
	Swarm json.RawMessage `json:"swarm,omitempty"`
	// Rewards names a rewards file relative to the server's rewards directory.
	Rewards     string            `json:"rewards,omitempty"`
	Capture     bool              `json:"capture,omitempty"`
	Seed        int64             `json:"seed,omitempty"`
	TargetPrice string            `json:"target_price,omitempty"`
	RoleModels  map[string]string `json:"role_models,omitempty"`
	// RunID names the run directory (letters, digits, ".-_"); default generated.
	// Reusing an id resumes that run.
	RunID string `json:"run_id,omitempty"`
	Force bool   `json:"force,omitempty"`
}

// PolicyRequest is the policy part of a request.
type PolicyRequest struct {
	Model     string          `json:"model"`
	BaseURL   string          `json:"base_url,omitempty"`
	APIKeyEnv string          `json:"api_key_env,omitempty"`
	Sampling  json.RawMessage `json:"sampling,omitempty"`
}

var (
	runIDRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sampleRe = regexp.MustCompile(`^[0-9]{1,9}$`)
)

func (s *Server) resolveTask(raw json.RawMessage) (rl.Task, int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return rl.Task{}, http.StatusBadRequest, errors.New(`"task" is required`)
	}
	if raw[0] == '"' {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return rl.Task{}, http.StatusBadRequest, err
		}
		t, ok := s.tasks[id]
		if !ok {
			return rl.Task{}, http.StatusNotFound, fmt.Errorf("unknown task id %q", id)
		}
		return t, 0, nil
	}
	t, err := decodeTaskLine(raw)
	if err != nil {
		return rl.Task{}, http.StatusBadRequest, fmt.Errorf("inline task: %v", err)
	}
	if err := ValidateTask(t); err != nil {
		return rl.Task{}, http.StatusBadRequest, err
	}
	if len(s.roots) > 0 {
		if t.Repo.URL != "" || t.Repo.Path == "" {
			return rl.Task{}, http.StatusForbidden, errors.New("inline tasks must use a local repo.path on this server")
		}
		if !s.pathAllowed(t.Repo.Path) {
			return rl.Task{}, http.StatusForbidden, fmt.Errorf("repo.path %q is outside the allowed repository roots", t.Repo.Path)
		}
	}
	return t, 0, nil
}

func (s *Server) pathAllowed(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	for _, r := range s.roots {
		if real == r || strings.HasPrefix(real, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveUnder resolves rel below root, refusing anything that would leave it
// through ".." or a symlink.
func resolveUnder(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) {
		return "", errors.New("must be a relative path")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("escapes the rewards directory")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	p, err := filepath.EvalSymlinks(filepath.Join(realRoot, clean))
	if err != nil {
		return "", err
	}
	if p != realRoot && !strings.HasPrefix(p, realRoot+string(filepath.Separator)) {
		return "", errors.New("escapes the rewards directory")
	}
	return p, nil
}

func parseSwarm(raw json.RawMessage) (swarm bool, agents int, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "false" {
		return false, 0, nil
	}
	if string(raw) == "true" {
		return true, 0, nil
	}
	var o struct {
		Agents int `json:"agents"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return false, 0, fmt.Errorf(`"swarm" must be true, false or {"agents": N}: %v`, err)
	}
	if o.Agents < 0 || o.Agents > 256 {
		return false, 0, errors.New(`"swarm.agents" is out of range`)
	}
	return true, o.Agents, nil
}

// streamLine is one NDJSON line of the response.
type streamLine struct {
	Type     string          `json:"type"`
	RunID    string          `json:"run_id,omitempty"`
	Progress *Progress       `json:"progress,omitempty"`
	Task     string          `json:"task,omitempty"`
	Sample   *int            `json:"sample,omitempty"`
	Status   string          `json:"status,omitempty"`
	Episode  json.RawMessage `json:"episode,omitempty"`
	Summary  *Summary        `json:"summary,omitempty"`
	Dropped  int             `json:"dropped_progress_events,omitempty"`
	Error    string          `json:"error,omitempty"`
}

func (s *Server) handleRollouts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		httpError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	// The body is read completely (it is bounded) before it is parsed, so an
	// oversized request is reported as such rather than as whatever syntax error
	// the truncated JSON happens to contain.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.o.MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d bytes", s.o.MaxBodyBytes))
			return
		}
		httpError(w, http.StatusBadRequest, "reading the request: "+err.Error())
		return
	}
	var req RolloutRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if dec.More() {
		httpError(w, http.StatusBadRequest, "invalid request: trailing data after the JSON object")
		return
	}
	task, code, err := s.resolveTask(req.Task)
	if err != nil {
		httpError(w, code, err.Error())
		return
	}
	group := req.Group
	if group == 0 {
		group = 1
	}
	if group < 0 || group > s.o.MaxGroup {
		httpError(w, http.StatusBadRequest, fmt.Sprintf(`"group" must be between 1 and %d`, s.o.MaxGroup))
		return
	}
	if strings.TrimSpace(req.Policy.Model) == "" {
		httpError(w, http.StatusBadRequest, `"policy.model" is required`)
		return
	}
	swarm, agents, err := parseSwarm(req.Swarm)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	runID := req.RunID
	if runID != "" && !runIDRe.MatchString(runID) {
		httpError(w, http.StatusBadRequest, `"run_id" must match `+runIDRe.String())
		return
	}
	var scorer Scorer
	if req.Rewards != "" {
		if s.o.RewardsDir == "" || s.o.NewScorer == nil {
			httpError(w, http.StatusBadRequest, "this server does not support the rewards field")
			return
		}
		p, err := resolveUnder(s.o.RewardsDir, req.Rewards)
		if err != nil {
			httpError(w, http.StatusBadRequest, "rewards: "+err.Error())
			return
		}
		if scorer, err = s.o.NewScorer(p); err != nil {
			httpError(w, http.StatusBadRequest, "rewards: "+err.Error())
			return
		}
	}

	// A slot per concurrent request; further ones are told to come back.
	select {
	case s.sem <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "5")
		httpError(w, http.StatusTooManyRequests, "too many rollouts in progress")
		return
	}
	defer func() { <-s.sem }()

	if runID == "" {
		runID = fmt.Sprintf("run-%s-%s", time.Now().UTC().Format("20060102-150405"), randHex(3))
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		httpError(w, http.StatusServiceUnavailable, "server is shutting down")
		return
	}
	if _, busy := s.active[runID]; busy {
		s.mu.Unlock()
		httpError(w, http.StatusConflict, "run "+runID+" is already running")
		return
	}
	runCtx, cancel := context.WithCancel(s.baseCtx)
	ar := &activeRun{cancel: cancel, done: make(chan struct{})}
	s.active[runID] = ar
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.active, runID)
		s.mu.Unlock()
		close(ar.done)
		s.wg.Done()
	}()
	// The client going away cancels the run (its manifest and summary are still
	// written, and reusing run_id resumes it).
	go func() {
		select {
		case <-r.Context().Done():
			cancel()
		case <-runCtx.Done():
		}
	}()

	runner := *s.r
	runner.Out = filepath.Join(s.o.Root, runID)
	if s.o.Concurrency > 0 {
		runner.Concurrency = s.o.Concurrency
	}
	if scorer != nil {
		runner.Score = scorer
	}
	lines := make(chan streamLine, 4096)
	var dropped int
	var dmu sync.Mutex
	userProgress := s.r.Progress
	runner.Progress = func(p Progress) {
		if userProgress != nil {
			userProgress(p)
		}
		select {
		case lines <- streamLine{Type: "progress", Progress: &p}:
		default: // a slow client must not stall the workers; progress is advisory
			dmu.Lock()
			dropped++
			dmu.Unlock()
		}
	}
	opts := RolloutOpts{
		RunID: runID,
		Policy: PolicySpec{
			Model: req.Policy.Model, BaseURL: req.Policy.BaseURL, APIKeyEnv: req.Policy.APIKeyEnv, Sampling: req.Policy.Sampling,
		},
		Capture: req.Capture, Swarm: swarm, Agents: agents, RoleModels: req.RoleModels, TargetPrice: req.TargetPrice,
		Seed: req.Seed, Force: req.Force,
	}
	if req.Rewards != "" {
		opts.Extra = map[string]any{"rewards": req.Rewards}
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	writeLine := func(l streamLine) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(60 * time.Second))
		if err := enc.Encode(l); err != nil {
			return false
		}
		_ = rc.Flush()
		return true
	}
	w.WriteHeader(http.StatusOK)
	if !writeLine(streamLine{Type: "accepted", RunID: runID}) {
		cancel()
		return
	}

	type outcome struct {
		sum *Summary
		err error
	}
	doneCh := make(chan outcome, 1)
	go func() {
		sum, err := runner.Rollout(runCtx, []rl.Task{task}, group, opts)
		doneCh <- outcome{sum, err}
	}()

	var out outcome
	alive := true
loop:
	for {
		select {
		case l := <-lines:
			if alive && !writeLine(l) {
				alive = false
				cancel()
			}
		case out = <-doneCh:
			break loop
		}
	}
	// Drain what the run queued before it finished.
	for drained := false; !drained; {
		select {
		case l := <-lines:
			if alive && !writeLine(l) {
				alive = false
			}
		default:
			drained = true
		}
	}
	if !alive {
		return
	}
	if out.sum == nil {
		writeLine(streamLine{Type: "error", Error: errString(out.err)})
		return
	}
	// The episodes come last, in deterministic (task, sample) order, read back
	// from the run directory rather than kept in memory.
	for _, res := range out.sum.Results {
		if res.Status == StatusCancelled || res.Status == "pending" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(runner.Out, res.Task, strconv.Itoa(res.Sample), "episode.json"))
		if err != nil {
			continue
		}
		sample := res.Sample
		if !writeLine(streamLine{Type: "episode", Task: res.Task, Sample: &sample, Status: res.Status, Episode: json.RawMessage(raw)}) {
			return
		}
	}
	dmu.Lock()
	dr := dropped
	dmu.Unlock()
	sum := *out.sum
	writeLine(streamLine{Type: "summary", Summary: &sum, Dropped: dr})
	if out.err != nil {
		writeLine(streamLine{Type: "error", Error: errString(out.err)})
		return
	}
	writeLine(streamLine{Type: "done", RunID: runID})
}

func errString(err error) string {
	if err == nil {
		return "run failed"
	}
	return err.Error()
}

// ---- GET /v1/runs/... ----

// RunStatus is the body of GET /v1/runs/{id}.
type RunStatus struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"` // running, done, cancelled, unknown
	Manifest *Manifest `json:"manifest,omitempty"`
	Summary  *Summary  `json:"summary,omitempty"`
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id := parts[0]
	// Every path element that reaches the filesystem is matched against a strict
	// pattern first: no separators, no dots-only names, so "../" cannot appear.
	if !runIDRe.MatchString(id) {
		httpError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	if len(parts) == 4 && (parts[1] != "episodes" || !ValidTaskID(parts[2]) || !sampleRe.MatchString(parts[3])) {
		httpError(w, http.StatusBadRequest, "want /v1/runs/{id}/episodes/{task}/{sample}")
		return
	}
	dir := filepath.Join(s.o.Root, id)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		httpError(w, http.StatusNotFound, "no such run")
		return
	}
	switch len(parts) {
	case 1:
		s.writeRunStatus(w, id, dir)
	case 4:
		raw, err := os.ReadFile(filepath.Join(dir, parts[2], parts[3], "episode.json"))
		if err != nil {
			httpError(w, http.StatusNotFound, "no such episode")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) writeRunStatus(w http.ResponseWriter, id, dir string) {
	st := RunStatus{ID: id, Status: "unknown"}
	if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		var m Manifest
		if json.Unmarshal(b, &m) == nil {
			st.Manifest, st.Status = &m, m.Status
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "summary.json")); err == nil {
		var sum Summary
		if json.Unmarshal(b, &sum) == nil {
			st.Summary = &sum
		}
	}
	s.mu.Lock()
	_, running := s.active[id]
	s.mu.Unlock()
	if running {
		st.Status = "running"
	} else if st.Status == "running" {
		// The manifest says running but nothing is: the server restarted mid-run.
		st.Status = "interrupted"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}
