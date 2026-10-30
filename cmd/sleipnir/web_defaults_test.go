package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A team whose isolation nobody chose works in trees of its own in the root of a repository with a commit, and in a folder that the
// request makes a repository; elsewhere the session's own default (none) stands.
func TestATeamInARepositoryDefaultsToWorktrees(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	g, err := gitx.Init(ctx, repo, false)
	if err != nil {
		t.Skipf("git: %v", err)
	}
	empty := t.TempDir()
	if got := defaultIsolation(ctx, repo, false); got != "" {
		t.Errorf("a repository without a commit: %q", got)
	}
	if _, err := g.CommitEmpty(ctx, firstCommit, gitx.DefaultAuthor); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dir     string
		newRepo bool
		want    string
	}{
		{repo, false, config.IsolationWorktree},
		{sub, false, ""},
		{empty, false, ""},
		{empty, true, config.IsolationWorktree},
	} {
		if got := defaultIsolation(ctx, c.dir, c.newRepo); got != c.want {
			t.Errorf("%s (new repository %v): %q, want %q", c.dir, c.newRepo, got, c.want)
		}
	}
}

func TestConfigIsolationDistinguishesDefaultFromExplicitNone(t *testing.T) {
	project := webWorld(t)
	home := t.TempDir()
	h := &webHostImpl{base: session.Options{Home: home}}
	if h.configIsolation(project) {
		t.Fatal("built-in none was treated as an explicit choice")
	}
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), []byte(`{"swarm":{"isolation":"none"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !h.configIsolation(project) {
		t.Fatal("explicit none in the host's configured home was ignored")
	}
}

func TestConfigIsolationIgnoresUntrustedSharedIsolation(t *testing.T) {
	for _, filename := range []string{"config.json", "config.local.json"} {
		for _, mode := range []string{"none", "shared", "worktree"} {
			t.Run(filename+"/"+mode, func(t *testing.T) {
				project := webWorld(t)
				h := &webHostImpl{base: session.Options{Home: t.TempDir()}}
				dir := filepath.Join(project, ".sleipnir")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, filename), []byte(`{"swarm":{"isolation":"`+mode+`"}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if got := h.configIsolation(project); got != (mode == "worktree") {
					t.Fatalf("explicit isolation = %v for untrusted %s", got, mode)
				}
			})
		}
	}
}

// A request that arrives while the session starts waits for the start instead of being refused, and is answered when the start
// ends (here: it failed, so there is no session); a closed tab answers at once.
func TestARequestWaitsForTheSessionToStart(t *testing.T) {
	tab := &webTab{starting: true}
	tab.cond = sync.NewCond(&tab.mu)
	go func() {
		time.Sleep(50 * time.Millisecond)
		tab.mu.Lock()
		tab.starting = false
		tab.cond.Broadcast()
		tab.mu.Unlock()
	}()
	start := time.Now()
	if s := tab.readySession(context.Background()); s != nil {
		t.Fatalf("a failed start gave a session")
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("the request did not wait for the start: %v", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	tab.mu.Lock()
	tab.starting = true
	tab.mu.Unlock()
	if s := tab.readySession(ctx); s != nil {
		t.Fatal("a cancelled wait gave a session")
	}
	if ctx.Err() == nil {
		t.Error("readySession returned before its context ended")
	}
}

// The New session dialog picks worktree isolation for a team in a repository unless someone named the isolation: the defaults say
// whether --isolation of sleipnir web or swarm.isolation of the configuration did, and the built-in "none" is no one's choice.
func TestTheDefaultsSayWhetherTheIsolationWasNamed(t *testing.T) {
	project := webWorld(t)
	home, _ := os.UserHomeDir()
	defaults := func(flag string) newSessionDefaults {
		return (&webHostImpl{d: webDefaults{Cwd: project, Isolation: flag}}).defaults()
	}
	if d := defaults(""); d.IsolationSet || d.Isolation != "none" {
		t.Errorf("nothing named: isolation %q, set %v", d.Isolation, d.IsolationSet)
	}
	if d := defaults("none"); !d.IsolationSet || d.Isolation != "none" {
		t.Errorf("--isolation none: isolation %q, set %v", d.Isolation, d.IsolationSet)
	}
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), []byte(`{"swarm":{"isolation":"none"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := defaults(""); !d.IsolationSet || d.Isolation != "none" {
		t.Errorf("swarm.isolation none in the person's file: isolation %q, set %v", d.Isolation, d.IsolationSet)
	}
}

// A request that outlasts startWait while the start goes on is told to send again, not that the session did not start: an
// eighteen-agent team resumed after a container restart took six minutes to start, and a goal resumed in that time was refused three
// times as "the session did not start". A start that ended without a session still says it did not start.
func TestARequestThatOutlastsAStartIsToldItIsStillStarting(t *testing.T) {
	tab := &webTab{starting: true}
	tab.cond = sync.NewCond(&tab.mu)
	var we *wire.Error
	if err := tab.noSession(); !errors.As(err, &we) || we.Status != http.StatusServiceUnavailable || we.Code != "starting" || !strings.Contains(we.Msg, "send this again") {
		t.Errorf("a start under way = %v, want 503 starting", err)
	}
	tab.starting, tab.restarting = false, true
	if err := tab.noSession(); !errors.As(err, &we) || we.Code != "starting" {
		t.Errorf("a restart under way = %v, want starting", err)
	}
	tab.restarting = false
	if err := tab.noSession(); !errors.As(err, &we) || we.Status != http.StatusConflict || we.Code != "no_session" {
		t.Errorf("a start that ended without a session = %v, want 409 no_session", err)
	}
	tab.startErr = "no key"
	if err := tab.noSession(); !errors.As(err, &we) || we.Code != "no_session" || !strings.Contains(we.Msg, "no key") {
		t.Errorf("a failed start = %v, want no_session with the reason", err)
	}
	tab.starting, tab.closed = true, true
	if err := tab.noSession(); !errors.As(err, &we) || we.Code != "no_session" {
		t.Errorf("a closed tab = %v, want no_session", err)
	}
}

// sleipnir web --team NAME with no --model: the New session dialog starts on the team's manager model, not on whatever model the
// provider suggests, and an explicit --model still wins.
func TestTheDefaultsStartOnTheTeamsManagerModel(t *testing.T) {
	project := webWorld(t)
	defaults := func(model string) newSessionDefaults {
		return (&webHostImpl{d: webDefaults{Cwd: project, Model: model, RoleModels: map[string]string{"manager": "heimdallm/lead", "judge": "heimdallm/judge"}}}).defaults()
	}
	if d := defaults(""); d.Model != "heimdallm/lead" {
		t.Errorf("no --model: the dialog's model is %q, want the manager's", d.Model)
	}
	if d := defaults("heimdallm/chosen"); d.Model != "heimdallm/chosen" {
		t.Errorf("--model given: the dialog's model is %q", d.Model)
	}
}
