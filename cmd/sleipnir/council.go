package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/council"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// init registers the design council with the process's standard output streams.
func init() {
	extraCommands["council"] = func(ctx context.Context, args []string) error {
		return cmdCouncil(ctx, args, os.Stdout, os.Stderr)
	}
}

const councilUsage = `council [flags] SCREENSHOT...

Scores screenshots of a user interface with a council of models that see images, one round per call, and says whether every
member rates the candidate at --target or more in every category. Each round is appended to --ledger, and the report shows the
climb across rounds, the scores by category and each member's findings with the change that fixes them. Members are told neither
the target nor which product is which. Exit status 0 means the council passed, 3 that it did not yet (a verify command can
hill-climb on it), 1 an error.

  sleipnir council --model heimdallm/qwen/qwen3.8-flash-next-abliterated --model heimdallm/z-ai/glm-5.3-flash \
      --product "a diagram editor for engineers" --ref rival=shots/rival-*.png --staged shots/app-*.png`

// cmdCouncil runs one council round over the screenshots given.
func cmdCouncil(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("council", stderr, councilUsage)
	var models, refs multiFlag
	fs.Var(&models, "model", "a council member, provider/model of a model that sees images (repeat, or separate with commas; with --staged, in the order they are asked)")
	fs.Var(&refs, "ref", "a reference scored beside the candidate on the same scale, NAME=PATH[,PATH...]: a competitor, or the previous version (repeat)")
	product := fs.String("product", "", "what the product is and who uses it: what good design means depends on it")
	categories := fs.String("categories", strings.Join(council.DefaultCategories, ","), "the categories every member scores from 1 to 10, comma-separated")
	target := fs.Float64("target", 9, "the score every member must give the candidate in every category")
	staged := fs.Bool("staged", false, "ask each member only once every member before it rated the candidate at the target in every category")
	beat := fs.Bool("beat-refs", false, "also require every member to rate the candidate above every reference on average")
	ledger := fs.String("ledger", filepath.Join(".sleipnir", "council", "rounds.jsonl"), "the file each round is appended to, and the climb is read from")
	timeout := fs.Duration("timeout", council.DefaultMemberTimeout, "how long one member may take to answer; one that has not is a missing vote")
	asJSON := fs.Bool("json", false, "print the round as JSON")
	trust := fs.Bool("trust-project", false, "read the project's own config file, including its provider settings")
	shots, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(shots) == 0 {
		return usageError(fs, "council: pass the screenshots of the candidate")
	}
	b := council.Brief{Product: strings.TrimSpace(*product), Categories: splitList(*categories), Candidate: council.Subject{Name: council.CandidateSubject}}
	for _, p := range shots {
		img, err := council.LoadImage(p)
		if err != nil {
			return fmt.Errorf("council: %w", err)
		}
		b.Candidate.Images = append(b.Candidate.Images, img)
	}
	for _, r := range refs {
		name, paths, ok := strings.Cut(r, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return usageError(fs, fmt.Sprintf("council: --ref %q is not NAME=PATH[,PATH...]", r))
		}
		s := council.Subject{Name: strings.TrimSpace(name)}
		for _, p := range splitList(paths) {
			matches, _ := filepath.Glob(p)
			if len(matches) == 0 {
				matches = []string{p}
			}
			for _, m := range matches {
				img, err := council.LoadImage(m)
				if err != nil {
					return fmt.Errorf("council: --ref %s: %w", s.Name, err)
				}
				s.Images = append(s.Images, img)
			}
		}
		b.References = append(b.References, s)
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("council: %w", err)
	}
	var refsList []string
	for _, m := range models {
		refsList = append(refsList, splitList(m)...)
	}
	if len(refsList) == 0 {
		return usageError(fs, "council: name its members with --model provider/model (models that see images)")
	}
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: !*trust})
	if err != nil {
		return fmt.Errorf("council: %w", err)
	}
	var members []council.Member
	names := map[string]int{}
	for _, ref := range refsList {
		mr, err := session.ResolveModel(cfg, ref)
		if err != nil {
			return fmt.Errorf("council: %s: %w", ref, err)
		}
		p, m, err := session.BuildProvider(cfg, mr, session.ProviderOptions{})
		if err != nil {
			return fmt.Errorf("council: %s: %w", ref, err)
		}
		name := memberName(mr.Model)
		if names[name]++; names[name] > 1 {
			name += "#" + strconv.Itoa(names[name])
		}
		members = append(members, council.Member{Name: name, Provider: p, Model: m})
	}
	l := council.Ledger(*ledger)
	history, err := l.Rounds()
	if err != nil {
		return fmt.Errorf("council: %w", err)
	}
	n := 1
	if len(history) > 0 {
		n = history[len(history)-1].N + 1
	}
	r, err := council.Run(ctx, b, members, council.Options{Target: *target, Staged: *staged, BeatReferences: *beat, Round: n,
		MemberTimeout: *timeout, OnReview: func(rv council.Review) {
			switch {
			case rv.Error != "":
				fmt.Fprintf(stderr, "council: %s: %s\n", rv.Member, rv.Error)
			case rv.Skipped == "":
				fmt.Fprintf(stderr, "council: %s scored the candidate %.1f at its lowest\n", rv.Member, council.Min(rv.Scores[b.Candidate.Name]))
			}
		}})
	if err != nil {
		return fmt.Errorf("council: %w", err)
	}
	if err := l.Append(r); err != nil {
		return fmt.Errorf("council: keeping the round: %w", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else {
		var w strings.Builder
		council.Report(&w, history, r)
		fmt.Fprint(stdout, w.String())
	}
	if !r.Pass {
		if !scored(r) {
			// a round that no member scored says nothing about the design (a provider down, every answer unreadable): an error
			return fmt.Errorf("council: round %d could not be judged: no member gave a score", r.N)
		}
		// exitUnfinished, not 1 (an error), so that a script or a goal's verify command can tell "not good enough yet" from
		// "could not judge"; the report is already printed.
		return &exitError{code: exitUnfinished, err: fmt.Errorf("council: round %d did not pass", r.N)}
	}
	return nil
}

// scored reports whether some member asked in round r gave a score.
func scored(r council.Round) bool {
	for _, rv := range r.Reviews {
		if rv.Error == "" && rv.Skipped == "" {
			return true
		}
	}
	return false
}

// memberName is how a member is shown: the model id without its vendor ("qwen/qwen3.8-flash" is "qwen3.8-flash").
func memberName(model string) string {
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	return model
}
