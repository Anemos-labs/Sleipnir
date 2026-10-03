package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/sched"
)

// init registers schedule management and daemon commands.
func init() {
	extraCommands["schedule"] = cmdSchedule
	extraCommands["daemon"] = cmdDaemon
}

// scheduleStore returns the schedule file location under the user's state directory without
// opening it.
func scheduleStore() sched.Store {
	home, _ := os.UserHomeDir()
	return sched.Store{Path: filepath.Join(stateDir(home), "schedule.json")}
}

// cmdSchedule adds, lists and removes scheduled goals.
func cmdSchedule(_ context.Context, args []string) error {
	st := scheduleStore()
	if len(args) == 0 || args[0] == "list" {
		return listJobs(os.Stdout, st, time.Now())
	}
	switch args[0] {
	case "-h", "--help", "help":
		fs, _ := scheduleAddFlags()
		fs.SetOutput(os.Stdout)
		fmt.Println("usage: sleipnir schedule [list]\n       sleipnir schedule add --cron EXPR [flags] <goal>\n       sleipnir schedule rm <id>\n\nflags of add:")
		printFlags(fs)
		return nil
	case "add":
		fs, o := scheduleAddFlags()
		words, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		goal := strings.Join(words, " ")
		if o.dir == "" {
			o.dir, _ = os.Getwd()
		}
		j, err := st.Add(sched.Job{Cron: o.cron, Goal: goal, Model: o.model, Dir: o.dir, Mode: o.mode, BudgetUSD: o.budget}, time.Now())
		if err != nil {
			return fmt.Errorf("schedule add: %w", err)
		}
		next, _ := sched.Next(j, time.Now())
		fmt.Printf("%s: runs next at %s; `sleipnir daemon` starts the jobs that are due\n", j.ID, next.Format("2006-01-02 15:04"))
		return nil
	case "rm", "remove":
		if len(args) != 2 {
			return errors.New("schedule rm: want a job id (see `sleipnir schedule`)")
		}
		return st.Remove(args[1])
	}
	return errors.New("usage: sleipnir schedule [list] | add --cron EXPR [--model M] [--cwd D] [--mode MODE] [--budget-usd N] <goal> | rm <id>")
}

type addOpts struct {
	cron, model, dir, mode string
	budget                 float64
}

// scheduleAddFlags registers schedule creation options with unattended permission and one-dollar
// budget defaults.
func scheduleAddFlags() (*flag.FlagSet, *addOpts) {
	var o addOpts
	fs := newFlagSet("schedule add", flag.ExitOnError)
	fs.StringVar(&o.cron, "cron", "", `when: five fields "minute hour day-of-month month day-of-week", or @hourly, @daily, @weekly (required)`)
	fs.StringVar(&o.model, "model", "", "model for the run (default: your configured default)")
	fs.StringVar(&o.dir, "cwd", "", "working directory of the run (default: the current directory)")
	fs.StringVar(&o.mode, "mode", "", "permissions of the run: default | accept-edits | plan | bypass | yolo. The default refuses whatever needs a person to say yes, since nobody is there")
	fs.Float64Var(&o.budget, "budget-usd", 1, "stop a run at this many dollars (0: no limit)")
	return fs, &o
}

