package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/sandbox"
)

// A reproducer runs in a container of its own that mounts only the disposable copy of the code, not in the session's container,
// which mounts the person's checkout read-write: a proof script cannot change the project it was sent to check.
func TestProofsRunInAContainerThatMountsOnlyTheCopy(t *testing.T) {
	root, copyDir := t.TempDir(), t.TempDir()
	helper := filepath.Join(t.TempDir(), "helper")
	// stands in for the exec helper: says which container it was sent to, from where, and what it runs
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho \"$2 $(pwd) $4 $5\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var spec sandbox.Spec
	var dirs []string
	old := startProofBox
	t.Cleanup(func() { startProofBox = old })
	startProofBox = func(ctx context.Context, sp sandbox.Spec, dir string, lim sandbox.Limits) (*sandbox.Box, error) {
		spec = sp
		dirs = append(dirs, dir)
		return &sandbox.Box{Name: "proof-box", Spec: sp, Mounts: []string{dir}}, nil
	}
	s := &Session{box: &sandbox.Box{Name: "session-box", Mounts: []string{root}}}
	s.opts.Root = root
	s.opts.Sandbox = sandbox.Spec{Engine: "docker", Network: true}
	s.opts.ShellWrap = s.box.Wrap(helper)
	out, exit, err := s.runProof(context.Background(), copyDir, []string{"bash", ".sleipnir-proof.sh"}, time.Minute)
	if err != nil || exit != 0 {
		t.Fatalf("runProof = %q, %d, %v", out, exit, err)
	}
	real, _ := filepath.EvalSymlinks(copyDir)
	if got := strings.TrimSpace(string(out)); got != "proof-box "+real+" bash .sleipnir-proof.sh" && got != "proof-box "+copyDir+" bash .sleipnir-proof.sh" {
		t.Errorf("the reproducer ran as %q, not in the proof's own container from the copy", got)
	}
	if len(dirs) != 1 || dirs[0] != copyDir {
		t.Errorf("the proof's container mounts %v, not the copy alone", dirs)
	}
	if spec != s.opts.Sandbox {
		t.Errorf("the proof's container is %+v, not one of the session's sandbox %+v", spec, s.opts.Sandbox)
	}
}
