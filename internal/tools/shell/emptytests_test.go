package shell

import "testing"

// A run that executed no test is not a pass: the exit code is 0 and the model must be told what the output says.
func TestNoTestsRanIsRecognised(t *testing.T) {
	for out, want := range map[string]bool{
		"Ran 0 tests in 0.000s\n\nOK":                     true,
		"collected 0 items\n":                             true,
		"testing: warning: no tests to run\nPASS":         true,
		"============ no tests ran in 0.01s ============": true,
		"Ran 12 tests in 0.5s\n\nOK":                      false,
		"ok  \texample.com/x\t0.2s":                       false,
		"":                                                false,
	} {
		if got := noTestsRan(out); got != want {
			t.Errorf("%q: %v, want %v", out, got, want)
		}
	}
}
