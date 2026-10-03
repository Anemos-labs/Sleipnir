package session

import (
	"context"
	"time"

	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// runVerify uses the integration verifier's command isolation, environment
// scrubbing, and bounded head-and-tail output capture for task verification.
// A nonzero exit is a test verdict; startup failures, cancellation, and timeouts
// return errors so the swarm can distinguish them from failed tests.
func runVerify(ctx context.Context, dir, cmd string) (string, int, error) {
	res := workspace.RunShell(ctx, workspace.VerifyRequest{
		Dir: dir, Cmd: cmd, Timeout: 20 * time.Minute, MaxOutput: 256 << 10,
	})
	if res.TimedOut {
		return res.Output, res.ExitCode, context.DeadlineExceeded
	}
	return res.Output, res.ExitCode, res.Err
}
