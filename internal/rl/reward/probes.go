package reward

import "github.com/reee344/sleipnir/internal/rl"

// PromptText resolves the exact text a step's model call saw (system blocks,
// pins, thread), for fidelity probes. Implementations expand the step's prompt
// manifest; the reward package itself never touches the blob store.
type PromptText func(ep *rl.Episode, st *rl.Step) (string, error)

// PromptSource may be implemented by a DiffSource that can also resolve prompt
// text, so a single blob-backed object serves both needs.
type PromptSource interface {
	PromptText(ep *rl.Episode, st *rl.Step) (string, error)
}
