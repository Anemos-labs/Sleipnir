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

// IDVar is the variable each command runs with inside the container, set to the id of its run: the processes it starts inherit
// it, so the helper finds all of them to stop the command.
const IDVar = "SLEIPNIR_SANDBOX_RUN"

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

// ExecArgs is the docker command line (after "docker") that runs prog inside container name from directory wd, with the
// formatting variables of environ and the run's id (IDVar), by which stopInside finds its processes.
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
	args = append(args, "--env", IDVar+"="+id, name)
	return append(args, prog...)
}

// stopInside sends SIGTERM to every process of the command started under id (each carries the id in its environment, and its
// process group goes with it), then SIGKILL to what is left after a grace period. Nothing is written in the container to find
// them, so nothing is left behind when a command ends normally.
func stopInside(bin, name, id string) {
	find := `ps() { for d in /proc/[0-9]*; do tr '\000' '\n' < "$d/environ" 2>/dev/null | grep -qx "` + IDVar + `=` + id + `" && echo "${d#/proc/}"; done; }; `
	script := find + `for p in $(ps); do kill -TERM -"$p" 2>/dev/null; kill -TERM "$p" 2>/dev/null; done; ` +
		`i=0; while [ -n "$(ps)" ] && [ $i -lt 20 ]; do sleep 0.1; i=$((i+1)); done; ` +
		`for p in $(ps); do kill -KILL -"$p" 2>/dev/null; kill -KILL "$p" 2>/dev/null; done`
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
