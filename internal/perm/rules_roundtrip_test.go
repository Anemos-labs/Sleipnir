package perm

import "testing"

// The rules Rules returns build an engine again (the hooks runner judges a condition with the session's ask rules).
func TestRulesBuildAnEngineAgain(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	ask := []string{"Edit(./.sleipnir/**)", "Edit(~/.claude/**)", "Bash(git status)", "WebFetch(domain:example.com)", "Read(/etc/**)"}
	e, err := NewEngine(Config{Mode: ModeDefault, Root: root, Home: home, Ask: ask})
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewEngine(Config{Mode: ModeDefault, Root: root, Home: home, Ask: e.Rules(Ask)})
	if err != nil {
		t.Fatalf("%v: %q", err, e.Rules(Ask))
	}
	if got, want := again.Rules(Ask), e.Rules(Ask); len(got) != len(want) {
		t.Fatalf("%q, want %q", got, want)
	}
}
