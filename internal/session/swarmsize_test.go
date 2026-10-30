package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// swarm.max_workers is the ceiling on a session's workers. --swarm N asks for a manager
// and N workers: a request over the ceiling is refused before anything is created,
// a request at or under it is honoured, and with no ceiling DefaultMaxTeamWorkers is the ceiling.
func TestSwarmSizeIsBoundedBySwarmMaxWorkers(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	build := func(ceiling, workers int) (*session.Session, session.Options, error) {
		cfg := config.Defaults()
		cfg.Swarm.MaxWorkers = ceiling
		o := opts(t, repo, client, model)
		o.Config = cfg
		o.Swarm, o.Workers = true, workers
		s, err := session.New(context.Background(), o)
		return s, o, err
	}

	_, o, err := build(4, 8) // --swarm 8 against a ceiling of 4
	if err == nil {
		t.Fatal("a swarm over swarm.max_workers was started")
	}
	for _, want := range []string{"8 workers requested", "swarm.max_workers caps a session at 4 workers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(o.Dir, "events.jsonl")); statErr == nil {
		t.Error("a refused swarm still opened its event log")
	}

	// Without swarm.max_workers a session still has a ceiling, so that a typo (--swarm 1000) is not a thousand agents; a negative
	// size is refused too.
	for _, workers := range []int{session.DefaultMaxTeamWorkers + 1, 1000, -3} {
		if _, _, err := build(0, workers); err == nil {
			t.Errorf("--swarm %d was started", workers)
		}
	}
	if _, _, err := build(0, 1000); err == nil || !strings.Contains(err.Error(), "swarm.max_workers") {
		t.Errorf("the refusal does not say how to allow more: %v", err)
	}

	for _, tc := range []struct {
		name             string
		ceiling, workers int
	}{{"at the ceiling", 4, 4}, {"under it", 8, 3}, {"no ceiling", 0, 40}, {"a raised ceiling", 100, 100}} {
		s, _, err := build(tc.ceiling, tc.workers)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := s.Swarm.MaxWorkers(); got != tc.workers {
			t.Errorf("%s: the swarm allows %d workers, want the requested %d", tc.name, got, tc.workers)
		}
		s.Close()
	}
}

// A role model for a role the session does not have would never apply; the person
// would believe a worker ran on the model they named.
func TestRoleModelForAnUnknownRoleIsRefused(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	ts := httptest.NewServer(http.NotFoundHandler()) // a second endpoint for the role's model; never called
	defer ts.Close()
	build := func(roleModels map[string]string, mailman bool) (*session.Session, error) {
		cfg := config.Defaults()
		cfg.Providers = map[string]config.Provider{"other": {BaseURL: ts.URL}}
		o := opts(t, repo, client, model)
		o.Config = cfg
		o.Swarm, o.Workers, o.Mailman = true, 5, &mailman
		o.RoleModels = roleModels
		return session.New(context.Background(), o)
	}

	_, err := build(map[string]string{"backnd": "other/small", "reviewer": "other/small"}, false)
	if err == nil {
		t.Fatal("a role model for a misspelt role was accepted")
	}
	for _, want := range []string{`no role named "backnd"`, "backend", "reviewer", "manager"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), `named "reviewer"`) {
		t.Errorf("a real role was reported unknown: %v", err)
	}
	if strings.Contains(err.Error(), "mailman") {
		t.Errorf("the mailman is not a role of a session without --mailman: %v", err)
	}

	// The mailman is a role only while its mode is on.
	if _, err := build(map[string]string{"mailman": "other/small"}, false); err == nil {
		t.Error("a mailman model was accepted with the mailman off")
	} else if !strings.Contains(err.Error(), "the mailman is off") {
		t.Errorf("the refusal of a mailman model does not say the mailman is off: %v", err)
	}
	for _, roleModels := range []map[string]string{{"reviewer": "other/small"}, {"manager": "other/big", "backend": "other/small"}} {
		s, err := build(roleModels, false)
		if err != nil {
			t.Fatalf("%v: %v", roleModels, err)
		}
		s.Close()
	}
	s, err := build(map[string]string{"mailman": "other/small"}, true)
	if err != nil {
		t.Fatalf("mailman model with the mailman on: %v", err)
	}
	s.Close()
}

// Without --swarm there is one agent and nothing for a role model to apply to: say so.
func TestRoleModelWithoutASwarmWarns(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.RoleModels = map[string]string{"reviewer": "mock/small"}
	sink := &notices{}
	o.Sink = sink
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !sink.has("--role-model has no effect without --swarm") {
		t.Fatalf("no warning about the ignored --role-model; notices: %v", sink.logs)
	}
	// A role that serves a single agent too (the judge) is applied, so it is not called ignored.
	o.Config = config.Defaults()
	o.Config.Providers = map[string]config.Provider{"gw": {BaseURL: "http://127.0.0.1:1"}}
	o.Offline, o.RoleModels = true, map[string]string{session.JudgeRole: "gw/acme/judge"}
	quiet := &notices{}
	o.Sink, o.Dir = quiet, t.TempDir()
	s2, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if quiet.has("has no effect") {
		t.Errorf("the judge role was called ignored: %v", quiet.logs)
	}
}

// A team of eight workers can have all its workers writing at once: the cap of four concurrent writers left a manager unable to start the fifth
// worker of "--swarm 8" (and, with a verifier over the whole repository, deadlocked the four that had finished their part).
func TestEveryWorkerOfATeamMayWriteAtOnce(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	for _, workers := range []int{2, 8, 11} {
		o := opts(t, repo, client, model)
		o.Swarm, o.Workers = true, workers
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := s.Swarm.MaxWriters(), max(4, workers); got != want {
			t.Errorf("a team of %d workers may have %d writers at once, want %d", workers, got, want)
		}
		s.Close()
	}
}

// A team that names its manager's model has named the session's model: the manager is the session's own agent, so a
// team file with models.manager (or --role-model manager=…) needs no --model beside it.
func TestManagerRoleModelIsTheSessionModelWhenNoneIsNamed(t *testing.T) {
	repo := newRepo(t)
	agent.RetryBase = time.Millisecond
	ts := mock.New(mock.Config{}, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} }).Start()
	defer ts.Close()
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"other": {BaseURL: ts.URL}}
	o := session.Options{Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Swarm: true, Workers: 8,
		RoleModels: map[string]string{"manager": "other/big"}}
	t.Setenv("SLEIPNIR_MODEL", "")
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatalf("a team naming its manager's model did not start: %v", err)
	}
	defer s.Close()
	if got := s.ModelRef(); got != "other/big" {
		t.Errorf("session model = %q, want the manager's other/big", got)
	}
}
