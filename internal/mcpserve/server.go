package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProtocolVersions are the MCP revisions the server speaks, newest first. A
// client that asks for one of them gets it; any other request gets the newest.
var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Modes orders the permission modes from the strictest to the most permissive.
var Modes = []string{"plan", "default", "accept-edits", "bypass", "yolo"}

// Defaults of a Config.
const (
	DefaultMaxJobs  = 4
	DefaultMode     = "bypass" // what an unattended `sleipnir run` uses when nothing else is configured
	maxKeptJobs     = 64       // finished jobs beyond this are forgotten, oldest first
	maxGoalBytes    = 1 << 20
	defaultEvents   = 20
	maxEvents       = 200
	defaultWait     = 300 * time.Second
	maxWait         = 1800 * time.Second
	minSwarmWorkers = 8
)

// Config is what the person decided when they configured the server. Nothing a
// caller sends can widen it.
type Config struct {
	// Exe is the Sleipnir binary jobs run (normally os.Executable()).
	Exe string
	// Roots are the directories a job's cwd must be inside (at least one).
	Roots []string
	// Dir is the directory a job runs in when the call names none, and the base
	// of a relative cwd. Empty: a call must name an absolute cwd.
	Dir string
	// Mode is the most permissive mode a job may run in (default DefaultMode).
	Mode string
	// Allow rules are passed to every job.
	Allow []string
	// TrustProject passes --trust-project to every job.
	TrustProject bool
	// MaxJobs bounds the jobs that run at once (default DefaultMaxJobs).
	MaxJobs int
	// Version is reported to the client.
	Version string
	// Env is the jobs' environment (nil: the server's own).
	Env []string
	// Prepare is called with each job's command before it starts and returns
	// the function to call with Start's error (sched.PassKeys).
	Prepare func(*exec.Cmd) func(error)
}

// Server is one MCP connection's state.
type Server struct {
	cfg Config

	outMu sync.Mutex
	out   *json.Encoder

	mu    sync.Mutex
	jobs  map[string]*Job
	order []string // job ids, oldest first
	next  int
}

// New checks cfg and returns a server for it.
func New(cfg Config) (*Server, error) {
	if cfg.Exe == "" {
		return nil, errors.New("mcp serve: no Sleipnir binary to run")
	}
	if cfg.Mode == "" {
		cfg.Mode = DefaultMode
	}
	if !slices.Contains(Modes, cfg.Mode) {
		return nil, fmt.Errorf("mcp serve: unknown mode %q (one of %s)", cfg.Mode, strings.Join(Modes, ", "))
	}
	if cfg.MaxJobs <= 0 {
		cfg.MaxJobs = DefaultMaxJobs
	}
	if len(cfg.Roots) == 0 {
		return nil, errors.New("mcp serve: no root directory for runs")
	}
	roots := make([]string, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		real, err := realDir(r)
		if err != nil {
			return nil, fmt.Errorf("mcp serve: root %s: %v", r, err)
		}
		if !slices.Contains(roots, real) {
			roots = append(roots, real)
		}
	}
	cfg.Roots = roots
	if cfg.Dir != "" {
		real, err := realDir(cfg.Dir)
		if err != nil {
			return nil, fmt.Errorf("mcp serve: directory %s: %v", cfg.Dir, err)
		}
		if !Inside(roots, real) {
			return nil, fmt.Errorf("mcp serve: directory %s is outside the roots %s", cfg.Dir, strings.Join(roots, ", "))
		}
		cfg.Dir = real
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	return &Server{cfg: cfg, jobs: map[string]*Job{}}, nil
}

// Serve answers requests read from in on out until in ends or ctx is done, then
// cancels the jobs that still run and waits for them to end.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = json.NewEncoder(out)
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		br := bufio.NewReaderSize(in, 64<<10)
		for {
			line, err := readLine(br, maxGoalBytes+64<<10)
			if len(line) > 0 {
				lines <- line
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				readErr <- err
				return
			}
		}
	}()
	var wg sync.WaitGroup
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err = <-readErr:
			break loop
		case line := <-lines:
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.handle(ctx, line)
			}()
		}
	}
	s.shutdown()
	wg.Wait()
	return err
}

