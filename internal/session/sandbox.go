package session

import (
	"context"
	"fmt"
	"os"

	"github.com/anemos-labs/sleipnir/internal/sandbox"
)

// startSandbox starts the session's container (Options.Sandbox) and points every shell command at it: the project root, and the
// directory the team's trees are made in when the team works in trees, are mounted at their own paths, and the shell's argument
// prefix becomes the exec helper. A session that asked for a sandbox and cannot have one does not start: running its commands on
// the host instead would be the opposite of what was asked.
func (s *Session) startSandbox(ctx context.Context) error {
	if len(s.opts.ShellWrap) > 0 {
		return fmt.Errorf("--sandbox: this session already runs its commands under another wrapper")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("--sandbox: %w", err)
	}
	mounts := []string{s.opts.Root}
	if s.opts.Cwd != "" && !inside(s.opts.Root, s.opts.Cwd) {
		mounts = append(mounts, s.opts.Cwd)
	}
	if s.iso != nil {
		mounts = append(mounts, s.iso.top)
	}
	sandbox.Prune(ctx)
	box, err := sandbox.Start(ctx, s.opts.Sandbox, mounts, s.opts.Cwd, sandbox.DefaultLimits)
	if err != nil {
		return fmt.Errorf("--sandbox: %w", err)
	}
	// the bash tool runs bash: an image without it (alpine, distroless) would fail every command with "bash: not found"
	if !box.Has(ctx, "bash") {
		_ = box.Close()
		return fmt.Errorf("--sandbox: the image %s has no bash, which the bash tool runs; pick an image that has it (the default, %s, does)", box.Spec.Image, sandbox.DefaultImage)
	}
	s.box = box
	s.opts.ShellWrap = box.Wrap(exe)
	net := "no network"
	if box.Spec.Network {
		net = "network on"
	}
	s.notice("", fmt.Sprintf("commands run in the sandbox %s (%s, %s); the harness and its keys stay outside", box.Name, box.Spec.Image, net))
	return nil
}

// Sandbox is the container the session's commands run in, nil when they run on the host.
func (s *Session) Sandbox() *sandbox.Box { return s.box }

// stopSandbox removes the session's container, once.
func (s *Session) stopSandbox() {
	s.mu.Lock()
	box := s.box
	s.box = nil
	s.mu.Unlock()
	if box != nil {
		if err := box.Close(); err != nil {
			s.notice("", err.Error())
		}
	}
}
