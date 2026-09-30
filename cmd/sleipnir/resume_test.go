package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/provider/mock"
)

// resumeRepo is a git repository configured for a mock endpoint, with private
// state and home directories.
func resumeRepo(t *testing.T, url string) (repo, state string) {
	t.Helper()
	repo, state = t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"providers":{"mock":{"base_url":%q}},"models":{"default":"mock/mock-1"}}`, url)
	if err := os.WriteFile(filepath.Join(repo, ".sleipnir", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEIPNIR_HOME", state)
	t.Setenv("HOME", t.TempDir())
	return repo, state
}

// `run --continue` picks up this project's newest session: the second request
// carries the first exchange, and the log stays in the same directory.
func TestRunContinuePicksUpTheLastSession(t *testing.T) {
	var mu sync.Mutex
	var history []string // the whole conversation as the model saw it on each call
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		var sb strings.Builder
		for _, m := range c.Messages {
			sb.WriteString(m.Role + ": " + m.Content + "\n")
		}
		mu.Lock()
		history = append(history, sb.String())
		mu.Unlock()
		return mock.Reply{Text: "noted"}
	})
	ts := srv.Start()
	defer ts.Close()
	repo, state := resumeRepo(t, ts.URL)
	base := []string{"--cwd", repo, "--mode", "bypass", "--no-web", "--trust-project", "--quiet"}

	if err := cmdRun(context.Background(), append([]string{"the password is lark"}, base...)); err != nil {
		t.Fatal(err)
	}
	if err := cmdRun(context.Background(), append([]string{"--continue", "what was the password?"}, base...)); err != nil {
		t.Fatalf("continue: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(history) != 2 {
		t.Fatalf("expected two requests, got %d", len(history))
	}
	if !strings.Contains(history[1], "the password is lark") || !strings.Contains(history[1], "noted") {
		t.Errorf("the resumed request lost the earlier conversation:\n%s", history[1])
	}
	if !strings.Contains(history[1], "what was the password?") {
		t.Errorf("the resumed request lost the new prompt:\n%s", history[1])
	}
	ents, err := os.ReadDir(filepath.Join(state, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Errorf("resuming must continue in the same session directory, found %d sessions", len(ents))
	}

	// The listing says which sessions can be continued.
	out := capture(t, func() { err = cmdSessions(context.Background(), nil) })
	if err != nil || !strings.Contains(out, "↺") {
		t.Errorf("sessions must mark a resumable session: %v\n%s", err, out)
	}

	// An unknown id is a clear error, not a fresh session.
	err = cmdRun(context.Background(), append([]string{"--resume", "no-such-session", "hi"}, base...))
	if err == nil || !strings.Contains(err.Error(), "no session") {
		t.Errorf("resuming an unknown session: %v", err)
	}
	// And so is --continue with nothing to continue.
	repo2, _ := resumeRepo(t, ts.URL)
	err = cmdRun(context.Background(), []string{"--continue", "hi", "--cwd", repo2, "--mode", "bypass", "--no-web", "--trust-project", "--quiet"})
	if err == nil || !strings.Contains(err.Error(), "no sessions") && !strings.Contains(err.Error(), "resume") {
		t.Errorf("--continue with no sessions: %v", err)
	}
}

func TestResumeFlagsAreAlternatives(t *testing.T) {
	get := func(args ...string) (string, error) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		f := resumeFlags(fs)
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		return f()
	}
	for _, c := range []struct {
		args []string
		want string
		bad  bool
	}{
		{nil, "", false},
		{[]string{"--continue"}, "latest", false},
		{[]string{"--resume", "20260930-abc"}, "20260930-abc", false},
		{[]string{"--resume", "latest"}, "latest", false},
		{[]string{"--resume", "x", "--continue"}, "", true},
	} {
		got, err := get(c.args...)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%v: got %q, %v", c.args, got, err)
		}
	}
}

func TestCompactSlashSaysWhatItDid(t *testing.T) {
	s := chatSession(t, false, nil)
	var out strings.Builder
	compactNow(context.Background(), s, "the entry point", &out)
	if !strings.Contains(out.String(), "nothing to compact") {
		t.Fatalf("before any goal: %q", out.String())
	}
	if _, err := s.Run(context.Background(), "say hi"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	compactNow(context.Background(), s, "", &out)
	// One short exchange has nothing worth folding, and the command says so
	// rather than failing or rewriting the prefix for nothing.
	if !strings.Contains(out.String(), "nothing to compact") && !strings.Contains(out.String(), "compacted") {
		t.Fatalf("after a short turn: %q", out.String())
	}
}