// shutdown cancels every running job and waits until they end.
func (s *Server) shutdown() {
	s.mu.Lock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	for _, j := range jobs {
		if j.cancel() {
			<-j.done
		}
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handle answers one message. Notifications and responses get no answer.
func (s *Server) handle(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(json.RawMessage("null"), nil, &rpcError{-32700, "parse error"})
		return
	}
	id := strings.TrimSpace(string(req.ID))
	if req.Method == "" || id == "" || id == "null" {
		return // a notification (initialized, cancelled) or a response to nothing we asked
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := ProtocolVersions[0]
		if slices.Contains(ProtocolVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		s.reply(req.ID, map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "sleipnir", "version": s.cfg.Version},
			"instructions":    instructions,
		}, nil)
	case "ping":
		s.reply(req.ID, map[string]any{}, nil)
	case "tools/list":
		s.reply(req.ID, map[string]any{"tools": toolSpecs()}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.reply(req.ID, nil, &rpcError{-32602, "invalid params"})
			return
		}
		res, err := s.call(ctx, p.Name, p.Arguments)
		if errors.Is(err, errUnknownTool) {
			s.reply(req.ID, nil, &rpcError{-32602, err.Error()})
			return
		}
		s.reply(req.ID, toolResult(res, err), nil)
	default:
		s.reply(req.ID, nil, &rpcError{-32601, "method not found: " + req.Method})
	}
}

// reply writes one response.
func (s *Server) reply(id json.RawMessage, result any, e *rpcError) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		msg["error"] = e
	} else {
		msg["result"] = result
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_ = s.out.Encode(msg)
}

// toolResult is the tools/call result for a tool's value or error: the value
// as indented JSON text, and also as structured content.
func toolResult(v any, err error) map[string]any {
	if err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(b)}},
		"structuredContent": v,
	}
}

var errUnknownTool = errors.New("unknown tool")

// call runs one tool.
func (s *Server) call(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	switch name {
	case "sleipnir_start":
		var a StartArgs
		if err := strictDecode(raw, &a); err != nil {
			return nil, err
		}
		return s.start(a)
	case "sleipnir_status":
		var a statusArgs
		if err := strictDecode(raw, &a); err != nil {
			return nil, err
		}
		j, err := s.job(a.Job)
		if err != nil {
			return nil, err
		}
		return j.status(a.After, eventLimit(a.Events)), nil
	case "sleipnir_wait":
		var a waitArgs
		if err := strictDecode(raw, &a); err != nil {
			return nil, err
		}
		j, err := s.job(a.Job)
		if err != nil {
			return nil, err
		}
		d := defaultWait
		if a.TimeoutSeconds > 0 {
			d = min(time.Duration(a.TimeoutSeconds)*time.Second, maxWait)
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-j.done:
		case <-t.C:
		case <-ctx.Done():
		}
		return j.status(a.After, eventLimit(a.Events)), nil
	case "sleipnir_cancel":
		var a jobArgs
		if err := strictDecode(raw, &a); err != nil {
			return nil, err
		}
		j, err := s.job(a.Job)
		if err != nil {
			return nil, err
		}
		if !j.cancel() {
			return nil, fmt.Errorf("job %s is not running (%s)", j.ID, j.status(0, 0).State)
		}
		return map[string]any{"job": j.ID, "state": "cancelling", "note": "the run is ending as Ctrl-C ends it; work a team verified is applied first. sleipnir_wait reports when it has ended."}, nil
	case "sleipnir_jobs":
		if err := strictDecode(raw, &struct{}{}); err != nil {
			return nil, err
		}
		return s.list(), nil
	}
	return nil, fmt.Errorf("%w %q", errUnknownTool, name)
}

// strictDecode decodes a tool's arguments, refusing unknown fields so that a
// misspelt option is an error the caller sees, not a silent default.
func strictDecode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// eventLimit bounds a requested number of activity lines.
func eventLimit(n int) int {
	switch {
	case n <= 0:
		return defaultEvents
	case n > maxEvents:
		return maxEvents
	}
	return n
}

// StartArgs are sleipnir_start's arguments.
type StartArgs struct {
	Goal      string   `json:"goal"`
	Cwd       string   `json:"cwd,omitempty"`
	Create    bool     `json:"create,omitempty"`
	Model     string   `json:"model,omitempty"`
	Workers   int      `json:"workers,omitempty"`
	Team      string   `json:"team,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	Verify    string   `json:"verify,omitempty"`
	Isolation string   `json:"isolation,omitempty"`
	BudgetUSD float64  `json:"budget_usd,omitempty"`
	Resume    string   `json:"resume,omitempty"`
	RoleModel []string `json:"role_models,omitempty"`
}

type jobArgs struct {
	Job string `json:"job"`
}

type statusArgs struct {
	Job    string `json:"job"`
	After  int    `json:"after,omitempty"`
	Events int    `json:"events,omitempty"`
}

type waitArgs struct {
	Job            string `json:"job"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	After          int    `json:"after,omitempty"`
	Events         int    `json:"events,omitempty"`
}

