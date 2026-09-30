// Package taskgen builds tasks.jsonl for the RL environment from repositories
// instead of by hand (docs/TRAINING-DATA.md, section 3.2).
//
//	FromGit    commits that change source and tests become tasks: the parent commit
//	           is the starting state, the commit message the prompt, the commit's
//	           test files the hidden, protected verifier.
//	Mutate     small language-aware bugs (flipped comparison, dropped nil check,
//	           off-by-one, negated condition, inverted boolean) injected into Go and
//	           Python, with the project's own tests as the verifier.
//	Composite  k independent tasks of one commit combined into a swarm task whose
//	           verifier needs all of them.
//	RecallGenerator  a documented hook only.
//
// # Soundness
//
// A generated task is only worth having if its verifier separates doing nothing
// from doing the work. FromGit and Mutate therefore run the verifier in the real
// environment (env.CheckTask) before emitting a task: it must FAIL on the starting
// state (otherwise the task rewards inaction) and PASS with the reference solution
// applied (otherwise it is unsolvable, or its command cannot run here). Candidates
// that fail either check are reported with the reason, and infrastructure errors
// skip a candidate without rejecting it. Validation is what catches the classic
// failures of mined benchmarks: a test that passes before the fix, a -run pattern
// that matches nothing, a missing toolchain, a commit that fails its own test.
//
// # Leak control
//
// Prompts come from the cleaned commit message: trailers, links to the upstream
// commit or pull request and bare commit ids are removed, and workspaces are
// history-free (see package env), so the agent cannot look the solution up.
// Hidden test files are stored by content hash in a blob store that travels with
// the tasks file (GitOptions.HiddenBlobs); without one they are inlined as text,
// which puts verifier content in tasks.jsonl. Task.Meta records the source commit
// and author date for date-based splits (env.Holdout) and the changed files that
// Composite uses to prove independence.
package taskgen
