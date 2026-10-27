package shellparse

import "testing"

// A long option's separate value is no script operand: node --require ./hook.js with no script reads its program from standard
// input, and the preload is not mistaken for the script.
func TestReadsCodeFromStdinSkipsLongOptionValues(t *testing.T) {
	for _, tc := range []struct {
		prog  string
		args  []string
		stdin bool
	}{
		{"node", []string{"--require", "hook.js"}, true},
		{"node", []string{"--import", "./loader.mjs"}, true},
		{"node", []string{"--require=hook.js"}, true},
		{"node", []string{"--require", "hook.js", "app.js"}, false},
		{"node", []string{"--test", "a.test.js"}, false},
		{"bun", []string{"--preload", "setup.ts"}, true},
		{"ruby", []string{"--encoding", "UTF-8"}, true},
		{"python3", []string{"--check-hash-based-pycs", "always", "x.py"}, false},
	} {
		if got := ReadsCodeFromStdin(tc.prog, tc.args); got != tc.stdin {
			t.Errorf("%s %q: reads code from stdin = %v, want %v", tc.prog, tc.args, got, tc.stdin)
		}
	}
}
