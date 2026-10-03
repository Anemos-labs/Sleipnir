//go:build !unix

package session

import "os/exec"

// isolate leaves process attributes unchanged on platforms without process isolation support.
func isolate(c *exec.Cmd) {}
