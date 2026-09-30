// Package reward turns one recorded episode (a team's run of a task) into
// named reward components, hack flags, a counterfactual cost and, from those,
// a scalar reward per agent, per role and per compaction event. It is the
// reward side of the RL pipeline described in docs/TRAINING-DATA.md, sections 5
// and 6, and it never calls a model.
//
// # Shape
//
//	Score      Episode (+ Task, blobs) -> Reward.Components/Total, Agent.Reward,
//	           compactor Step.Reward, Cost.ITE, hack:* flags. Idempotent.
//	Reprice    Episode + target price/cache model -> ITE, per agent, per step.
//	Probes     compaction fidelity probes derived from the archived thread.
//	Config     weights, caps, target price model; JSON loadable.
//
// # Design rules
//
//   - Every reward is a weighted sum of components that are stored raw, so a
//     run can be re-scored (Score again, or Config.Total on stored components)
//     without re-running anything. Penalty components are stored as values in
//     [-1, 0] and bonuses in [0, 1]; weights are therefore always non-negative
//     and comparable.
//   - Hack detection reads evidence the policy cannot rewrite: the harness's
//     own unified diff (before protected paths are stripped), the tool calls
//     it made, and the task's verifier definition. Flags never disappear once
//     set, and any hack:* flag forces the outcome component to zero.
//   - Cost is priced like production: the recorded request recipes (prompt
//     sizes, shared prefixes, timing) are replayed through a deterministic
//     prefix-cache model of the *target* provider, not the one the rollout
//     happened to run on.
//   - Score never mutates prompts, and on error leaves the episode untouched.
//   - Everything is deterministic: sorted iteration, no clocks, no randomness
//     (probe sampling is a hash of episode and step ids).
//   - Input is hostile. Diffs, commands, paths and tool output are written by
//     the policy being trained, so every detector runs in time linear in its
//     input (single passes, no per-prefix or per-suffix rescans) and the few
//     inherently pairwise steps have explicit budgets that fail towards
//     "suspicious". Protected patterns longer than 1 KiB are refused. The
//     tests in scaling_test.go pin the growth rate of each detector that once
//     met an input it could not take.
package reward
