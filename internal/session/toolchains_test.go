package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The shared context says which build and test commands are installed (the machine's half of the survey), so a worker knows before it
// writes a test whether it can run it; a session without the survey (NoRecon) leaves it out.
func TestSharedContextListsTheToolchains(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	for _, noRecon := range []bool{false, true} {
		o := opts(t, repo, client, model)
		o.NoRecon = noRecon
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		has := s.Shared != nil && strings.Contains(s.Shared.Text(), "Commands on PATH: ")
		if has == noRecon {
			t.Errorf("NoRecon=%v: toolchain list in the shared layer = %v", noRecon, has)
		}
		s.Close()
	}
}
