package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/anemos-labs/sleipnir/internal/friction"
)

func init() {
	extraCommands["friction"] = func(ctx context.Context, args []string) error {
		return cmdFriction(ctx, args, os.Stdout, os.Stderr)
	}
}

const frictionUsage = "friction [flags] PATH..."

// cmdFriction ranks what slowed recorded sessions down: refused and asked permissions, failed tool calls, stuck and cancelled
// runs, retried requests, cache breaks, repeated reads. PATH is a session directory, a directory of sessions
// (~/.sleipnir/sessions), or a run directory of rollouts; every events.jsonl below it is one session.
func cmdFriction(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("friction", stderr, frictionUsage)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	top := fs.Int("top", 15, "how many findings to print (0 = all)")
	minCount := fs.Int("min-count", 1, "leave out findings seen fewer times")
	examples := fs.Int("examples", 3, "examples kept for each finding")
	category := fs.String("category", "", "only this category, or a prefix of one (permission, tool, agent, request, cache, compaction, file, call)")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fs.Usage()
		return errors.New("friction: pass a session, a directory of sessions or a run directory")
	}
	rep, err := friction.Mine(paths, friction.Options{MinCount: *minCount, Examples: *examples})
	if err != nil {
		return fmt.Errorf("friction: %w", err)
	}
	if *category != "" {
		kept := rep.Findings[:0]
		for _, f := range rep.Findings {
			if strings.HasPrefix(f.Category, *category) {
				kept = append(kept, f)
			}
		}
		rep.Findings = kept
	}
	if *top > 0 && len(rep.Findings) > *top {
		rep.Findings = rep.Findings[:*top]
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printFriction(stdout, rep)
	return nil
}

func printFriction(w io.Writer, rep friction.Report) {
	fmt.Fprintf(w, "friction in %d sessions (%d events, %d model requests)\n\n", rep.Sessions, rep.Events, rep.Requests)
	if len(rep.Findings) == 0 {
		fmt.Fprintln(w, "nothing that cost anything: no refusals, failed tool calls, stuck or cancelled runs, retries or repeated reads.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tSCORE\tCATEGORY\tCOUNT\tSESSIONS\tWASTED\tWHAT")
	for i, f := range rep.Findings {
		fmt.Fprintf(tw, "%d\t%.0f\t%s\t%d\t%d\t%d\t%s\n", i+1, f.Score, f.Category, f.Count, f.Sessions, f.Wasted, f.Key)
	}
	tw.Flush()
	fmt.Fprintln(w, "\nscore = count x severity squared (1 a cost, 2 waste, 3 a stop) x (1 + requests wasted per occurrence); WASTED is model requests.")
	fmt.Fprintln(w)
	for i, f := range rep.Findings {
		if i >= 5 {
			break
		}
		fmt.Fprintf(w, "%d. %s: %s\n", i+1, f.Category, f.Key)
		if f.Hint != "" {
			fmt.Fprintf(w, "   %s\n", f.Hint)
		}
		for _, e := range f.Examples {
			fmt.Fprintf(w, "   %s seq %d: %s\n", e.Session, e.Seq, e.Detail)
		}
	}
}
