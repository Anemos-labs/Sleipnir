package session_test

import (
	"context"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A run that nobody can answer does not ask: instead of the default mode, which would only refuse its edits and commands, it runs
// in bypass. A mode given on the command line, another mode in the configuration, or an --allow rule is the person's policy and
// holds; "default" in the configuration is what sleipnir init writes for everyone, not a choice for unattended runs.
func TestAnUnattendedRunWithNoModeChosenDoesNotAsk(t *testing.T) {
	client, model := startMock(t, nil)
	for _, c := range []struct {
		name     string
		set      func(o *session.Options)
		want     perm.Mode
		inferred bool
	}{
		{"nothing chosen", func(o *session.Options) {}, perm.ModeBypass, true},
		{"someone to ask", func(o *session.Options) { o.Unattended = false }, perm.ModeDefault, false},
		{"--mode default", func(o *session.Options) { o.Mode = perm.ModeDefault }, perm.ModeDefault, false},
		{"--mode accept-edits", func(o *session.Options) { o.Mode = perm.ModeAcceptEdits }, perm.ModeAcceptEdits, false},
		{"an --allow rule", func(o *session.Options) { o.Allow = []string{"Bash(go test:*)"} }, perm.ModeDefault, false},
		{"the default mode in the configuration", func(o *session.Options) {
			writeUserConfig(t, o.Home, `{"permissions": {"mode": "default"}}`)
		}, perm.ModeBypass, true},
		{"another mode in the configuration", func(o *session.Options) {
			writeUserConfig(t, o.Home, `{"permissions": {"mode": "accept-edits"}}`)
		}, perm.ModeAcceptEdits, false},
		{"other configuration", func(o *session.Options) {
			writeUserConfig(t, o.Home, `{"permissions": {"allow": ["Bash(make:*)"]}}`)
		}, perm.ModeBypass, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepo(t)
			o := opts(t, repo, client, model)
			o.Mode, o.Unattended = "", true
			c.set(&o)
			s, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got := s.Perm.Mode(); got != c.want || s.UnattendedBypass() != c.inferred {
				t.Fatalf("mode %q (inferred %v), want %q (%v)", got, s.UnattendedBypass(), c.want, c.inferred)
			}
		})
	}
}
