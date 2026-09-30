//go:build unix

package shell

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// configureCmd puts the command in a session of its own. That isolates the
// process tree (pid == process-group id, so kill(-pid) reaches every
// descendant that did not deliberately leave) and detaches it from the
// harness's controlling terminal: a command that opens /dev/tty (ssh, sudo, git
// credential prompts) fails at once instead of stopping on SIGTTIN and
// corrupting the user's terminal UI.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func termGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// groupAlive reports whether any live process remains in the group.
//
// kill(-pgid, 0) also succeeds for zombies, which linger until their parent
// reaps them. Orphans are reparented to init, and in containers whose PID 1 is
// not an init that can be a long time: waiting on them would burn the whole
// kill grace period on every timeout. Where /proc exists, zombies are
// therefore ignored.
func groupAlive(pid int) bool {
	err := syscall.Kill(-pid, 0)
	if err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	if alive, ok := liveMembers(pid); ok {
		return alive
	}
	return true
}

// liveMembers scans /proc for non-zombie members of process group pgid. ok is
// false when /proc cannot be used (macOS, BSD), in which case the caller falls
// back to the signal probe alone.
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

func exitFrom(ps *os.ProcessState, err error) exitStatus {
	if ps == nil {
		return exitStatus{code: -1, err: err}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return exitStatus{code: 128 + int(ws.Signal()), signal: int(ws.Signal())}
	}
	return exitStatus{code: ps.ExitCode()}
}
