package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ExecCommand is the hidden first argument that makes the program the exec helper (ExecMain) instead of the command line it
// normally is. Box.Wrap puts it in front of every command of a sandboxed session.
const ExecCommand = "__sandbox-exec"

// passEnv are the variables of the host's command environment that a command keeps inside the sandbox: how it should format
// its output. Everything else (PATH, HOME, the toolchains' variables, anything that could carry a credential) stays outside.
var passEnv = []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "NO_COLOR", "FORCE_COLOR", "CLICOLOR", "CI", "PAGER",
	"GIT_PAGER", "GIT_TERMINAL_PROMPT", "DEBIAN_FRONTEND", "PYTHONUNBUFFERED"}

// pidDir is where, inside the container, each command writes the process id the helper signals to stop it.
const pidDir = "/tmp/.sleipnir-sandbox"

// ExecMain is the exec helper: args are CONTAINER -- PROGRAM ARGS... It runs PROGRAM inside the container from the helper's own
// working directory (the shell tool starts it where the command is to run), with the variables of passEnv, its standard
// streams joined to the helper's, and returns the program's exit status. A program given by an absolute host path is run by
// its name, found on the container's PATH. SIGTERM, SIGINT and SIGHUP stop the program inside the container (a docker client
// that dies leaves what it started running), then the helper.
func ExecMain(args []string) int {
	if len(args) < 3 || args[1] != "--" {
		fmt.Fprintln(os.Stderr, "sleipnir sandbox: usage: "+ExecCommand+" CONTAINER -- PROGRAM [ARGS...]")
		return 2
	}
	name, prog := args[0], append([]string(nil), args[2:]...)
	if filepath.IsAbs(prog[0]) {
		prog[0] = filepath.Base(prog[0])
	}
	bin, err := exec.LookPath("docker")
	if err != nil {
		fmt.Fprintln(os.Stderr, "sleipnir sandbox: the docker command is not installed")
		return 127
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sleipnir sandbox:", err)
		return 126
	}
	id := randomHex(8)
	cmd := exec.Command(bin, ExecArgs(name, wd, id, os.Environ(), prog)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "sleipnir sandbox:", err)
		return 126
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err)
	case sig := <-sigs:
		stopInside(bin, name, id)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
		if s, ok := sig.(syscall.Signal); ok {
			return 128 + int(s)
		}
		return 143
	}
}

// ExecArgs is the docker command line (after "docker") that runs prog inside container name from directory wd: the process
// writes its id where stopInside finds it under id, then becomes prog, so that the id is prog's.
func ExecArgs(name, wd, id string, environ, prog []string) []string {
	args := []string{"exec", "--interactive", "--workdir", wd}
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, p := range passEnv {
			if k == p {
				args = append(args, "--env", kv)
				break
			}
		}
	}
	script := `mkdir -p ` + pidDir + ` 2>/dev/null; echo $$ > ` + pidDir + `/` + id + ` 2>/dev/null; exec "$@"`
	args = append(args, name, "sh", "-c", script, "sleipnir-sandbox")
	return append(args, prog...)
}

// stopInside sends SIGTERM to the command started under id and to its process group, then SIGKILL after a grace period.
func stopInside(bin, name, id string) {
	f := pidDir + "/" + id
	script := `p=$(cat ` + f + ` 2>/dev/null) || exit 0; kill -TERM -"$p" 2>/dev/null; kill -TERM "$p" 2>/dev/null; ` +
		`i=0; while kill -0 "$p" 2>/dev/null && [ $i -lt 20 ]; do sleep 0.1; i=$((i+1)); done; ` +
		`kill -KILL -"$p" 2>/dev/null; kill -KILL "$p" 2>/dev/null; rm -f ` + f
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, bin, "exec", name, "sh", "-c", script).Run()
}

// exitCode is the exit status a finished docker exec reports for its program.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if c := ee.ExitCode(); c >= 0 {
			return c
		}
		return 137
	}
	fmt.Fprintln(os.Stderr, "sleipnir sandbox:", err)
	return 126
}
