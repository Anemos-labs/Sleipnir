package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A helper binary inside a mounted directory is run from a private copy outside every mount; one outside is run as it is.
func TestSandboxHelperIsRunFromOutsideTheMounts(t *testing.T) {
	proj := t.TempDir()
	exe := filepath.Join(proj, "bin", "sleipnir")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := helperOutside(exe, []string{proj})
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(proj)
	if inside(real, got) || inside(proj, got) {
		t.Fatalf("the copy %s is inside the mount", got)
	}
	if b, _ := os.ReadFile(got); string(b) != "binary" {
		t.Fatalf("copy holds %q", b)
	}
	cleanup()
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("the copy was not removed: %v", err)
	}
	other := t.TempDir()
	if got, _, err := helperOutside(exe, []string{other}); err != nil || got != func() string { r, _ := filepath.EvalSymlinks(exe); return r }() {
		t.Fatalf("outside every mount: %q %v", got, err)
	}
}
