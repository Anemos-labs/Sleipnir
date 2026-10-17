package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// A read-only role's refusal says what the role does instead. The plan profile's own words ("Present a plan instead of acting") sent
// a reviewer in a trial to write a plan when it had asked to run the tests.
func TestAReadOnlyRolesRefusalSaysWhatItDoesInstead(t *testing.T) {
	got := readOnlyRoleReason("reviewer", "role reviewer: plan mode (read-only): touch x is not a read-only command. Present a plan instead of acting")
	for _, want := range []string{"the reviewer role is read-only: touch x is not a read-only command", "node --test", "reports what should change"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "Present a plan") {
		t.Errorf("still tells the reviewer to present a plan: %q", got)
	}
	for _, other := range []string{
		"plan mode (read-only): touch x is not a read-only command. Present a plan instead of acting", // the session is planning
		"role reviewer: denied by rule Bash(rm:*)",
	} {
		if got := readOnlyRoleReason("reviewer", other); got != other {
			t.Errorf("%q was rewritten to %q", other, got)
		}
	}
}

// Through the role requester: in a session that may act, the reviewer's refusal is the read-only one; in a planning session it is
// the session's own plan-mode refusal (nobody acts while the session plans), which the requester returns unchanged by design.
func TestAReviewersRefusalThroughTheRequester(t *testing.T) {
	for _, c := range []struct {
		mode perm.Mode
		want string
	}{
		{perm.ModeBypass, "the reviewer role is read-only: touch x is not a read-only command"},
		{perm.ModePlan, "Present a plan instead of acting"},
	} {
		eng, err := perm.NewEngine(perm.Config{Mode: c.mode, Root: t.TempDir(), Home: t.TempDir(),
			Roles: map[string]perm.RoleProfile{"reviewer": {Mode: perm.ModePlan, Allow: []string{"Bash(go test:*)"}}}})
		if err != nil {
			t.Fatal(err)
		}
		rr := roleRequester{inner: eng, role: Role{Name: "reviewer", ReadOnly: true}, denyWrites: "the reviewer role is read-only: report findings instead of changing files"}
		d := rr.Check(context.Background(), perm.Request{Agent: "rv-1", Tool: "bash", Command: "touch x"})
		if d.Allow || !strings.Contains(d.Reason, c.want) {
			t.Errorf("%s session: %+v, want a refusal with %q", c.mode, d, c.want)
		}
	}
}

// A reviewer type-checks a TypeScript project with the project's own tsc: npx --no-install runs it (npx installs nothing then), and a
// plain npx, which would install a package it does not find, is refused with the form that works.
func TestAReviewerTypeChecksThroughNpxNoInstall(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	eng, err := perm.NewEngine(perm.Config{Mode: perm.ModeBypass, Root: root, Home: t.TempDir(),
		Roles: map[string]perm.RoleProfile{"reviewer": {Mode: perm.ModePlan, Allow: perm.ReadOnlyAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	rr := roleRequester{inner: eng, role: Role{Name: "reviewer", ReadOnly: true}, denyWrites: "the reviewer role is read-only: report findings instead of changing files"}
	check := func(cmd string) perm.Decision {
		return rr.Check(context.Background(), perm.Request{Agent: "rv-1", Tool: "bash", Command: cmd, Cwd: root})
	}
	for _, cmd := range []string{"cd web && npx --no-install tsc --noEmit", "cd web && npx --no tsc --noEmit 2>&1 | tail -20"} {
		if d := check(cmd); !d.Allow {
			t.Errorf("%s: %+v", cmd, d)
		}
	}
	d := check("cd web && npx tsc --noEmit")
	if d.Allow || !strings.Contains(d.Reason, "npx --no-install tsc --noEmit") {
		t.Errorf("a plain npx: %+v, want a refusal naming npx --no-install", d)
	}
}
