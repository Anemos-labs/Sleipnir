//go:build unix

package shell

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A command that runs pkill -f with a pattern found in its own command line ends itself on SIGTERM, before the rest of it runs;
// its result says why, rather than leave a bare exit code 143 to puzzle over.
func TestACommandThatPkillsItselfIsToldWhy(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("pkill"); err != nil {
		t.Skip("no pkill")
	}
	h := newHarness(t)
	res := h.bash(h.env("a"), `pkill -f selfkill-marker-e7; echo still here`)
	if strings.Contains(res.Text, "still here") {
		t.Skipf("pkill did not match its own shell here: %q", res.Text)
	}
	if !strings.Contains(res.Text, "[exit code 143]") || !strings.Contains(res.Text, "most likely ended itself") {
		t.Errorf("result does not explain the self-kill:\n%s", res.Text)
	}
}

// A shell that exits with status 143 on its own, after a pkill -f that cannot match itself, was not signalled: no note.
func TestAShellThatExitsWith143IsNotToldItEndedItself(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res := h.bash(h.env("a"), `pkill -f '[s]elfkill-marker-n5'; exit 143`)
	if !strings.Contains(res.Text, "[exit code 143]") {
		t.Fatalf("want exit code 143:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "ended itself") {
		t.Errorf("a normal exit with 143 is said to have ended itself:\n%s", res.Text)
	}
}

// The same holds for a background job: bash_output says why it ended once it has.
func TestABackgroundJobThatPkillsItselfIsToldWhy(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("pkill"); err != nil {
		t.Skip("no pkill")
	}
	h := newHarness(t)
	env := h.env("a")
	id := h.startJob(env, `pkill -f selfkill-marker-j3; echo still here`)
	var text string
	waitFor(t, "the job to end", 10*time.Second, func() bool {
		text = mustText(t, h.output(env, id, map[string]any{"since": 0}))
		return !strings.Contains(text, " running;")
	})
	if strings.Contains(text, "still here") {
		t.Skipf("pkill did not match its own shell here: %q", text)
	}
	if !strings.Contains(text, "exited 143") || !strings.Contains(text, "most likely ended itself") {
		t.Errorf("bash_output does not explain the self-kill:\n%s", text)
	}
}

func TestSelfKillNote(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		command string
		signal  int
		reason  killReason
		want    bool
	}{
		{`pkill -f "bin/slotwise serve"`, 15, killNone, true},
		{`cd app && pkill -9 -f server.rb; ruby server.rb`, 9, killNone, true},
		{`pgrep --full puma | xargs kill`, 15, killNone, true},
		{`pkill -fx node`, 15, killNone, true},
		{`pkill slotwise`, 15, killNone, false},                 // matches process names, not the shell's command line
		{`pkill -f slotwise`, 0, killNone, false},               // ended normally, even with status 143 from a child
		{`pkill -f slotwise; sleep 99`, 15, killTimeout, false}, // the harness ended it
		{`kill -f 12`, 15, killNone, false},
		{`echo pkill; ls -f`, 15, killNone, false},
	} {
		if got := selfKillNote(c.command, c.signal, c.reason) != ""; got != c.want {
			t.Errorf("selfKillNote(%q, signal %d, %d) noted %v, want %v", c.command, c.signal, c.reason, got, c.want)
		}
	}
}
