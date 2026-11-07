package provider_test

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

// Marking a failover marks its primary only: the fallback is a model of its own, marked (or not) by what its catalogue says.
func TestMarkTextOnly(t *testing.T) {
	primary, secondary := &profiled{}, &profiled{}
	if !provider.MarkTextOnly(&provider.Failover{Primary: primary, Secondary: secondary}) {
		t.Fatal("not marked")
	}
	if !primary.prof.NoImages || secondary.prof.NoImages {
		t.Fatalf("primary %v secondary %v", primary.prof.NoImages, secondary.prof.NoImages)
	}
}

type wrapper struct{ provider.Provider }

func (w wrapper) Unwrap() provider.Provider { return w.Provider }

// A wrapper that takes no profile of its own (a session's WrapProvider) is seen through: the client it wraps renders the
// requests, so that is the one marked.
func TestMarkTextOnlyThroughAWrapper(t *testing.T) {
	inner := &profiled{}
	if !provider.MarkTextOnly(wrapper{inner}) || !inner.prof.NoImages {
		t.Fatalf("the wrapped client was not marked: %v", inner.prof.NoImages)
	}
	if provider.MarkTextOnly(struct{ provider.Provider }{inner}) {
		t.Fatal("a wrapper that neither takes a profile nor unwraps was reported marked")
	}
}
