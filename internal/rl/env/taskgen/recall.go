package taskgen

import (
	"context"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// RecallGenerator is the hook through which recall tasks are made. It is an
// interface only: turning a finished run into a recall task is out of scope for
// this package.
//
// The idea (docs/TRAINING-DATA.md, section 3.2) is to run an ordinary task, then
// ask a question that can only be answered from an early detail of the run (an
// error message, a file name, a constant found ten compactions ago) and to
// check the answer by exact match. Such a task has Kind rl.TaskRecall, a prompt
// holding the question, and Verifier.Expect {"contains": [...]} listing what the
// answer must contain; env.Verify judges it against the agent's final message
// with no command needed. The generator needs the run's events and blobs to find
// early details and an Episode to know when each was produced, which is why it
// lives with the trajectory tooling rather than here.
type RecallGenerator interface {
	// FromRun derives recall tasks from the run in runDir. The tasks start from the
	// same repository state as the original run so that the agent's workspace
	// (and therefore what it can recall) is comparable.
	FromRun(ctx context.Context, runDir string, ep *rl.Episode) ([]rl.Task, error)
}
