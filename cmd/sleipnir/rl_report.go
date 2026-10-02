package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

func init() {
	rlCommands["report"] = rlReport
	rlCommands["compare"] = rlCompare
}

// loadReport reads a run directory (recomputed from its episodes, so a rescored run reports what it is now) or a report
// saved earlier (a committed baseline).
func loadReport(path string) (env.Report, error) {
	st, err := os.Stat(path)
	if err != nil {
		return env.Report{}, err
	}
	if st.IsDir() {
		return env.LoadRun(path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return env.Report{}, err
	}
	var r env.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return env.Report{}, fmt.Errorf("%s: %w", path, err)
	}
	if !strings.HasPrefix(r.Schema, "sleipnir.rl.eval/") {
		return env.Report{}, fmt.Errorf("%s is not a report (schema %q): pass a run directory or a report written by rl report --format json", path, r.Schema)
	}
	return r, nil
}

// ---- rl report ----

type column struct {
	head string
	get  func(env.Report) string
}

func label(r env.Report) string {
	switch {
	case r.RunID != "":
		return r.RunID
	case r.Model != "":
		return r.Model
	}
	return "-"
}

func pct(v float64, n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*v)
}

func perEp(v float64, n int, format string) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf(format, v)
}

func mode(r env.Report) string {
	if r.Identity != nil {
		return r.Identity.Mode
	}
	return "-"
}

var reportColumns = []column{
	{"RUN", label},
	{"MODEL", func(r env.Report) string { return firstOr(r.Model, "-") }},
	{"MODE", mode},
	{"DONE", func(r env.Report) string {
		s := fmt.Sprintf("%d/%d", r.Completed, r.Tasks*r.Samples)
		if r.Infra > 0 {
			s += fmt.Sprintf(" (+%d infra)", r.Infra)
		}
		return s
	}},
	{"PASS", func(r env.Report) string { return pct(float64(r.Passed)/float64(max(r.Completed, 1)), r.Completed) }},
	{"95% CI", func(r env.Report) string {
		if r.Completed == 0 {
			return "-"
		}
		return fmt.Sprintf("%.0f-%.0f", 100*r.PassLow, 100*r.PassHigh)
	}},
	{"SOLVED", func(r env.Report) string { return pct(r.Solved, r.Completed) }},
	{"$/EP", func(r env.Report) string { return perEp(r.USDTotal/float64(max(r.Completed, 1)), r.Completed, "%.4f") }},
	{"$/PASS", func(r env.Report) string { return perEp(r.USDPerPass, r.Passed, "%.4f") }},
	{"HIT", func(r env.Report) string { return pct(r.HitRatio, r.Tokens.Prompt()) }},
	{"REQ", func(r env.Report) string { return perEp(r.Requests.Mean, r.Completed, "%.1f") }},
	{"WALL p50", func(r env.Report) string { return perEp(r.WallMs.Median/1000, r.Completed, "%.0fs") }},
	{"p90", func(r env.Report) string { return perEp(r.WallMs.P90/1000, r.Completed, "%.0fs") }},
	{"TOOLERR", func(r env.Report) string { return perEp(r.ToolErrors, r.Completed, "%.2f") }},
	{"INVALID", func(r env.Report) string { return perEp(r.InvalidCalls, r.Completed, "%.2f") }},
	{"RETRY", func(r env.Report) string { return perEp(r.Retries, r.Completed, "%.2f") }},
	{"FALSEDONE", func(r env.Report) string { return pct(r.FalseDone, r.Completed) }},
	{"HACK", func(r env.Report) string { return pct(r.HackRate, r.Completed) }},
}

func firstOr(s, or string) string {
	if s != "" {
		return s
	}
	return or
}

