package state

import (
	"runtime"
	"testing"
)

// skipWhereTheEngineDoesNotKnowThePaths skips a test whose log is made by a real session that the permission engine judges. The engine
// writes and matches paths with slashes: on Windows it takes every path for one outside the workspace and matches no credential
// directory, so a read of a file of the project is refused in plan mode and asked about in default mode, and the script of these
// sessions does not do what it says (docs/SECURITY.md, "macOS and Windows, honestly"). The State itself is fine there, and every test
// that folds a log that was written elsewhere runs.
func skipWhereTheEngineDoesNotKnowThePaths(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the permission engine does not know a Windows path yet (docs/SECURITY.md): the session this test records does not do what its script says")
	}
}