// Args turns a start request into the child's arguments under cfg, and returns
// the job's directory. It refuses what cfg does not allow.
func Args(cfg Config, a StartArgs) (args []string, dir string, err error) {
	if strings.TrimSpace(a.Goal) == "" {
		return nil, "", errors.New("goal is empty")
	}
	if len(a.Goal) > maxGoalBytes {
		return nil, "", fmt.Errorf("goal is over %d bytes", maxGoalBytes)
	}
	dir, err = resolveDir(cfg, a.Cwd, a.Create)
	if err != nil {
		return nil, "", err
	}
	mode := cfg.Mode
	if a.Mode != "" {
		want, ceiling := slices.Index(Modes, a.Mode), slices.Index(Modes, cfg.Mode)
		switch {
		case want < 0:
			return nil, "", fmt.Errorf("unknown mode %q (one of %s)", a.Mode, strings.Join(Modes, ", "))
		case want > ceiling:
			return nil, "", fmt.Errorf("mode %q is more permissive than this server allows (%s); the person sets that with `sleipnir mcp serve --mode`", a.Mode, cfg.Mode)
		}
		mode = a.Mode
	}
	for _, v := range []struct{ name, value string }{{"model", a.Model}, {"team", a.Team}, {"verify", a.Verify}, {"isolation", a.Isolation}, {"resume", a.Resume}} {
		if strings.ContainsAny(v.value, "\x00\n\r") {
			return nil, "", fmt.Errorf("%s holds a control character", v.name)
		}
	}
	if a.Isolation != "" && a.Isolation != "none" && a.Isolation != "worktree" {
		return nil, "", fmt.Errorf("isolation is none or worktree, not %q", a.Isolation)
	}
	if a.Workers != 0 && a.Workers < minSwarmWorkers {
		return nil, "", fmt.Errorf("workers is 0 (one agent) or %d or more (a manager and one worker of each role)", minSwarmWorkers)
	}
	if a.BudgetUSD < 0 {
		return nil, "", errors.New("budget_usd is negative")
	}
	args = []string{"run", "--json", "--cwd", dir, "--mode", mode}
	for _, r := range cfg.Allow {
		args = append(args, "--allow", r)
	}
	if cfg.TrustProject {
		args = append(args, "--trust-project")
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if a.Workers > 0 {
		args = append(args, "--swarm", strconv.Itoa(a.Workers))
	}
	if a.Team != "" {
		args = append(args, "--team", a.Team)
	}
	for _, rm := range a.RoleModel {
		if rm == "" || strings.ContainsAny(rm, "\x00\n\r") {
			return nil, "", errors.New("role_models holds an empty value or a control character")
		}
		args = append(args, "--role-model", rm)
	}
	if a.Verify != "" {
		args = append(args, "--verify", a.Verify)
	}
	if a.Isolation != "" {
		args = append(args, "--isolation", a.Isolation)
	}
	if a.BudgetUSD > 0 {
		args = append(args, "--budget-usd", strconv.FormatFloat(a.BudgetUSD, 'f', -1, 64))
	}
	if a.Resume != "" {
		args = append(args, "--resume", a.Resume)
	}
	return append(args, "-"), dir, nil
}

// realDir is dir made absolute with its symlinks resolved; it must be a directory.
func realDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(real); err != nil {
		return "", err
	} else if !fi.IsDir() {
		return "", errors.New("not a directory")
	}
	return real, nil
}

