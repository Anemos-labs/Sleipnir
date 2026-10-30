package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/sandbox"
)

// startSandbox starts the session's container (Options.Sandbox) and points every shell command at it: the project root, and the
// directory this session's trees are made in when the team works in trees, are mounted at their own paths, and the shell's argument
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
		// this session's trees only: the directory above holds every other session's, of every project
		if err := os.MkdirAll(s.iso.dir, 0o700); err != nil {
			return fmt.Errorf("--sandbox: %w", err)
		}
		mounts = append(mounts, s.iso.dir)
	}
	// The helper runs on the host for every command. A binary inside a mounted directory (a build of the project itself) could be
	// replaced from inside the container, so a private copy outside every mount is run instead.
	exe, cleanup, err := helperOutside(exe, mounts)
	if err != nil {
		return fmt.Errorf("--sandbox: %w", err)
	}
	sandbox.Prune(ctx)
	box, err := sandbox.Start(ctx, s.opts.Sandbox, mounts, s.opts.Cwd, sandbox.DefaultLimits)
	if err != nil {
		cleanup()
		return fmt.Errorf("--sandbox: %w", err)
	}
	s.boxCleanup = cleanup
	// the bash tool runs bash: an image without it (alpine, distroless) would fail every command with "bash: not found"
	if !box.Has(ctx, "bash") {
		_ = box.Close()
		cleanup()
		return fmt.Errorf("--sandbox: the image %s has no bash, which the bash tool runs; pick an image that has it (the default, %s, does)", box.Spec.Image, sandbox.DefaultImage)
	}
	s.box = box
	s.opts.ShellWrap = box.Wrap(exe)
	net := "no network"
	if box.Spec.Network {
		net = "network on"
	}
	image := box.Spec.Image
	if tc := box.Toolchain; tc.Lang != "" {
		image += ", for the " + tc.Manifest + " of the project"
	}
	if box.Fetched != "" {
		net += "; before the network went off, " + box.Fetched
	}
	s.notice("", fmt.Sprintf("commands run in the sandbox %s (%s, %s); the harness and its keys stay outside", box.Name, image, net))
	return nil
}

// Sandbox is the container the session's commands run in, nil when they run on the host.
func (s *Session) Sandbox() *sandbox.Box { return s.box }

// stopSandbox removes the session's container, once.
func (s *Session) stopSandbox() {
	s.mu.Lock()
	box, cleanup := s.box, s.boxCleanup
	s.box, s.boxCleanup = nil, nil
	s.mu.Unlock()
	if box != nil {
		if err := box.Close(); err != nil {
			s.notice("", err.Error())
		}
	}
	if cleanup != nil {
		cleanup()
	}
}

// helperOutside returns exe when no mount holds it, else a copy of it in a new private directory that none holds, with the function
// that removes the copy.
func helperOutside(exe string, mounts []string) (string, func(), error) {
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	within := func(p string) bool {
		for _, m := range mounts {
			if r, err := filepath.EvalSymlinks(m); err == nil {
				m = r
			}
			if inside(m, p) {
				return true
			}
		}
		return false
	}
	if !within(exe) {
		return exe, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "sleipnir-helper-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if r, err := filepath.EvalSymlinks(dir); err == nil && within(r) {
		cleanup()
		return "", nil, fmt.Errorf("the harness binary %s is inside a mounted directory and no place outside the mounts was found for a copy of it", exe)
	}
	src, err := os.Open(exe)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer src.Close()
	dst := filepath.Join(dir, filepath.Base(exe))
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err == nil {
		_, err = io.Copy(out, src)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return dst, cleanup, nil
}
