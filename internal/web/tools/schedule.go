package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The schedule: the jobs of <state>/schedule.json, the daemon that starts the due ones, run
// now, logs. The job file is package sched's, and so are its locks: the write lock keeps this page, a terminal's `sleipnir schedule`
// and a daemon from losing each other's change, and the daemon lock keeps two daemons (this page's and a terminal's) from starting a
// job twice. A daemon started here runs in this process and stops with the server.

// jobIDRE is the shape of a job id.
var jobIDRE = regexp.MustCompile(`^j\d{1,6}$`)

// isoLocal is how times of the schedule are sent: local time, to the second, no zone (the page shows it as it is).
const isoLocal = "2006-01-02T15:04:05"

// Bounds of a job's fields.
const (
	maxCron  = 100
	maxGoal  = 4000
	maxModel = 200
)

// schedModes are the permission modes a job may be given here (bypass and yolo are refused: nobody is there to see what they do).
var schedModes = map[string]bool{"": true, "default": true, "accept-edits": true, "plan": true}

// registerSchedule adds the routes of the schedule: jobs, run now, logs and the daemon.
func (s *service) registerSchedule() {
	s.srv.HandleFunc("GET /api/schedule", s.handleSchedule, web.RouteOpts{})
	s.srv.HandleFunc("GET /api/schedule/next", s.handleNext, web.RouteOpts{})
	s.srv.HandleFunc("POST /api/schedule/jobs", s.handleAddJob, web.RouteOpts{NeedsConfirm: true, ConfirmScope: "job.add"})
	s.srv.HandleFunc("PUT /api/schedule/jobs/{job}", s.handleEditJob, web.RouteOpts{})
	s.srv.HandleFunc("POST /api/schedule/jobs/{job}/pause", s.handlePause, web.RouteOpts{})
	s.srv.HandleFunc("DELETE /api/schedule/jobs/{job}", s.handleRemoveJob, web.RouteOpts{NoBody: true})
	s.srv.HandleFunc("POST /api/schedule/jobs/{job}/run", s.handleRunJob, web.RouteOpts{NoBody: true})
	s.srv.HandleFunc("GET /api/schedule/jobs/{job}/log", s.handleJobLog, web.RouteOpts{})
	s.srv.HandleFunc("POST /api/schedule/daemon", s.handleDaemon, web.RouteOpts{})
}

// iso formats a time of the schedule ("" for none).
func iso(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(isoLocal)
}

// shortHome writes a path under the home directory with ~.
func (s *service) shortHome(p string) string {
	home := s.o.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}

// jobOf is a job as the page shows it.
func (s *service) jobOf(j sched.Job, now time.Time) wire.ScheduleJob {
	out := wire.ScheduleJob{
		ID: j.ID, Cron: j.Cron, Goal: runner.Clean(j.Goal), Dir: j.Dir, Model: j.Model, Mode: j.Mode, BudgetUSD: j.BudgetUSD,
		Created: iso(j.Created), LastRun: iso(j.LastRun), LastExit: runner.Clean(j.LastExit), Paused: j.Paused,
	}
	if j.Log != "" {
		out.Log = s.shortHome(j.Log)
	}
	if n, ok := sched.Next(j, now); ok {
		out.Next = iso(n)
	}
	return out
}

// handleSchedule is GET /api/schedule: the jobs, the daemon and the last logs of each job.
func (s *service) handleSchedule(w http.ResponseWriter, r *http.Request) {
	st := s.store()
	jobs, err := st.List()
	if err != nil {
		web.Logf(r, "reading the schedule: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the schedule file could not be read: fix or delete it")
		return
	}
	now := s.o.Now()
	view := wire.ScheduleView{Jobs: []wire.ScheduleJob{}, Daemon: s.daemonState(), Logs: []wire.JobLog{}}
	for _, j := range jobs {
		view.Jobs = append(view.Jobs, s.jobOf(j, now))
	}
	for i, j := range jobs {
		if i >= 64 {
			break
		}
		for _, f := range s.jobLogs(j.ID, 2) {
			view.Logs = append(view.Logs, s.readLog(j, f, 16<<10))
		}
	}
	_ = web.WriteJSON(w, http.StatusOK, view)
}

