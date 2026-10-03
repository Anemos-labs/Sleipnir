//go:build darwin

package harden

import "golang.org/x/sys/unix"

// platformHarden denies debugger attachment on macOS unless opted out and reports that environment
// memory remains readable.
func platformHarden(optOut bool) Status {
	var st Status
	// ptrace(PT_DENY_ATTACH) refuses debugger attach. It exits the process if a
	// debugger is already attached, hence the SLEIPNIR_DUMPABLE=1 opt-out.
	// The environment of a same-user process stays readable through sysctl
	// kern.procargs2 (`ps eww`), and there is no equivalent of erasing it here: keys
	// must not be in the environment on macOS.
	st.Notes = append(st.Notes, "the environment is not erased in memory on darwin; keep credentials out of it")
	if optOut {
		st.OptedOut = true
		return st
	}
	if err := unix.PtraceDenyAttach(); err != nil {
		st.Notes = append(st.Notes, "ptrace(PT_DENY_ATTACH): "+err.Error())
	} else {
		st.Protected = true
	}
	return st
}
