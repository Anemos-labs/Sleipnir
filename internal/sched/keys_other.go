//go:build !unix

package sched

import (
	"os"
	"os/exec"
)

// passKeys puts the keys in the child's environment where no pipe can be inherited (Windows, which has no /proc to read another
// process's environment from).
func passKeys(cmd *exec.Cmd, _ []byte) func(error) {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = JobEnv(cmd.Env)
	return func(error) {}
}

// readKeysFD reads nothing: a parent here passes keys in the environment.
func readKeysFD(string) []byte { return nil }
