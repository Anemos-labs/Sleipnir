//go:build unix

package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

type riskRecorder struct{ got []perm.Request }

func (r *riskRecorder) Check(_ context.Context, req perm.Request) perm.Decision {
	r.got = append(r.got, req)
	return perm.Decision{Reason: "recorded"}
}

// A command for a shell whose grammar the engine does not read (PowerShell, cmd.exe) is put to it as high risk; one for a POSIX
// shell is not.
func TestNonPOSIXShellCommandsAreHighRisk(t *testing.T) {
	dir := t.TempDir()
	pwsh := filepath.Join(dir, "pwsh")
	if err := os.WriteFile(pwsh, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for shell, want := range map[string]perm.Risk{pwsh: perm.RiskHigh, "": perm.RiskLow} {
		h := newHarness(t, Options{Shell: shell})
		rec := &riskRecorder{}
		env := h.env("a")
		env.Perm = rec
		h.bash(env, "echo hi")
		if len(rec.got) != 1 || rec.got[0].Risk != want {
			t.Errorf("shell %q: requests %+v, want risk %v", shell, rec.got, want)
		}
	}
}
