package tooleval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// Model is a model the cases are asked of.
type Model struct {
	Name     string
	Provider provider.Provider
	Model    cost.Model
}

// Options shape a run.
type Options struct {
	// Samples is how many times each case is asked of each model under each variant (default 3): a model samples, so one
	// answer says little.
	Samples int
	// Timeout bounds one answer (default 5 minutes: a slow endpoint can take minutes on a 30 KB prompt); one that has not come is a failed sample with its error.
	Timeout time.Duration
	// Parallel is how many requests are in flight at once (default 4).
	Parallel int
	// MaxTokens bounds an answer (default 8192).
	MaxTokens int
	// Base gives the prompt of a role in a team (default: Base). A run asks for each once.
	Base func(ctx context.Context, role string, team []string) (*core.Prompt, error)
	// OnSample sees each sample as it is checked.
	OnSample func(Sample)
}

// Defaults of Options.
const (
	DefaultSamples   = 3
	DefaultTimeout   = 5 * time.Minute
	DefaultParallel  = 4
	DefaultMaxTokens = 8192
	maxAttempts      = 3
)

// Sample is one answer to one case.
type Sample struct {
	Model   string  `json:"model"`
	Variant string  `json:"variant"`
	Case    string  `json:"case"`
	N       int     `json:"n"`
	Pass    bool    `json:"pass"`
	Why     string  `json:"why,omitempty"`   // why the answer failed its case
	Error   string  `json:"error,omitempty"` // the request failed: no answer was checked
	USD     float64 `json:"usd,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
}

// Report is the outcome of a run.
type Report struct {
	Models   []string        `json:"models"`
	Variants []VariantReport `json:"variants"`
	Cases    []CaseReport    `json:"cases"`
	Samples  []Sample        `json:"samples"`
	USD      float64         `json:"usd"`
}

// VariantReport is how a variant of the descriptions did, beside what it costs every prompt.
type VariantReport struct {
	Name   string         `json:"name"`
	Passed int            `json:"passed"`
	Asked  int            `json:"asked"` // samples that got an answer
	Errors int            `json:"errors"`
	Bytes  int            `json:"bytes"` // the size of every tool of the prompts asked (see Size), each tool once
	Tools  map[string]int `json:"tools"` // the size of each tool
}

// Rate is the share of answered samples that passed (0 with none).
func (v VariantReport) Rate() float64 {
	if v.Asked == 0 {
		return 0
	}
	return float64(v.Passed) / float64(v.Asked)
}

// CaseReport is how one case did under each variant.
type CaseReport struct {
	ID      string         `json:"id"`
	Source  string         `json:"source,omitempty"`
	Failure string         `json:"failure,omitempty"`
	Passed  map[string]int `json:"passed"` // variant to samples passed
	Asked   map[string]int `json:"asked"`
	// Why is the most common reason a sample failed, under each variant.
	Why map[string]string `json:"why,omitempty"`
}

// Run asks every case of every model under every variant, opts.Samples times, and checks each answer.
func Run(ctx context.Context, models []Model, cases []Case, variants []Variant, opts Options) (*Report, error) {
	if len(models) == 0 || len(cases) == 0 {
		return nil, fmt.Errorf("tooleval: nothing to run (%d models, %d cases)", len(models), len(cases))
	}
	if len(variants) == 0 {
		variants = []Variant{{Name: Current}}
	}
	if opts.Samples <= 0 {
		opts.Samples = DefaultSamples
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Parallel <= 0 {
		opts.Parallel = DefaultParallel
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = DefaultMaxTokens
	}
	if opts.Base == nil {
		opts.Base = Base
	}
	bases := map[string]*core.Prompt{} // baseKey to the prompt
	for _, c := range cases {
		k := baseKey(c)
		if _, ok := bases[k]; ok || c.Prompt != nil {
			continue
		}
		b, err := opts.Base(ctx, roleOf(c), c.Team)
		if err != nil {
			return nil, fmt.Errorf("tooleval: the %s prompt: %w", k, err)
		}
		bases[k] = b
	}
	type job struct {
		m      Model
		v      Variant
		c      Case
		n      int
		prompt *core.Prompt
	}
	var jobs []job
	rep := &Report{}
	for _, m := range models {
		rep.Models = append(rep.Models, m.Name)
	}
	for _, v := range variants {
		vr := VariantReport{Name: v.Name, Tools: map[string]int{}}
		var all []core.ToolSpec // every tool of the prompts asked, each name once
		for _, k := range sortedKeys(bases) {
			all = append(all, bases[k].Tools...)
		}
		for _, c := range cases {
			if c.Prompt != nil {
				all = append(all, c.Prompt.Tools...)
			}
		}
		tools, err := v.Apply(unique(all))
		if err != nil {
			return nil, err
		}
		for _, t := range tools {
			vr.Tools[t.Name] = Size(t)
			vr.Bytes += Size(t)
		}
		rep.Variants = append(rep.Variants, vr)
		for _, c := range cases {
			p, err := Build(bases[baseKey(c)], c, v)
			if err != nil {
				return nil, err
			}
			for _, m := range models {
				for n := 1; n <= opts.Samples; n++ {
					jobs = append(jobs, job{m: m, v: v, c: c, n: n, prompt: p})
				}
			}
		}
	}
	out := make([]Sample, len(jobs))
	sem := make(chan struct{}, opts.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, j job) {
			defer wg.Done()
			defer func() { <-sem }()
			s := ask(ctx, j.m, j.prompt, j.c.Expect, opts)
			s.Model, s.Variant, s.Case, s.N = j.m.Name, j.v.Name, j.c.ID, j.n
			out[i] = s
			if opts.OnSample != nil {
				mu.Lock()
				opts.OnSample(s)
				mu.Unlock()
			}
		}(i, j)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rep.Samples = out
	tally(rep, cases)
	return rep, nil
}

// sortedKeys is the keys of m in order.
func sortedKeys(m map[string]*core.Prompt) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// unique is tools with each name once, the first kept.
func unique(tools []core.ToolSpec) []core.ToolSpec {
	seen := map[string]bool{}
	var out []core.ToolSpec
	for _, t := range tools {
		if !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	return out
}

// baseKey names the base prompt of c: its role, and its team when it names one.
func baseKey(c Case) string {
	if len(c.Team) == 0 {
		return roleOf(c)
	}
	return roleOf(c) + " (team " + strings.Join(c.Team, ",") + ")"
}

// ask sends one prompt to m, again on an error worth retrying, and checks the answer.
func ask(ctx context.Context, m Model, base *core.Prompt, e Expect, opts Options) Sample {
	p := *base
	p.Model = m.Model.ID
	p.Params.MaxTokens = opts.MaxTokens
	if m.Model.MaxOutput > 0 && p.Params.MaxTokens > m.Model.MaxOutput {
		p.Params.MaxTokens = m.Model.MaxOutput
	}
	var s Sample
	start := time.Now()
	defer func() { s.Seconds = time.Since(start).Seconds() }()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rctx, cancel := context.WithTimeout(ctx, opts.Timeout)
		var thinking, text, tools int // what streamed before the answer was complete, which says why one never came
		var mu sync.Mutex
		resp, err := m.Provider.Do(rctx, &provider.Request{Prompt: &p, Label: "tooleval:" + m.Name}, func(e provider.Event) {
			mu.Lock()
			defer mu.Unlock()
			switch e.Kind {
			case provider.EvThinking:
				thinking += len(e.Text)
			case provider.EvText:
				text += len(e.Text)
			case provider.EvToolStart:
				tools++
			case provider.EvReset:
				thinking, text, tools = 0, 0, 0
			}
		})
		timedOut := rctx.Err() != nil && ctx.Err() == nil
		cancel()
		if err != nil {
			s.Error = clip(err.Error(), 300)
			if timedOut {
				mu.Lock()
				s.Error = fmt.Sprintf("no answer within %s (streamed %d bytes of reasoning, %d of text, %d tool calls)", opts.Timeout, thinking, text, tools)
				mu.Unlock()
				return s
			}
			if pe, ok := provider.AsError(err); ok && pe.Retryable() && ctx.Err() == nil {
				continue
			}
			return s
		}
		s.Error = ""
		if resp.CostUSD != nil {
			s.USD += *resp.CostUSD
		} else {
			s.USD += m.Model.Price.USD(resp.Usage)
		}
		s.Pass, s.Why = Check(resp.Turn.ToolCalls(), p.Tools, e)
		return s
	}
	return s
}

// tally fills the variant and case summaries of rep from its samples.
func tally(rep *Report, cases []Case) {
	vi := map[string]int{}
	for i, v := range rep.Variants {
		vi[v.Name] = i
	}
	byCase := map[string]*CaseReport{}
	why := map[string]map[string]map[string]int{} // case, variant, reason -> count
	for _, c := range cases {
		cr := &CaseReport{ID: c.ID, Source: c.Source, Failure: c.Failure, Passed: map[string]int{}, Asked: map[string]int{}, Why: map[string]string{}}
		byCase[c.ID] = cr
		why[c.ID] = map[string]map[string]int{}
	}
	for _, s := range rep.Samples {
		rep.USD += s.USD
		v := &rep.Variants[vi[s.Variant]]
		cr := byCase[s.Case]
		if s.Error != "" {
			v.Errors++
			continue
		}
		v.Asked++
		cr.Asked[s.Variant]++
		if s.Pass {
			v.Passed++
			cr.Passed[s.Variant]++
			continue
		}
		if why[s.Case][s.Variant] == nil {
			why[s.Case][s.Variant] = map[string]int{}
		}
		why[s.Case][s.Variant][s.Why]++
	}
	for _, c := range cases {
		cr := byCase[c.ID]
		for v, reasons := range why[c.ID] {
			best, n := "", 0
			for r, k := range reasons {
				if k > n || (k == n && r < best) {
					best, n = r, k
				}
			}
			cr.Why[v] = best
		}
		rep.Cases = append(rep.Cases, *cr)
	}
}

// clip shortens s to at most n bytes.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
