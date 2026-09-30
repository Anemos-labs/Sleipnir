//go:build unix

package mcp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// configureProc puts the server in a session of its own: pid == process-group
// id, so kill(-pid) reaches every descendant that did not deliberately leave,
// and the server is detached from the harness's terminal (it cannot open
// /dev/tty to prompt the user, and a Ctrl-C aimed at the harness does not
// reach it before the harness has had a chance to shut it down in order).
func configureProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func termGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// groupAlive reports whether any live process remains in the group. Zombies do
// not count: they linger until their parent reaps them, and orphans are
// reparented to init, which in a container may be a process that never does.
func groupAlive(pid int) bool {
	if err := syscall.Kill(-pid, 0); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	if alive, ok := liveMembers(pid); ok {
		return alive
	}
	return true
}

// liveMembers scans /proc for non-zombie members of process group pgid; ok is
// false where /proc cannot be used (macOS, BSD) and the signal probe stands.
func liveMembers(pgid int) (alive, ok bool) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return false, false
	}
	seen := false
	for _, e := range ents {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		b, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue // exited while we were looking
		}
		seen = true
		s := string(b)
		i := strings.LastIndexByte(s, ')') // the command name may contain anything
		if i < 0 || i+2 >= len(s) {
			continue
		}
		f := strings.Fields(s[i+2:]) // state ppid pgrp ...
		if len(f) < 3 || f[0] == "Z" || f[0] == "X" {
			continue
		}
		if g, err := strconv.Atoi(f[2]); err == nil && g == pgid {
			return true, true
		}
	}
	return false, seen
}

// resolveCommand finds the executable the way the shell would, but against the
// PATH the server will actually run with (the safe base plus the configured
// env), not the harness's. Relative and empty PATH entries are skipped, as
// Go's own exec.LookPath does: an executable named "npx" planted in the
// repository must never win a lookup just because the working directory is
// the repository.
func resolveCommand(command string, env []string, dir string) (string, error) {
	if strings.ContainsRune(command, filepath.Separator) {
		return command, nil // explicit path: exec resolves it against cmd.Dir
	}
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, command)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", command, exec.ErrNotFound)
}
