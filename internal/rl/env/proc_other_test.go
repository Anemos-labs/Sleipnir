//go:build !unix

package env

import (
	"os"
	"testing"
)

func alive(pid int) bool { return false }

func waitDead(t testing.TB, pid int) { t.Skip("process checks need a Unix host") }

func mkfifo(path string) error { return os.ErrInvalid }
