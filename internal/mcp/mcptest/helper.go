package mcptest

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Environment variables that turn a test binary into a server process.
const (
	// EnvHelper marks the process as an MCP server helper; TestMain checks it.
	EnvHelper = "SLEIPNIR_MCPTEST_HELPER"
	// EnvMode selects the behaviour (see HelperMain).
	EnvMode = "SLEIPNIR_MCPTEST_MODE"
	// EnvPidFile is where the "grandchild" mode writes the grandchild's pid.
	EnvPidFile = "SLEIPNIR_MCPTEST_PIDFILE"
	// EnvSecret is echoed to stderr by the "secret-stderr" mode.
	EnvSecret = "SLEIPNIR_MCPTEST_SECRET"
	// EnvDelay is how long the "slow-init" mode waits before serving.
	EnvDelay = "SLEIPNIR_MCPTEST_DELAY"
)

// IsHelper reports whether this process was started as a server helper.
func IsHelper() bool { return os.Getenv(EnvHelper) != "" }

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// HelperMain runs the process as an MCP server on stdin/stdout and never
// returns. A test binary calls it from TestMain when IsHelper is true, which is
// how stdio tests get a real child process without shipping a binary: the
// test executable re-executes itself.
//
// Modes (EnvMode):
//
//	""           the reference server; the "crash" tool exits the process
//	hang         read input, never answer (a server that hangs in initialize)
//	exit         print a fatal message to stderr and exit 3 at once
//	garbage      print non-JSON lines to stdout, then serve
//	flood        serve, and after that write notifications without pause
//	ignoreterm   serve, but ignore SIGTERM and stay alive after stdin closes
//	grandchild   serve, with a child process of its own (pid in EnvPidFile)
//	secret-stderr print EnvSecret to stderr, then exit 2
//	slow-init    wait EnvDelay (a Go duration) before serving
//	sleep        sleep forever (the grandchild's mode)
func HelperMain() {
	mode := os.Getenv(EnvMode)
	out := &lockedWriter{w: os.Stdout}
	s := New()
	s.OnCrash = func() { os.Exit(3) }
	switch mode {
	case "hang":
		_, _ = io.Copy(io.Discard, os.Stdin)
		select {}
	case "exit":
		fmt.Fprintln(os.Stderr, "fatal: cannot start: missing configuration")
		os.Exit(3)
	case "secret-stderr":
		fmt.Fprintln(os.Stderr, "starting with token "+os.Getenv(EnvSecret))
		os.Exit(2)
	case "sleep":
		select {}
	case "garbage":
		for i := 0; i < 200; i++ {
			fmt.Fprintf(out, "log line %d: this is not JSON-RPC\n", i)
		}
	case "flood":
		go func() {
			line := []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"flood"}}` + "\n")
			for {
				if _, err := out.Write(line); err != nil {
					return
				}
			}
		}()
	case "ignoreterm":
		signal.Ignore(syscall.SIGTERM)
	case "grandchild":
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), EnvMode+"=sleep")
		if err := cmd.Start(); err == nil {
			if f := os.Getenv(EnvPidFile); f != "" {
				_ = os.WriteFile(f, []byte(fmt.Sprint(cmd.Process.Pid)), 0o600)
			}
		}
	case "slow-init":
		if d, err := time.ParseDuration(os.Getenv(EnvDelay)); err == nil {
			time.Sleep(d)
		}
	}
	err := s.ServeStdio(os.Stdin, out)
	if mode == "ignoreterm" {
		select {} // stdin closed: an ill-behaved server keeps running
	}
	if err != nil && !strings.Contains(err.Error(), "closed") {
		fmt.Fprintln(os.Stderr, "serve:", err)
	}
	os.Exit(0)
}
