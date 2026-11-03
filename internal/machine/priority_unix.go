//go:build unix && !linux

package machine

import (
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

// nicePaths are where nice(1) lives on the BSDs and macOS; a PATH lookup is the last resort.
var nicePaths = []string{"/usr/bin/nice", "/bin/nice"}

// startLow starts cmd at nice lowNice. Here the nice value belongs to the whole process and cannot be set between fork and
// exec, so the command is started through nice(1), which sets it and then execs the command in the same process: the command
// has it from its first instruction, and nothing it starts can be quicker than the change. cmd.Process is still the command's
// own process. Without nice(1), the command is reniced once it runs, and a process it starts in its first instants keeps the
// default priority.
func startLow(cmd *exec.Cmd) error {
	own, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		own = 0
	}
	if own >= lowNice {
		return cmd.Start() // already at least as low: the command inherits it
	}
	if cmd.Err == nil && cmd.Path != "" {
		if nice := findNice(); nice != "" {
			// nice -n adds to the inherited value, so the step is measured from ours to reach lowNice exactly.
			args := []string{"nice", "-n", strconv.Itoa(lowNice - own), "--", cmd.Path}
			if len(cmd.Args) > 1 {
				args = append(args, cmd.Args[1:]...)
			}
			cmd.Path, cmd.Args = nice, args
			return cmd.Start()
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if cur, err := unix.Getpriority(unix.PRIO_PROCESS, cmd.Process.Pid); err != nil || cur < lowNice {
		_ = unix.Setpriority(unix.PRIO_PROCESS, cmd.Process.Pid, lowNice)
	}
	return nil
}

func findNice() string {
	for _, p := range nicePaths {
		if unix.Access(p, unix.X_OK) == nil {
			return p
		}
	}
	if p, err := exec.LookPath("nice"); err == nil {
		return p
	}
	return ""
}
