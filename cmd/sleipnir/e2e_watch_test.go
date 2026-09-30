package main

// `sleipnir replay` and `sleipnir watch` on a terminal: what a person sees and can do. The program draws on the alternate screen,
// reads keys in raw mode and follows the window's size, all of which only a terminal decides, so these tests run the real command
// on a pseudo-terminal and look at the screen through a terminal emulator (internal/tui/vt) fed with what the program wrote.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/ptytest"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
	"github.com/reee344/sleipnir/internal/tui/vt"
)

// screenOf is the screen a terminal of cols x rows shows after what the program has written so far.
func screenOf(s *ptytest.Session, cols, rows int) *vt.Term {
	t := vt.New(cols, rows)
	_, _ = t.WriteString(s.Transcript())
	return t
}

// waitScreen waits until the screen satisfies ok and returns it; a screen that never does fails the test with what it showed.
func waitScreen(t *testing.T, s *ptytest.Session, cols, rows int, what string, ok func(*vt.Term) bool) *vt.Term {
	t.Helper()
	deadline := time.Now().Add(e2eGuard)
	for {
		scr := screenOf(s, cols, rows)
		if ok(scr) {
			return scr
		}
		if time.Now().After(deadline) {
			t.Fatalf("the screen never showed %s; it shows:\n%s\n--- transcript (%d bytes)", what, scr.String(), len(s.Transcript()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func shows(words ...string) func(*vt.Term) bool {
	return func(t *vt.Term) bool {
		text := t.String()
		for _, w := range words {
			if !strings.Contains(text, w) {
				return false
			}
		}
		return true
	}
}

// startReplay runs `sleipnir replay LOG --speed 0.05 args...` on a 100x36 terminal: a recording of a second played at a twentieth
// of its speed stays on the screen for as long as a test needs it to.
func startReplay(t *testing.T, args ...string) (*world, *ptytest.Session) {
	t.Helper()
	w := newWorld(t, "")
	log := statetest.DemoLogFile(t)
	cmd := w.cmd(append([]string{"replay", log, "--speed", "0.05"}, args...)...)
	return w, ptytest.Start(t, cmd, ptytest.Size(36, 100))
}

func exitStatus(t *testing.T, s *ptytest.Session, want int) {
	t.Helper()
	st, err := s.Wait(e2eGuard)
	if err != nil {
		t.Fatal(err)
	}
	if st.ExitCode() != want {
		t.Errorf("the command exited with %v, want status %d.\n%s", st, want, s.Transcript())
	}
	if err := s.ExpectEOF(e2eGuard); err != nil { // everything it wrote, before the transcript is read
		t.Error(err)
	}
	noCrash(t, s.Transcript())
}

// A replay draws the cockpit on the alternate screen, answers the keys, and puts the terminal back as it found it.
func TestE2EReplayIsAFullScreenProgram(t *testing.T) {
	_, s := startReplay(t)
	scr := waitScreen(t, s, 100, 36, "the cockpit", shows("SLEIPNIR", "▶ replay", "q quit"))
	if !scr.AltScreen() {
		t.Error("the program must draw on the alternate screen, where the scrollback is not touched")
	}
	if _, _, visible := scr.Cursor(); visible {
		t.Error("the cursor is hidden while the program draws")
	}

	s.WriteString("c")
	waitScreen(t, s, 100, 36, "the cache view", shows("cache ·", "its latest prompt"))
	s.WriteString("m")
	waitScreen(t, s, 100, 36, "the mail view", shows("mail ·", "who writes to whom"))
	s.WriteString("b")
	waitScreen(t, s, 100, 36, "the board view", shows("board ·", "task board"))
	s.WriteString("o")
	waitScreen(t, s, 100, 36, "the cockpit again", shows("riders"))
	s.WriteString("?")
	waitScreen(t, s, 100, 36, "the help", shows("keys", "pause and go on"))
	s.WriteString("x")
	waitScreen(t, s, 100, 36, "the help closed", func(t *vt.Term) bool { return !strings.Contains(t.String(), "pause and go on") })

	s.WriteString(" ")
	waitScreen(t, s, 100, 36, "a paused screen", shows("⏸ paused"))
	s.WriteString(" ")
	waitScreen(t, s, 100, 36, "the replay going on", func(t *vt.Term) bool { return !strings.Contains(t.String(), "paused") })

	s.WriteString("q")
	exitStatus(t, s, 0)
	after := screenOf(s, 100, 36)
	if after.AltScreen() {
		t.Error("the program left the alternate screen on its way out")
	}
	if _, _, visible := after.Cursor(); !visible {
		t.Error("the cursor is back after the program")
	}
	if len(after.Rejected) > 0 {
		t.Errorf("the program wrote sequences it must never write: %v", after.Rejected)
	}
}

// The keys are read in raw mode: Ctrl-C is a key, not a signal, and ends the program the way q does.
func TestE2EReplayQuitsOnCtrlC(t *testing.T) {
	_, s := startReplay(t)
	waitScreen(t, s, 100, 36, "the cockpit", shows("SLEIPNIR"))
	if err := s.SendCtrlC(); err != nil {
		t.Fatal(err)
	}
	exitStatus(t, s, 0)
	if screenOf(s, 100, 36).AltScreen() {
		t.Error("the alternate screen must be left")
	}
}

// A signal that ends the program still leaves the terminal as it was, and is reported as an interruption.
func TestE2EReplayRestoresTheTerminalOnSIGTERM(t *testing.T) {
	_, s := startReplay(t)
	waitScreen(t, s, 100, 36, "the cockpit", shows("SLEIPNIR"))
	if err := s.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exitStatus(t, s, 143)
	after := screenOf(s, 100, 36)
	if after.AltScreen() {
		t.Error("the alternate screen must be left when a signal ends the program")
	}
	if _, _, visible := after.Cursor(); !visible {
		t.Error("the cursor must be visible again")
	}
	if !strings.Contains(s.Transcript(), "interrupted") {
		t.Errorf("a program that was told to stop says so:\n%s", s.Transcript())
	}
}

// The screen follows the window: a new size is a new layout, every line of it inside the window.
func TestE2EReplayFollowsTheSizeOfTheWindow(t *testing.T) {
	_, s := startReplay(t)
	waitScreen(t, s, 100, 36, "the cockpit", shows("SLEIPNIR", "riders"))
	if err := s.Resize(24, 70); err != nil {
		t.Fatal(err)
	}
	// what the program writes after the resize is laid out for the new window: the emulator is the new size from the start of it
	mark := len(s.Transcript())
	waitScreen(t, s, 70, 24, "a cockpit of 70 columns", func(tm *vt.Term) bool {
		// the tail of the transcript, from the first byte written after the signal, is a whole frame at the new size
		fresh := vt.New(70, 24)
		_, _ = fresh.WriteString(s.Transcript()[mark:])
		text := fresh.String()
		return strings.Contains(text, "SLEIPNIR") && strings.Contains(text, "╰") && len(strings.Split(text, "\n")) == 24
	})
	s.WriteString("q")
	exitStatus(t, s, 0)
}

// Without a terminal there is no full screen: the command says what to do instead, and the text form works in a pipe.
func TestE2EReplayWithoutATerminal(t *testing.T) {
	w := newWorld(t, "")
	log := statetest.DemoLogFile(t)
	res := w.run("", "replay", log)
	if res.code != 1 || !strings.Contains(res.stderr, "--final") || !strings.Contains(res.stderr, "--record") {
		t.Errorf("replay on a pipe: status %d, stderr %q: want 1 and the two ways out", res.code, res.stderr)
	}
	if strings.ContainsRune(res.stdout, '\x1b') {
		t.Errorf("nothing may be drawn on a pipe: %q", res.stdout)
	}

	res = w.run("", "replay", log, "--final", "--view", "cache", "--cols", "90", "--rows", "30")
	if res.code != 0 {
		t.Fatalf("--final: status %d\n%s", res.code, res.stderr)
	}
	if strings.ContainsRune(res.stdout, '\x1b') {
		t.Errorf("--final is plain text: %q", res.stdout)
	}
	lines := strings.Split(strings.TrimSuffix(res.stdout, "\n"), "\n")
	if len(lines) != 30 || !strings.Contains(res.stdout, "cache · ") || !strings.Contains(res.stdout, "■ ended") {
		t.Errorf("--final printed %d lines; want 30 of the cache view of a finished session:\n%s", len(lines), res.stdout)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n > 90 {
			t.Errorf("line %d is %d characters wide: %q", i, n, l)
		}
	}

	out := filepath.Join(t.TempDir(), "demo.svg")
	res = w.run("", "replay", log, "--record", out, "--cols", "80", "--rows", "24", "--speed", "0.25", "--fps", "5")
	if res.code != 0 || !strings.Contains(res.stderr, "wrote") {
		t.Fatalf("--record: status %d\n%s", res.code, res.stderr)
	}
	svg, err := os.ReadFile(out)
	if err != nil || !strings.HasPrefix(string(svg), "<svg") || !strings.Contains(string(svg), "@keyframes") {
		t.Errorf("--record did not write an animated SVG (%v): %.200s", err, svg)
	}
}

func TestE2EReplayNamesWhatItCannotFind(t *testing.T) {
	w := newWorld(t, "")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"replay", "no-such-session"}, `no session "no-such-session"`},
		{[]string{"replay"}, "no sessions yet"},
		{[]string{"replay", w.tmp}, "holds no events.jsonl"},
		{[]string{"replay", "a", "b"}, "at most one session"},
		{[]string{"replay", "--view", "graph", "x"}, "unknown view"},
		{[]string{"replay", "--record", "x.svg", "--final", "latest"}, "alternatives"},
		{[]string{"watch", "no-such-session"}, `no session "no-such-session"`},
	} {
		res := w.run("", c.args...)
		if res.code == 0 || !strings.Contains(res.stderr, c.want) {
			t.Errorf("%v: status %d, stderr %q; want a failure that says %q", c.args, res.code, res.stderr, c.want)
		}
	}
}

// A session is named by its id, by the start of it, by its directory or by latest.
func TestE2EReplayFindsASessionByIdPrefixDirectoryAndLatest(t *testing.T) {
	w := newWorld(t, "")
	log, err := os.ReadFile(statetest.DemoLogFile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"20260102-030405-aaaaaa", "20260102-030406-bbbbbb"} {
		dir := filepath.Join(w.state, "sessions", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), log, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(w.state, "sessions", "20260102-030405-aaaaaa", "events.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	for name, arg := range map[string]string{
		"id":        "20260102-030405-aaaaaa",
		"prefix":    "20260102-030406",
		"directory": filepath.Join(w.state, "sessions", "20260102-030405-aaaaaa"),
		"file":      filepath.Join(w.state, "sessions", "20260102-030405-aaaaaa", "events.jsonl"),
		"latest":    "latest",
	} {
		res := w.run("", "replay", arg, "--final", "--rows", "12")
		if res.code != 0 || !strings.Contains(res.stdout, "SLEIPNIR") {
			t.Errorf("%s (%s): status %d\n%s%s", name, arg, res.code, res.stdout, res.stderr)
		}
	}
	res := w.run("", "replay", "2026", "--final")
	if res.code == 0 || !strings.Contains(res.stderr, "start of several sessions") {
		t.Errorf("a prefix of two sessions must be refused, naming them: status %d, %q", res.code, res.stderr)
	}
	// the session directory that `sleipnir demo --dir` writes
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "session", "events.jsonl"), log, 0o600); err != nil {
		t.Fatal(err)
	}
	if res := w.run("", "replay", d, "--final", "--rows", "12"); res.code != 0 {
		t.Errorf("the directory of a demo: status %d\n%s", res.code, res.stderr)
	}
}

// Watch follows a log that is being written: the screen has what was there, and then what is appended.
func TestE2EWatchFollowsASessionAsItIsWritten(t *testing.T) {
	w := newWorld(t, "")
	data, err := os.ReadFile(statetest.DemoLogFile(t))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines[:len(lines)/3], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	s := ptytest.Start(t, w.cmd("watch", path, "--poll", "50ms"), ptytest.Size(36, 100))
	waitScreen(t, s, 100, 36, "the session so far", shows("SLEIPNIR", "● live"))
	if strings.Contains(screenOf(s, 100, 36).String(), "■ ended") {
		t.Fatal("the session has not ended yet")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines[len(lines)/3:], "")); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, s, 100, 36, "the end of the session", shows("■ ended", "8 agents"))
	s.WriteString("c")
	waitScreen(t, s, 100, 36, "the cache view of the session", shows("cache ·", "every agent's cache"))
	s.WriteString("q")
	exitStatus(t, s, 0)
}

// Watch on something that is not a terminal explains itself and leaves nothing on the output.
func TestE2EWatchWithoutATerminal(t *testing.T) {
	w := newWorld(t, "")
	res := w.run("", "watch", statetest.DemoLogFile(t))
	if res.code != 1 || !strings.Contains(res.stderr, "needs a terminal") || !strings.Contains(res.stderr, "--final") {
		t.Errorf("status %d, stderr %q", res.code, res.stderr)
	}
	if res.stdout != "" {
		t.Errorf("nothing on the output: %q", res.stdout)
	}
}