// rlReport prints what a run (or several) measured: outcome with its interval, cost, cache, friction.
func rlReport(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl report", stderr, "rl report [flags] RUN_DIR|REPORT.json...")
	format := fs.String("format", "table", "table | md | json (json is what rl compare reads: commit it as a baseline)")
	byTag := fs.Bool("by-tag", false, "also break each run down by task tag")
	perTask := fs.Bool("tasks", false, "also list every task: samples passed, cost, requests, wall time")
	outFile := fs.String("o", "", "write the report to this file instead of standard output")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return usageError(fs, "rl report: pass one or more run directories")
	}
	switch *format {
	case "table", "md", "json":
	default:
		return fmt.Errorf("rl report: unknown format %q (table, md, json)", *format)
	}
	var reports []env.Report
	for _, p := range paths {
		r, err := loadReport(p)
		if err != nil {
			return fmt.Errorf("rl report: %w", err)
		}
		reports = append(reports, r)
	}
	w := stdout
	if *outFile != "" {
		f, err := os.OpenFile(*outFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("rl report: %w", err)
		}
		defer f.Close()
		w = f
	}
	if *format == "json" {
		var v any = reports
		if len(reports) == 1 {
			v = reports[0]
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	md := *format == "md"
	rows := make([][]string, len(reports))
	heads := make([]string, len(reportColumns))
	for i, c := range reportColumns {
		heads[i] = c.head
	}
	for i, r := range reports {
		for _, c := range reportColumns {
			rows[i] = append(rows[i], c.get(r))
		}
	}
	printTable(w, heads, rows, md)
	var notes []string
	for _, r := range reports {
		var n []string
		if r.Pending > 0 {
			n = append(n, fmt.Sprintf("%d of %d rollouts have no outcome yet", r.Pending, r.Tasks*r.Samples))
		}
		if r.Infra > 0 {
			n = append(n, fmt.Sprintf("%d failed for infrastructure reasons and are not counted in any rate", r.Infra))
		}
		if a := r.Attempts; a != nil && a.Wasted > 0 {
			n = append(n, fmt.Sprintf("%d of %d attempts ended without an answer and cost $%.4f of $%.4f (%d of %d requests)", a.Wasted, a.Attempts, a.WastedUSD, a.SpentUSD, a.WastedRequests, a.Requests))
		}
		if len(n) > 0 {
			notes = append(notes, fmt.Sprintf("  %s: %s", label(r), strings.Join(n, "; ")))
		}
	}
	if len(notes) > 0 {
		fmt.Fprintln(w, "\nnotes:")
		for _, n := range notes {
			fmt.Fprintln(w, n)
		}
	}
	if !md {
		fmt.Fprintln(w, "\nPASS: passed episodes / completed ones, with a 95% Wilson interval; SOLVED: tasks that at least one sample passed; HIT: cache-read tokens / input tokens.")
		fmt.Fprintln(w, "Friction is per completed episode: tool calls that failed (TOOLERR), malformed or unknown ones (INVALID), requests the endpoint did not answer and were repeated (RETRY).")
	}
	if *byTag {
		for _, r := range reports {
			tags := make([]string, 0, len(r.ByTag))
			for t := range r.ByTag {
				tags = append(tags, t)
			}
			sort.Strings(tags)
			var trows [][]string
			for _, t := range tags {
				s := r.ByTag[t]
				trows = append(trows, []string{t, fmt.Sprint(s.Tasks), fmt.Sprint(s.Samples), pct(s.PassAt1, s.Samples), perEp(s.MeanUSD, s.Samples, "%.4f"), pct(s.Hack, s.Samples)})
			}
			fmt.Fprintf(w, "\n%s by tag\n\n", label(r))
			printTable(w, []string{"TAG", "TASKS", "SAMPLES", "PASS", "$/EP", "HACK"}, trows, md)
		}
	}
	if *perTask {
		for _, r := range reports {
			var trows [][]string
			for _, t := range r.PerTask {
				trows = append(trows, []string{t.ID, strings.Join(t.Tags, ","), fmt.Sprintf("%d/%d", t.Correct, t.N), perEp(t.MeanUSD, t.N, "%.4f"), perEp(t.MeanRequests, t.N, "%.1f"), perEp(t.MeanWallMs/1000, t.N, "%.0fs")})
			}
			fmt.Fprintf(w, "\n%s by task\n\n", label(r))
			printTable(w, []string{"TASK", "TAGS", "PASSED", "$/EP", "REQ", "WALL"}, trows, md)
		}
	}
	return nil
}

// printTable writes rows as an aligned text table or a markdown one.
func printTable(w io.Writer, heads []string, rows [][]string, md bool) {
	if md {
		fmt.Fprintln(w, "| "+strings.Join(heads, " | ")+" |")
		sep := make([]string, len(heads))
		for i := range sep {
			sep[i] = "---"
		}
		fmt.Fprintln(w, "| "+strings.Join(sep, " | ")+" |")
		for _, r := range rows {
			fmt.Fprintln(w, "| "+strings.Join(r, " | ")+" |")
		}
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(heads, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// ---- rl compare ----

// gateFlag collects --gate values: repeatable, and comma-separated too.
type gateFlag []env.Gate

func (g *gateFlag) String() string {
	parts := make([]string, len(*g))
	for i, x := range *g {
		parts[i] = x.String()
	}
	return strings.Join(parts, ",")
}

func (g *gateFlag) Set(s string) error {
	for _, part := range splitList(s) {
		x, err := env.ParseGate(part)
		if err != nil {
			return err
		}
		*g = append(*g, x)
	}
	return nil
}

func fmtMetric(name string, v float64) string {
	switch {
	case strings.HasSuffix(name, "_wall_ms"):
		return fmt.Sprintf("%.1fs", v/1000)
	case strings.HasSuffix(name, "_usd"):
		return fmt.Sprintf("%.5f", v)
	case strings.HasSuffix(name, "_ite"):
		return fmt.Sprintf("%.0f", v)
	case strings.HasSuffix(name, "_requests"), strings.HasSuffix(name, "_steps"):
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.3f", v)
}

func fmtDelta(name string, v float64) string {
	s := fmtMetric(name, v)
	if !strings.HasPrefix(s, "-") {
		s = "+" + s
	}
	return s
}

// gateResult is one gate's verdict, as printed and as written to --format json.
type gateResult struct {
	Gate   string `json:"gate"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// rlCompare compares two runs (or saved reports) over the tasks both ran, with paired bootstrap intervals, and applies
// gates. It exits non-zero when a gate fails, so a script can refuse a regression.
func rlCompare(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl compare", stderr, "rl compare [flags] A B")
	var gates gateFlag
	fs.Var(&gates, "gate", "metric[:tolerance] that must not get worse (repeatable, or comma-separated): e.g. pass_at_1:0.05 or mean_usd:25%; fails only when the drop is bigger than the tolerance and the interval excludes zero")
	format := fs.String("format", "table", "table | json")
	resamples := fs.Int("resamples", 2000, "bootstrap resamples")
	seed := fs.Int64("seed", 1, "bootstrap seed (the comparison is deterministic)")
	confidence := fs.Float64("confidence", 0.95, "confidence level of the intervals")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) != 2 {
		return usageError(fs, "rl compare: pass two runs (or reports): A, the reference, then B")
	}
	if *format != "table" && *format != "json" {
		return fmt.Errorf("rl compare: unknown format %q (table, json)", *format)
	}
	var reps [2]env.Report
	for i, p := range paths {
		if reps[i], err = loadReport(p); err != nil {
			return fmt.Errorf("rl compare: %w", err)
		}
	}
	a, b := reps[0], reps[1]
	c := env.CompareWith(a, b, env.CompareOptions{Confidence: *confidence, Resamples: *resamples, Seed: *seed})

	var results []gateResult
	failed := 0
	for _, g := range gates {
		ok, detail := g.Check(c)
		results = append(results, gateResult{g.String(), ok, detail})
		if !ok {
			failed++
		}
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"comparison": c, "gates": results}); err != nil {
			return err
		}
	} else {
		printCompare(stdout, a, b, c, results)
	}
	if failed > 0 {
		return fmt.Errorf("rl compare: %d of %d gates failed", failed, len(gates))
	}
	return nil
}

func printCompare(w io.Writer, a, b env.Report, c env.Comparison, gates []gateResult) {
	line := func(tag string, r env.Report) {
		fmt.Fprintf(w, "%s  %s", tag, label(r))
		if r.Model != "" {
			fmt.Fprintf(w, "  (%s)", r.Model)
		}
		fmt.Fprintf(w, "  %d episodes, pass %s [%.0f-%.0f]", r.Completed, pct(float64(r.Passed)/float64(max(r.Completed, 1)), r.Completed), 100*r.PassLow, 100*r.PassHigh)
		if id := r.Identity; id != nil && (id.Version != "" || id.Commit != "") {
			fmt.Fprintf(w, "  build %s %s", id.Version, id.Commit)
		}
		fmt.Fprintln(w)
	}
	line("A", a)
	line("B", b)
	fmt.Fprintf(w, "\n%d tasks ran in both (only in A: %d, only in B: %d); %.0f%% intervals from %d paired-bootstrap resamples over tasks\n", c.Paired, len(c.OnlyA), len(c.OnlyB), 100*c.Confidence, c.Resamples)
	if a.Identity != nil && b.Identity != nil && a.Identity.TasksDigest != b.Identity.TasksDigest {
		fmt.Fprintln(w, "note: the two runs have different task sets; only the tasks they share are compared")
	}
	if a.Identity != nil && b.Identity != nil && (a.Identity.Mode != b.Identity.Mode || a.Identity.Group != b.Identity.Group) {
		fmt.Fprintf(w, "note: the runs differ in mode (%s vs %s) or samples per task (%d vs %d)\n", a.Identity.Mode, b.Identity.Mode, a.Identity.Group, b.Identity.Group)
	}
	if c.Paired > 0 && c.Paired < 10 {
		fmt.Fprintf(w, "note: %d paired tasks make a wide interval; treat verdicts as hints\n", c.Paired)
	}
	fmt.Fprintln(w)
	rows := make([][]string, 0, len(c.Metrics))
	for _, m := range c.Metrics {
		rows = append(rows, []string{m.Name, fmtMetric(m.Name, m.A), fmtMetric(m.Name, m.B), fmtDelta(m.Name, m.Delta),
			fmt.Sprintf("[%s, %s]", fmtDelta(m.Name, m.Low), fmtDelta(m.Name, m.High)), m.Verdict()})
	}
	printTable(w, []string{"METRIC", "A", "B", "DELTA", "INTERVAL", "VERDICT"}, rows, false)
	if len(gates) > 0 {
		fmt.Fprintln(w, "\ngates:")
		for _, g := range gates {
			mark := "ok  "
			if !g.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(w, "  %s  %-18s %s\n", mark, g.Gate, g.Detail)
		}
	}
}
