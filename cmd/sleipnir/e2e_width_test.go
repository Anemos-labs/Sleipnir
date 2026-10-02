package main

// What the commands print, on a terminal of 80 columns, the width a window opens at: no line is wider than 79 characters, because a
// terminal breaks a longer one in the middle of a word (a help text went to 370 characters, a table of models to 99). wrap_test.go holds
// the pieces; this runs the real commands, on a pseudo-terminal.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/ptytest"
)

// wide runs `sleipnir args...` on a terminal of 80 columns and returns what it printed and its exit status.
func wide(t *testing.T, w *world, args ...string) (string, int) {
	t.Helper()
	s := ptytest.Start(t, w.cmd(args...), ptytest.Size(24, 80))
	if err := s.ExpectEOF(e2eGuard); err != nil {
		t.Fatalf("sleipnir %s: %v", strings.Join(args, " "), err)
	}
	st, err := s.Wait(e2eGuard)
	if err != nil {
		t.Fatalf("sleipnir %s: %v", strings.Join(args, " "), err)
	}
	return strings.ReplaceAll(s.Transcript(), "\r\n", "\n"), st.ExitCode()
}

// fitsEighty fails the test for each line of out that a terminal of 80 columns would break.
func fitsEighty(t *testing.T, args []string, out string) {
	t.Helper()
	for _, l := range tooWide(out, 79) {
		t.Errorf("sleipnir %s: a line of %d characters: %q", strings.Join(args, " "), utf8.RuneCountInString(l), l)
	}
}

var listedCommand = regexp.MustCompile(`(?m)^  ([a-z][a-z-]*)  +\S`)

// commandNames are the names a help text lists ("  name   what it does").
func commandNames(help string) []string {
	var out []string
	for _, m := range listedCommand.FindAllStringSubmatch(help, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestEveryHelpFitsAnEightyColumnTerminal(t *testing.T) {
	w := newWorld(t, "")
	check := func(args ...string) string {
		t.Helper()
		out, code := wide(t, w, args...)
		if code != 0 {
			t.Errorf("sleipnir %s exited %d:\n%s", strings.Join(args, " "), code, out)
		}
		fitsEighty(t, args, out)
		return out
	}
	var top strings.Builder
	usage(&top)
	names := commandNames(top.String())
	if len(names) < 15 {
		t.Fatalf("the usage text lists %d commands: %q", len(names), names)
	}
	for _, name := range names {
		if name != "version" {
			check(name, "-h")
		}
	}
	check("rl", "help")
	var subs []string
	for name := range rlCommands {
		subs = append(subs, name)
	}
	sort.Strings(subs)
	for _, name := range subs {
		check("rl", name, "-h")
	}
	for _, group := range []string{"taskgen", "tasks"} {
		for _, sub := range commandNames(check("rl", group, "help")) {
			check("rl", group, sub, "-h")
		}
	}
}

func TestReportsFitAnEightyColumnTerminal(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "w") // a short path (macOS's $TMPDIR is 50 characters): a path wider than the terminal cannot be broken
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	w := newWorldAt(t, root, "")
	if err := os.WriteFile(filepath.Join(w.project, ".sleipnir", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(w.state, "sessions", "20261002-023227-7ba0c7")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"seq":1,"type":"session.start","data":{"model":"heimdall/deepseek/deepseek-v4.1-flash"}}` + "\n" +
		`{"seq":2,"type":"user.input","data":{"text":"Fix the failing test in the slugify package, and add one for the case of a title with accents"}}` + "\n"
	if err := os.WriteFile(filepath.Join(d, "events.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"config"}, 0, "swarm.isolation"},
		{[]string{"init"}, 1, "already exists"}, // an error that names a long path
		{[]string{"schedule", "list"}, 0, "no scheduled jobs"},
		{[]string{"sessions"}, 0, "Fix the failing test"},
		{[]string{"run"}, 1, "a prompt is required"}, // no goal: one line, with an example
	} {
		out, code := wide(t, w, tc.args...)
		if code != tc.code || !strings.Contains(strings.Join(strings.Fields(out), " "), tc.want) { // a wrap may fall between the words of want
			t.Errorf("sleipnir %s exited %d, want %d, and %q:\n%s", strings.Join(tc.args, " "), code, tc.code, tc.want, out)
		}
		fitsEighty(t, tc.args, out)
	}
}
