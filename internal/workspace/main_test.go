package workspace

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// The tests drive a real git and POSIX shell commands. The suite ends by checking that no
// goroutine of the module is left running.
func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		fmt.Println("workspace tests skipped on windows: they need a POSIX shell")
		os.Exit(0)
	}
	os.Exit(testutil.CheckLeaks(m))
}
