package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// `sleipnir storage` shows what the data directory holds, area by area; `sleipnir storage clean` removes what is old in it, as the
// automatic cleanup of `sleipnir web` does once a day (session.CleanStorage, docs/STORAGE.md).

// init registers the storage commands with the process's standard output streams.
func init() {
	extraCommands["storage"] = func(ctx context.Context, args []string) error {
		if len(args) > 0 && args[0] == "clean" {
			return cmdStorageClean(ctx, os.Stdout, os.Stderr, args[1:], time.Now())
		}
		return cmdStorage(os.Stdout, os.Stderr, args)
	}
}

// cmdStorage is `sleipnir storage`: the areas of the data directory and the room each takes.
func cmdStorage(stdout, stderr io.Writer, args []string) error {
	fset := newFlagSet("storage", flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() {
		printHelp(stderr, `usage: sleipnir storage [clean [flags]]

Shows where the harness keeps its data ($SLEIPNIR_HOME, else ~/.sleipnir) and the room each part of it takes: the recorded
sessions, their scratch directories, the worktrees of isolated teams, the RL work directories and the rest (settings, credentials,
memories, plugins). `+"`storage clean`"+` removes what is old; sleipnir web does that by itself once a day unless storage.clean is "off".
`)
		printFlags(fset)
	}
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		fset.Usage()
		return fmt.Errorf("storage: unexpected argument %q", fset.Arg(0))
	}
	state := session.StateRoot("")
	areas, total := session.StorageUsage("")
	fmt.Fprintf(stdout, "data directory: %s\n", oneLineSafe(state))
	if len(areas) == 0 {
		fmt.Fprintln(stdout, "nothing stored yet")
		return nil
	}
	for _, a := range areas {
		mark := " "
		if a.Cleaned {
			mark = "*"
		}
		fmt.Fprintf(stdout, "  %s %-14s %9s  %s\n", mark, a.Name, sizeText(a.Bytes), oneLineSafe(a.What))
	}
	fmt.Fprintf(stdout, "  %-16s %9s\n", "total", sizeText(total))
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true})
	if err != nil || cfg == nil {
		cfg = config.Defaults()
	}
	auto := "off (storage.clean)"
	if cfg.Storage.AutoClean() {
		auto = fmt.Sprintf("once a day in sleipnir web: sessions older than %s, past the newest %d", cfg.Storage.MaxAge(), cfg.Storage.Keep())
	}
	fmt.Fprintf(stdout, "* cleaned by `sleipnir storage clean`; automatic cleanup: %s\n", auto)
	return nil
}

// cmdStorageClean is `sleipnir storage clean`.
func cmdStorageClean(ctx context.Context, stdout, stderr io.Writer, args []string, now time.Time) error {
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true})
	if err != nil || cfg == nil {
		cfg = config.Defaults()
	}
	fset := newFlagSet("storage clean", flag.ContinueOnError)
	fset.SetOutput(stderr)
	older := fset.String("older-than", config.DefaultSessionMaxAge, "delete sessions whose newest file is older than this (30d, 36h, 2w; 0 is any age); unset, storage.session_max_age")
	keep := fset.Int("keep", config.DefaultKeepSessions, "never delete the newest N sessions, whatever their age; unset, storage.keep_sessions")
	yes := fset.Bool("yes", false, "clean (without it, what would go is listed and nothing is removed)")
	fset.Usage = func() {
		printHelp(stderr, `usage: sleipnir storage clean [flags]

Removes what is old from the data directory: the sessions older than --older-than past the newest --keep (never a pinned one, nor
one a process holds), the scratch directories of sessions nobody has written to for a day, and the worktrees of sessions that are
gone. Worktrees are released as a deleted session's are: uncommitted edits are committed onto their Git branch first, and a branch
that holds commits no other branch has is kept. Nothing is removed without --yes; without it what would go is listed, with its size.

flags:
`)
		printFlags(fset)
	}
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		fset.Usage()
		return fmt.Errorf("storage clean: unexpected argument %q", fset.Arg(0))
	}
	given := map[string]bool{}
	fset.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["older-than"] {
		*older = cfg.Storage.MaxAge()
	}
	if !given["keep"] {
		*keep = cfg.Storage.Keep()
	}
	age, err := parseAge(*older)
	if err != nil {
		return fmt.Errorf("storage clean: --older-than: %w", err)
	}
	if *keep < 0 {
		return errors.New("storage clean: --keep must not be negative")
	}
	rep, err := session.CleanStorage(ctx, "", session.CleanOptions{OlderThan: age, Keep: *keep, DryRun: !*yes, Now: now})
	if err != nil {
		return fmt.Errorf("storage clean: %w", err)
	}
	printCleanReport(stdout, stderr, rep, !*yes, now)
	if len(rep.Errors) > 0 {
		return fmt.Errorf("storage clean: %d part(s) could not be cleaned", len(rep.Errors))
	}
	return nil
}

