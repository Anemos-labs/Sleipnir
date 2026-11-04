package session_test

import (
	"context"
	"os"
	"path/filepath"
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
	readOnlyRole(t, repo, "auditor")
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
	for _, role := range []string{"auditor"} {
		for _, tool := range []string{"bash_output", "bash_kill"} {
			if d := check(role, tool); d.Allow {
				t.Errorf("%s may use %s: %+v", role, tool, d)
			}
		}
	}
	for _, role := range []string{"backend", "reviewer", "scout"} { // every built-in role writes
		if d := check(role, "bash_output"); !d.Allow {
			t.Errorf("%s, a writer, must keep bash_output: %+v", role, d)
		}
	}
}

// readOnlyRole defines a project role, name, with readonly: true in repo (every built-in role writes), for a test of the
// read-only policy.
func readOnlyRole(t *testing.T, repo, name string) {
	t.Helper()
	dir := filepath.Join(repo, ".sleipnir", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: reviews the code without changing it\nreadonly: true\n---\nYou review the code and report findings; you change nothing.\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
