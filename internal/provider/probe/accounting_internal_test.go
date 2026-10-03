package probe

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

type pricedProbe struct {
	calls, priced atomic.Int64
	costMode      string
	failLabel     string
	cancel        context.CancelFunc
}

func (*pricedProbe) Profile() provider.Profile { return provider.Profile{} }

func (p *pricedProbe) Do(ctx context.Context, req *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	p.calls.Add(1)
	if req.Label == p.failLabel && p.failLabel != "" {
		if p.cancel != nil {
			p.cancel()
			return nil, ctx.Err()
		}
		return nil, errors.New("fixture refused request")
	}
	response := &provider.Response{Turn: core.Turn{Blocks: []core.Block{core.Text("ok")}}, Usage: core.Usage{CacheReadTokens: 1024, OutputTokens: 1}}
	known := p.costMode == "all" || p.costMode == "partial" && req.Label != "probe:cache-warm" || p.costMode == "warmup" && strings.HasPrefix(req.Label, "probe:warmup-")
	if known {
		cost := 1.0 // Synthetic fixture unit, never a network charge.
		response.CostUSD = &cost
		p.priced.Add(1)
	}
	return response, nil
}

func TestProbeAccountsForEveryDeepRequest(t *testing.T) {
	for _, mode := range []string{"all", "partial", "warmup", "none"} {
		t.Run(mode, func(t *testing.T) {
			p := &pricedProbe{costMode: mode}
			report, err := Run(context.Background(), Config{Provider: p, Deep: true, Capture: true})
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(report.Steps)) != p.calls.Load() || report.TotalUSD != float64(p.priced.Load()) {
				t.Fatalf("report has %d requests costing %g units; provider handled %d with %d priced", len(report.Steps), report.TotalUSD, p.calls.Load(), p.priced.Load())
			}
			warmup := 0
			seen := map[string]bool{}
			for _, step := range report.Steps {
				if strings.HasPrefix(step.Name, "warmup-") {
					warmup++
					if seen[step.Name] || !step.OK || step.Usage.TotalInput() == 0 {
						t.Errorf("invalid warm-up step: %+v", step)
					}
					seen[step.Name] = true
				}
			}
			if warmup != 8 {
				t.Errorf("reported %d warm-up requests, want 8", warmup)
			}
			if report.Findings.CostReported != (mode != "none") {
				t.Errorf("cost reporting capability = %t for mode %s", report.Findings.CostReported, mode)
			}
			if report.CostComplete != (mode == "all") {
				t.Errorf("cost completeness = %t for mode %s", report.CostComplete, mode)
			}
			text := report.Text()
			if mode != "all" && !strings.Contains(text, "total unavailable") {
				t.Errorf("an incomplete cost must not look like a total:\n%s", text)
			}
		})
	}
}

func TestFailedWarmupRemainsVisibleAndInconclusive(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		p := &pricedProbe{costMode: "all", failLabel: "probe:warmup-cold-0"}
		if canceled {
			p.cancel = cancel
		}
		report, err := Run(ctx, Config{Provider: p, Deep: true})
		cancel()
		if canceled && !errors.Is(err, context.Canceled) || !canceled && err != nil {
			t.Errorf("canceled=%t: err=%v", canceled, err)
		}
		if report.Findings.WarmupNeeded != nil || int64(len(report.Steps)) != p.calls.Load() {
			t.Fatalf("failed burst was hidden or classified: %+v; reported=%d actual=%d", report.Findings, len(report.Steps), p.calls.Load())
		}
		failed := false
		for _, step := range report.Steps {
			if step.Name == "warmup-cold-0" {
				failed = !step.OK && step.Detail != ""
			}
			if strings.HasPrefix(step.Name, "warmup-warm-") {
				t.Error("sent a warm burst after the cold burst failed")
			}
		}
		if !failed || report.CostComplete || !strings.Contains(report.Text(), "total unavailable") {
			t.Fatalf("missing failure or incomplete cost indication:\n%s", report.Text())
		}
	}
}

func TestCancellationStopsAfterACapabilityHandlesItsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &pricedProbe{costMode: "all", failLabel: "probe:reasoning", cancel: cancel}
	report, err := Run(ctx, Config{Provider: p, Deep: true, Capture: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if len(report.Steps) == 0 || report.Steps[len(report.Steps)-1].Name != "reasoning" {
		t.Fatalf("continued sending requests after cancellation: %+v", report.Steps)
	}
	if report.CostComplete || report.TotalUSD != float64(p.priced.Load()) || int64(len(report.Steps)) != p.calls.Load() {
		t.Fatalf("lost cost information on cancellation: %+v", report)
	}
}
