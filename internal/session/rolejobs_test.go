package session_test

import (
	"context"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// Background jobs are session-wide, and the engine alone answers "allow" to a
// bash_output that touches no files: a read-only role would read another agent's job.
// Its profile denies the job tools by name; a writer keeps them, and so does the
// engine's answer for a role the profile does not name.
func TestReadOnlyRolesAreDeniedTheJobToolsByName(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Mode = perm.ModeDefault
	o.Swarm, o.Workers = true, 4
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(role, tool string) perm.Decision {
		return s.Perm.Check(context.Background(), perm.Request{Agent: "x-1", Role: role, Tool: tool, Summary: tool})
	}
	for _, role := range []string{"reviewer", "scout"} {
		for _, tool := range []string{"bash_output", "bash_kill"} {
			if d := check(role, tool); d.Allow {
				t.Errorf("%s may use %s: %+v", role, tool, d)
			}
		}
	}
	if d := check("backend", "bash_output"); !d.Allow {
		t.Errorf("a writer must keep bash_output: %+v", d)
	}
}
