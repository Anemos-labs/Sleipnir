package main

import (
	"context"
	"testing"
)

func TestDemoCommandRunsWithoutAKey(t *testing.T) {
	for _, k := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	// --plain: the test runs in whatever terminal the developer has (`go test` in a package directory hands it over), and what it is about
	// is the run, not the screen
	if err := cmdDemo(context.Background(), []string{"--plain", "--topics", "4", "--dir", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

// What `sleipnir demo` runs when it is not told: the shop where it is watched (it takes about twenty seconds, which a cockpit is for),
// the handbook everywhere else (a second or two, and a report nobody has to wait for). What was asked for is always what runs.
func TestDemoPicksItsScenario(t *testing.T) {
	for _, c := range []struct {
		asked          string
		cockpit, tools bool
		want           string
	}{
		{"", true, true, "shop"},
		{"", true, false, "handbook"}, // no git or no sh: the shop cannot be built
		{"", false, true, "handbook"}, // a pipe, a file, --plain: no one to watch it
		{"", false, false, "handbook"},
		{"handbook", true, true, "handbook"},
		{"shop", false, true, "shop"},
		{"shop", false, false, "shop"}, // the run says what is missing
	} {
		if got := pickScenario(c.asked, c.cockpit, c.tools); got != c.want {
			t.Errorf("asked %q, cockpit %v, tools %v: %s, want %s", c.asked, c.cockpit, c.tools, got, c.want)
		}
	}
}
