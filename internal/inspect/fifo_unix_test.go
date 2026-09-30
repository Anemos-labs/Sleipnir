//go:build unix

package inspect

import "syscall"

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