// jobLogs lists the newest n log files of a job, newest first.
func (s *service) jobLogs(id string, n int) []string {
	files, _ := filepath.Glob(filepath.Join(s.store().LogDir(), id+"-*.log"))
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // the names end in the start time
	if len(files) > n {
		files = files[:n]
	}
	return files
}

// readLog reads the end of a log file (at most limit bytes), made safe to show; the exit is the job's when the log is its last.
func (s *service) readLog(j sched.Job, file string, limit int64) wire.JobLog {
	out := wire.JobLog{Job: j.ID, File: s.shortHome(file)}
	if file == j.Log {
		out.Exit = runner.Clean(j.LastExit)
	}
	f, err := os.Open(file)
	if err != nil {
		return out
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > limit {
		_, _ = f.Seek(fi.Size()-limit, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	text := strings.ToValidUTF8(string(b), "")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = runner.Clean(l)
	}
	out.Text = strings.Join(lines, "\n")
	return out
}

// handleNext is GET /api/schedule/next?cron=: whether an expression is valid, and when it runs next.
func (s *service) handleNext(w http.ResponseWriter, r *http.Request) {
	expr := r.URL.Query().Get("cron")
	if len(expr) > maxCron {
		_ = web.WriteJSON(w, http.StatusOK, wire.CronCheck{Err: "the cron expression is too long"})
		return
	}
	c, err := sched.ParseCron(expr)
	if err != nil {
		_ = web.WriteJSON(w, http.StatusOK, wire.CronCheck{Err: "the cron expression is not valid: " + runner.Clean(strings.TrimPrefix(err.Error(), "cron "))})
		return
	}
	n, ok := c.Next(s.o.Now())
	if !ok {
		_ = web.WriteJSON(w, http.StatusOK, wire.CronCheck{Err: "the expression names no time in the next five years"})
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, wire.CronCheck{OK: true, Next: iso(n)})
}

// allowedDir reports whether a job may run in dir: a project of the page (the host's list), a live tab's directory, or the server's.
func (s *service) allowedDir(ctx context.Context, dir string) bool {
	if dir == s.o.Cwd {
		return true
	}
	if s.host == nil {
		return false
	}
	for _, p := range s.host.Projects(ctx) {
		if p.Dir == dir || p.Root == dir {
			return true
		}
	}
	for _, t := range s.host.Tabs() {
		if t.Cwd == dir {
			return true
		}
	}
	return false
}

// checkJob validates a job request and returns the job it describes (prev is the job being edited, or nil).
func (s *service) checkJob(ctx context.Context, req wire.JobRequest, prev *sched.Job) (sched.Job, error) {
	j := sched.Job{Cron: strings.TrimSpace(req.Cron), Goal: strings.TrimSpace(req.Goal), Dir: strings.TrimSpace(req.Dir), Model: strings.TrimSpace(req.Model), Mode: strings.TrimSpace(req.Mode), BudgetUSD: req.BudgetUSD}
	if len(j.Cron) > maxCron {
		return j, werr(http.StatusBadRequest, "bad_cron", "the cron expression is not valid")
	}
	if _, err := sched.ParseCron(j.Cron); err != nil {
		return j, werr(http.StatusBadRequest, "bad_cron", "the cron expression is not valid")
	}
	if j.Goal == "" {
		return j, werr(http.StatusBadRequest, "empty", "a goal is required")
	}
	if utf8.RuneCountInString(j.Goal) > maxGoal || strings.ContainsRune(j.Goal, 0) {
		return j, werr(http.StatusBadRequest, "empty", fmt.Sprintf("the goal is longer than %d characters", maxGoal))
	}
	if math.IsNaN(j.BudgetUSD) || math.IsInf(j.BudgetUSD, 0) || j.BudgetUSD < 0 || j.BudgetUSD > 100000 {
		return j, werr(http.StatusBadRequest, "bad_budget", "the budget is a number of dollars, 0 for no limit")
	}
	switch {
	case j.Mode == "bypass" || j.Mode == "yolo":
		return j, werr(http.StatusForbidden, "dangerous_mode", "bypass and yolo cannot be scheduled from here")
	case !schedModes[j.Mode]:
		return j, werr(http.StatusBadRequest, "bad_mode", "the mode is default, accept-edits or plan")
	}
	if len(j.Model) > maxModel || strings.HasPrefix(j.Model, "-") || strings.ContainsAny(j.Model, " \t\n\x00") {
		return j, werr(http.StatusBadRequest, "bad_flags", "the model is written provider/model")
	}
	if j.Dir == "" {
		j.Dir = s.o.Cwd
	}
	if !filepath.IsAbs(j.Dir) || !(s.allowedDir(ctx, j.Dir) || (prev != nil && prev.Dir == j.Dir)) {
		return j, werr(http.StatusForbidden, "not_a_project", "a job runs in one of the projects of this page")
	}
	return j, nil
}

// handleAddJob is POST /api/schedule/jobs (confirmed for job.add).
func (s *service) handleAddJob(w http.ResponseWriter, r *http.Request) {
	var req wire.JobRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	j, err := s.checkJob(r.Context(), req, nil)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	added, err := s.store().Add(j, s.o.Now())
	if err != nil {
		web.Logf(r, "adding a job: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the job could not be added")
		return
	}
	web.Logf(r, "job %s added", added.ID)
	_ = web.WriteJSON(w, http.StatusCreated, s.jobOf(added, s.o.Now()))
}

// jobParam resolves the {job} path parameter.
func jobParam(r *http.Request) (string, error) {
	id := r.PathValue("job")
	if !jobIDRE.MatchString(id) {
		return "", werr(http.StatusBadRequest, "bad_request", "that is not a job id")
	}
	return id, nil
}

// find returns a job of the file.
func (s *service) find(id string) (sched.Job, error) {
	jobs, err := s.store().List()
	if err != nil {
		return sched.Job{}, err
	}
	for _, j := range jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return sched.Job{}, werr(http.StatusNotFound, "not_found", "no such job")
}

// handleEditJob is PUT /api/schedule/jobs/{job} (confirmed for job.edit:<job>): the job's cron, goal, directory, model, mode and
// budget; its id, creation and run records stay.
func (s *service) handleEditJob(w http.ResponseWriter, r *http.Request) {
	id, err := jobParam(r)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	var req wire.JobRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	prev, err := s.find(id)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	j, err := s.checkJob(r.Context(), req, &prev)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	if !s.srv.RequireConfirm(w, r, "job.edit:"+id) {
		return
	}
	edited, err := s.store().Update(id, func(x *sched.Job) {
		x.Cron, x.Goal, x.Dir, x.Model, x.Mode, x.BudgetUSD = j.Cron, j.Goal, j.Dir, j.Model, j.Mode, j.BudgetUSD
	})
	if err != nil {
		s.jobError(w, r, err)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, s.jobOf(edited, s.o.Now()))
}

// jobError answers a failed change of a job.
func (s *service) jobError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sched.ErrNoJob) {
		web.Error(w, http.StatusNotFound, "not_found", "no such job")
		return
	}
	web.Logf(r, "changing a job: %v", err)
	web.Error(w, http.StatusInternalServerError, "internal", "the schedule file could not be written")
}

