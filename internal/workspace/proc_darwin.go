//go:build darwin

package workspace

import "golang.org/x/sys/unix"

// szomb is the kernel's process state for a zombie (SZOMB in <sys/proc.h>).
const szomb = 5

// procStat asks the kernel (sysctl kern.proc.pid) for the process: macOS has no
// /proc. The start time is the kernel's own, in microseconds, so a pid reused by
// another process differs from the one a marker recorded.
func procStat(pid int) (procInfo, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return procInfo{}, false
	}
	st := byte('S')
	if kp.Proc.P_stat == szomb {
		st = 'Z'
	}
	tv := kp.Proc.P_starttime
	return procInfo{state: st, start: int64(tv.Sec)*1_000_000 + int64(tv.Usec)}, true
}
