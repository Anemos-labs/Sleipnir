package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// swarm.max_workers is the ceiling on a session's workers. --swarm N asks for a manager
// and N workers: a request over the ceiling is refused before anything is created,
// a request at or under it is honoured, and with no ceiling any size goes.
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

	for _, tc := range []struct {
		name             string
		ceiling, workers int
	}{{"at the ceiling", 4, 4}, {"under it", 8, 3}, {"no ceiling", 0, 40}} {
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
