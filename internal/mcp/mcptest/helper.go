package mcptest

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// EnvDelay is how long the "slow-init" mode waits before serving and the
	// "linger" mode between hanging up and exiting.
	EnvDelay = "SLEIPNIR_MCPTEST_DELAY"
	// EnvBarrier is the directory the "barrier" mode meets its siblings in, and
	// EnvBarrierCount how many helpers must be there before any of them serves.
	EnvBarrier      = "SLEIPNIR_MCPTEST_BARRIER"
	EnvBarrierCount = "SLEIPNIR_MCPTEST_BARRIER_COUNT"
)

// barrierWait is the longest a "barrier" helper waits for its siblings. It is far
// beyond the time a loaded machine needs to start a few processes and below the
// connect timeout the tests use, so a helper that gives up did so because no one
// else was coming.
const barrierWait = 8 * time.Second

// arriveAtBarrier records that this helper is running (<dir>/<pid>.up), waits until
// n helpers are, and then records how the wait ended: <dir>/<pid>.ok when all of them
// arrived, <dir>/<pid>.late when the wait ran out first.
func arriveAtBarrier(dir string, n int) {
	if dir == "" || n < 1 {
		return
	}
	mark := func(suffix string) {
		_ = os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+suffix), nil, 0o600)
	}
	mark(".up")
	deadline := time.Now().Add(barrierWait)
	for {
		if up, _ := filepath.Glob(filepath.Join(dir, "*.up")); len(up) >= n {
			mark(".ok")
			return
		}
		if time.Now().After(deadline) {
			mark(".late")
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

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
//	barrier      wait until EnvBarrierCount helpers have started in the directory EnvBarrier
//	             (or barrierWait has passed), then serve; each leaves a file saying how it went
//	linger       serve; the "crash" tool closes stdout at once but exits (status
//	             3) only after EnvDelay: the client sees the stream end long
//	             before it can learn how the process died
//	sleep        sleep forever (the grandchild's mode)
func HelperMain() {
	mode := os.Getenv(EnvMode)
	var hungUp atomic.Bool
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
	case "barrier":
		n, _ := strconv.Atoi(os.Getenv(EnvBarrierCount))
		arriveAtBarrier(os.Getenv(EnvBarrier), n)
	case "linger":
		d, err := time.ParseDuration(os.Getenv(EnvDelay))
		if err != nil {
			d = 500 * time.Millisecond
		}
		s.OnCrash = func() {
			hungUp.Store(true)
			_ = os.Stdout.Close()
			time.Sleep(d)
			os.Exit(3)
		}
	}
	err := s.ServeStdio(os.Stdin, out)
	if mode == "ignoreterm" {
		select {} // stdin closed: an ill-behaved server keeps running
	}
	if hungUp.Load() {
		select {} // a call after the hang-up ended the serve loop; the crash exits when its delay is over
	}
	if err != nil && !strings.Contains(err.Error(), "closed") {
		fmt.Fprintln(os.Stderr, "serve:", err)
	}
	os.Exit(0)
}
