package session_test

import (
	"context"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// tagged is a provider that a WrapProvider put in front of another.
type tagged struct{ provider.Provider }

// A rollout watches every model it calls for outages: the providers the session builds from the configuration (the main model,
// the roles') come to it behind Options.WrapProvider, so a role's endpoint going down pauses the run as the policy's does.
func TestEveryProviderTheSessionBuildsIsWrapped(t *testing.T) {
	repo := newRepo(t)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: "http://127.0.0.1:9/v1"}}
	var mu sync.Mutex
	var wrapped []string
	o := session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Offline: true,
		Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
		Swarm: true, Workers: 2, RoleModels: map[string]string{"reviewer": "gw/acme/small"},
		WrapProvider: func(p provider.Provider) provider.Provider {
			mu.Lock()
			wrapped = append(wrapped, p.Profile().BaseURL)
			mu.Unlock()
			return tagged{p}
		},
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.Provider.(tagged); !ok {
		t.Errorf("the main model's provider is %T, not the wrapper", s.Provider)
	}
	if len(wrapped) != 2 {
		t.Errorf("wrapped %d providers (%v), want the main model's and the reviewer's", len(wrapped), wrapped)
	}

	// A provider the caller hands over is the caller's: used as given.
	given := tagged{s.Provider}
	o.Provider, o.Model, o.RoleModels, o.Swarm, o.Dir = given, "acme/main", nil, false, t.TempDir()
	wrapped = nil
	s2, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Provider != provider.Provider(given) || len(wrapped) != 0 {
		t.Errorf("a given provider was wrapped: %T, %v", s2.Provider, wrapped)
	}
}
