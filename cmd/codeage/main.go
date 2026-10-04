// Command codeage reports file and surviving-line ages from a pinned Git revision.
// It writes local HTML and JSON reports without contacting external services.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"
)

//go:embed report.html
var reportHTML string

// main runs the audit and reports collection failures with a nonzero exit status.
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "codeage:", err)
		os.Exit(1)
	}
}

// run resolves command options, collects committed history, and saves both reports.
func run(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("codeage", flag.ContinueOnError)
	f.SetOutput(out)
	repo := f.String("repo", ".", "Git working directory")
	rev := f.String("rev", "HEAD", "committed revision to inspect; working changes are excluded")
	dest := f.String("out", ".sleipnir/tmp/code-age", "directory for index.html and report.json")
	jobs := f.Int("jobs", 4, "parallel Git processes (1–32)")
	full := f.Bool("require-full-history", false, "refuse shallow history instead of showing a warning")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || *jobs < 1 || *jobs > 32 {
		return errors.New("expected flags only, with -jobs between 1 and 32")
	}
	r, err := collect(ctx, *repo, *rev, *jobs, *full, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := writeReports(*dest, r); err != nil {
		return err
	}
	summarize(out, r)
	fmt.Fprintf(out, "Reports: %s\n", *dest)
	for _, warning := range r.Warnings {
		fmt.Fprintln(out, "Warning:", warning)
	}
	if len(r.Errors) > 0 {
		return fmt.Errorf("%d files could not be analyzed; see report errors", len(r.Errors))
	}
	return nil
}

// writeReports safely embeds the report as JSON and creates the output directory.
func writeReports(dir string, r report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0644); err != nil {
		return err
	}
	t, err := template.New("report").Parse(reportHTML)
	if err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	// encoding/json escapes HTML delimiters, including hostile file names. Only
	// serialized data, never raw Git output, crosses the template.JS boundary.
	err = t.Execute(f, template.JS(data))
	return errors.Join(err, f.Close())
}

// summarize prints both age rankings with paths and revision-relative line numbers.
func summarize(w io.Writer, r report) {
	fmt.Fprintf(w, "Revision %s: %d files; %d collection errors\n", r.Revision, len(r.Files), len(r.Errors))
	fmt.Fprintln(w, "Age prioritizes review; it does not measure quality or performance.")
	for _, byLine := range []bool{false, true} {
		files := append([]fileAge(nil), r.Files...)
		stamp := func(f fileAge) int64 {
			if byLine {
				return f.Oldest
			}
			return f.LastChange
		}
		sort.Slice(files, func(i, j int) bool {
			a, b := stamp(files[i]), stamp(files[j])
			if a == b {
				return files[i].Path < files[j].Path
			}
			return a < b
		})
		label := "Oldest file modifications"
		if byLine {
			label = "Oldest surviving nonblank lines (whitespace ignored)"
		}
		fmt.Fprintln(w, "\n"+label)
		n := 0
		for _, f := range files {
			if stamp(f) == 0 {
				continue
			}
			line := ""
			if byLine && len(f.Samples) > 0 {
				line = fmt.Sprintf(":%d", f.Samples[0].Line)
			}
			fmt.Fprintf(w, "%s  %q%s\n", time.Unix(stamp(f), 0).UTC().Format("2006-01-02"), f.Path, line)
			n++
			if n == 15 {
				break
			}
		}
	}
}
