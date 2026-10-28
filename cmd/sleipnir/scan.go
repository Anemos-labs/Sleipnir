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

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/scan"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// init registers the deep scans with the CLI dispatcher.
func init() { extraCommands["scan"] = cmdScan }

const scanCLIUsage = `usage: sleipnir scan [SCAN] [flags]

The deep scans of a project (docs/SCAN.md). A scan is started in the chat or the web page:
/scan [survey|standard|deep] [DIR] has a team map the repository at HEAD, hunt it for holes, and report
only what verifiers confirmed and, from the depth's bar up, a reproducer showed in the sandbox
(start the session with --sandbox docker). Each scan keeps its ledger and its report under
.sleipnir/scan/SCAN.

Without SCAN it lists the scans, newest first; with SCAN it shows where the scan stands. --start creates a
scan of HEAD (of DIR, the positional, when given) and prints the goal that drives it, for a headless team:
  sleipnir swarm 12 --sandbox docker "$(sleipnir scan --start)"
--start --diff BASE reviews a change instead: the files HEAD changed since BASE (a commit or a branch, from
their merge base), with leads only where a step of the trace is on a line the change wrote:
  sleipnir swarm 8 --sandbox docker "$(sleipnir scan --start --diff origin/main)"

flags:
`

// cmdScan lists a project's deep scans, shows one, or rewrites its report.
func cmdScan(ctx context.Context, args []string) error {
	fs := newFlagSet("scan", flag.ContinueOnError)
	fs.Usage = func() {
		printHelp(os.Stderr, scanCLIUsage)
		printFlags(fs)
	}
	cwd := fs.String("cwd", "", "the project (default: the current directory)")
	report := fs.Bool("report", false, "rewrite SCAN's report (REPORT.md, findings.json, findings.sarif, map.json) from its ledger and print where it is")
	asJSON := fs.Bool("json", false, "machine-readable output")
	start := fs.Bool("start", false, "create a scan of the repository at HEAD (below the positional DIR, when given) and print the goal that drives it")
	depth := fs.String("depth", scan.DefaultDepth, "with --start: survey, standard or deep")
	diff := fs.String("diff", "", "with --start: review the change from `BASE` (a commit or branch) to HEAD instead of the whole repository")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("usage: sleipnir scan [SCAN] [flags]")
	}
	dir := *cwd
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	project, _ := config.FindRoot(dir)
	root := scanRoot(project)
	if *diff != "" && !*start {
		return errors.New("scan: --diff goes with --start")
	}
	if *start {
		focus := ""
		if len(pos) == 1 {
			focus = pos[0]
		}
		if *diff != "" && focus != "" {
			return errors.New("scan: --diff reviews the whole change: leave DIR out")
		}
		return scanStartCLI(ctx, os.Stdout, root, *depth, focus, *diff)
	}
	if len(pos) == 0 {
		if *report {
			return errors.New("scan: --report needs the SCAN")
		}
		return scanListCLI(os.Stdout, root, *asJSON)
	}
	r, err := scan.Load(root, pos[0])
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if *report {
		if err := scan.WriteReport(root, r); err != nil {
			return fmt.Errorf("scan: report: %w", err)
		}
		fmt.Println(filepath.Join(scan.RunDir(root, r.Slug), scan.ReportFile))
		return nil
	}
	if *asJSON {
		return writeJSON(os.Stdout, map[string]any{"scan": r.Slug, "commit": r.Commit, "depth": r.Depth, "report": scan.Check(r), "findings": scan.Findings(r)})
	}
	fmt.Println(tools.SanitizeForTerminal(scan.StatusText(r)))
	return nil
}

// scanStartCLI creates a scan of HEAD, or a review of the change from base to HEAD, and prints its goal (the session that runs
// it opens the scan its goal names).
func scanStartCLI(ctx context.Context, out io.Writer, root, depth, focus, base string) error {
	if _, ok := scan.DepthByID(depth); !ok {
		return fmt.Errorf("scan: unknown depth %q (%s)", depth, strings.Join(scan.DepthIDs(), ", "))
	}
	repo, err := gitx.Open(root, gitx.WithMaxOutput(256<<20), gitx.WithTimeout(5*time.Minute))
	if err != nil {
		return fmt.Errorf("scan: %s is not a git repository: %w", root, err)
	}
	if focus != "" {
		focus = filepath.ToSlash(filepath.Clean(focus))
		if !filepath.IsLocal(focus) {
			return fmt.Errorf("scan: %s is not a directory of the repository", focus)
		}
	}
	var r *scan.Run
	if base != "" {
		r, err = scan.StartReview(ctx, scan.NewStore(root), repo, depth, base)
	} else {
		r, err = scan.Start(ctx, scan.NewStore(root), repo, depth, focus)
	}
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	_, err = fmt.Fprintln(out, scan.Objective(r, filepath.Join(root, filepath.FromSlash(scan.BriefPath(r.Slug)))))
	return err
}

// scanListCLI prints the scans of the project at root.
func scanListCLI(out io.Writer, root string, asJSON bool) error {
	runs := scan.List(root)
	if asJSON {
		type row struct {
			Slug    string      `json:"slug"`
			Commit  string      `json:"commit"`
			Depth   string      `json:"depth"`
			Focus   string      `json:"focus,omitempty"`
			Created string      `json:"created"`
			Phase   string      `json:"phase"`
			Counts  scan.Counts `json:"counts"`
		}
		rows := []row{}
		for _, r := range runs {
			rep := scan.Check(r)
			rows = append(rows, row{r.Slug, r.Commit, r.Depth, r.Focus, r.Created, rep.Phase, rep.Counts})
		}
		return writeJSON(out, map[string]any{"scans": rows})
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "no scans in this project: /scan in the chat starts one")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCAN\tDEPTH\tPHASE\tFILES\tFUNCTIONS\tMAPPED\tHUNTED\tLEADS\tCONFIRMED")
	for _, r := range runs {
		c := scan.Check(r).Counts
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d/%d\t%d/%d\t%d\t%d\n", r.Slug, r.Depth, scan.Check(r).Phase, c.Files, c.Funcs, c.Mapped, c.Shards,
			c.Hunted, c.Shards, c.Leads, c.Confirmed)
	}
	return tw.Flush()
}

// scanRoot is where a project's scans are: the project, or for a linked worktree of it, the main checkout (see researchRoot).
func scanRoot(project string) string {
	if _, err := os.Stat(scan.Dir(project)); err == nil {
		return project
	}
	repo, err := gitx.Open(project)
	if err != nil || !repo.IsLinked() || repo.IsBare() {
		return project
	}
	if main := filepath.Dir(repo.CommonDir()); main != "" {
		if _, err := os.Stat(scan.Dir(main)); err == nil {
			return main
		}
	}
	return project
}
