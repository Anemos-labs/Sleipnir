package agent

import "context"

// AwaitCompactionForTest waits for background jobs while the test provider pauses
// the main run. No new compaction job can start until that request returns.
// This helper is compiled only for tests; it is not part of the agent API.
func AwaitCompactionForTest(ctx context.Context, a *Agent) error {
	done := make(chan struct{})
	go func() {
		a.jobs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
