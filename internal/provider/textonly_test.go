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
