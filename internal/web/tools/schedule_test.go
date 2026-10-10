package tools

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// addJob adds a job through the route, confirmed.
func (rg *rig) addJob(body wire.JobRequest) wire.ScheduleJob {
	rg.t.Helper()
	w := rg.do(req{method: "POST", path: "/api/schedule/jobs", body: body, header: map[string]string{"X-Confirm": rg.confirm("job.add")}})
	if w.Code != http.StatusCreated {
		rg.t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	return decode[wire.ScheduleJob](rg.t, w)
}

func TestScheduleAddEditPauseRemove(t *testing.T) {
	rg := newRig(t, nil)
	good := wire.JobRequest{Cron: "0 9 * * 1-5", Goal: "summarise the open issues", Dir: rg.project, Mode: "plan", BudgetUSD: 1, Model: "openai/gpt-x"}
	if w := rg.do(req{method: "POST", path: "/api/schedule/jobs", body: good}); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("add without a confirmation: %d", w.Code)
	}
	for name, tc := range map[string]struct {
		mut  func(*wire.JobRequest)
		st   int
		code string
	}{
		"bad cron":   {func(j *wire.JobRequest) { j.Cron = "every day" }, 400, "bad_cron"},
		"no goal":    {func(j *wire.JobRequest) { j.Goal = "  " }, 400, "empty"},
		"budget":     {func(j *wire.JobRequest) { j.BudgetUSD = -1 }, 400, "bad_budget"},
		"yolo":       {func(j *wire.JobRequest) { j.Mode = "yolo" }, 403, "dangerous_mode"},
		"bypass":     {func(j *wire.JobRequest) { j.Mode = "bypass" }, 403, "dangerous_mode"},
		"odd mode":   {func(j *wire.JobRequest) { j.Mode = "chaos" }, 400, "bad_mode"},
		"elsewhere":  {func(j *wire.JobRequest) { j.Dir = "/etc" }, 403, "not_a_project"},
		"relative":   {func(j *wire.JobRequest) { j.Dir = "proj" }, 403, "not_a_project"},
		"flag model": {func(j *wire.JobRequest) { j.Model = "--yolo" }, 400, "bad_flags"},
	} {
		body := good
		tc.mut(&body)
		w := rg.do(req{method: "POST", path: "/api/schedule/jobs", body: body, header: map[string]string{"X-Confirm": rg.confirm("job.add")}})
		if w.Code != tc.st || errCode(w) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, w.Code, w.Body.String(), tc.st, tc.code)
		}
	}
	j := rg.addJob(good)
	if j.ID != "j1" || j.Next == "" || j.Mode != "plan" || j.Model != "openai/gpt-x" || j.Created == "" {
		t.Errorf("added %+v", j)
	}
	edit := good
	edit.Goal, edit.Cron = "summarise the closed issues", "@daily"
	if w := rg.do(req{method: "PUT", path: "/api/schedule/jobs/j1", body: edit}); w.Code != http.StatusPreconditionRequired {
		t.Errorf("edit without a confirmation: %d", w.Code)
	}
	if w := rg.do(req{method: "PUT", path: "/api/schedule/jobs/j1", body: edit, header: map[string]string{"X-Confirm": rg.confirm("job.add")}}); w.Code != http.StatusForbidden {
		t.Errorf("edit with another scope's confirmation: %d", w.Code)
	}
	w := rg.do(req{method: "PUT", path: "/api/schedule/jobs/j1", body: edit, header: map[string]string{"X-Confirm": rg.confirm("job.edit:j1")}})
	if e := decode[wire.ScheduleJob](t, w); w.Code != 200 || e.Goal != edit.Goal || e.Cron != "@daily" || e.ID != "j1" {
		t.Errorf("edit: %d %s", w.Code, w.Body.String())
	}
	if w := rg.do(req{method: "PUT", path: "/api/schedule/jobs/j9", body: edit, header: map[string]string{"X-Confirm": rg.confirm("job.edit:j9")}}); w.Code != 404 {
		t.Errorf("edit a job that is not there: %d", w.Code)
	}
	w = rg.do(req{method: "POST", path: "/api/schedule/jobs/j1/pause", body: map[string]bool{"paused": true}})
	if p := decode[wire.ScheduleJob](t, w); !p.Paused || p.Next != "" {
		t.Errorf("paused: %s", w.Body.String())
	}
	view := decode[wire.ScheduleView](t, rg.do(req{method: "GET", path: "/api/schedule"}))
	if len(view.Jobs) != 1 || !view.Jobs[0].Paused || view.Daemon.Owner != "none" || view.Daemon.Running {
		t.Errorf("view %+v", view)
	}
	jobs, _ := sched.Store{Path: filepath.Join(rg.state, "schedule.json")}.List()
	if len(jobs) != 1 || !jobs[0].Paused || jobs[0].Goal != edit.Goal {
		t.Errorf("the file: %+v", jobs)
	}
	for path, st := range map[string]int{"/api/schedule/jobs/x1": 400, "/api/schedule/jobs/j1": 200, "/api/schedule/jobs/j1 ": 400} {
		if strings.HasSuffix(path, " ") {
			continue
		}
		if w := rg.do(req{method: "DELETE", path: path}); w.Code != st {
			t.Errorf("DELETE %s: %d", path, w.Code)
		}
	}
	if w := rg.do(req{method: "DELETE", path: "/api/schedule/jobs/j1"}); w.Code != 404 {
		t.Errorf("removed twice: %d", w.Code)
	}
}

