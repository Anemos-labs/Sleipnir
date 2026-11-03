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
		{"node", []string{"-e", "1"}, false},
		{"node", []string{"-i", "-e", "1"}, true}, // forced interactive reads stdin after the inline code
		{"node", []string{"-e", "1", "--interactive"}, true},
		{"node", []string{"-ie", "1"}, true},
		{"node", []string{"app.js", "-i"}, false}, // the script's own argument
		{"node", []string{"-e", "-i"}, false},     // -i is the code
	} {
		if got := ReadsCodeFromStdin(tc.prog, tc.args); got != tc.stdin {
			t.Errorf("%s %q: reads code from stdin = %v, want %v", tc.prog, tc.args, got, tc.stdin)
		}
	}
}

// Removing the backslash-newlines of an unquoted here-document, as the shell does before expanding it, can join "$" and "(" into a
// substitution the text as written does not show; such a command is not parsed as complete. Continuations that only join plain
// text, as in a Dockerfile written with cat, parse as before.
func TestAHeredocContinuationThatJoinsASubstitutionIsNotParsed(t *testing.T) {
	for src, parsed := range map[string]bool{
		"cat <<EOF\n$\\\n(touch /tmp/x)\nEOF":        false,
		"cat <<EOF\n`\\\ntouch x`\nEOF":              false, // inside an open substitution: not judged
		"cat > Dockerfile <<EOF\nRUN a \\\n  b\nEOF": true,
		"cat <<'EOF'\n$\\\n(touch /tmp/x)\nEOF":      true, // quoted: nothing is expanded
	} {
		if an := Parse(src); an.Parsed != parsed {
			t.Errorf("%q: Parsed %v, want %v (%s)", src, an.Parsed, parsed, an.Problem)
		}
	}
}
