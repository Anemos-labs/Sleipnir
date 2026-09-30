//go:build linux

package shell

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// docs/SECURITY.md recommends running model-run commands in their own PID namespace with a private
// /proc through Options.Wrap: the harness process is then not merely unreadable, it does not exist
// for the command (`/proc/$PPID` is gone), which is what the dumpable flag cannot give against a
// root user or a process with CAP_SYS_PTRACE. This is the example from the document, run for real.
// It needs unprivileged user namespaces (or root), so it skips where the sandbox forbids them.
func TestWrapInAPIDNamespaceHidesTheHarnessFromCommands(t *testing.T) {
	wrap := []string{"unshare", "--user", "--map-current-user", "--pid", "--fork", "--mount-proc", "--kill-child"}
	probe := append(append([]string(nil), wrap...), "true")
	if out, err := exec.Command(probe[0], probe[1:]...).CombinedOutput(); err != nil {
		t.Skipf("cannot start a PID namespace here (%v: %s)", err, strings.TrimSpace(string(out)))
	}
	visible := fmt.Sprintf("if [ -d /proc/%d ]; then echo VISIBLE; else echo HIDDEN; fi", os.Getpid())

	plain := newHarness(t)
	if got := outLine(plain.bash(plain.env("a"), visible).Text); got != "VISIBLE" {
		t.Skipf("control: without a wrapper the harness's /proc entry is not visible to commands either (%q)", got)
	}

	wrapped := newHarness(t, Options{Wrap: wrap})
	env := wrapped.env("a")
	if got := outLine(wrapped.bash(env, visible).Text); got != "HIDDEN" {
		t.Fatalf("under the PID-namespace wrapper the command still sees the harness in /proc: %q", got)
	}
	if got := outLine(wrapped.bash(env, `echo "ppid=$PPID"`).Text); got != "ppid=0" {
		t.Errorf("the parent of the wrapped shell should be outside the namespace (0), got %q", got)
	}
	// The wrapper is transparent to what the tool promises: exit codes and the working directory
	// that persists between calls (the trap's temp file is outside the private /proc).
	if res := wrapped.bash(env, `exit 7`); !strings.Contains(res.Text, "[exit code 7]") {
		t.Errorf("exit code lost under the wrapper: %q", res.Text)
	}
	wrapped.bash(env, `mkdir sub && cd sub`)
	if got := outLine(wrapped.bash(env, `basename "$PWD"`).Text); got != "sub" {
		t.Errorf("cd did not persist under the wrapper: %q", got)
	}
	// Background jobs and their cleanup work through the wrapper too.
	id := wrapped.startJob(env, `echo job-in-namespace; sleep 30`)
	waitFor(t, "job output", 10*1e9, func() bool {
		return strings.Contains(wrapped.output(env, id, map[string]any{"since": 0}).Text, "job-in-namespace")
	})
	wrapped.kill(env, id)
}
