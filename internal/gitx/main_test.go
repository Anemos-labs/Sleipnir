package gitx

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// The tests drive a real git through POSIX shell shims and unix file semantics.
func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		fmt.Println("gitx tests skipped on windows: they need a POSIX shell")
		os.Exit(0)
	}
	if dir := os.Getenv("GITX_TERM_HELPER"); dir != "" {
		termHelper(dir)
	}
	os.Exit(m.Run())
}

// termHelper is the test binary run by a shim in place of git (TestRunnerStopsCommandsGentlyFirstSoTheyCanRemoveTheirLocks): it holds
// a lock file in dir and, asked to stop with SIGTERM, says so and removes it, as git's signal handler does. Its handler is in place
// before it writes "ready", which a shell's trap cannot promise: macOS's sh (bash 3.2) can leave a SIGTERM that arrives just before
// `wait` pending until the script has ended, and the trap never runs.
func termHelper(dir string) {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	lock := filepath.Join(dir, "held.lock")
	_ = os.WriteFile(lock, nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "ready"), []byte("ready\n"), 0o644)
	select {
	case <-term:
		_ = os.WriteFile(filepath.Join(dir, "got-term"), []byte("term\n"), 0o644)
		_ = os.Remove(lock)
		os.Exit(143)
	case <-time.After(30 * time.Second):
		os.Exit(0)
	}
}