// Inside reports whether dir (absolute, symlinks resolved) is one of roots or below one.
func Inside(roots []string, dir string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, dir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// resolveDir turns a call's cwd into a job directory inside cfg's roots. A
// leading ~ is the home directory, a relative cwd is relative to cfg.Dir, and
// with create a missing directory is made (with its missing parents), but only
// when the part of the path that exists is inside a root.
func resolveDir(cfg Config, cwd string, create bool) (string, error) {
	roots := strings.Join(cfg.Roots, ", ")
	if cwd == "~" || strings.HasPrefix(cwd, "~/") || strings.HasPrefix(cwd, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cwd %s: no home directory: %v", cwd, err)
		}
		cwd = filepath.Join(home, cwd[1:])
	}
	var dir string
	switch {
	case cwd == "" && cfg.Dir == "":
		return "", fmt.Errorf("cwd is required: pass the absolute path of the project's folder (this server allows folders inside %s; set create to make a new one)", roots)
	case cwd == "":
		dir = cfg.Dir
	case filepath.IsAbs(cwd):
		dir = filepath.Clean(cwd)
	case cfg.Dir == "":
		return "", fmt.Errorf("cwd %s is relative and this server has no default directory: pass an absolute path inside %s", cwd, roots)
	default:
		dir = filepath.Join(cfg.Dir, cwd)
	}
	// The longest part of dir that exists decides where it is, with its
	// symlinks resolved; what is missing below it can hold no symlink yet.
	existing, missing := dir, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		missing = filepath.Join(filepath.Base(existing), missing)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("cwd: %v", err)
	}
	target := filepath.Join(real, missing)
	if !Inside(cfg.Roots, target) {
		return "", fmt.Errorf("cwd %s is outside this server's roots %s (the person widens them with `sleipnir mcp serve --root DIR`)", cwd, roots)
	}
	if missing != "" {
		if !create {
			return "", fmt.Errorf("cwd %s does not exist: set create to true to make it", cwd)
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return "", fmt.Errorf("cwd: %v", err)
		}
		// A symlink raced in while the directory was made would show here.
		if real, err := filepath.EvalSymlinks(target); err != nil || !Inside(cfg.Roots, real) {
			return "", fmt.Errorf("cwd %s did not stay inside this server's roots %s", cwd, roots)
		}
	}
	if fi, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("cwd: %v", err)
	} else if !fi.IsDir() {
		return "", fmt.Errorf("cwd %s is not a directory", cwd)
	}
	return target, nil
}

// start launches a job.
func (s *Server) start(a StartArgs) (any, error) {
	args, dir, err := Args(s.cfg, a)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	running := 0
	for _, j := range s.jobs {
		if j.running() {
			running++
		}
	}
	if running >= s.cfg.MaxJobs {
		return nil, fmt.Errorf("%d jobs are running, the most this server runs at once; wait for one or cancel one", running)
	}
	s.next++
	j := &Job{ID: "j" + strconv.Itoa(s.next), Goal: a.Goal, Dir: dir, Args: args, StartedAt: time.Now()}
	env := s.cfg.Env
	if env == nil {
		env = environ()
	}
	if err := j.start(s.cfg.Exe, env, s.cfg.Prepare); err != nil {
		return nil, fmt.Errorf("could not start sleipnir: %v", err)
	}
	s.jobs[j.ID] = j
	s.order = append(s.order, j.ID)
	s.forget()
	what := "one agent"
	if a.Workers > 0 {
		what = fmt.Sprintf("a manager and %d workers", a.Workers)
	} else if a.Team != "" {
		what = "the saved team " + a.Team
	}
	return map[string]any{
		"job": j.ID, "state": StateRunning, "dir": dir, "runs": what,
		"next": "sleipnir_wait (blocks up to timeout_seconds) or sleipnir_status; pass after=last_seq to see only new activity",
	}, nil
}

// forget drops the oldest finished jobs beyond maxKeptJobs. s.mu is held.
func (s *Server) forget() {
	for len(s.order) > maxKeptJobs {
		dropped := false
		for i, id := range s.order {
			if !s.jobs[id].running() {
				delete(s.jobs, id)
				s.order = append(s.order[:i], s.order[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			return
		}
	}
}

// job finds a job by id.
func (s *Server) job(id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		return j, nil
	}
	ids := slices.Clone(s.order)
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("no job %q: this server has started none", id)
	}
	return nil, fmt.Errorf("no job %q (jobs: %s)", id, strings.Join(ids, ", "))
}

// jobLine is one row of sleipnir_jobs.
type jobLine struct {
	Job        string `json:"job"`
	State      string `json:"state"`
	Goal       string `json:"goal"`
	ElapsedSec int64  `json:"elapsed_s"`
}

// list is every job, oldest first.
func (s *Server) list() map[string]any {
	s.mu.Lock()
	ids := slices.Clone(s.order)
	jobs := make([]*Job, 0, len(ids))
	for _, id := range ids {
		jobs = append(jobs, s.jobs[id])
	}
	s.mu.Unlock()
	rows := make([]jobLine, 0, len(jobs))
	for _, j := range jobs {
		st := j.status(0, 0)
		rows = append(rows, jobLine{Job: st.Job, State: st.State, Goal: clip(st.Goal, 120), ElapsedSec: st.ElapsedSec})
	}
	return map[string]any{"jobs": rows, "max_running": s.cfg.MaxJobs, "mode_ceiling": s.cfg.Mode, "roots": s.cfg.Roots, "default_dir": s.cfg.Dir}
}