func TestCronCheck(t *testing.T) {
	rg := newRig(t, nil)
	if c := decode[wire.CronCheck](t, rg.do(req{method: "GET", path: "/api/schedule/next?cron=0+9+*+*+1-5"})); !c.OK || len(c.Next) != len("2006-01-02T15:04:05") {
		t.Errorf("valid: %+v", c)
	}
	if c := decode[wire.CronCheck](t, rg.do(req{method: "GET", path: "/api/schedule/next?cron=61+*+*+*+*"})); c.OK || c.Err == "" {
		t.Errorf("invalid: %+v", c)
	}
	if c := decode[wire.CronCheck](t, rg.do(req{method: "GET", path: "/api/schedule/next?cron=" + strings.Repeat("1", 200)})); c.OK {
		t.Errorf("too long: %+v", c)
	}
}

// Run now runs the job as the daemon would (its flags, the held keys, its log), records how it ended, and refuses a second start.
func TestRunNowStreamsAndRecords(t *testing.T) {
	const name, key = "TOOLSTEST_API_KEY", "sk-test-tools-0123456789abcdefghijk"
	t.Setenv(name, "")
	harden.Provide(name, key)
	t.Cleanup(func() { harden.Provide(name, "") })
	rg := newRig(t, nil, "TOOLS_FAKE_SLEEP=1500ms")
	rg.addJob(wire.JobRequest{Cron: "@daily", Goal: "check CI", Dir: rg.project, BudgetUSD: 2})
	w := rg.do(req{method: "POST", path: "/api/schedule/jobs/j1/run"})
	started := decode[wire.RunStarted](t, w)
	if w.Code != http.StatusAccepted || !strings.Contains(started.Cmdline, "run --quiet --cwd") {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	if w := rg.do(req{method: "POST", path: "/api/schedule/jobs/j1/run"}); w.Code != http.StatusConflict || errCode(w) != "running" {
		t.Errorf("a second start: %d %s", w.Code, w.Body.String())
	}
	if j := decode[wire.ScheduleView](t, rg.do(req{method: "GET", path: "/api/schedule"})).Jobs[0]; j.LastExit != "running" || j.LastRun == "" {
		t.Errorf("the start is recorded before the run: %+v", j)
	}
	frames := rg.waitRun(started.ID)
	var text strings.Builder
	var end *wire.RunResult
	for _, f := range frames {
		for _, l := range f.Lines {
			text.WriteString(l.T + "\n")
		}
		if f.Result != nil {
			end = f.Result
		}
	}
	if end == nil || end.Exit != 0 || !strings.Contains(text.String(), "argv: run|--quiet|--cwd|"+rg.project+"|--budget-usd|2|--|check CI") {
		t.Fatalf("output %q end %+v", text.String(), end)
	}
	if !strings.Contains(text.String(), "key seen: [redacted]") || strings.Contains(text.String(), key) {
		t.Errorf("the job gets the key, the page does not: %q", text.String())
	}
	jobs, _ := sched.Store{Path: filepath.Join(rg.state, "schedule.json")}.List()
	if jobs[0].LastExit != "ok" || jobs[0].Log == "" {
		t.Errorf("recorded %+v", jobs[0])
	}
	b, err := os.ReadFile(jobs[0].Log)
	if err != nil || !strings.Contains(string(b), "argv: run|--quiet") {
		t.Errorf("the log %q %v", b, err)
	}
	if !strings.Contains(string(b), key) {
		t.Error("the job's own log file is its raw output (as the daemon's): the masking is for the page")
	}
	lg := decode[wire.JobLog](t, rg.do(req{method: "GET", path: "/api/schedule/jobs/j1/log"}))
	if lg.Job != "j1" || lg.Exit != "ok" || !strings.Contains(lg.Text, "argv: run") || strings.Contains(lg.Text, key) || !strings.HasPrefix(lg.File, "~/") {
		t.Errorf("log route %+v", lg)
	}
	if w := rg.do(req{method: "POST", path: "/api/schedule/jobs/j7/run"}); w.Code != 404 {
		t.Errorf("no such job: %d", w.Code)
	}
}

// The page's daemon holds the schedule's lock: a daemon of another process is reported and cannot be stopped from here, and two
// daemons never run.
func TestDaemonStartStopAndTheLock(t *testing.T) {
	rg := newRig(t, nil)
	st := sched.Store{Path: filepath.Join(rg.state, "schedule.json")}
	w := rg.do(req{method: "POST", path: "/api/schedule/daemon", body: map[string]string{"action": "start", "every": "1m"}})
	d := decode[wire.DaemonState](t, w)
	if w.Code != 200 || !d.Running || d.Owner != "here" || d.Every != "1m0s" {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	if _, _, err := sched.Lock(st.LockPath()); err == nil {
		t.Fatal("a second daemon took the lock")
	}
	if w := rg.do(req{method: "POST", path: "/api/schedule/daemon", body: map[string]string{"action": "start"}}); w.Code != 200 {
		t.Errorf("start twice: %d", w.Code)
	}
	w = rg.do(req{method: "POST", path: "/api/schedule/daemon", body: map[string]string{"action": "stop"}})
	if d := decode[wire.DaemonState](t, w); d.Running || d.Owner != "none" {
		t.Errorf("stop: %s", w.Body.String())
	}
	unlock, _, err := sched.Lock(st.LockPath()) // another daemon (as far as the page can tell)
	if err != nil {
		t.Fatal(err)
	}
	if v := decode[wire.ScheduleView](t, rg.do(req{method: "GET", path: "/api/schedule"})); v.Daemon.Owner != "external" || v.Daemon.PID != os.Getpid() {
		t.Errorf("external: %+v", v.Daemon)
	}
	for _, action := range []string{"start", "stop", "once"} {
		if w := rg.do(req{method: "POST", path: "/api/schedule/daemon", body: map[string]string{"action": action}}); w.Code != 409 || errCode(w) != "external" {
			t.Errorf("%s with another daemon: %d %s", action, w.Code, w.Body.String())
		}
	}
	unlock()
	for name, b := range map[string]map[string]string{"action": {"action": "restart"}, "every": {"action": "start", "every": "1ms"}} {
		if w := rg.do(req{method: "POST", path: "/api/schedule/daemon", body: b}); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

// "Run what is due" starts the due jobs once, as `sleipnir daemon --once` does, and lets the lock go.
func TestDaemonOnceRunsWhatIsDue(t *testing.T) {
	var later atomic.Int64
	rg := newRig(t, func(o *Options) { o.Now = func() time.Time { return time.Now().Add(time.Duration(later.Load())) } })
	rg.addJob(wire.JobRequest{Cron: "@hourly", Goal: "due", Dir: rg.project})
	later.Store(int64(48 * time.Hour)) // two days on: the job is due
	if w := rg.do(req{method: "POST", path: "/api/schedule/daemon", body: map[string]string{"action": "once"}}); w.Code != 200 {
		t.Fatalf("once: %d %s", w.Code, w.Body.String())
	}
	st := sched.Store{Path: filepath.Join(rg.state, "schedule.json")}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := st.List()
		if len(jobs) == 1 && jobs[0].LastExit == "ok" && !rg.daemonRunning() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	jobs, _ := st.List()
	if jobs[0].LastExit != "ok" || jobs[0].Log == "" {
		t.Fatalf("the due job: %+v", jobs[0])
	}
	if _, held := sched.Probe(st.LockPath()); held {
		t.Error("the lock is still held after one look")
	}
}

// daemonRunning reports whether the page's daemon runs.
func (rg *rig) daemonRunning() bool {
	return decode[wire.ScheduleView](rg.t, rg.do(req{method: "GET", path: "/api/schedule"})).Daemon.Owner == "here"
}
