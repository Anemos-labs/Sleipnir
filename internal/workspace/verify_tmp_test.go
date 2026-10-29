package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A verification run keeps its temporary files in a directory of its own inside the base environment's temporary directory,
// and nothing of it is left when the run ends. In a team's day of merge gates, test helpers that built a server binary
// into os.MkdirTemp and never removed it filled /tmp with 290 copies.
func TestAVerifyRunsTemporaryFilesGoWithIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	parent := t.TempDir()
	res := RunShell(context.Background(), VerifyRequest{Dir: t.TempDir(), Cmd: `d=$(mktemp -d) && echo bin > "$d/server" && echo "$d"`,
		BaseEnv: []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + parent}})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run: %+v", res)
	}
	made := strings.TrimSpace(res.Output)
	if rel, err := filepath.Rel(parent, made); err != nil || strings.HasPrefix(rel, "..") || !strings.Contains(rel, string(filepath.Separator)) {
		t.Fatalf("the run's temporary directory %q is not a directory of its own inside %q", made, parent)
	}
	if left, _ := os.ReadDir(parent); len(left) != 0 {
		t.Fatalf("the run left %v in %s", left, parent)
	}
	// The caller's own TMPDIR still decides.
	own := t.TempDir()
	res = RunShell(context.Background(), VerifyRequest{Dir: t.TempDir(), Cmd: `echo "$TMPDIR"`, Env: []string{"TMPDIR=" + own},
		BaseEnv: []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + parent}})
	if got := strings.TrimSpace(res.Output); got != own {
		t.Fatalf("TMPDIR = %q, want the caller's %q", got, own)
	}
}
