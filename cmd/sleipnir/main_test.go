package main

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/mcp/mcptest"
)

// The test binary is re-executed as two other programs:
//
//   - the MCP reference server for `sleipnir mcp test` (mcptest.IsHelper);
//   - the sleipnir command itself, for the end-to-end tests (e2e_test.go): main() runs in
//     a child process, with its own signals, exit status and terminal.
func TestMain(m *testing.M) {
	switch {
	case mcptest.IsHelper():
		mcptest.HelperMain()
		return
	case os.Getenv(e2eChildEnv) == "1":
		runAsSleipnir()
		return
	}
	os.Exit(m.Run())
}

// The context of a command that does one thing is cancelled by the signals it asks for, and says which one came; after it the
// system has the signals back (a second Ctrl-C ends the process at once), which is why only one is sent here.
func TestInterruptContextIsCancelledByTheSignalAndSaysWhich(t *testing.T) {
	ctx, caught, stop := interruptContext(syscall.SIGTERM)
	defer stop()
	if caught() != nil || ctx.Err() != nil {
		t.Fatal("cancelled before any signal")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Minute): // a hang guard, not a timing
		t.Fatal("SIGTERM did not cancel the context")
	}
	if caught() != syscall.SIGTERM {
		t.Fatalf("caught() = %v, want SIGTERM", caught())
	}
	for sig, want := range map[os.Signal]int{os.Interrupt: 130, syscall.SIGTERM: 143} {
		if got := interruptStatus(sig); got != want {
			t.Errorf("interruptStatus(%v) = %d, want %d", sig, got, want)
		}
	}
}

func TestInterruptContextStopCancelsWithoutASignal(t *testing.T) {
	ctx, caught, stop := interruptContext(syscall.SIGTERM)
	stop()
	stop() // twice is fine
	if ctx.Err() == nil || caught() != nil {
		t.Fatalf("stop: ctx.Err() = %v, caught() = %v", ctx.Err(), caught())
	}
}
