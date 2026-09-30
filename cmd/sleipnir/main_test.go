package main

import (
	"os"
	"testing"

	"github.com/reee344/sleipnir/internal/mcp/mcptest"
)

// The test binary is re-executed as two other programs:
//
//   - the MCP reference server for `sleipnir mcp test` (mcptest.IsHelper);
//   - the sleipnir command itself, for the end-to-end tests (e2e_test.go): main() runs in
//     a child process, with its own signals, exit status and terminal.
func TestMain(m *testing.M) {
	switch {
	case mcptest.IsHelper():
		mcptest.HelperMain()
		return
	case os.Getenv(e2eChildEnv) == "1":
		runAsSleipnir()
		return
	}
	os.Exit(m.Run())
}
