package shell

import "strings"

// noTestsRan reports whether the output of a command that succeeded says that it ran no tests (unittest's "Ran 0 tests", pytest's "collected 0
// items", go's "no tests to run"): an exit code of 0 that a model reads as a green run, and says so to the person.
func noTestsRan(out string) bool {
	low := strings.ToLower(out)
	for _, s := range []string{"ran 0 tests", "collected 0 items", "no tests ran", "no tests to run"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

const noTestsNote = "[note: the output says that no test ran, so this is not a passing run]"