func listJobs(w io.Writer, st sched.Store, now time.Time) error {
	jobs, err := st.List()
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Fprintln(w, "no scheduled jobs. Add one:\n  sleipnir schedule add --cron \"0 9 * * 1-5\" \"summarize yesterday's commits\"")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCRON\tNEXT\tLAST\tGOAL")
	for _, j := range jobs {
		next := "-"
		if n, ok := sched.Next(j, now); ok {
			next = n.Format("01-02 15:04")
		}
		last := "never"
		if !j.LastRun.IsZero() {
			last = j.LastRun.Format("01-02 15:04") + " " + j.LastExit
		}
		goal := strings.Join(strings.Fields(j.Goal), " ")
		if len(goal) > 60 {
			goal = goal[:59] + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", j.ID, j.Cron, next, last, goal)
	}
	return tw.Flush()
}

// cmdDaemon starts the jobs that are due, once a half minute, until it is stopped. Each job is a headless `sleipnir run` of its own
// (so a crash or a stuck run harms nothing else), its output in <state>/schedule-logs. It runs one job at a time.
func cmdDaemon(ctx context.Context, args []string) error {
	fs := newFlagSet("daemon", flag.ExitOnError)
	once := fs.Bool("once", false, "start what is due now, wait for it, and exit (for cron or a systemd timer)")
	every := fs.Duration("every", 30*time.Second, "how often to look for due jobs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	logs := filepath.Join(stateDir(home), "schedule-logs")
	launch := func(ctx context.Context, j sched.Job, now time.Time) (exit, log string) {
		return runJob(ctx, self, logs, j, now)
	}
	st := scheduleStore()
	if *once {
		return daemonTick(ctx, st, time.Now(), launch, os.Stderr)
	}
	fmt.Fprintf(os.Stderr, "sleipnir daemon: looking for due jobs every %s (ctrl-c stops it); jobs: %s\n", *every, st.Path)
	for {
		if err := daemonTick(ctx, st, time.Now(), launch, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "sleipnir daemon:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*every):
		}
	}
}

// daemonTick starts every job that is due at now, one after another, recording each start before the run so that a crash cannot start
// it twice, and how it ended after.
func daemonTick(ctx context.Context, st sched.Store, now time.Time, launch func(context.Context, sched.Job, time.Time) (string, string), logw io.Writer) error {
	jobs, err := st.List()
	if err != nil {
		return err
	}
	for _, j := range sched.Due(jobs, now) {
		if ctx.Err() != nil {
			return nil
		}
		if err := record(st, j.ID, func(x *sched.Job) { x.LastRun, x.LastExit = now, "running" }); err != nil {
			return err
		}
		fmt.Fprintf(logw, "sleipnir daemon: %s starts: %s\n", j.ID, strings.Join(strings.Fields(j.Goal), " "))
		exit, log := launch(ctx, j, now)
		if err := record(st, j.ID, func(x *sched.Job) { x.LastExit, x.Log = exit, log }); err != nil {
			return err
		}
		fmt.Fprintf(logw, "sleipnir daemon: %s ended: %s\n", j.ID, exit)
	}
	return nil
}

// record changes one job in the file as it is now (a person may have added or removed others since).
func record(st sched.Store, id string, f func(*sched.Job)) error {
	jobs, err := st.List()
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID == id {
			f(&jobs[i])
			return st.Save(jobs)
		}
	}
	return nil // removed while it ran
}

// jobTimeout bounds one run: a stuck run must not hold the daemon, and so every job after it, for ever.
const jobTimeout = time.Hour

// jobEnv is the environment of a job's process: ours, with the provider keys that the harness took out of it at start (harden.MoveKeys)
// put back. The process is Sleipnir itself, which hides them again from whatever its tools run; no other program is started with them.
func jobEnv(env []string) []string {
	for _, name := range harden.Held() {
		env = append(env, name+"="+harden.Secret(name))
	}
	return env
}

// runJob runs one job as a child process and says how it ended.
func runJob(ctx context.Context, self, logs string, j sched.Job, now time.Time) (exit, log string) {
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return "no log directory: " + err.Error(), ""
	}
	log = filepath.Join(logs, j.ID+"-"+now.Format("20060102-150405")+".log")
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "no log file: " + err.Error(), ""
	}
	defer f.Close()
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
	args = append(args, "--", j.Goal)
	rctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, self, args...)
	cmd.Env = jobEnv(os.Environ())
	cmd.Stdout, cmd.Stderr = f, f
	switch err := cmd.Run(); {
	case err == nil:
		return "ok", log
	case rctx.Err() == context.DeadlineExceeded:
		return "timed out after " + jobTimeout.String(), log
	case ctx.Err() != nil:
		return "interrupted", log
	default:
		return err.Error(), log
	}
}
