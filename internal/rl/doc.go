// Package rl is the shared vocabulary of Sleipnir's reinforcement-learning
// pipeline: the harness as an environment.
//
// The pipeline has four stages, each in its own package so they can be used
// alone or together (see docs/TRAINING-DATA.md):
//
//	env     tasks, isolated rollouts, clean-checkout verification, task generators
//	traj    event log + blobs -> Episode (exact prompts, completions, token traces)
//	reward  Episode (+ Task) -> named reward components, hack flags, repricing
//	export  Episode -> trainer-ready formats (steps, tokens, groups, sft, dpo, ...)
//
// This package holds only the types those stages exchange, so it imports
// nothing from them. Everything here is plain data with a JSON encoding; the
// JSON field names are a stable contract (bump SchemaEpisode on a breaking
// change).
package rl
