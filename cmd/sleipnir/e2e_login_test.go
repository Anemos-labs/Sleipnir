package main

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/ptytest"
)

// Ctrl-C at the hidden prompt for a key ends the login at once, the way it ends any command: the read of a password does not return when
// the context ends, so the first Ctrl-C did nothing and the second killed the process with the terminal's echo off.
func TestCtrlCAtTheKeyPromptEndsTheLogin(t *testing.T) {
	w := newWorld(t, "")
	s := ptytest.Start(t, w.cmd("login", "heimdall"), ptytest.Size(24, 100))
	if err := s.ExpectString("Paste your heimdall key", e2eGuard); err != nil {
		t.Fatal(err)
	}
	if err := s.SendCtrlC(); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpectEOF(e2eGuard); err != nil {
		t.Fatalf("the first Ctrl-C did not end the login: %v", err)
	}
	st, err := s.Wait(e2eGuard)
	if err != nil {
		t.Fatal(err)
	}
	if st.ExitCode() != 130 || !strings.Contains(s.Transcript(), "sleipnir: interrupted") {
		t.Errorf("exit %d, want 130 and \"sleipnir: interrupted\":\n%s", st.ExitCode(), s.Transcript())
	}
}