// handlePause is POST /api/schedule/jobs/{job}/pause {paused}: a paused job is listed and can be run by hand, and the daemon skips it.
func (s *service) handlePause(w http.ResponseWriter, r *http.Request) {
	id, err := jobParam(r)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	var body struct {
		Paused bool `json:"paused"`
	}
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	j, err := s.store().Update(id, func(x *sched.Job) { x.Paused = body.Paused })
	if err != nil {
		s.jobError(w, r, err)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, s.jobOf(j, s.o.Now()))
}

// handleRemoveJob is DELETE /api/schedule/jobs/{job}.
func (s *service) handleRemoveJob(w http.ResponseWriter, r *http.Request) {
	id, err := jobParam(r)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	if err := s.store().Remove(id); err != nil {
		if strings.HasPrefix(err.Error(), "no job") {
			web.Error(w, http.StatusNotFound, "not_found", "no such job")
			return
		}
		s.jobError(w, r, err)
		return
	}
	web.Logf(r, "job %s removed", id)
	ok(w)
}

// running reports whether a job runs now: run now from this page, the page's daemon, or another daemon that recorded its start.
func (s *service) running(j sched.Job) bool {
	s.mu.Lock()
	_, now := s.jobRuns[j.ID]
	busy := s.busy[j.ID]
	s.mu.Unlock()
	if now || busy {
		return true
	}
	if j.LastExit == "running" {
		_, held := sched.Probe(s.store().LockPath())
		return held && !s.daemonHere()
	}
	return false
}

