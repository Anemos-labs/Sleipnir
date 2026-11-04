package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tooleval"
)

// init registers the tool-use eval with the process's standard output streams.
func init() {
	extraCommands["tooleval"] = func(ctx context.Context, args []string) error {
		return cmdTooleval(ctx, args, os.Stdout, os.Stderr)
	}
}

const toolevalUsage = `tooleval [flags]

Measures how well models use the harness's tools in situations where models went wrong in real runs. Each case is the actual
first prompt of a role in a team session of this build (its system text and every tool spec), then what had happened, and a rule
for the next response: the call it must make, or must not. Every case is asked --samples times of each --model under each
variant of the tool descriptions (--variants), and the report gives each variant's pass rate beside the size of its
descriptions, so a description can be rewritten or cut and the change kept when the pass rate holds. Without --cases it runs the
suite shipped with this build, whose cases name the run they came from. Exit status 0 means every model passed at --min-pass or
more under the build's descriptions, 3 that one did not, 1 an error.

--harvest DIR turns the failed tool calls of a session kept with --training-logs into cases that replay its exact prompts, and
writes them (JSON lines) to --out instead of running anything.

  sleipnir tooleval --model heimdallm/z-ai/glm-5.3-flash --variants shorter.json --sizes
  sleipnir tooleval --harvest ~/.sleipnir/sessions/20261104-101500-ab12cd --out cases/harvested.jsonl`

