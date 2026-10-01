package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func chatSession(t *testing.T, trust bool, files map[string]string) *session.Session {
	t.Helper()
	return chatSessionWith(t, trust, files, nil, nil)
}

// chatSessionWith is chatSession with a say in the options of the session and in what the model answers (nil: "ok").
func chatSessionWith(t *testing.T, trust bool, files map[string]string, adjust func(*session.Options), respond func(*mock.Call) mock.Reply) *session.Session {
	t.Helper()
	agent.RetryBase = time.Millisecond
	repo, home := t.TempDir(), t.TempDir()
	for rel, body := range files {
		p := filepath.Join(repo, rel)
		if strings.HasPrefix(rel, "~/") {
			p = filepath.Join(home, rel[2:])
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if respond == nil {
		respond = func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} }
	}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, respond)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	prof := openaichat.DefaultProfile("mock", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Profile: &prof})
	m := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, MaxOutput: 4096, Cache: cost.OpenAICacheModel(), Price: cost.Price{InputPerM: 1, OutputPerM: 2}}
	o := session.Options{
		Cwd: repo, Root: repo, Home: home, Dir: t.TempDir(), Provider: client, ModelInfo: &m, Model: m.ID,
		Mode: perm.ModeDefault, NoWeb: true, TrustProject: trust, Offline: true, NoRecon: true,
	}
	if adjust != nil {
		adjust(&o)
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSlashExpandsCustomCommandsAndUserSkills(t *testing.T) {
	s := chatSession(t, true, map[string]string{
		".sleipnir/commands/fix.md":           "---\ndescription: fix an issue\n---\nFix issue $1 and add a regression test.\n",
		"~/.sleipnir/skills/deploy/SKILL.md":  "---\nname: deploy\ndescription: deploy the service\ndisable-model-invocation: true\n---\nRun the deploy checklist for $ARGUMENTS.\n",
		".sleipnir/commands/frontend/lint.md": "Lint the frontend.\n",
		".sleipnir/commands/permissions.md":   "impersonation\n",
	})
	ctx := context.Background()

	p, _, ok, err := expandSlash(ctx, s, "fix", "1234")
	if err != nil || !ok || p != "Fix issue 1234 and add a regression test." {
		t.Fatalf("custom command: %q %v %v", p, ok, err)
	}
	p, _, ok, err = expandSlash(ctx, s, "frontend:lint", "")
	if err != nil || !ok || !strings.Contains(p, "Lint the frontend.") {
		t.Fatalf("namespaced command: %q %v %v", p, ok, err)
	}
	// A skill only the user may invoke still works as a slash command.
	p, _, ok, err = expandSlash(ctx, s, "deploy", "staging")
	if err != nil || !ok || !strings.Contains(p, "Run the deploy checklist for staging.") {
		t.Fatalf("user skill: %q %v %v", p, ok, err)
	}
	if _, _, ok, _ := expandSlash(ctx, s, "nope", ""); ok {
		t.Error("an unknown name must not be handled")
	}
	if _, _, ok, _ := expandSlash(ctx, s, "permissions", ""); ok {
		t.Error("a repository must not be able to define a built-in name")
	}
}

func TestSlashHelpListsCustomCommands(t *testing.T) {
	s := chatSession(t, true, map[string]string{".sleipnir/commands/fix.md": "---\ndescription: fix an issue\n---\nx\n"})
	var out strings.Builder
	printCustom(s, &out)
	if !strings.Contains(out.String(), "/fix") || !strings.Contains(out.String(), "fix an issue") {
		t.Fatalf("help output:\n%s", out.String())
	}
}

// chat is the interactive command: its swarm is not held to its board and finished
// work wakes the manager (session.Options.Interactive). run and swarm are batch runs.
func TestChatSessionsAreInteractive(t *testing.T) {
	o := chatOptions(session.Options{Swarm: true, MaxAgents: 5})
	if !o.Interactive || !o.Swarm || o.MaxAgents != 5 {
		t.Fatalf("chatOptions = %+v", o)
	}
	if (session.Options{}).Interactive {
		t.Fatal("a session is a batch run unless its command says otherwise")
	}
}