// handleRunJob is POST /api/schedule/jobs/{job}/run: the job now, as the daemon would run it (a quiet headless run with the job's
// flags and the held keys), its output streamed as run frames and written to its log; the start and the end are recorded.
func (s *service) handleRunJob(w http.ResponseWriter, r *http.Request) {
	id, err := jobParam(r)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	j, err := s.find(id)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	if s.running(j) {
		web.Error(w, http.StatusConflict, "running", "this job is running now")
		return
	}
	if !schedModes[j.Mode] { // a bypass or yolo job made in a terminal runs from the daemon, not from a click
		web.Error(w, http.StatusForbidden, "dangerous_mode", "bypass and yolo jobs cannot be run from here: sleipnir daemon runs them")
		return
	}
	st := s.store()
	now := s.o.Now()
	logs := st.LogDir()
	if err := os.MkdirAll(logs, 0o700); err != nil {
		web.Logf(r, "the log directory: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the log directory could not be made")
		return
	}
	logPath := sched.LogFile(logs, j, now)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		web.Logf(r, "the log file: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the log file could not be made")
		return
	}
	s.mu.Lock()
	if _, dup := s.jobRuns[id]; dup {
		s.mu.Unlock()
		f.Close()
		web.Error(w, http.StatusConflict, "running", "this job is running now")
		return
	}
	s.jobRuns[id] = ""
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		delete(s.jobRuns, id)
		s.mu.Unlock()
	}
	prev := j // what the records were, should the run not start
	if err := sched.Record(st, id, func(x *sched.Job) { x.LastRun, x.LastExit, x.Log = now, "running", logPath }); err != nil {
		release()
		f.Close()
		web.Logf(r, "recording a run: %v", err)
		web.Error(w, http.StatusInternalServerError, "internal", "the schedule file could not be written")
		return
	}
	args := sched.Args(j)
	started, err := s.runs.Start(runner.Spec{
		Path: []string{"schedule", "run"}, Cmdline: runner.Cmdline(args), Args: args, Net: true, Tee: f, Timeout: sched.JobTimeout,
		End: func(res *wire.RunResult) *wire.DoctorVerdict {
			f.Close()
			exit := exitText(res)
			if err := sched.Record(st, id, func(x *sched.Job) { x.LastExit, x.Log = exit, logPath }); err != nil {
				s.srv.Logf("recording the end of job %s: %v", id, err)
			}
			release()
			res.Card = &wire.RunCard{Title: "job " + id, Rows: [][2]string{{"exit", exit}, {"log", s.shortHome(logPath)}}}
			return nil
		},
	})
	if err != nil {
		f.Close()
		release()
		_ = sched.Record(st, id, func(x *sched.Job) { x.LastRun, x.LastExit, x.Log = prev.LastRun, prev.LastExit, prev.Log })
		_ = os.Remove(logPath)
		web.WriteError(w, err)
		return
	}
	s.mu.Lock()
	if _, still := s.jobRuns[id]; still {
		s.jobRuns[id] = started.ID
	}
	s.mu.Unlock()
	web.Logf(r, "job %s run now as %s", id, started.ID)
	_ = web.WriteJSON(w, http.StatusAccepted, started)
}

// exitText says how a run of a job ended, as the daemon records it.
func exitText(res *wire.RunResult) string {
	switch {
	case res.Canceled:
		return "interrupted"
	case res.Exit == 0:
		return "ok"
	case res.Ms >= sched.JobTimeout.Milliseconds():
		return "timed out after " + sched.JobTimeout.String()
	case res.Exit < 0:
		return "it could not start"
	}
	return fmt.Sprintf("exit status %d", res.Exit)
}

// handleJobLog is GET /api/schedule/jobs/{job}/log: the job's newest log (its end, 256 KiB at most).
func (s *service) handleJobLog(w http.ResponseWriter, r *http.Request) {
	id, err := jobParam(r)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	j, err := s.find(id)
	if err != nil {
		web.WriteError(w, err)
		return
	}
	files := s.jobLogs(id, 1)
	if len(files) == 0 {
		_ = web.WriteJSON(w, http.StatusOK, wire.JobLog{Job: id})
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, s.readLog(j, files[0], 256<<10))
}
