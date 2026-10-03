package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// runVerify runs the harness-owned verification command in dir and returns its
// combined output and exit code. It is what lets "done" be decided by code: the
// swarm calls it before a worker's task may leave "doing".
func runVerify(ctx context.Context, dir, cmd string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/C", cmd)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmd)
	}
	c.Dir = dir
	c.Env = scrubEnv(os.Environ())
	isolate(c)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &limitedWriter{w: &out, max: 256 << 10}, &limitedWriter{w: &out, max: 256 << 10}
	err := c.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code, err = ee.ExitCode(), nil
		}
	}
	return out.String(), code, err
}

// scrubEnv drops variables that look like credentials from a child environment.
func scrubEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		if strings.Contains(u, "KEY") || strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET") ||
			strings.Contains(u, "PASSWORD") || strings.Contains(u, "CREDENTIAL") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// limitedWriter stops accepting output past max bytes but keeps reporting success
// so the child is not killed by a broken pipe.
type limitedWriter struct {
	w   *bytes.Buffer
	max int
}

// Write retains only the output prefix that fits the configured cap while reporting all bytes
// consumed.
func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.max - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
