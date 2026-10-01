package ptytest_test

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/ptytest"
)

// These tests drive cat and sh, which every system that can run them has: what they
// check is the helper itself, the properties the program tests in other packages rely on.

func need(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed", name)
	}
}

func mustExpect(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Typed input is echoed by the terminal, and what a program prints comes back through
// the same stream; a match consumes the output it matched.
func TestEchoAndConsumption(t *testing.T) {
	need(t, "cat")
	s := ptytest.Start(t, exec.Command("cat"))
	if _, err := s.WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	mustExpect(t, s.ExpectString("hello", ptytest.Guard)) // the terminal's echo
	mustExpect(t, s.ExpectString("hello", ptytest.Guard)) // cat's output
	// Both are consumed: a third is not there. (This waits out a short timeout by design:
	// it can only fail if the output has a third copy.)
	err := s.ExpectString("hello", 100*time.Millisecond)
	if !errors.Is(err, ptytest.ErrTimeout) {
		t.Fatalf("a third \"hello\": %v", err)
	}
	if n := strings.Count(s.Transcript(), "hello"); n != 2 {
		t.Fatalf("the transcript has %d copies of hello, want the echo and cat's output:\n%q", n, s.Transcript())
	}
}

// Ctrl-D is end of input at the start of a line: cat finishes and exits 0, and the
// output ends.
func TestCtrlDEndsInput(t *testing.T) {
	need(t, "cat")
	s := ptytest.Start(t, exec.Command("cat"))
	if err := s.SendCtrlD(); err != nil {
		t.Fatal(err)
	}
	st, err := s.Wait(ptytest.Guard)
	mustExpect(t, err)
	if st.ExitCode() != 0 {
		t.Fatalf("cat exited %d after Ctrl-D", st.ExitCode())
	}
	mustExpect(t, s.ExpectEOF(ptytest.Guard))
}

// Ctrl-C is a byte the terminal turns into SIGINT for the foreground group: cat has no
// handler, so the signal kills it. That it does proves that the command has the terminal
// as its controlling terminal and leads the foreground group.
func TestCtrlCIsSIGINT(t *testing.T) {
	need(t, "cat")
	s := ptytest.Start(t, exec.Command("cat"))
	if err := s.SendCtrlC(); err != nil {
		t.Fatal(err)
	}
	st, err := s.Wait(ptytest.Guard)
	mustExpect(t, err)
	ws, ok := st.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("cat after Ctrl-C: %v, want killed by SIGINT", st)
	}
}

func TestStandardStreamsAreTheTerminal(t *testing.T) {
	need(t, "sh")
	s := ptytest.Start(t, exec.Command("sh", "-c", "test -t 0 && test -t 1 && test -t 2 && echo on-a-terminal"))
	mustExpect(t, s.ExpectString("on-a-terminal", ptytest.Guard))
	st, err := s.Wait(ptytest.Guard)
	mustExpect(t, err)
	if st.ExitCode() != 0 {
		t.Fatalf("exit %d:\n%s", st.ExitCode(), s.Transcript())
	}
}

func TestWindowSize(t *testing.T) {
	need(t, "sh")
	need(t, "stty")
	for _, c := range []struct {
		name string
		opts []ptytest.Option
		want string
	}{
		{"default", nil, "24 80"},
		{"sized", []ptytest.Option{ptytest.Size(10, 20)}, "10 20"},
	} {
		t.Run("at start, "+c.name, func(t *testing.T) {
			s := ptytest.Start(t, exec.Command("stty", "size"), c.opts...)
			mustExpect(t, s.ExpectString(c.want, ptytest.Guard))
		})
	}
	t.Run("resize", func(t *testing.T) {
		s := ptytest.Start(t, exec.Command("sh", "-c", "stty size; read line; stty size"))
		mustExpect(t, s.ExpectString("24 80", ptytest.Guard))
		mustExpect(t, s.Resize(40, 100))
		if _, err := s.WriteString("\n"); err != nil {
			t.Fatal(err)
		}
		mustExpect(t, s.ExpectString("40 100", ptytest.Guard))
	})
}