// cmdTooleval runs the tool-use eval, or harvests cases from a recorded session.
func cmdTooleval(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("tooleval", stderr, toolevalUsage)
	var models, ids, harvest multiFlag
	fs.Var(&models, "model", "a model to ask, provider/model (repeat, or separate with commas)")
	fs.Var(&ids, "case", "run only this case, or the cases whose id starts with it and a dash (repeat)")
	fs.Var(&harvest, "harvest", "a session directory kept with --training-logs: write its failed calls as cases to --out (repeat)")
	casesPath := fs.String("cases", "", "a case file (JSON, a JSON array, or JSON lines) or a directory of them, in place of the shipped suite")
	variantsPath := fs.String("variants", "", `a JSON file of variants to try beside the build's descriptions: {"name": {"tool": "description"}}`)
	samples := fs.Int("samples", tooleval.DefaultSamples, "how many times each case is asked of each model under each variant")
	parallel := fs.Int("parallel", tooleval.DefaultParallel, "how many requests are in flight at once")
	timeout := fs.Duration("timeout", tooleval.DefaultTimeout, "how long one answer may take; one that has not come counts as no answer")
	maxTokens := fs.Int("max-tokens", tooleval.DefaultMaxTokens, "the longest answer, in tokens")
	minPass := fs.Float64("min-pass", 0, "the pass rate, from 0 to 1, each model must reach under the build's descriptions (exit 3 below it)")
	sizes := fs.Bool("sizes", false, "also print the size of each tool's name, description and schema under each variant")
	out := fs.String("out", "", "with --harvest: the file the cases are written to (default: standard output)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	trust := fs.Bool("trust-project", false, "read the project's own config file, including its provider settings")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usageError(fs, fmt.Sprintf("tooleval: unexpected argument %q", rest[0]))
	}
	if len(harvest) > 0 {
		return harvestCases(harvest, *out, stdout, stderr)
	}
	if !(*minPass >= 0 && *minPass <= 1) {
		return usageError(fs, "tooleval: --min-pass is a rate from 0 to 1")
	}
	var cases []tooleval.Case
	if *casesPath != "" {
		cases, err = tooleval.Load(*casesPath)
	} else {
		cases, err = tooleval.Builtin()
	}
	if err != nil {
		return fmt.Errorf("tooleval: %w", err)
	}
	if cases = tooleval.Filter(cases, ids); len(cases) == 0 {
		return usageError(fs, "tooleval: no case matches --case")
	}
	variants := []tooleval.Variant{{Name: tooleval.Current}}
	if *variantsPath != "" {
		b, err := os.ReadFile(*variantsPath)
		if err != nil {
			return fmt.Errorf("tooleval: %w", err)
		}
		if variants, err = tooleval.LoadVariants(b); err != nil {
			return fmt.Errorf("tooleval: %w", err)
		}
	}
	var refs []string
	for _, m := range models {
		refs = append(refs, splitList(m)...)
	}
	if len(refs) == 0 {
		return usageError(fs, "tooleval: name the models to ask with --model provider/model")
	}
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: !*trust})
	if err != nil {
		return fmt.Errorf("tooleval: %w", err)
	}
	var ms []tooleval.Model
	names := map[string]int{}
	for _, ref := range refs {
		mr, err := session.ResolveModel(cfg, ref)
		if err != nil {
			return fmt.Errorf("tooleval: %s: %w", ref, err)
		}
		p, m, err := session.BuildProvider(cfg, mr, session.ProviderOptions{})
		if err != nil {
			return fmt.Errorf("tooleval: %s: %w", ref, err)
		}
		name := memberName(mr.Model)
		if names[name]++; names[name] > 1 {
			name += "#" + strconv.Itoa(names[name])
		}
		ms = append(ms, tooleval.Model{Name: name, Provider: p, Model: m})
	}
	total := len(ms) * len(cases) * len(variants) * *samples
	done := 0
	r, err := tooleval.Run(ctx, ms, cases, variants, tooleval.Options{Samples: *samples, Parallel: *parallel, Timeout: *timeout,
		MaxTokens: *maxTokens, OnSample: func(s tooleval.Sample) {
			done++
			mark := "pass"
			switch {
			case s.Error != "":
				mark = "no answer: " + s.Error
			case !s.Pass:
				mark = "fail: " + s.Why
			}
			fmt.Fprintf(stderr, "tooleval: [%d/%d] %s %s %s: %s\n", done, total, s.Model, s.Variant, s.Case, mark)
		}})
	if err != nil {
		return fmt.Errorf("tooleval: %w", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else {
		tooleval.Print(stdout, r)
		if *sizes {
			fmt.Fprintln(stdout)
			tooleval.PrintSizes(stdout, r)
		}
	}
	if below := belowMinPass(r, *minPass); len(below) > 0 {
		return &exitError{code: exitUnfinished, err: fmt.Errorf("tooleval: below --min-pass %.2f under the build's descriptions: %v", *minPass, below)}
	}
	return nil
}

// belowMinPass names the models whose pass rate under the build's descriptions is below min (none when min is 0).
func belowMinPass(r *tooleval.Report, min float64) []string {
	if min <= 0 {
		return nil
	}
	passed, asked := map[string]int{}, map[string]int{}
	for _, s := range r.Samples {
		if s.Variant != tooleval.Current {
			continue
		}
		asked[s.Model]++ // a sample with no answer counts against the model: it did not pass
		if s.Pass {
			passed[s.Model]++
		}
	}
	var out []string
	for _, m := range r.Models {
		if asked[m] == 0 || float64(passed[m])/float64(asked[m]) < min {
			out = append(out, m)
		}
	}
	return out
}

// harvestCases writes the cases of the sessions in dirs as JSON lines to path (stdout when empty).
func harvestCases(dirs []string, path string, stdout, stderr io.Writer) error {
	var all []tooleval.Case
	for _, d := range dirs {
		cs, err := tooleval.Harvest(d)
		if err != nil {
			return fmt.Errorf("tooleval: %w", err)
		}
		fmt.Fprintf(stderr, "tooleval: %s: %d failed calls\n", d, len(cs))
		all = append(all, cs...)
	}
	w := stdout
	if path != "" {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("tooleval: %w", err)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	for _, c := range all {
		if err := enc.Encode(c); err != nil {
			return fmt.Errorf("tooleval: %w", err)
		}
	}
	if f, ok := w.(*os.File); ok && path != "" {
		return f.Close()
	}
	return nil
}
