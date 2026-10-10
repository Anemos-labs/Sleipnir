package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// init registers schedule management and daemon commands.
func init() {
	extraCommands["schedule"] = cmdSchedule
	extraCommands["daemon"] = cmdDaemon
}

// scheduleStore returns the schedule file location under the user's state directory without
// opening it.
func scheduleStore() sched.Store {
	return sched.Store{Path: filepath.Join(session.StateRoot(""), "schedule.json")}
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

// listJobs prints the jobs as a table (id, cron, the next run, the last run and how it ended, the goal on one line), or how to add one.
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
		} else if j.Paused {
			next = "paused"
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
// (so a crash or a stuck run harms nothing else), its output in <state>/schedule-logs. It runs one job at a time, and holds the
// schedule's daemon lock while it runs, so that a second daemon (another terminal, a cron --once, `sleipnir web`) cannot start the
// same jobs.
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
	st := scheduleStore()
	logs := st.LogDir()
	launch := func(ctx context.Context, j sched.Job, now time.Time) (exit, log string) {
		return runJob(ctx, self, logs, j, now)
	}
	unlock, pid, err := sched.Lock(st.LockPath())
	if errors.Is(err, sched.ErrLocked) {
		holder := "another process"
		if pid > 0 {
			holder = fmt.Sprintf("process %d", pid)
		}
		if *once {
			// a cron --once that comes while a daemon (or the previous --once) is still at work: that one starts what is due
			fmt.Fprintf(os.Stderr, "sleipnir daemon: the schedule is being run by %s; nothing started\n", holder)
			return nil
		}
		return fmt.Errorf("daemon: the schedule is already being run by %s (a daemon, or sleipnir web); stop it first", holder)
	}
	if err != nil {
		return err
	}
	defer unlock()
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
// it twice, and how it ended after (sched.Tick).
func daemonTick(ctx context.Context, st sched.Store, now time.Time, launch func(context.Context, sched.Job, time.Time) (string, string), logw io.Writer) error {
	return sched.Tick(ctx, st, now, launch, logw)
}

// jobEnv is env with the provider keys that the harness took out of it at start (harden.MoveKeys) put back (sched.JobEnv): what a
// job's process gets where its keys cannot be passed on an inherited pipe. runJob passes them with sched.PassKeys instead.
func jobEnv(env []string) []string { return sched.JobEnv(env) }

// runJob runs one job as a child process and says how it ended (sched.RunJob: the held keys reach it on a pipe, not in its
// environment).
func runJob(ctx context.Context, self, logs string, j sched.Job, now time.Time) (exit, log string) {
	return sched.RunJob(ctx, self, logs, j, now, os.Environ(), nil)
}
