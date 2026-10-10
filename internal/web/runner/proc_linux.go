package runner

import "syscall"

// setPdeathsig makes the child die when the server's process does (it is in a group of its own and would otherwise outlive a
// killed server).
func setPdeathsig(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
