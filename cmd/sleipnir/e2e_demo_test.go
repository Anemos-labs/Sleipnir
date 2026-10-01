package main

// `sleipnir demo` on a terminal: the team is watched in the live cockpit, the last screen stays until the person leaves it, and the
// report follows. As with replay and watch the screen, the keys and the way out are the terminal's business, so these tests run the
// real command on a pseudo-terminal and look at what an emulator makes of what it wrote.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/ptytest"
)

// startDemo runs `sleipnir demo args...` on a 100x36 terminal; --dir puts the session where the test can look at it.
func startDemo(t *testing.T, args ...string) (demoDir string, s *ptytest.Session) {
	t.Helper()
	w := newWorld(t, "")
	demoDir = filepath.Join(w.tmp, "demo")
	cmd := w.cmd(append([]string{"demo", "--dir", demoDir}, args...)...)
	return demoDir, ptytest.Start(t, cmd, ptytest.Size(36, 100))
}

// The handbook is over in a second, so the screen that is left is the last one of a finished team: the keys say what q does now, the
// other screens can be looked at, and the report is printed on the normal screen after the program has left the alternate one.
func TestE2EDemoShowsTheTeamAndKeepsTheLastScreenUntilQ(t *testing.T) {
	dir, s := startDemo(t, "--scenario", "handbook", "--topics", "2")
	scr := waitScreen(t, s, 100, 36, "the team done, its screen kept", shows("SLEIPNIR", "■ ended", "q leave, then the report"))
	if !scr.AltScreen() {
		t.Error("the cockpit must be on the alternate screen, so that the report is not written over it")
	}
	if strings.Contains(scr.String(), "Sleipnir demo:") || strings.Contains(scr.String(), "hit ratio") {
		t.Errorf("the report belongs after the screen, not on it:\n%s", scr.String())
	}

	s.WriteString("c")
	waitScreen(t, s, 100, 36, "the cache view of the finished session", shows("cache ·", "its latest prompt", "■ ended"))
	s.WriteString("m")
	waitScreen(t, s, 100, 36, "the mail view", shows("mail ·"))
	s.WriteString("o")
	waitScreen(t, s, 100, 36, "the cockpit again", shows("riders"))

	s.WriteString("q")
	exitStatus(t, s, 0)
	after := screenOf(s, 100, 36)
	if after.AltScreen() {
		t.Error("the alternate screen must be left before the report")
	}
	if _, _, visible := after.Cursor(); !visible {
		t.Error("the cursor is back after the program")
	}
	for _, want := range []string{"Sleipnir demo:", "a 2-topic handbook", "hit ratio", "See it again: sleipnir replay", "Try it on a real model"} {
		if !strings.Contains(after.String(), want) {
			t.Errorf("the report lacks %q:\n%s", want, after.String())
		}
	}
	if len(after.Rejected) > 0 {
		t.Errorf("the program wrote sequences it must never write: %v", after.Rejected)
	}
	if _, err := os.Stat(filepath.Join(dir, "session", "events.jsonl")); err != nil {
		t.Errorf("the session the cockpit showed was not kept: %v", err)
	}
}

// Leaving before the team is done stops it: the screen goes, the terminal is as it was, and the run says where what it did is.
func TestE2EDemoStopsTheTeamWhenThePersonLeavesEarly(t *testing.T) {
	if !haveCommands("git", "sh") {
		t.Skip("the shop needs git and sh")
	}
	dir, s := startDemo(t, "--scenario", "shop", "--scale", "20")
	scr := waitScreen(t, s, 100, 36, "the team at work", shows("SLEIPNIR", "● live", "q stop the demo"))
	if !scr.AltScreen() {
		t.Error("the cockpit must be on the alternate screen")
	}
	s.WriteString("q")
	exitStatus(t, s, 0)
	after := screenOf(s, 100, 36)
	if after.AltScreen() {
		t.Error("the alternate screen must be left")
	}
	if !strings.Contains(after.String(), "stopped before the team was done") {
		t.Errorf("a team that was stopped says so:\n%s", after.String())
	}
	if strings.Contains(after.String(), "hit ratio") {
		t.Errorf("a team that did not finish has no report:\n%s", after.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "session", "events.jsonl")); err != nil {
		t.Errorf("what the team did so far must be kept: %v", err)
	}
}

// A run that fails has nothing to hold the screen for: the terminal is given back at once and the error is what is printed.
func TestE2EDemoThatFailsLeavesTheScreenAtOnce(t *testing.T) {
	w := newWorld(t, "")
	blocker := filepath.Join(w.tmp, "a-file")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := ptytest.Start(t, w.cmd("demo", "--scenario", "handbook", "--dir", filepath.Join(blocker, "sub")), ptytest.Size(36, 100))
	exitStatus(t, s, 1)
	after := screenOf(s, 100, 36)
	if after.AltScreen() {
		t.Error("the alternate screen must be left")
	}
	if _, _, visible := after.Cursor(); !visible {
		t.Error("the cursor must be visible again")
	}
	// the terminal wraps a long error where the window ends, so the words are read without the line breaks
	if !strings.Contains(strings.Join(strings.Fields(after.String()), ""), "notadirectory") {
		t.Errorf("the error is what there is to say:\n%s", after.String())
	}
}

// --plain is the report and nothing else, on a terminal too.
func TestE2EDemoPlainOnATerminalDrawsNoScreen(t *testing.T) {
	_, s := startDemo(t, "--plain", "--topics", "2")
	exitStatus(t, s, 0)
	out := s.Transcript()
	if strings.Contains(out, "\x1b[?1049h") {
		t.Errorf("--plain must not use the alternate screen:\n%q", out)
	}
	for _, want := range []string{"Sleipnir demo:", "hit ratio", "Try it on a real model"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
}
