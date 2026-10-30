package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/research"
	"github.com/anemos-labs/sleipnir/internal/tools"
	webui "github.com/anemos-labs/sleipnir/internal/web"
)

// init registers the research runs with the CLI dispatcher.
func init() { extraCommands["research"] = cmdResearch }

const researchCLIUsage = `usage: sleipnir research [RUN] [flags]

The deep research runs of a project (docs/RESEARCH.md). A run is started in the chat or the web page:
/research [quick|standard|deep] QUESTION researches a question into a wiki, and /goal --research=DEPTH GOAL
researches before building. Each run keeps its ledger (plan, sources with their checked quotes, findings,
pages, reviews) under .sleipnir/research/RUN.

Without RUN it lists the runs, newest first; with RUN it shows where the run stands.

flags:
`

// cmdResearch lists a project's research runs, shows one, builds its wiki or exports it.
func cmdResearch(ctx context.Context, args []string) error {
	fs := newFlagSet("research", flag.ContinueOnError)
	fs.Usage = func() {
		printHelp(os.Stderr, researchCLIUsage)
		printFlags(fs)
	}
	cwd := fs.String("cwd", "", "the project (default: the current directory)")
	build := fs.Bool("build", false, "build RUN's wiki from its ledger into .sleipnir/research/RUN/site")
	export := fs.String("export", "", "render RUN's wiki into this directory of the project (an earlier export there is replaced)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("usage: sleipnir research [RUN] [flags]")
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
	root := researchRoot(project)
	if len(pos) == 0 {
		if *build || *export != "" {
			return errors.New("research: --build and --export need the RUN")
		}
		return researchListCLI(os.Stdout, root, *asJSON)
	}
	slug := pos[0]
	st := research.NewStore(root)
	r, err := st.Snapshot(slug)
	if err != nil {
		return fmt.Errorf("research: %w", err)
	}
	if *build {
		_, res, err := st.Publish(slug, research.BuildOptions{Fonts: webui.Fonts()})
		if err != nil {
			return fmt.Errorf("research: build: %w", err)
		}
		fmt.Printf("built %s (%d pages, search over %d entries)\n", filepath.Join(res.Dir, "index.html"), res.Pages, res.Indexed)
		if r, err = st.Snapshot(slug); err != nil {
			return err
		}
	}
	if *export != "" {
		dst := *export
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(dir, dst)
		}
		dst = filepath.Clean(dst)
		rel, err := filepath.Rel(project, dst)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("research: %s is not a directory inside the project", *export)
		}
		site, err := research.Render(root, r, research.BuildOptions{Fonts: webui.Fonts()})
		if err != nil {
			return fmt.Errorf("research: export: %w", err)
		}
		if err := research.Export(project, rel, site.Files); err != nil {
			return fmt.Errorf("research: export: %w", err)
		}
		fmt.Printf("exported the wiki of %s to %s\n", slug, dst)
	}
	if *build || *export != "" {
		return nil
	}
	if *asJSON {
		return writeJSON(os.Stdout, map[string]any{"run": r, "report": research.Check(root, r)})
	}
	fmt.Println(tools.SanitizeForTerminal(research.StatusText(root, r)))
	return nil
}

// researchListCLI prints the runs of the project at root.
func researchListCLI(out io.Writer, root string, asJSON bool) error {
	runs := research.List(root)
	if asJSON {
		type row struct {
			Slug     string          `json:"slug"`
			Question string          `json:"question"`
			Depth    string          `json:"depth"`
			Mode     string          `json:"mode"`
			Created  string          `json:"created"`
			Phase    string          `json:"phase"`
			Counts   research.Counts `json:"counts"`
		}
		rows := []row{}
		for _, r := range runs {
			rep := research.Check(root, r)
			rows = append(rows, row{r.Slug, r.Question, r.Depth, r.Mode, r.Created, rep.Phase, rep.Counts})
		}
		return writeJSON(out, map[string]any{"runs": rows})
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "no research runs in this project: /research QUESTION in the chat starts one")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tDEPTH\tMODE\tPHASE\tSOURCES\tFINDINGS\tPAGES\tQUESTION")
	for _, r := range runs {
		rep := research.Check(root, r)
		c := rep.Counts
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d/%d\t%s\n", oneLineSafe(r.Slug), oneLineSafe(r.Depth), oneLineSafe(r.Mode), rep.Phase, c.Sources, c.Findings, c.Pages, c.Planned,
			clip(r.Question, 90))
	}
	return tw.Flush()
}

// researchRoot is where a project's research runs are: the project itself, or, for a linked git worktree of it (a worker's tree
// of an isolated team), the main checkout, whose .sleipnir holds the runs the team's research tool keeps.
func researchRoot(project string) string {
	if _, err := os.Stat(research.Dir(project)); err == nil {
		return project
	}
	repo, err := gitx.Open(project)
	if err != nil || !repo.IsLinked() || repo.IsBare() {
		return project
	}
	main := filepath.Dir(repo.CommonDir())
	if _, err := os.Stat(research.Dir(main)); err == nil {
		return main
	}
	return project
}
