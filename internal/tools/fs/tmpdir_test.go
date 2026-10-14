package fs

import (
	"os"
	"path/filepath"
	"testing"
)

// A model told that scratch files go in $TMPDIR writes the path as "$TMPDIR/x": the file tools take it to the session's scratch
// directory, as the shell would. In a trial an agent's write to "$TMPDIR/probe.txt" became a directory literally named "$TMPDIR"
// under the project (and, refused there, convinced the agent it could not write at all). Without a scratch directory, and for any
// other variable, the path is taken as written.
func TestFileToolsExpandALeadingTMPDIR(t *testing.T) {
	env := testEnv(t)
	env.Tmp = realTemp(t)
	for _, p := range []string{"$TMPDIR/probe.txt", "${TMPDIR}/sub/probe2.txt"} {
		mustOK(t, run(t, Write{}, env, map[string]any{"path": p, "content": "x"}))
	}
	for _, want := range []string{filepath.Join(env.Tmp, "probe.txt"), filepath.Join(env.Tmp, "sub", "probe2.txt")} {
		if got := readFileT(t, want); got != "x" {
			t.Errorf("%s = %q", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "$TMPDIR")); err == nil {
		t.Error(`a directory named "$TMPDIR" was made in the project`)
	}
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "$TMPDIR/probe.txt"}))

	plain := testEnv(t)
	mustOK(t, run(t, Write{}, plain, map[string]any{"path": "$HOME/x.txt", "content": "y"}))
	if got := readFileT(t, filepath.Join(plain.Cwd, "$HOME", "x.txt")); got != "y" {
		t.Errorf("another variable was expanded: %q", got)
	}
}
