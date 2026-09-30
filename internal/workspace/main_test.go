package workspace

import (
	"fmt"
	"os"
	"runtime"
	"testing"
)

// The tests drive a real git and POSIX shell commands.
func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		fmt.Println("workspace tests skipped on windows: they need a POSIX shell")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
