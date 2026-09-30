// Package traj turns a recorded run (an event log plus its content-addressed
// blobs) into a canonical rl.Episode: who did what, in which exact prompt, with
// which completion, token ids and outcome.
//
// It is the first stage of the RL data pipeline (docs/TRAINING-DATA.md). It reads
// what internal/agent and internal/swarm already log and derives everything else,
// so the same code serves rollouts against a policy and ordinary interactive
// sessions.
//
//	run, _ := traj.Open("runs/r001/task-7/0")     // events.jsonl + blobs/ (+ task.json)
//	if bad := run.Verify(); len(bad) > 0 { ... }  // replay check
//	ep, _ := run.Episode(traj.Options{TaskID: "task-7", Sample: 0, Policy: rl.PolicyRef{Model: "my-policy"}})
//	prompt, _ := run.Prompt(ep.Agents[0].Steps[3].Prompt.Req) // the exact model-visible prompt
//
// # Fidelity
//
// A prompt is never re-rendered. Every model.request carries a manifest of blob
// hashes, delta-encoded against the agent's previous request, and a wire hash of
// the model-visible prompt. [Run.Prompt] rebuilds the prompt from the manifest
// chain and refuses to return one whose parts do not hash to the recorded wire
// hash. [Run.Verify] does that for every request, and also checks that every
// completion blob is an assistant turn and every token trace parses. An episode
// built from a log that fails the check is flagged replay_mismatch and exporters
// drop it.
//
// # Steps, segments, edges
//
// Each model.request with a matching model.response (by req id; the log
// interleaves responses of concurrent calls) is a step. A request that never got
// a response (error, kill, cancel) is dropped and counted in
// Signals["request_errors"]. Step.Kind is the request kind (main, compactor, ...)
// and Step.Role the request's role, so a compactor fork of a "backend" agent
// trains the role "compactor".
//
// A segment is a maximal run of one agent's main steps whose prompts are
// append-only extensions of each other. The manifest chain decides where a
// segment ends (message hash lists are compared; no blob is read). Events only
// label why: "compact" (compact.commit or an agent layer.commit in between),
// "epoch" (a shared-epoch or shared-sync layer.commit), "strip" (any other
// rebase: thinking strip, a changed tool list, a changed system block, a model
// swap). A hot tail (the swarm's live board block, appended to the last message
// of a request and replaced on the next) is not a rebase: message-level
// continuity is judged on the persistent messages. Side calls (compactor and the
// like) are forks of the agent's current prompt and take the current segment.
//
// Token traces are kept when they are individually consistent. Whether one
// segment's traces chain into a single sequence (the strict prefix property
// P[i+1] = P[i] ++ C[i] ++ observation) is checked and counted in
// Signals["token_prefix_breaks"]; see [Options.StrictTokens] for the stricter
// reading. Ids are never repaired, re-tokenised or invented.
//
// Edges: spawn (parent's last main step before agent.spawn -> child agent), mail
// (sender's last main step before mail.send -> recipient's first main step after
// mail.deliver, Ref the mail id), compact (the compactor call behind a
// compact.commit -> the first main step after it, Ref "<agent>#<n>"), promote
// (each note call between shared epochs -> "epoch:<n>").
//
// # Signals
//
// Signals are counts and durations the reward package turns into terms. All of
// them are derived from the log alone; where the log lacks a field the
// derivation falls back to a documented heuristic, marked (h).
//
//	requests               model.request events + retry attempts (model.error with an attempt field): every provider call
//	request_errors         requests with no usable step (no response, unreadable completion)
//	request_retries        retry attempts among requests
//	steps                  kept main-kind steps, all agents
//	worker_steps           kept main steps of agents whose role is not "manager"
//	critical_path_steps    longest chain of main steps: same-agent order, spawn and mail edges; agents run concurrently
//	compactions            compact.commit events
//	compact_rejects        compact.reject events (rejected patch or mechanical fallback)
//	cache_anomalies        cache.anomaly events
//	tool_errors            tool results that are errors (tool.result error flag or tool_result is_error)
//	invalid_tool_calls     tool calls whose arguments were unusable: tool_use flagged Invalid, or a result classified
//	                       unknown_tool / invalid_input (h)
//	stale_writes           results classified stale: "changed since you last read it", "has not been read by you yet" (h)
//	lease_conflicts        results classified lease: "is being edited by" (h)
//	scope_violations       results classified scope: "outside your scope", "cannot widen scope", "overlaps" (h)
//	recalls                tool calls to recall
//	verifier_runs          bash calls that ran the task's checks: the task's verifier command, or a common test runner (h)
//	done_claims            task calls with action done
//	done_accepted          done claims whose result was not an error (the harness gate passed)
//	reads_after_compaction reads of a path that the agent read before a compaction folded that read away, with no edit
//	                       of it in between; needs the commit's keep_from and the turn ids
//	duplicate_work         identical (tool, arguments) calls by different agents (coordination tools excluded) plus,
//	                       per file, each agent beyond the first that successfully edited it (h)
//	mail_sent              mail.send events
//	mail_duplicate         mail.send with the same sender, recipient and text as an earlier one
//	mail_ignored           delivered mail whose recipient made no main request afterwards
//	spawns                 agent.spawn events that have a parent
//	spawn_no_result        spawned workers with no main step, or that ended failed, or whose file edits all failed and
//	                       that never had a done accepted (h, coarse)
//	idle_ms                time spawned workers sat between an agent.end and their next request
//	token_prefix_breaks    consecutive main steps in a segment whose token traces violate the strict prefix property
//
// Tool error kinds are heuristic because tool.result carries no error kind. A
// tool that puts meta["error_kind"] (stale, lease, scope, unknown_tool,
// invalid_input) in its result makes the derivation exact.
//
// # Outcome, cost, flags
//
// outcome events become verdicts (the last verifier verdict is the result;
// review, human and protocol verdicts are kept in order); Claimed is derived from
// the manager's or sole agent's final state (see [Options]); Labels count
// perm.decide refusals ("perm_denied:n") and user.steer messages ("user_steer:n").
// Cost sums usage over every response (compactor calls included) and
// cost_usd; WallMs spans first to last event. Flags: infra_error (an outcome of
// kind infra failed), truncated (no session.end and the root agent neither ended
// nor finished with a final answer), replay_mismatch (Verify found a problem that
// touches exported data), weak_label (no verifier outcome), token_mismatch (a
// token trace was present but inconsistent, or StrictTokens found a chain break),
// budget_exceeded (a governor event mentions a budget).
//
// # Determinism
//
// Episodes are pure functions of the log and the options: agents are ordered by
// first appearance, edges by log position, maps are keyed and sorted, and no
// timestamp is embedded in derived text.
package traj
