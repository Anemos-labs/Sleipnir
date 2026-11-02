package session

import (
	"context"
	"time"

	"github.com/anemos-labs/sleipnir/internal/machine"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// runVerify uses the integration verifier's command isolation, environment
// scrubbing, and bounded head-and-tail output capture for task verification.
// A nonzero exit is a test verdict; startup failures, cancellation, and timeouts
// return errors so the swarm can distinguish them from failed tests.
func runVerify(ctx context.Context, dir, cmd string) (string, int, error) {
	return runVerifyIn(ctx, dir, cmd, nil, nil, nil)
}

// verifier returns the swarm's verify function for this session: the agent's
// own command sandbox (Options.ShellWrap) and environment (Options.ShellEnv)
// apply to the done check too, since it runs code the agents wrote.
func (s *Session) verifier() func(ctx context.Context, dir, cmd string) (string, int, error) {
	wrap, env, b, mb := s.opts.ShellWrap, s.opts.ShellEnv, s.barrier, s.machine
	if len(wrap) == 0 && env == nil && b == nil && mb == nil {
		return runVerify
	}
	return func(ctx context.Context, dir, cmd string) (string, int, error) {
		if b != nil {
			// Tests write (caches, reports): a savepoint must not capture the tree half way.
			b.RLock()
			defer b.RUnlock()
		}
		return runVerifyIn(ctx, dir, cmd, wrap, env, mb)
	}
}

// runVerifyIn is runVerify under an argv prefix, a base environment (nil: the process's own) and a machine share (nil: none).
func runVerifyIn(ctx context.Context, dir, cmd string, wrap, env []string, mb *machine.Budget) (string, int, error) {
	res := workspace.RunShell(ctx, workspace.VerifyRequest{
		Dir: dir, Cmd: cmd, Timeout: 20 * time.Minute, MaxOutput: 256 << 10, Wrap: wrap, BaseEnv: env, Machine: mb,
	})
	if res.TimedOut {
		return res.Output, res.ExitCode, context.DeadlineExceeded
	}
	return res.Output, res.ExitCode, res.Err
}

// queueVerifier is the integration queue's verify function for this session: the merged code of the agents runs under the same
// sandbox and environment as their own commands (and as the done check, verifier), keeping the queue's timeout, output bound,
// extra environment and cancellation, in the session's share of the machine. Nil when the session sets none of these, which
// leaves the queue's default.
func (s *Session) queueVerifier() workspace.VerifyFunc {
	wrap, env, mb := s.opts.ShellWrap, s.opts.ShellEnv, s.machine
	if len(wrap) == 0 && env == nil && mb == nil {
		return nil
	}
	return func(ctx context.Context, req workspace.VerifyRequest) workspace.VerifyResult {
		if req.Wrap == nil {
			req.Wrap = wrap
		}
		if req.BaseEnv == nil {
			req.BaseEnv = env
		}
		if req.Machine == nil {
			req.Machine = mb
		}
		return workspace.RunShell(ctx, req)
	}
}
