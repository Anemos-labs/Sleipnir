package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// A session is a directory of the state directory: its event log, the blobs the log points at, the checkpoints. Nothing deletes one,
// and a person who uses the harness every day has some thousands of them within a year. `sleipnir sessions prune` is how they
// go: the old ones, never the newest few, never one that may still be written to, and nothing at all without --yes.

const (
	pruneDefaultAge  = 30 * 24 * time.Hour
	pruneDefaultKeep = 20
)

type pruneItem struct {
	id    string
	path  string
	age   time.Duration
	bytes int64
}

type prunePlan struct {
	total      int         // sessions found
	items      []pruneItem // the ones to delete, oldest first
	skipped    []string    // directories that are not sessions (no event log): never touched
	inUse      []string    // sessions old enough but written to a moment ago
	freed      int64
	keptNewest int // the newest ones that were kept whatever their age
}

// planPrune decides what to delete from the sessions in root: every session whose newest file is older than olderThan, except the
// newest keep sessions and any written to within ten minutes of now (session.PlanPruneDir, the rule the web interface applies too).
// A directory without an events.jsonl is not a session and is left alone, and so is anything that is not a plain directory.
func planPrune(root string, now time.Time, olderThan time.Duration, keep int) (*prunePlan, error) {
	sp, err := session.PlanPruneDir(root, now, olderThan, keep, nil)
	if err != nil {
		return nil, err
	}
	p := &prunePlan{total: sp.Total, skipped: sp.Skipped, inUse: sp.InUse, freed: sp.Bytes, keptNewest: sp.KeptNewest}
	for _, r := range sp.Delete {
		p.items = append(p.items, pruneItem{id: r.ID, path: r.Dir, age: max(now.Sub(r.LastWritten), 0), bytes: r.Bytes})
	}
	return p, nil
}

// parseAge reads a duration the way a person writes one for a directory of old things: 36h, 90m, 30d, 2w, or 0 (session.ParseAge).
func parseAge(s string) (time.Duration, error) { return session.ParseAge(s) }

// ageText is an age as a person reads it in a list: 3h, 12d, 5mo.
func ageText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 90*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
}

// sizeText is a number of bytes as a person reads it: 640 KB, 31 MB, 1.4 GB.
func sizeText(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%d KB", (n+k/2)/k)
	case n < k*k*k:
		return fmt.Sprintf("%d MB", (n+k*k/2)/(k*k))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
}

// cmdSessionsPrune is `sleipnir sessions prune`.
func cmdSessionsPrune(stdout, stderr io.Writer, args []string, now time.Time) error {
	fset := newFlagSet("sessions prune", flag.ContinueOnError)
	fset.SetOutput(stderr)
	dir := fset.String("dir", "", "the directory that holds the sessions (default <state>/sessions, <state> being $SLEIPNIR_HOME or ~/.sleipnir)")
	older := fset.String("older-than", "30d", "delete sessions whose newest file is older than this (30d, 36h, 2w; 0 is any age)")
	keep := fset.Int("keep", pruneDefaultKeep, "never delete the newest N sessions, whatever their age")
	yes := fset.Bool("yes", false, "delete them (without it, the sessions that would go are listed and nothing is deleted)")
	fset.Usage = func() {
		printHelp(stderr, `usage: sleipnir sessions prune [flags]

Deletes recorded sessions that are old: the event log, the blobs it points at and the checkpoints of each. The newest ones are
kept whatever their age, and so is any session that was written to in the last ten minutes. Nothing is deleted without --yes;
without it the sessions that would go are listed, with their size.

flags:
`)
		printFlags(fset)
	}
	if err := fset.Parse(args); err != nil {
		return err // -h is flag.ErrHelp, which main takes for success
	}
	if fset.NArg() > 0 {
		fset.Usage()
		return fmt.Errorf("sessions prune: unexpected argument %q", fset.Arg(0))
	}
	age, err := parseAge(*older)
	if err != nil {
		return fmt.Errorf("sessions prune: --older-than: %w", err)
	}
	if *keep < 0 {
		return errors.New("sessions prune: --keep must not be negative")
	}
	root := *dir
	if root == "" {
		root = session.SessionsDir("")
	}
	plan, err := planPrune(root, now, age, *keep)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(stderr, "no sessions yet")
		return nil
	}
	if err != nil {
		return fmt.Errorf("sessions prune: %w", err)
	}
	if len(plan.items) == 0 {
		fmt.Fprintf(stdout, "nothing to prune: %d sessions, %d of the newest kept, none older than %s beyond those\n", plan.total, plan.keptNewest, *older)
		return nil
	}
	for _, it := range plan.items {
		fmt.Fprintf(stdout, "  %-30s %5s old  %8s\n", it.id, ageText(it.age), sizeText(it.bytes))
	}
	verb := "would delete"
	if *yes {
		verb = "deleting"
	}
	fmt.Fprintf(stdout, "%s %d of %d sessions (%s); the newest %d are kept\n", verb, len(plan.items), plan.total, sizeText(plan.freed), plan.keptNewest)
	if len(plan.inUse) > 0 {
		fmt.Fprintf(stdout, "%d old enough but written to in the last ten minutes: left alone\n", len(plan.inUse))
	}
	if len(plan.skipped) > 0 {
		fmt.Fprintf(stderr, "left alone, not sessions (no events.jsonl): %s\n", strings.Join(plan.skipped, ", "))
	}
	if !*yes {
		fmt.Fprintln(stdout, "nothing deleted: run again with --yes")
		return nil
	}
	var failed int
	var freed int64
	for _, it := range plan.items {
		if rel, err := filepath.Rel(root, it.path); err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			failed++ // not under the sessions directory: never delete
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		report, err := session.RemoveStoredSession(ctx, it.path)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "sessions prune: %s: %v\n", it.id, err)
			failed++
			continue
		}
		if report != nil {
			for _, kept := range report.BranchesKept {
				fmt.Fprintf(stdout, "  kept Git branch %s: %s\n", kept.Branch, kept.Reason)
			}
		}
		freed += it.bytes
	}
	fmt.Fprintf(stdout, "deleted %d sessions, %s freed\n", len(plan.items)-failed, sizeText(freed))
	if failed > 0 {
		return fmt.Errorf("sessions prune: %d sessions could not be deleted", failed)
	}
	return nil
}
