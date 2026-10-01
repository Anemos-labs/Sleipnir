package ptytest

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// What WaitInputRead says about a program that has exited does not depend on the system's
// terminal: macOS takes the terminal from every holder when the program that leads its
// session exits, so the question has no answer there (the first CI run on macOS: "it exited
// with -1 bytes unread" for a program that had read its line and printed it). These tests
// give the session the answers the terminals give, and so run everywhere.

// exitedSession is a session whose command has exited, with unread as the count noted then.
func exitedSession(unread int) *Session {
	s := &Session{exited: make(chan struct{}), unread: unread}
	s.ask = func() (int, error) { panic("an exited program's terminal is not asked") }
	close(s.exited)
	return s
}

func TestWaitInputReadAfterAnExitSaysWhatTheTerminalHeldAtTheExit(t *testing.T) {
	for _, c := range []struct {
		name    string
		unread  int
		wantErr string
	}{
		{"everything was read", 0, ""},
		{"the terminal could not be asked", -1, ""},
		{"the line was left unread", 4, "4 bytes unread"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := exitedSession(c.unread).WaitInputRead(time.Second)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("got %v, want nil: the program has exited, and nothing says it left input unread", err)
				}
				return
			}
			if !errors.Is(err, ErrEOF) || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got %v, want the end of the output with %q", err, c.wantErr)
			}
		})
	}
}

// The program can exit between the check for the exit and the question, and take the terminal
// with it: the question fails, and the exit follows. That is not an error of the wait.
func TestWaitInputReadWhoseQuestionFailsBecauseTheProgramJustExited(t *testing.T) {
	s := &Session{exited: make(chan struct{}), unread: -1}
	asked := make(chan struct{}, 1)
	s.ask = func() (int, error) {
		select {
		case asked <- struct{}{}:
		default:
		}
		return 0, errors.New("the terminal was revoked")
	}
	go func() {
		<-asked // the question has failed once at least
		close(s.exited)
	}()
	if err := s.WaitInputRead(Guard); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

// An error that is not an exit is the wait's error, with the cause kept, once the time is up.
func TestWaitInputReadReportsAQuestionThatNeverAnswers(t *testing.T) {
	boom := errors.New("not a terminal")
	s := &Session{exited: make(chan struct{})}
	s.ask = func() (int, error) { return 0, boom }
	err := s.WaitInputRead(20 * time.Millisecond)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "asking how much typed input is unread") {
		t.Fatalf("got %v, want the failed question", err)
	}
}
