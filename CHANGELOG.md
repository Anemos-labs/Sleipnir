# Changelog

Release entries describe behavior and compatibility changes. Prompt changes
include their cache implications.

## [Unreleased]

- Label assigned task paths as `SCOPE` in the agent table. A write lease on one
  file no longer appears as ownership of its enclosing directory.
- Publish a worker's ending before allowing reassignment or mail recovery, so
  late terminal events cannot make its new run appear stopped. Ignore cleanup
  from an older run when a newer run owns the worker and its write leases.
- Show a stopped worker's recorded failure reason in its agent-table row.
- Preserve the beginning and end of long task-verification logs, including the
  final failure diagnostic. Use the same runner as integration verification for
  bounded output capture, environment scrubbing, and process cleanup.
  Keep the final verifier lines in worker retry prompts and manager notices when
  output also exceeds their line limit.
  Scrub inherited environment names containing a delimited `KEY` component,
  including `SIGNING_KEY_CONTENT`, from task and integration verifiers.
- Deliver recovery mail that arrives while a worker's request fails. New mail can
  wake the worker once; unread mail from before that run cannot create a retry
  loop. Report worker failures to the manager even after task submission, without
  undoing submitted work or newer assignments. Recovery appends mail to the
  conversation; stable prompt layers and tool schemas are unchanged.
- Preserve Windows `cmd` quoting for verification, hooks, and shell commands,
  including paths with spaces. Disable Command Processor AutoRun for those
  commands so registry startup commands do not alter harness execution.
- Keep canceled verification runners within the configured concurrency limit
  until they exit. Include queue wait and scope discovery in the verification
  deadline, and discard results received after that deadline. Serialize captured
  stdout and stderr to prevent buffer races and preserve the combined output cap.
- Bound repeated task-verification failures across explicit completion calls and
  implicit worker stops. Persist the repair count through recovery; after two
  repair retries, count a failed attempt and notify the manager with verifier
  evidence. Cancellation and verifier infrastructure errors do not spend repair
  retries. Show terminal failures in their own board column instead of counting
  them as running tasks. Stable prompt layers and tool schemas are unchanged.
- Preserve ChatGPT sign-in authentication when listing a provider's models or
  tuning built-in provider options. Label plan model prices as `plan` instead of
  zero dollars and exclude them from numeric price limits. Explicit catalog URLs
  do not inherit sign-in credentials.
- Include concurrent `doctor --deep` requests in request, usage, and reported-cost
  totals. Mark incomplete cost data as unavailable, retain failed warm-up samples,
  and stop later probes after cancellation.
- Preserve complete messages during `doctor` cache probes for message-boundary
  providers, including concurrent warm-up bursts. Grow the sequential probe by
  appending responses and user turns, and avoid
  interpreting rounded usage counts as cache-write block sizes. Probe history
  adds input tokens; ordinary session prompts and the renderer are unchanged.
- Resume isolated teams with their original Git base, worker conversations and
  unfinished worktrees. Preserve task ownership and applied integration positions;
  reconcile interrupted checkout writes without overwriting ambiguous edits. Keep
  recovery state until explicit session pruning, which salvages unique worker work.
  Recovery rebuilds shared context through the existing declared rebase; stable
  prompt layers, tool schemas and the renderer format are unchanged.
- Allow up to 16,384 estimated instruction tokens by default, bounded to one
  quarter of the initial model's context window, with an explicit
  `cache.instruction_max_tokens` override. Preserve later instruction files
  before earlier files when the allowance is exceeded, and identify the affected
  paths. G1 changes for sessions whose instructions exceeded the old 3,000-token
  limit: their first request uses a new shared prefix and may incur a cache write.
  Subsequent turns retain identical instruction bytes; larger instructions add
  input tokens. G0, tool schemas, and the renderer format are unchanged.
- Send the Responses `session-id` routing header consistently with the body
  cache key, preserving affinity across related requests.
- Preserve live-state notices in Responses history so later requests retain
  earlier message boundaries. The stable G0-G2 layers and renderer format are
  unchanged. Persisted notices add input until compaction; the update interval
  limits growth. Provider hit rates require endpoint measurement, and the
  simulator does not model Responses message-boundary writes.
- Derive routing keys from the full session identity. Independent sessions no
  longer share a key just because their IDs start with the same date. Existing
  sessions may incur a cold request when moving to the new key.
- Wait for generated Responses content before releasing cache warm-up followers.
- Account for Responses cache writes as a separate input category without
  double-counting tokens. Cache diagnostics distinguish internal prefix stability
  from unverified provider behavior.
- Show live statistics and agents panels without adding stale snapshots to chat.
- Interrupt active work when pausing or clearing a goal, or exiting chat.
- Replace development journals and anecdotal performance claims with usage,
  design contracts, limitations, and reproducible validation procedures.

## [0.1.0]

- Interactive coding sessions, provider login, model selection, resumable history,
  terminal tools, checkpoints, and standing goals with completion checks.
- Manager and worker teams with task boards, mail, leases, budgets, verification,
  optional worktree isolation, and a serial merge queue.
- Layered prompts, cache diagnostics, background compaction, recall, and a
  simulator with explicit workload and pricing assumptions.
- Chat Completions, Responses, and Anthropic Messages adapters; ChatGPT sign-in;
  configurable gateways and local inference servers.
- RL task generation, rollouts, rewards, and dataset exports.

### Prompt compatibility

`sleipnir-kv/3` separates adjacent user turns with a blank line when they render
as one message. It changes affected thread bytes only; G0-G2 are unchanged.

Stable JSON encodes invalid UTF-8 as a replacement character consistently across
Go versions. Valid text, tool schemas, and stable layers are unchanged.
