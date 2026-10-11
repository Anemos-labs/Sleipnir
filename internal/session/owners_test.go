package session_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// webOwners gives every path under web/ to the frontend role.
type webOwners struct{}

func (webOwners) OwnersOf(rel string) []string {
	if strings.HasPrefix(rel, "web/") {
		return []string{"frontend"}
	}
	return nil
}

// Options.Owners reaches the swarm's write guard.
func TestOwnersReachTheSwarmsWriteGuard(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Swarm, o.Workers, o.Owners = true, 2, webOwners{}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.Swarm == nil {
		t.Fatal("no swarm")
	}
	err = s.Swarm.Leases.BeforeWrite("be-1", filepath.Join(repo, "web", "app.css"))
	if err == nil || !strings.Contains(err.Error(), "web/app.css belongs to the frontend role") {
		t.Fatalf("a write to an owned path: %v", err)
	}
	if err := s.Swarm.Leases.BeforeWrite("be-1", filepath.Join(repo, "main.go")); err != nil {
		t.Fatalf("a write to a path nobody owns: %v", err)
	}
}
