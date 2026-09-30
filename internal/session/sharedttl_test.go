package session_test

import (
	"context"
	"testing"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
)

// cache.shared_ttl asks an explicit-breakpoint provider to keep the shared and role
// layers for an hour instead of five minutes. The first request of a run writes
// those layers, so the tier its cache write is billed in shows which lifetime the
// breakpoints asked for: for a single agent and for a swarm alike (the swarm used to
// build its own agents' policy without it).
func TestCacheSharedTTLReachesTheBreakpoints(t *testing.T) {
	for _, swarmed := range []bool{false, true} {
		for _, ttl := range []string{"5m", "1h"} {
			name := "single agent " + ttl
			if swarmed {
				name = "swarm " + ttl
			}
			t.Run(name, func(t *testing.T) {
				repo := newRepo(t)
				srv, o := startAnthropic(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "nothing to do"} })
				cfg := config.Defaults()
				cfg.Cache.SharedTTL = ttl
				o.Config = cfg
				o.Cwd, o.Root = repo, repo
				if swarmed {
					o.Swarm, o.MaxAgents = true, 3
				}
				s, err := session.New(context.Background(), o)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if _, err := s.Run(context.Background(), "say when there is nothing to do"); err != nil {
					t.Fatal(err)
				}
				stats := srv.AnthropicStats()
				if len(stats) == 0 || stats[0].Err != "" {
					t.Fatalf("no successful request: %+v", stats)
				}
				first := stats[0]
				switch ttl {
				case "1h":
					if first.Write1h == 0 {
						t.Errorf("shared_ttl 1h: nothing was written to the one-hour tier: %+v", first)
					}
				default:
					if first.Write1h != 0 {
						t.Errorf("shared_ttl 5m: %d tokens went to the one-hour tier", first.Write1h)
					}
					if first.Write5m == 0 {
						t.Errorf("shared_ttl 5m: nothing was written to the five-minute tier: %+v", first)
					}
				}
			})
		}
	}
}
