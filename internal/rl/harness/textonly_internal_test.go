package harness

import (
	"context"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

type profiled struct{ prof provider.Profile }

func (p *profiled) Profile() provider.Profile        { return p.prof }
func (p *profiled) SetProfile(prof provider.Profile) { p.prof = prof }
func (p *profiled) Do(context.Context, *provider.Request, func(provider.Event)) (*provider.Response, error) {
	return nil, nil
}

// A text-only model behind the rollout's wrappers (budget, pacing, outage watch) is marked on the client that renders its
// requests, so a text-only fallback of a rollout gets notes in place of images.
func TestMarkTextOnlyReachesThroughTheRolloutWrappers(t *testing.T) {
	inner := &profiled{}
	watch := &outageWatch{clock: nil}
	var p provider.Provider = &watched{Provider: inner, w: watch}
	p = &paced{Provider: p}
	p = &budgeted{Provider: p, l: &limited{Provider: p}}
	if !provider.MarkTextOnly(p) || !inner.prof.NoImages {
		t.Fatalf("the wrapped client was not marked: %v", inner.prof.NoImages)
	}
}
