package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestInstructionAllowanceReachesProviderAndRemainsStable(t *testing.T) {
	for _, tc := range []struct {
		name               string
		window, configured int
		wantFull           bool
	}{
		{"large model default", 200000, 0, true},
		{"small model default", 32768, 0, false},
		{"explicit allowance", 32768, 24000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			body := strings.Repeat("Follow the project conventions.\n", 1380) + "FINAL-PROJECT-RULE\n"
			for name, text := range map[string]string{"AGENTS.md": body, "SLEIPNIR.local.md": "LOCAL-OVERRIDE\n"} {
				if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			var prompts []string
			client, model := startMock(t, func(c *mock.Call) mock.Reply {
				for _, m := range c.Messages {
					if m.Role == "user" && strings.Contains(m.Content, "<shared-context>") {
						_, tail, _ := strings.Cut(m.Content, "<shared-context>")
						shared, _, _ := strings.Cut(tail, "</shared-context>")
						mu.Lock()
						prompts = append(prompts, shared)
						mu.Unlock()
					}
				}
				return mock.Reply{Text: "ok"}
			})
			model.ContextTokens = tc.window
			o := opts(t, repo, client, model)
			o.NoRecon = true
			o.Config = config.Defaults()
			o.Config.Cache.InstructionMaxTokens = tc.configured
			sink := &noticeSink{}
			o.Sink = sink
			s, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for i := 0; i < 2; i++ {
				if _, err := s.Run(context.Background(), "Acknowledge the project rules."); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(prompts) != 2 || prompts[0] != prompts[1] {
				t.Fatalf("shared instructions absent or changed between requests: %d payloads", len(prompts))
			}
			if got := strings.Contains(prompts[0], "FINAL-PROJECT-RULE"); got != tc.wantFull {
				t.Errorf("final project rule present=%v, want %v", got, tc.wantFull)
			}
			if !strings.Contains(prompts[0], "LOCAL-OVERRIDE") {
				t.Error("local override missing from the provider payload")
			}
			if warned := strings.Contains(sink.all(), "Truncated or omitted: AGENTS.md"); warned == tc.wantFull {
				t.Errorf("overflow notice=%v, full instructions=%v", warned, tc.wantFull)
			}
		})
	}
}
