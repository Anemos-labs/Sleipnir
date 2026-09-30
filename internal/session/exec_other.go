//go:build !unix

package session

import "os/exec"

func isolate(c *exec.Cmd) {}