// Every failure carries what the program printed, and says whether it ran out of time or
// the output ended.
func TestFailuresCarryTheTranscript(t *testing.T) {
	need(t, "cat")
	need(t, "sh")

	t.Run("timeout", func(t *testing.T) {
		s := ptytest.Start(t, exec.Command("cat"))
		if _, err := s.WriteString("typed-text\n"); err != nil {
			t.Fatal(err)
		}
		mustExpect(t, s.ExpectString("typed-text", ptytest.Guard))
		err := s.ExpectString("never-printed", 100*time.Millisecond)
		if !errors.Is(err, ptytest.ErrTimeout) || errors.Is(err, ptytest.ErrEOF) {
			t.Fatalf("got %v, want a timeout", err)
		}
		for _, want := range []string{"never-printed", "typed-text", "matched up to here"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error lacks %q:\n%v", want, err)
			}
		}
	})

	t.Run("the output ended", func(t *testing.T) {
		s := ptytest.Start(t, exec.Command("sh", "-c", "echo goodbye"))
		err := s.ExpectString("never-printed", ptytest.Guard)
		if !errors.Is(err, ptytest.ErrEOF) || errors.Is(err, ptytest.ErrTimeout) {
			t.Fatalf("got %v, want the output to have ended", err)
		}
		if !strings.Contains(err.Error(), "goodbye") {
			t.Errorf("the error lacks the transcript:\n%v", err)
		}
	})

	t.Run("a bad pattern is not a timeout", func(t *testing.T) {
		s := ptytest.Start(t, exec.Command("cat"))
		_, err := s.ExpectRegexp("(unclosed", ptytest.Guard)
		if err == nil || errors.Is(err, ptytest.ErrTimeout) || errors.Is(err, ptytest.ErrEOF) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestExpectRegexpReturnsSubmatches(t *testing.T) {
	need(t, "sh")
	s := ptytest.Start(t, exec.Command("sh", "-c", "echo count=42 of 100"))
	got, err := s.ExpectRegexp(`count=(\d+)( of (\d+))?`, ptytest.Guard)
	mustExpect(t, err)
	want := []string{"count=42 of 100", "42", " of 100", "100"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A terminal ends lines with CR LF; matching sees LF, and a lone CR stays.
func TestLineEndsAreFolded(t *testing.T) {
	need(t, "sh")
	for _, c := range []struct{ name, script, pattern string }{
		{"lines", `printf 'x\ny\n'`, "x\ny\n"},
		{"a lone CR stays", `printf 'a\rb\n'`, "a\rb\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := ptytest.Start(t, exec.Command("sh", "-c", c.script))
			mustExpect(t, s.ExpectString(c.pattern, ptytest.Guard))
		})
	}
}

func TestWaitTimesOut(t *testing.T) {
	need(t, "cat")
	s := ptytest.Start(t, exec.Command("cat"))
	if _, err := s.Wait(100 * time.Millisecond); !errors.Is(err, ptytest.ErrTimeout) {
		t.Fatalf("got %v, want a timeout", err)
	}
}

// WaitInputRead returns when the program has read what was typed, and not before.
func TestWaitInputRead(t *testing.T) {
	need(t, "sh")
	t.Run("the program reads", func(t *testing.T) {
		s := ptytest.Start(t, exec.Command("sh", "-c", "read line; echo got:$line"))
		if _, err := s.WriteString("typed\n"); err != nil {
			t.Fatal(err)
		}
		mustExpect(t, s.ExpectString("typed", ptytest.Guard)) // the echo: the terminal has the line
		mustExpect(t, s.WaitInputRead(ptytest.Guard))
		mustExpect(t, s.ExpectString("got:typed", ptytest.Guard))
	})
	t.Run("the program does not", func(t *testing.T) {
		s := ptytest.Start(t, exec.Command("sleep", "600"))
		if _, err := s.WriteString("typed\n"); err != nil {
			t.Fatal(err)
		}
		mustExpect(t, s.ExpectString("typed", ptytest.Guard))
		if err := s.WaitInputRead(100 * time.Millisecond); !errors.Is(err, ptytest.ErrTimeout) {
			t.Fatalf("got %v, want a timeout: nothing reads the terminal", err)
		}
	})
}

// What a command writes just before it exits is in the transcript: some systems discard what is
// queued on a terminal when its last slave descriptor closes, and the session holds one until the
// reader has taken everything.
func TestOutputWrittenJustBeforeExitIsNotLost(t *testing.T) {
	need(t, "sh")
	need(t, "yes")
	need(t, "head")
	const lines = 3000
	s := ptytest.Start(t, exec.Command("sh", "-c", "yes xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx | head -n 3000; echo THE-END"))
	mustExpect(t, s.ExpectString("THE-END", ptytest.Guard))
	if _, err := s.Wait(ptytest.Guard); err != nil {
		t.Fatal(err)
	}
	mustExpect(t, s.ExpectEOF(ptytest.Guard))
	if n := strings.Count(s.Transcript(), "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"); n != lines {
		t.Fatalf("%d of %d lines in the transcript", n, lines)
	}
}

// Close ends a command that is still running, the way the end of a test does.
func TestCloseKillsWhatIsRunning(t *testing.T) {
	need(t, "sleep")
	s := ptytest.Start(t, exec.Command("sleep", "600"))
	s.Close()
	st, err := s.Wait(ptytest.Guard)
	mustExpect(t, err)
	ws, ok := st.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("sleep after Close: %v, want killed by SIGKILL", st)
	}
	s.Close() // twice is fine
}

func TestSignalGoesToTheCommand(t *testing.T) {
	need(t, "sleep")
	s := ptytest.Start(t, exec.Command("sleep", "600"))
	if err := s.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	st, err := s.Wait(ptytest.Guard)
	mustExpect(t, err)
	if ws, ok := st.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("sleep after SIGTERM: %v", st)
	}
}
