package sched

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// JobTimeout bounds one run: a stuck run must not hold the daemon, and so every job after it, for ever.
const JobTimeout = time.Hour

// Tick starts every job that is due at now, one after another, recording each start before the run so that a crash cannot start it
// twice, and how it ended after. launch runs one job and says how it ended (an exit text, and the log file it wrote); logw receives
// one line when a job starts and one when it ends. A job removed while it ran is not recorded again. The caller holds the daemon lock.
func Tick(ctx context.Context, st Store, now time.Time, launch func(context.Context, Job, time.Time) (exit, log string), logw io.Writer) error {
	jobs, err := st.List()
	if err != nil {
		return err
	}
	for _, j := range Due(jobs, now) {
		if ctx.Err() != nil {
			return nil
		}
		if err := record(st, j.ID, func(x *Job) { x.LastRun, x.LastExit = now, "running" }); err != nil {
			return err
		}
		fmt.Fprintf(logw, "sleipnir daemon: %s starts: %s\n", j.ID, strings.Join(strings.Fields(j.Goal), " "))
		exit, log := launch(ctx, j, now)
		if err := record(st, j.ID, func(x *Job) { x.LastExit, x.Log = exit, log }); err != nil {
			return err
		}
		fmt.Fprintf(logw, "sleipnir daemon: %s ended: %s\n", j.ID, exit)
	}
	return nil
}

// Record changes the run records of one job in the file as it is now (a person may have added or removed others since); a job that
// was removed in the meantime is no error.
func Record(st Store, id string, f func(*Job)) error { return record(st, id, f) }

// record is Record.
func record(st Store, id string, f func(*Job)) error {
	_, err := st.update(id, func(j *Job) error { f(j); return nil })
	if errors.Is(err, ErrNoJob) {
		return nil // removed while it ran
	}
	return err
}

// JobEnv is env with the provider keys that the harness took out of it at start (harden.MoveKeys) put back: the environment a child
// Sleipnir gets where its keys cannot be passed on an inherited pipe (PassKeys does this on Windows only; a child's environment is
// readable by every same-user process on Linux). No key is ever in an argument list.
func JobEnv(env []string) []string {
	for _, name := range harden.Held() {
		env = append(env, name+"="+harden.Secret(name))
	}
	return env
}

// Args is the argument list of a job's run, after the program's name: a quiet headless `run` with the job's model, directory, mode
// and budget, and the goal after "--".
func Args(j Job) []string {
	args := []string{"run", "--quiet"}
	if j.Model != "" {
		args = append(args, "--model", j.Model)
	}
	if j.Dir != "" {
		args = append(args, "--cwd", j.Dir)
	}
	if j.Mode != "" {
		args = append(args, "--mode", j.Mode)
	}
	if j.BudgetUSD > 0 {
		args = append(args, "--budget-usd", fmt.Sprint(j.BudgetUSD))
	}
	return append(args, "--", j.Goal)
}

// LogDir is the directory of the jobs' logs: schedule-logs beside the job file.
func (s Store) LogDir() string { return filepath.Join(filepath.Dir(s.Path), "schedule-logs") }

// LogFile is the log of one run of a job started at now, in the directory logs.
func LogFile(logs string, j Job, now time.Time) string {
	return filepath.Join(logs, j.ID+"-"+now.Format("20060102-150405")+".log")
}

// RunJob runs one job as a child process of self (Args, environment env, stdin closed, the held keys passed by PassKeys and never in
// env), writing its output to a new log file in logs, and says how it ended: "ok", "timed out after 1h0m0s", "interrupted" (ctx
// ended), or the error of the process. out, when not nil, receives the output too.
func RunJob(ctx context.Context, self, logs string, j Job, now time.Time, env []string, out io.Writer) (exit, log string) {
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return "no log directory: " + err.Error(), ""
	}
	log = LogFile(logs, j, now)
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "no log file: " + err.Error(), ""
	}
	defer f.Close()
	rctx, cancel := context.WithTimeout(ctx, JobTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, self, Args(j)...)
	cmd.Env = env
	var w io.Writer = f
	if out != nil {
		w = io.MultiWriter(f, out)
	}
	cmd.Stdout, cmd.Stderr = w, w
	started := PassKeys(cmd)
	err = cmd.Start()
	started(err)
	if err == nil {
		err = cmd.Wait()
	}
	switch {
	case err == nil:
		return "ok", log
	case rctx.Err() == context.DeadlineExceeded:
		return "timed out after " + JobTimeout.String(), log
	case ctx.Err() != nil:
		return "interrupted", log
	default:
		return err.Error(), log
	}
}
