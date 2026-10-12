package session_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A team session gets the default team, one slot of each role; the manager's pin carries the team card, every worker's pin says how
// documentation is kept, and the docs worker's says it keeps it by itself. Without auto-docs the text is the plain one.
func TestDefaultTeamAndTheCardsInThePins(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Swarm, o.Workers, o.DefaultTeam = true, 8, true
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := len(s.Team()); n != 8 {
		t.Fatalf("the default team has %d slots: %+v", n, s.Team())
	}
	mgr, _ := s.Swarm.Role("manager")
	for _, want := range []string{"Your team", "architect ×1", "devops ×1", "docs ×1", "standing (auto)", "Documentation is automatic", "web: fetch, search"} {
		if !strings.Contains(mgr.Pin, want) {
			t.Errorf("the manager's pin lacks %q:\n%s", want, mgr.Pin)
		}
	}
	be, _ := s.Swarm.Role("backend")
	if !strings.Contains(be.Pin, "Documentation is kept by the team's docs worker") {
		t.Errorf("a worker is not told how documentation is kept:\n%s", be.Pin)
	}
	dc, _ := s.Swarm.Role("docs")
	if !strings.Contains(dc.Pin, "standing documentation keeper") || !s.Swarm.AutoDocs() {
		t.Errorf("the docs worker is not told it runs by itself:\n%s", dc.Pin)
	}

	off := false
	o = opts(t, repo, client, model)
	o.Swarm, o.Workers, o.DefaultTeam, o.AutoDocs = true, 8, true, &off
	s2, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	mgr2, _ := s2.Swarm.Role("manager")
	be2, _ := s2.Swarm.Role("backend")
	if s2.Swarm.AutoDocs() || strings.Contains(mgr2.Pin, "Documentation is automatic") || strings.Contains(be2.Pin, "docs worker") {
		t.Error("--auto-docs=false: docs is an ordinary role")
	}
}

// A team named slot by slot is the team: two backends and no docs, with no auto-docs to run.
func TestACustomTeamIsTheSlotsGiven(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Swarm, o.DefaultTeam = true, true
	for _, r := range []string{"architect", "scout", "backend", "backend", "frontend", "devops", "tester", "reviewer"} {
		o.Team = append(o.Team, session.TeamSlot{Role: r})
	}
	o.Workers = len(o.Team)
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st := s.Swarm.TeamState(); !strings.Contains(st, "backend 0/2") || strings.Contains(st, "docs") {
		t.Errorf("TeamState = %q", st)
	}
	if s.Swarm.AutoDocs() {
		t.Error("a team without a docs slot runs no auto-docs")
	}
	team := append(append([]session.TeamSlot(nil), o.Team...), session.TeamSlot{Role: "fullstack"})
	o = opts(t, repo, client, model)
	o.Swarm, o.DefaultTeam, o.Team, o.Workers = true, true, team, len(team)
	if _, err := session.New(context.Background(), o); err == nil || !strings.Contains(err.Error(), "no worker role named fullstack") {
		t.Errorf("a legacy role is not a slot of a new team: %v", err)
	}
}

// The manager may inspect: every read a manager was refused in a real session (a pipe-free chain of reads, absolute paths inside
// the project, git -C, gh's reading commands) is allowed, and writes stay refused. Read-only roles may reach the web as far as the
// session's mode allows (default mode: it asks), never refused outright.
func TestTheManagerMayInspectButNotWrite(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Swarm, o.Workers = true, 8
	o.Mode = perm.ModeDefault
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, cmd := range []string{
		"ls -la " + filepath.Join(repo, "server"),
		"git -C " + repo + " status --short",
		"cd " + repo + " && ls -la server",
		"git ls-tree --name-only HEAD",
		"git -C " + repo + " log --oneline",
	} {
		d := s.Perm.Check(ctx, perm.Request{Agent: "mgr", Role: "manager", Tool: "bash", Command: cmd, Cwd: repo})
		if !d.Allow {
			t.Errorf("the manager may run %q: %+v", cmd, d)
		}
	}
	// gh and the tests are as the session's mode has them (default mode asks the person), never refused by the manager's own profile
	for _, cmd := range []string{"gh --version", "gh auth status", "gh pr view 12", "go test ./..."} {
		d := s.Perm.Check(ctx, perm.Request{Agent: "mgr", Role: "manager", Tool: "bash", Command: cmd, Cwd: repo})
		if strings.Contains(d.Reason, "plan mode") {
			t.Errorf("the manager's profile refuses %q: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{"touch notes.txt", "rm -rf server", "git commit -am x"} {
		if d := s.Perm.Check(ctx, perm.Request{Agent: "mgr", Role: "manager", Tool: "bash", Command: cmd, Cwd: repo, Writes: true}); d.Allow {
			t.Errorf("the manager must not run %q: %+v", cmd, d)
		}
	}
	for _, role := range []string{"manager", "scout", "architect"} {
		d := s.Perm.Check(ctx, perm.Request{Agent: "x", Role: role, Tool: "web_fetch", Input: json.RawMessage(`{"url":"https://github.com/Anemos-labs/Sleipnir"}`), Network: true, Cwd: repo})
		if strings.Contains(d.Reason, "plan mode") {
			t.Errorf("%s is refused the web outright: %+v", role, d)
		}
	}
}
