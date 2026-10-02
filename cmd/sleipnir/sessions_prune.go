package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A session is a directory of the state directory: its event log, the blobs the log points at, the checkpoints. Nothing deletes one,
// and a person who uses the harness every day has some thousands of them within a year. `sleipnir sessions prune` is how they
// go: the old ones, never the newest few, never one that may still be written to, and nothing at all without --yes.

const (
	pruneDefaultAge  = 30 * 24 * time.Hour
	pruneDefaultKeep = 20
	// pruneInUse is how recently a session's log must have been written for it to be taken for a session that is running.
	pruneInUse = 10 * time.Minute
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
// newest keep sessions and any written to within pruneInUse of now. A directory without an events.jsonl is not a session and is left
// alone (the state directory is the person's, and may hold other things), and so is anything that is not a plain directory.
func planPrune(root string, now time.Time, olderThan time.Duration, keep int) (*prunePlan, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	type sess struct {
		pruneItem
		mod time.Time
	}
	var all []sess
	p := &prunePlan{}
	for _, e := range ents {
		path := filepath.Join(root, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() { // a file, or a link somebody made: not ours to delete
			continue
		}
		log, err := os.Stat(filepath.Join(path, "events.jsonl"))
		if err != nil {
			p.skipped = append(p.skipped, e.Name())
			continue
		}
		mod := fi.ModTime()
		if log.ModTime().After(mod) {
			mod = log.ModTime()
		}
		all = append(all, sess{pruneItem{id: e.Name(), path: path, age: max(now.Sub(mod), 0)}, mod})
	}
	p.total = len(all)
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) }) // newest first
	for i, s := range all {
		switch {
		case i < keep:
			p.keptNewest++
		case s.age < olderThan:
		case s.age < pruneInUse:
			p.inUse = append(p.inUse, s.id)
		default:
			s.bytes = dirSize(s.path)
			p.items = append(p.items, s.pruneItem)
			p.freed += s.bytes
		}
	}
	for i, j := 0, len(p.items)-1; i < j; i, j = i+1, j-1 { // oldest first
		p.items[i], p.items[j] = p.items[j], p.items[i]
	}
	sort.Strings(p.skipped)
	return p, nil
}

// dirSize is the bytes of the regular files under dir; links are counted as themselves, never followed.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// parseAge reads a duration the way a person writes one for a directory of old things: 36h, 90m, 30d, 2w, or 0.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			v, err := strconv.ParseFloat(n, 64)
			if err != nil || v < 0 {
				return 0, fmt.Errorf("%q is not an age (try 30d, 36h or 2w)", s)
			}
			return time.Duration(v * float64(unit)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q is not an age (try 30d, 36h or 2w)", s)
	}
	return d, nil
}

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
		root = sessionsRoot()
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
		if err := os.RemoveAll(it.path); err != nil {
			fmt.Fprintf(stderr, "sessions prune: %s: %v\n", it.id, err)
			failed++
			continue
		}
		freed += it.bytes
	}
	fmt.Fprintf(stdout, "deleted %d sessions, %s freed\n", len(plan.items)-failed, sizeText(freed))
	if failed > 0 {
		return fmt.Errorf("sessions prune: %d sessions could not be deleted", failed)
	}
	return nil
}
