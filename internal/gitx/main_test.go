package gitx

import (
	"fmt"
	"os"
	"runtime"
	"testing"
)

// The tests drive a real git through POSIX shell shims and unix file semantics.
func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		fmt.Println("gitx tests skipped on windows: they need a POSIX shell")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
