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
// and nothing of it is left when the run ends.
func TestAVerifyRunsTemporaryFilesGoWithIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	parent := t.TempDir()
	res := RunShell(context.Background(), VerifyRequest{Dir: t.TempDir(), Cmd: `d=$(mktemp -d "$TMPDIR/build.XXXXXX") && echo bin > "$d/server" && echo "$d"`,
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

// The run's directory is made where the base environment's own variables point, in the platform's order, a relative one
// read from the command's directory.
func TestARunsTemporaryDirectoryFollowsTheBaseEnvironment(t *testing.T) {
	names := tempEnvNames()
	parent := t.TempDir()
	base := []string{names[0] + "=" + parent}
	if len(names) > 1 {
		base = append(base, names[1]+"="+t.TempDir())
	}
	got := runTempDir(base, t.TempDir())
	defer os.RemoveAll(got)
	if filepath.Dir(got) != parent {
		t.Fatalf("run directory %q, want one inside %q", got, parent)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = runTempDir([]string{names[0] + "=tmp"}, dir)
	defer os.RemoveAll(got)
	if filepath.Dir(got) != filepath.Join(dir, "tmp") {
		t.Fatalf("run directory %q, want one inside %q", got, filepath.Join(dir, "tmp"))
	}
}
