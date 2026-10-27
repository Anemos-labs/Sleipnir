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

// A command for a shell whose grammar the engine does not read (PowerShell, cmd.exe) is put to it as high risk unless it is plain
// words, which every shell reads the same way; one for a POSIX shell is not.
func TestNonPOSIXShellCommandsAreHighRisk(t *testing.T) {
	dir := t.TempDir()
	pwsh := filepath.Join(dir, "pwsh")
	if err := os.WriteFile(pwsh, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		shell, command string
		want           perm.Risk
	}{
		{pwsh, `echo "$env:HOME"`, perm.RiskHigh},
		{pwsh, `type a.txt > b.txt`, perm.RiskHigh},
		{pwsh, `echo a\b`, perm.RiskHigh},
		{pwsh, "go list ./...", perm.RiskLow},
		{"", `echo "$HOME"`, perm.RiskLow},
	} {
		h := newHarness(t, Options{Shell: c.shell})
		rec := &riskRecorder{}
		env := h.env("a")
		env.Perm = rec
		h.bash(env, c.command)
		if len(rec.got) != 1 || rec.got[0].Risk != c.want {
			t.Errorf("shell %q, %q: requests %+v, want risk %v", c.shell, c.command, rec.got, c.want)
		}
	}
}
