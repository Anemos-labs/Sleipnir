package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A model whose endpoint is out hands its requests to the fallback model (--role-model fallback=M), and the session says so.
func TestAModelWhoseEndpointIsOutFallsBackToTheFallbackModel(t *testing.T) {
	defer func(after time.Duration, base time.Duration) { session.FallbackAfter, agent.RetryBase = after, base }(session.FallbackAfter, agent.RetryBase)
	session.FallbackAfter, agent.RetryBase = 0, time.Millisecond
	var mu sync.Mutex
	served := map[string]int{}
	srv := mock.New(mock.Config{}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		served[c.Model]++
		mu.Unlock()
		if c.Model == "acme/main" {
			return mock.Reply{Fault: &mock.Fault{Status: 503, Message: "No endpoints are currently available"}}
		}
		return mock.Reply{Text: "answered by " + c.Model}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: ts.URL}}
	s, err := session.New(context.Background(), session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Offline: true,
		Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
		RoleModels: map[string]string{session.FallbackRole: "gw/acme/backup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var listed bool
	for _, r := range s.RoleModels() {
		listed = listed || r.Role == session.FallbackRole && r.Model == "gw/acme/backup" && r.From == "--role-model"
	}
	if !listed {
		t.Errorf("the roles table leaves out the fallback: %+v", s.RoleModels())
	}
	out, err := s.Run(context.Background(), "say hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "answered by acme/backup") {
		t.Fatalf("answer %q", out.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	if served["acme/main"] == 0 || served["acme/backup"] == 0 {
		t.Fatalf("served %v", served)
	}
	logged, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if !strings.Contains(string(logged), "its requests go to the fallback model gw/acme/backup") {
		t.Fatal("the session did not say its requests went to the fallback model")
	}
}
