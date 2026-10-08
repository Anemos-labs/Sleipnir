# Changelog

[Releases](https://github.com/Anemos-labs/Sleipnir/releases) are the versioned
release history, generated from merged pull requests and tied to their source
commits. The notes below are cumulative compatibility and prompt-cache changes;
they do not indicate whether a change has been released.

## Compatibility changes

- Count workers, not the manager, in every team size a person types or reads.
  `--swarm N`, `sleipnir swarm N`, `/swarm N` and `rl --mode swarm:N` mean a
  manager and N workers again; `--swarm 0` is a single agent and
  `sleipnir swarm` takes 1 or more. The chat on a terminal defaults to a
  manager and eight workers, nine agents in all, and the banner says
  `manager + N workers`. The cockpit's eight legs are the workers: the manager
  rides and takes no leg, and a ninth worker shares the first leg. The
  `swarm.max_agents` setting, which counted the manager, is replaced by
  `swarm.max_workers` (and `SLEIPNIR_SWARM_MAX_WORKERS`), which does not; a
  file or variable that still names `max_agents` is refused with the new key
  and its value, `max_agents` minus one. The mailman no longer takes a worker's
  place under the cap, and `sleipnir init` writes `"max_workers": 12`.
- The manager edits no file in any run. It plans, delegates and reviews; its
  writes and writing shell commands are refused at run time with a pointer to
  spawn a worker, in a shared checkout as in an isolated one, while it keeps
  the same tool list as every other agent. The manager role pin (G2) changes
  once to say so and can require a fresh provider cache prefill for the
  manager; other stable layers and tool-schema bytes are unchanged.
- Reject task actions that require an ID when it is omitted or blank. The error
  identifies the missing field and explains how to find the task ID before
  retrying; invalid calls leave the board unchanged. Role restrictions and
  manager review remain enforced. Stable prompt and tool-schema bytes are unchanged.
- Describe the detected command shell and operating system in frozen shared
  runtime context for every agent. The `bash` description no longer promises
  Bash execution or recommends `&&` for every shell. Tool-description (G0) and
  shared-context (G1) bytes change once on upgrade and can require a fresh
  provider cache prefill. Runtime guidance stays byte-identical within a
  session; tool input schemas, default shell selection, and permission rules are
  unchanged. Uppercase `.EXE` shell names retain their native invocation flags.
- Retry ref-lock creation when concurrent branch deletion removes its parent
  directory. The existing lock-wait bound still applies; missing revisions,
  permission failures, and ref-value conflicts are not treated as contention.
  Prompt and tool-schema bytes are unchanged.
- Base verification reminders on tool outcomes. Refused edits no longer count
  as code changes, and refused test commands no longer count as executed tests
  or clear evidence of a failing run. A successful edit outside test files also
  clears a pending warning from the same batch. Existing advisory messages,
  stable prompt layers, and tool-schema bytes are unchanged.
- Make `/plan <prompt>` submit a planning request after switching to plan mode,
  preserving the supplied text and current team. Bare `/plan` remains a mode-only
  command. The planning instruction is appended in the new user turn; stable
  prompt layers and tool-schema bytes are unchanged.

- Add `cache.compaction_mode=blocking` to finish an agent's automatic model
  compaction before its next request, avoiding overlapping copies of its history
  on endpoints with independent slot caches. Other agents remain concurrent;
  background compaction remains the default. Both modes retain the same commit
  policy, cancellation, timeout and fallback. Stable prompt and tool-schema
  bytes are unchanged; a blocking commit can rebase the next request earlier.
- Bind task verification and merge evidence to the checked assignment and scope.
  Scope changes invalidate in-flight results without spending verification retries;
  implicit completion retries the current scope. Acceptance rejects obsolete
  evidence. Stable prompt and tool-schema bytes are unchanged.
- Keep delayed task failures and merge results scoped to their original assignment.
  Exhausted merge retries and manager failure actions cannot cancel a replacement
  run, and an older merge record cannot erase a newer assignment's acceptance
  evidence. Stable prompt and tool-schema bytes are unchanged.
- Interpret Go package selectors separately from literal filenames, allowing
  recursive test, build, list, and vet commands on Windows. Preserve literal
  output flags and custom test arguments. Apply explicit deny and ask rules to
  descendants of recursive accesses; a child allow rule does not authorize its
  parent tree. Require approval for Go overlays before build/test allow rules,
  since their replacement paths are not visible in command operands. Stable
  prompt and tool-schema bytes are unchanged.
- Return an actionable error when a manager waits on unfinished work with no
  running workers, allowing the repetition guard to bound repeated impossible
  waits. Preserve waits for active workers, future work, mail, and settled targets.
  Planning-task claim errors name the spawn action needed to start a read-only
  owner. Stable prompt and tool-schema bytes are unchanged.
- Resolve native Windows workspace paths consistently for file permissions, including
  existing and new files, symlink and junction targets, protected files, explicit
  rules, and worker confinement. Require native permission and file-tool regressions
  in CI. Permission patterns use forward slashes; unsupported Windows path namespaces
  and aliases are refused. Shell-analysis limitations remain unchanged.
- Carry reviewed task agreements into dependent assignments, including transitive
  prerequisites, compaction, worker reuse, and recovery. Add full task retrieval
  and preserve tasks claimed during a run as harness assignments. Team guidance
  calls for shared decisions before dependent implementation and review of the
  assembled result. Read-only planning tasks require an agreement and manager
  acceptance, independently of the code verifier. Preserve blocked assignments
  and worktree instructions during reuse and claims. Bound compacted harness
  assignments by bytes so token calibration cannot truncate an admitted contract.
  Swarm constitution, manager role, and coordination tool bytes change once on
  upgrade; agreements do not rewrite shared project context per request. Assignment
  retention changes take effect at the existing declared compaction rebase.
- Add `/effort` to inspect and change reasoning effort during chat, including
  running teams. Map each model to its closest supported level and recover from
  explicit effort validation errors with bounded retries. Preserve the preference
  on resume. Effort changes declare a cache rebase and may invalidate the provider's
  message cache; prompt text and tool schemas remain stable.
- Bind watchdog cancellation and alerts to the worker run that was checked, so
  a delayed decision or alert cleanup cannot stop or clear warnings for its replacement.
- Check composite benchmark tasks against all recorded reference patches instead
  of skipping them. Reject malformed reference metadata during task admission.
  Exclude four mined tasks whose hidden tests require unspecified symbols and
  refresh the suite lock to 56 tasks.
- Label assigned task paths as `SCOPE` in the agent table. A write lease on one
  file no longer appears as ownership of its enclosing directory.
- Publish a worker's ending before allowing reassignment or mail recovery, so
  late terminal events cannot make its new run appear stopped. Ignore cleanup
  from an older run when a newer run owns the worker and its write leases.
- Show a stopped worker's recorded failure reason in its agent-table row.
- Contain Windows task and integration verifiers in Job Objects so cancellation,
  timeouts, and completed commands also terminate their remaining descendants.
  Command startup fails if the job cannot be established.
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
