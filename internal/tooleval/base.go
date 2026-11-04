package tooleval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// Base is the first prompt the agent of a role sends in a team session of this build: the system text and every tool spec,
// exactly as the harness assembles them, with the opening messages. A scenario is played on top of it, so a case written
// once keeps testing the descriptions the code has now. It runs a real session in a scratch directory against a provider
// that answers nothing: no request leaves the machine. team names the role of each worker slot (a role as often as it has
// slots); without one the session has the default team, one slot per role.
func Base(ctx context.Context, role string, team []string) (*core.Prompt, error) {
	if role == "" {
		role = "manager"
	}
	dir, err := os.MkdirTemp("", "sleipnir-tooleval-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ws := filepath.Join(dir, "project")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("# project\n"), 0o644); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cap := &capture{role: role, done: cancel}
	m := cost.Model{ID: "tooleval", ContextTokens: 200_000, MaxOutput: 8192}
	var slots []session.TeamSlot
	for _, r := range team {
		slots = append(slots, session.TeamSlot{Role: r})
	}
	s, err := session.New(rctx, session.Options{
		Cwd: ws, Root: ws, Home: filepath.Join(dir, "home"), Dir: filepath.Join(dir, "session"),
		Provider: cap, ModelInfo: &m, Model: m.ID,
		Mode: perm.ModeYolo, NoWeb: true, NoMCP: true, NoMemory: true, TrustProject: true, Offline: true,
		Swarm: role != "solo", Workers: 8, DefaultTeam: role != "solo", Team: slots, // the team a chat or run session gets, with its card
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_, _ = s.Run(rctx, "Build the project the README describes.")
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.got == nil {
		return nil, fmt.Errorf("no %s request was made", role)
	}
	return cap.got, nil
}

// capture is a provider that keeps the first prompt of the agent it waits for and ends the run.
type capture struct {
	role string
	done func()
	mu   sync.Mutex
	got  *core.Prompt
}

// errCaptured ends every request: the run has nothing to do once the prompt is kept.
var errCaptured = errors.New("tooleval: prompt captured")

func (c *capture) Profile() provider.Profile { return provider.Profile{Name: "tooleval"} }

func (c *capture) Do(_ context.Context, req *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.got == nil && req.Prompt != nil && agentOf(req.Label, c.role) {
		p := *req.Prompt
		c.got = &p
		c.done()
	}
	return nil, &provider.Error{Kind: provider.ErrAuth, Message: errCaptured.Error()}
}

// agentOf reports whether request label (agent id "." number) is a conversation request of role's agent: the manager is
// "mgr", a solo session's agent "main".
func agentOf(label, role string) bool {
	id, _, _ := strings.Cut(label, ".")
	switch role {
	case "manager":
		return id == "mgr"
	case "solo":
		return id == "main"
	}
	return strings.HasPrefix(id, role)
}
