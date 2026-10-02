package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -h is a success: the flag package has printed the usage, and there is nothing to
// complain about (commands built on flag.ContinueOnError return flag.ErrHelp).
func TestHelpIsNotAnError(t *testing.T) {
	var buf bytes.Buffer
	for _, err := range []error{nil, flag.ErrHelp, fmt.Errorf("mcp list: %w", flag.ErrHelp)} {
		if code := reportError(&buf, err); code != 0 {
			t.Errorf("reportError(%v) = %d, want 0", err, code)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("a help request printed %q", buf.String())
	}
	if code := reportError(&buf, errors.New("boom \x1b[31mred")); code != 1 || !strings.HasPrefix(buf.String(), "sleipnir: boom ") || strings.Contains(buf.String(), "\x1b") {
		t.Errorf("an error must exit 1 and be printed without control characters: %d %q", code, buf.String())
	}
}

// captureStderr runs fn with os.Stderr redirected and returns what was written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stderr = orig }()
	fn()
	w.Close()
	os.Stderr = orig
	return <-done
}

// `swarm -h` used to fail with "invalid value "-h" for flag -swarm": the shorthand
// put --swarm in front of whatever followed.
func TestSwarmShorthandUnderstandsHelpAndBadCounts(t *testing.T) {
	// -h ends the process itself (flag.ExitOnError), so run the binary's own logic in a
	// child process.
	if os.Getenv("SLEIPNIR_TEST_CHILD") == "swarm-help" {
		_ = cmdSwarm(context.Background(), []string{os.Getenv("SLEIPNIR_TEST_ARG")})
		return
	}
	for _, arg := range []string{"-h", "--help", "help"} {
		out, code := runSelf(t, "TestSwarmShorthandUnderstandsHelpAndBadCounts", "swarm-help", arg)
		if code != 0 {
			t.Errorf("swarm %s exited %d:\n%s", arg, code, out)
		}
		for _, want := range []string{"usage: sleipnir swarm <agents>", "-verify", "-isolation", "-budget-usd"} {
			if !strings.Contains(out, want) {
				t.Errorf("swarm %s: the usage lacks %q:\n%s", arg, want, out)
			}
		}
		if strings.Contains(out, "invalid value") {
			t.Errorf("swarm %s: %s", arg, out)
		}
	}
	for _, bad := range []string{"fix the login", "0", "-3"} {
		err := cmdSwarm(context.Background(), []string{bad, "goal"})
		if err == nil || !strings.Contains(err.Error(), "the first argument is the number of agents") || !strings.Contains(err.Error(), bad) {
			t.Errorf("swarm %q: %v", bad, err)
		}
	}
	if err := cmdSwarm(context.Background(), []string{"3"}); err == nil || !strings.Contains(err.Error(), "swarm: a prompt is required") {
		t.Errorf("swarm 3 with no goal: %v", err)
	}
}

func runSelf(t *testing.T, test, child, arg string) (string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^"+test+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SLEIPNIR_TEST_CHILD="+child, "SLEIPNIR_TEST_ARG="+arg, "HOME="+filepath.Join(t.TempDir(), "h"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	code := 0
	if err != nil {
		var ee interface{ ExitCode() int }
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return out.String(), code
}