// printCleanReport says what a cleanup removed (or, dry, would remove), what it kept and why, and the room freed.
func printCleanReport(stdout, stderr io.Writer, rep *session.CleanReport, dry bool, now time.Time) {
	p := rep.Sessions
	for _, r := range p.Delete {
		fmt.Fprintf(stdout, "  session  %-30s %5s old  %8s\n", oneLineSafe(r.ID), ageText(max(now.Sub(r.LastWritten), 0)), sizeText(r.Bytes))
	}
	for _, d := range rep.Trees {
		fmt.Fprintf(stdout, "  worktrees of a session that is gone: %s\n", oneLineSafe(d))
	}
	verb := "removed"
	if dry {
		verb = "would remove"
	}
	sessions := len(p.Delete)
	if !dry {
		sessions = 0
		for _, rm := range rep.Removed {
			if rm.Err == nil {
				sessions++
			}
		}
	}
	fmt.Fprintf(stdout, "%s %d of %d sessions, %d scratch directories (%s), %d folders of worktrees (%s): %s in all\n", verb, sessions,
		p.Total, rep.Scratch, sizeText(rep.ScratchBytes), len(rep.Trees), sizeText(rep.TreesBytes), sizeText(rep.Freed()))
	if n := len(p.Pinned) + len(p.Locked) + len(p.InUse); n > 0 {
		fmt.Fprintf(stdout, "left alone: %d pinned, %d in use by a process, %d written to in the last ten minutes\n", len(p.Pinned), len(p.Locked), len(p.InUse))
	}
	for _, k := range rep.Kept {
		fmt.Fprintf(stdout, "  kept %s\n", oneLineSafe(k))
	}
	for _, e := range rep.Errors {
		fmt.Fprintf(stderr, "storage clean: %s\n", oneLineSafe(e))
	}
	if dry {
		fmt.Fprintln(stdout, "nothing removed: run again with --yes")
	}
}

// storageSummary is one line of what an automatic cleanup did, for the log of sleipnir web; "" when it removed nothing.
func storageSummary(rep *session.CleanReport) string {
	if rep == nil || rep.Freed() == 0 && len(rep.Errors) == 0 {
		return ""
	}
	n := 0
	for _, rm := range rep.Removed {
		if rm.Err == nil {
			n++
		}
	}
	msg := fmt.Sprintf("storage cleanup: %d old sessions, %d scratch directories and %d folders of worktrees removed, %s freed", n, rep.Scratch,
		len(rep.Trees), sizeText(rep.Freed()))
	if len(rep.Kept) > 0 {
		msg += fmt.Sprintf("; %d branch(es) or tree(s) kept because they hold unmerged work (sleipnir storage clean lists them)", len(rep.Kept))
	}
	if len(rep.Errors) > 0 {
		msg += "; " + strings.Join(rep.Errors[:min(len(rep.Errors), 2)], "; ")
	}
	return msg
}

// storageFirstDelay is how long sleipnir web waits after it starts before its first automatic cleanup (the first tab and the pinned
// sessions are reopened by then, and count as live); storageCheckEvery how often it looks again whether a day has passed.
const (
	storageFirstDelay = 2 * time.Minute
	storageCheckEvery = time.Hour
)

// housekeep runs the automatic storage cleanup while the host runs: soon after it starts, then whenever a day has passed since the
// last cleanup of any process (session.AutoClean keeps that time in the data directory). The sessions the tabs hold are live and
// never deleted; the user's own configuration says whether, and what, to clean (storage.*, which a project cannot set).
func (h *webHostImpl) housekeep() {
	if h.fixture != nil {
		return
	}
	wait := time.NewTimer(storageFirstDelay)
	defer wait.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-wait.C:
		}
		cfg, _, err := config.Load(config.LoadOpts{Cwd: h.d.Cwd, UntrustedProject: true})
		if err != nil || cfg == nil {
			cfg = config.Defaults()
		}
		live := make([]string, 0)
		for sid := range h.HostedSessions() {
			live = append(live, sid)
		}
		rep, err := session.AutoClean(h.ctx, "", cfg.Storage, live, time.Now())
		switch {
		case err != nil && !errors.Is(err, context.Canceled):
			h.logf("storage cleanup: %s", clip(err.Error(), 400))
		case rep != nil:
			if msg := storageSummary(rep); msg != "" {
				h.logf("%s", msg)
			}
		}
		wait.Reset(storageCheckEvery)
	}
}
