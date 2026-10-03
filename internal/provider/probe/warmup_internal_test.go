package probe

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// burstBoundaryCache publishes whole-message entries only after all four cold
// requests have started. This separates prefill from later reuse without sleeps.
type burstBoundaryCache struct {
	mu      sync.Mutex
	ready   chan struct{}
	calls   int
	entries map[string]bool
	enabled bool
}

func (*burstBoundaryCache) Profile() provider.Profile {
	return provider.Profile{Cache: cost.CacheModel{MessageBoundaries: true}}
}

func (p *burstBoundaryCache) Do(ctx context.Context, req *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	wire, err := json.Marshal(req.Prompt)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	cold := p.calls < 4
	p.calls++
	cached := 0
	if cold {
		p.entries[string(wire)] = true
		if p.calls == 4 {
			close(p.ready)
		}
	} else if p.enabled && p.entries[string(wire)] {
		cached = 2000
	}
	p.mu.Unlock()
	if cold {
		select {
		case <-p.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &provider.Response{Usage: core.Usage{InputTokens: 2000 - cached, CacheReadTokens: cached, OutputTokens: 1}}, nil
}

func TestWarmupPreservesMessageBoundaries(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		p := &burstBoundaryCache{ready: make(chan struct{}), entries: map[string]bool{}, enabled: enabled}
		r := runner{cfg: Config{Provider: p, Log: func(string) {}}, rep: &Report{}, nonce: "fixture"}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.warmup(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got := r.rep.Findings.WarmupNeeded; got == nil || *got != enabled {
			t.Fatalf("warmup needed = %v with cache enabled=%t", got, enabled)
		}
		p.mu.Lock()
		calls := p.calls
		p.mu.Unlock()
		if calls != 8 {
			t.Errorf("sent %d requests, want two bursts of four", calls)
		}
	}
}
