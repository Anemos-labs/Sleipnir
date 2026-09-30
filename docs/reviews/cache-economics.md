# Adversarial review: prompt-cache economics and correctness

Reviewer lens: does the cache engine (`internal/kv`, `internal/agent`, `internal/provider/{openaichat,mock}`,
`internal/cost`, `internal/kv/sim`, `internal/swarm/gate.go`) save the money `docs/CACHE-DESIGN.md` says it saves,
and is it correct on Anthropic (explicit breakpoints, preserved thinking) and on automatic-prefix engines?

Tree state reviewed: 2026-09-30 00:15 to 01:00 UTC. Other builders were editing concurrently; two changes landed
mid-review and are covered: the cold-cache `ModeMask` path (`kv.MaskOnly`, `Planner.ShouldStart` modes) and the
`internal/kv/sim` simulator with CACHE-DESIGN §8. No non-test file was modified during the review. Ground truth for provider
semantics is the bundled `claude-api` reference (`prompt-caching.md`, `model-migration.md` "Breaking change 3",
`preserved-thinking-migration/causes.md`; citations like `prompt-caching.md:83` are line numbers in those files under
`.../claude-api/shared/`) and `docs/research/02-provider-caching.md`. ITE = input-token equivalent (1 = one plain input token).

## Status after the fix pass

The findings below describe the tree **as reviewed** and are kept as written, because the evidence and the numbers
are the argument for the design that replaced it. Each has since been fixed, or deliberately left, as recorded in
"Resolution" (next section). The review tests were then converted into permanent regression tests: they assert the
*fixed* behaviour, pass by default (the `REVIEW_STRICT` switch is gone), and the always-sound ones are kept. The
fixed design is in `docs/CACHE-DESIGN.md` (rewritten: corrected claims, regenerated §8 numbers).

## How to reproduce

The permanent tests are named `TestCacheEcon_*` (kv, agent, mock, openaichat, swarm) plus `TestSim*` in
`internal/kv/sim` and the planner/hot tests in `internal/kv` (see the Test index). They fail if the defect returns.

```sh
go test -race -count=1 ./internal/kv/... ./internal/agent ./internal/swarm ./internal/provider/...
go test -race -count=1 -run 'CacheEcon|TestSim' -v ./internal/kv ./internal/kv/sim ./internal/agent \
    ./internal/provider/mock ./internal/provider/openaichat ./internal/swarm
```

Shared instruments used by the tests (worth reusing):

* `internal/kv/cache_regress_test.go`: `cxSim`, a small model of Anthropic explicit caching (entries only at
  breakpoints, 20-position lookback, runs of tool_use/tool_result count once, read up to the highest hit, write to
  the last breakpoint) and `cxAuto`, the same for an automatic-prefix engine. Both are driven by the real
  `kv.Render`, `kv.PlanMarks`, `kv.Apply`, `Thread.Commit`; the planner's commit penalty is checked against what
  `cxSim` actually charges (within a few percent).
* `internal/agent/cache_regress_test.go`: `cxProv`, a fake provider that speaks the `anthropic` dialect and
  **enforces preserved-thinking bindings**: a thinking block's signature is the hash of everything on the wire
  before it (tools, system, every earlier message *as sent*). A replayed block whose prefix changed returns
  HTTP 400 (`provider.ErrThinkingBinding`), exactly what `prefix_mismatch_behavior: error` does.

## Ranked findings

| # | Sev | Finding | Review-time test (permanent ones: see Resolution) |
|---|---|---|---|
| R1 | blocker (Claude route) | Hot tail is a per-request rewrite of the last user message: every request after the first is a thinking-binding 400; Guard cannot see it | `HotTailBreaksPreservedThinking` |
| R2 | high | `Thread.Commit` carries the tail with pre-commit thinking; `SyncShared` strip is a flag consumed one boundary late | `BackgroundCommitBreaksThinkingOfCarriedTurns`, `SyncSharedBetween...`, `CommitTailKeeps...` |
| R3 | high ($) | Compactor fork sets `tool_choice: none`: on Anthropic that invalidates the whole messages tier (G1-G5). Sim understates the bill by 26-47% | `ForkChangesToolChoice`, `SimBillsForks...` |
| R4 | high | A marginal ready patch is held forever (hard limit evaluated on the snapshot, emergency path unreachable): unbounded growth for a warm agent | `HeldMarginalPatchBlocksCompactionForever` |
| R5 | high (marketplace) | Planner sizes the thread with reasoning text that is never sent: 12,274 planner tokens vs 152 actually sent | `PlannerCountsReasoningThatIsNeverSent` |
| R6 | high/med | "Warm" = `lastHit >= 0.3` mistakes big tool results and non-reporting providers for a cold cache; the low-hit alarm silences itself | `WarmHeuristicMisreads...`, `NonReportingProviderLooksColdForever`, `LowHitAnomaly...` |
| R7 | high/med | New `ModeMask`: commit storm (thinking strip counts as progress) or no fold at all above the hard limit; no fork fallback | `ColdModeCommitsEveryStep...`, `ColdModeWithNothingToMaskNeverFolds` |
| R8 | med | Planner penalty omits tail, old spine, notes; T excludes masking savings; compactor cost unused: 3.7x error, flips the decision | `PlannerPenaltyOmits...`, `PlannerAcceptsMoneyLosingCommit`, `SnapTotalExcludes...` |
| R9 | med | Breakpoint policy: 1500-token floor drops role/shared/notes markers, no G0 marker, whole spine block rewritten per commit | `SmallLayersLoseCrossAgentSharing` |
| R10 | med | 20-position lookback is in `Caps` but never planned for; a mail burst rewrites the whole thread | `LookbackWindowIsNotPlannedFor` |
| R11 | med | Guard/low-hit blind spots: 25% miss is silent, params/model/roles unhashed, first requests never checked | `GuardBlindSpots`, `LowHitAnomaly...` |
| R12 | med | Warm gate keyed by `GlobalKey` only (not shard, not role); warm window overshoots TTL; 45 s maxWait stampede | `GateKeyIgnoresShardAndRole`, `GateWarmWindow...`, `GateStampedes...` |
| R13 | med/low | Affinity is "tested" only on a mock that hard-codes it; one key per swarm by default vs OpenAI's ~15 RPM/key | (analysis) |
| R14 | med | "User instructions survive verbatim" is false for steering, long tasks, and later compactor note ops | `UserInstructionGuarantees` |
| R15 | med | A reused worker never receives the new task's brief or scope | `ReusedWorkerNeverSeesTheNewTasksBrief` |
| R16 | med | The mock is not cache-faithful for explicit caches (and evicts roots first); the simulator uses an oracle warm test, ignores fork tool_choice and models a gate the code does not implement | `MockEvictsRoots...`, `MockIsBlind...`, `SimBillsForks...` |
| R17 | low/med | `openaichat` silently drops the rolling marker when it lands on a tool_result | `RollingBreakpointOnToolResultIsDropped` |
| R18 | med/low | Spine, instructions and notes grow without bound; whole spine block rewritten every commit; `NotesOverBudget` has no consumer | `SpineAndInstructionsGrowWithoutBound` |
| R19 | med | CACHE-DESIGN.md factual errors and overclaims (list below) | n/a |
| R20 | low | Render merge guard, thinking-block marker, estimator flicker and calibration bias, keys, config footguns, profile defaults | see R20 |

## Resolution

"Fixed" means the defect's test now asserts the correct behaviour and passes by default. Names are `TestCacheEcon_*`
unless noted.

| # | Status | What changed | Regression tests |
|---|---|---|---|
| R1 | fixed | `kv.HotMode`: `HotInline` (default), `HotPersist` (frozen notice block in the user turn, only on change, at most every 3 requests) and `HotTurnScoped` (persisted `Role:system` turn rendered as `Message{ClearAt}`). `kv.ResolveHot` forces persist where preserved thinking is replayed and the route has no turn-scoped system messages. `ErrThinkingBinding` is recovered once: durable strip, declared rebase, retry | `HotTailKeepsPreservedThinkingValid`, `TurnScopedHotIsPersistedAsClearAtSystemMessages`, `InlineHotRemainsTheDefaultWhereNothingIsBound`, `BindingRejectionIsRecoveredOnce`; kv `TestResolveHotPicksTheMechanismForTheRoute`, `TestTurnScopedHotIsAPersistedSystemMessage` |
| R2 | fixed | Thinking is stripped from the carried tail inside `Thread.CommitWith`; `SyncShared` strips under the same lock; a response in flight during an epoch is stripped in `pushResponse` (the in-flight exception is gone) | `CommitStripsThinkingFromTheCarriedTail`, `BackgroundCommitStripsThinkingOfCarriedTurns`, `SyncSharedStripsInTheSameStepAsTheSwap`, `EpochDuringARequestStripsTheResponsesThinking` |
| R3 | fixed | The fork keeps every request parameter of the parent (nothing moves to `tool_choice: none`); the instruction asks for text only; a `tool_use` reply is a failure and falls back to the mechanical patch; only text blocks are parsed | `ForkKeepsEveryRequestParameter`, `CompactorReplyIsParsedFromAnswerTextOnly`; sim `TestSimForkReadsTheParentsPrefix` |
| R4 | fixed | Hard/soft limits are judged on the live thread; a held patch goes `Stale` (age or growth) and is re-proposed; emergency compaction stays reachable while a patch is pending | `HeldPatchCannotBlockCompaction`; kv `TestShouldCommitJudgesTheLiveThread`, `TestStalePatchesAreDiscarded` |
| R5 | fixed | `kv.Sizer` counts what is sent: thinking that is not replayed is 0, cleared turn-scoped system turns are 0 | `PlannerIgnoresReasoningThatIsNeverSent`; kv `TestSizerCountsWhatRenderSends` |
| R6 | fixed | Warmth is the modelled TTL first (`Planner.IsCold` with `ColdMargin`), then guard-expected against actual reads; a provider that reports no cache usage is unknown, treated as warm; the low-hit alarm fires on a miss of at least max(2000 tokens, 5%) and the first request is checked when the gate says the prefix is warm | `BigToolResultIsNotAColdCache`, `NonReportingProviderIsNotCold`, `LowHitAlarmFiresOnEveryLargeMiss`, `FirstRequestIsCheckedAgainstAWarmSharedPrefix` |
| R7 | fixed | `MaskOnly` returns `ErrNothingToMask` when there is nothing to save; mask commits need `MaskMinTokens` and `MaskMinInterval`; a thinking strip is not progress; a cold agent above the hard limit still folds | `HiddenCacheIsNotACommitStorm`, `ColdMaskCommitsAreRateLimitedAndNeverEmpty`, `ColdAgentAboveTheHardLimitStillFolds`; kv `TestMaskOnlyNeedsSomethingToMask` |
| R8 | fixed | `commitEconomics` prices the commit as the cache charges it (explicit: w(R+D'+S') - r(T+D'+S); automatic: w(A+R+D') - r(T+D'); a notes change adds notes and spine); T includes masking savings; the start decision prices the compactor call and `Remaining` | `PlannerPenaltyMatchesWhatTheCacheCharges`, `PlannerNeverApprovesAMoneyLosingCommit`, `SnapTokensIncludeMaskingSavings`; kv `TestPlannerStartMatrix` |
| R9 | fixed | `kv.PlanMarks`: priority thread > anchor > shared/constitution > role (at the provider minimum) > notes (at `MinLayerForBreakpoint`) > constitution; G0, G1, G2 markers exist; the 1500-token floor applies to notes only, role and shared markers need just the provider minimum | `SmallLayersStillShareThePrefixAcrossAgents`; kv `TestPlanMarksSpendsSlotsInPriorityOrder` |
| R10 | fixed | The 20-position lookback is planned: a lookback anchor marker keeps the previous marker reachable when a turn appends many positions | `LookbackWindowIsPlannedFor` |
| R11 | fixed | The guard hashes request parameters, role, `ClearAt` and the first-block flag; exposes `ReadableTokens`; the alarm rule is in R6 | `GuardSeesWhatTheProviderKeysOn`, `LowHitAlarmFiresOnEveryLargeMiss` |
| R12 | fixed | The gate is keyed per level (shared, shard, role: nested `\|` keys); the warm window runs from request start minus a margin and ends before the entry expires; after `maxWait` the release is staggered, with a doubling co-primer escalation; `Warm(key)` is optional on the `Gate` interface | swarm `GateWarmWindowEndsBeforeTheProviderEntry`, `GateStaggersReleaseWhenThePrimerIsStuck`, `GateRecoversFromAPrimerThatNeverReports`, `GateElectsOnePrimerPerLevel`, `GateNestedKeysStress`; agent `GateKeyCoversShardAndRole` |
| R13 | **not fixed** (documented) | Affinity and shard sizing are unverified against a real marketplace. CACHE-DESIGN now says "designed for, not verified"; the default shard count and the D11 canary are follow-up work | none (analysis) |
| R14 | fixed | Steering (human) and long task text are preserved to `instructions`, bounded, with `[tN]` pointers; mail (`[mail` prefix) is data and never preserved; `instructions` and `assignment` are protected note keys | `UserInstructionGuarantees`, `SteeringSurvivesCompactionAndMailDoesNot`, `InboxBurstIsOneBlockPerClass` |
| R15 | fixed | `startRun` sends a reused worker a `reassignCard` with the new task's full brief and scope | swarm `ReusedWorkerReceivesTheNewTasksBrief` |
| R16 | partly | Mock (automatic mode): eviction takes tails before roots. Explicit-breakpoint caching is **not** in this engine: another engineer owns `mock/anthropic.go` and its explicit engine. Simulator: real `PlanMarks`/`Planner`, warm by entry lifetime (no oracle), forks read the parent's prefix, the gate it models is the gate the code implements; §8 regenerated | `MockEvictsTailsBeforeRoots`, `AutomaticModeKeysOnPrefixBytesOnly`; sim `TestSimPlacesMarkersWithTheRealPlanner`, `TestSimForkReadsTheParentsPrefix`, `TestSimGateIsKeyedAndTimedLikeTheRealOne`, `TestSimHeadlineWithTheFixesStaysInTheDocumentedRange` |
| R17 | fixed | `openaichat` renders the marker on a tool result (one-part array), on a system block and on assistant text; unmarked results stay plain strings | `RollingBreakpointOnToolResultIsRendered`, `UnmarkedToolResultsStayPlainStrings`, `SystemBlockMarkerIsRendered`, `AssistantTextMarkerIsRendered` |
| R18 | fixed | Spine, instructions and notes are bounded (`MaxSpineTokens`, `MaxInstructionTokens`, `TaskMaxTokens`, per-section and total notes bounds) with eviction pointers that only replace their own pointers; `NotesOverBudget` now makes the compactor consolidate | `SpineAndInstructionsStayBounded`, `EvictionOnlyReplacesItsOwnPointers`; kv `TestNotesAreHeldToTheirBudgetAndTheCompactorIsAskedToConsolidate` |
| R19 | fixed | `docs/CACHE-DESIGN.md` rewritten; `docs/ARCHITECTURE.md` corrected (mock scope, boundary step, hot delivery) | n/a |
| R20 | partly | Fixed: adjacent user turns merge at the start; the rolling marker never lands on a thinking block; `Strict` is in `PrefixKey`/`GlobalKey` and `Params` in the guard; `ApplyPolicy.WithDefaults` fills bounds but keeps deliberate zeros. Left: estimator calibration and flicker, the string/array flip in `openaichat.renderUser`, the `openaichat.DefaultProfile` TTL/minimum, token-capture canary | `RenderMergesAdjacentUserTurnsAtStart`, `RollingBreakpointNeverLandsOnAThinkingBlock`, `EstimatorMovesBreakpointsButNeverBytes`; kv `TestApplyPolicyWithDefaultsFillsBoundsButKeepsDeliberateZeros` |
| F1 | fixed (found while fixing) | The first version of the turn-scoped hot mode built the compactor fork as `... user, system, user(instruction)`: a system message followed by a user message is a 400, the model patch failed on every turn-scoped route and silently fell back to the mechanical patch. Found by running the real agent against the native adapter and the enforcing Anthropic mock. `kv.ForkPrompt` drops the trailing board view; `kv.Render` folds a system turn into user text wherever the API would reject it; `Sizer` prices what `Render` sends; `drainInbox` no longer pushes mail behind a board view | `NativeAdapterAgentSurvivesACompactionCommit`, `MailBehindATurnScopedBoardViewRidesTheNextTurn`; kv `TestTurnScopedSystemMessagesObeyThePlacementRules`, `TestForkPromptOnATurnScopedThreadEndsInTheInstruction`, `TestSizerCountsFoldedBoardViewsAndSkipsClearedOnes` |
| F2 | fixed (found while fixing) | A route that enforces bindings although the model table does not flag it was rejected once per request (recovery worked, but every request paid a rejected round trip and a rewrite). The first rejection now marks the route as enforcing: the hot view is persisted from then on | `BindingRejectionIsRecoveredOnce`, `NativeAdapterUnflaggedRouteIsLearnedFromOneRejection` |

---

## R1. The hot tail breaks preserved thinking on every request (blocker for the Claude route)

* **Evidence.** `kv/render.go:128-141` computes the rolling breakpoint, then appends the ephemeral hot blocks
  into the last user message; the next request removes them and appends a new copy at the new tail. The
  constitution tells the model so (`agent/prompt.go`: "`<live>` (inside the last message) ... is replaced every
  turn"). `core/message.go:74-77` and CACHE-DESIGN §2 line 33 say hot is "never persisted". The reference says
  this is exactly the invalidating pattern: `model-migration.md:1616-1622` ("Injecting per-request text into an
  earlier turn (a reminder, a status line ...) that you remove or rebuild on the next request"),
  `prompt-caching.md:83`, `causes.md:45` (`reminder_stripped`). CACHE-DESIGN §5 (lines 154-158) claims "Between
  rebases the prompt is append-only, so cache and thinking bindings both stay valid" and that `kv.Guard` verifies
  it; `kv/guard.go:97` skips ephemeral blocks, so the guard is blind to this by construction.
* **Demonstration.** `TestCacheEcon_HotTailBreaksPreservedThinking`: with the binding-enforcing fake, the control
  (no hot block) completes 6 tool rounds replaying 6 thinking blocks; with Sleipnir's hot block the **second request
  is rejected** (`ErrThinkingBinding`) and `Run` returns. Nothing recovers: `agent/request.go` handles only
  `ErrContextLength`; `provider.Request.BindingMode`, `Profile.BindingControls`, `Profile.TurnScopedSystem` and
  `cost.Model.PreservedThinking` are declared and never read.
* **Why it matters.** On enforced accounts (created on/after 2026-08-31; Fable 5.1, Opus 5.5, Sonnet 5.5) every
  agent dies at step 2. If the harness "fixes" it by stripping thinking per request it loses reasoning and, when
  done as a render-time toggle, mis-hits the cache each time (§5 itself warns against this). Latent today only
  because `openaichat`'s default profile has `ReplayThinking=false` and no native Anthropic adapter exists yet;
  a `reasoning_details`-replaying gateway route enables it.
* **Design contradiction.** The documented alternative (`clear_at: next_user_message`, CACHE-DESIGN lines 66-68) is
  not compatible with "ephemeral, never persisted": the reference requires the cleared message to **stay in the
  transcript, replayed verbatim on every later request** (`prompt-caching.md:83`; `causes.md:45`). So `Block.Ephemeral`
  cannot be the mechanism.
* **Minimal fix.** (a) Until then, force `ReplayThinking=false` (or `BindingMode: drop_block`, set explicitly) for
  models with `cost.Model.PreservedThinking`. (b) Real fix: deliver hot as an appended turn (`role: system` with
  `clear_at`, after each tool_result message) that is **persisted in the thread** with frozen bytes; on providers
  without it, put the board inside the tool_result or accept losing thinking. (c) Add the documented recovery:
  on `ErrThinkingBinding`, strip all thinking durably and retry once; log `input_transformations`.
* **Test sketch.** Already in place (fake provider). Fix is verified when the second request passes with a hot block.

## R2. Commit and epoch leave stale thinking in the transcript

* **Evidence.** `kv/thread.go:79-83`: `Commit` appends `t.turns[snapLen:]` (turns produced while the compactor ran)
  verbatim. `agent/compact.go:252-258` (`commit`) does nothing to that tail; only the snapshot's retained turns are
  stripped (`kv/apply.go:282-283`), and `apply.go` even keeps thinking on an "in-flight" assistant turn, whose
  binding is void after any rebase (`model-migration.md:1673`: keep-tail and background compaction "fail on the
  retained turns ... strip the thinking blocks from the retained turns"; the guide says to strip carried turns
  or send `drop_block`). `Agent.SyncShared` (`agent.go:288-300`) only sets `a.strip`; `stripPending`
  (`compact.go:321-345`) consumes it at the **next** boundary.
* **Demonstrations.** `TestCacheEcon_CommitTailKeepsPreRebaseThinking` (kv): after commit a thinking block produced
  before it is still rendered. `TestCacheEcon_BackgroundCommitBreaksThinkingOfCarriedTurns` (agent): a compactor
  that takes two agent steps; the first request after the commit is a 400. `TestCacheEcon_SyncSharedBetween...`:
  a `SyncShared` that lands between `boundary()` and `requestOnce()` (swarm `SetShared` runs on another goroutine;
  the test fires it deterministically from the event emitter during `drainInbox`) sends the new shared layer with
  old thinking: 400. The control (epoch before the run) passes.
* **Why it matters.** Every background compaction on an enforced Claude route kills the run (in the swarm:
  task `failed: agent stopped`). The `inFlight` retention is dead code in the loop (boundaries always end on a user
  turn) but is wrong if ever reached.
* **Minimal fix.** In `commit()` map the tail through a thinking strip (same rule as `retain`); drop the
  `inFlight` exception (compact only at user-turn boundaries, which is already the case); in `requestOnce` consume
  `a.strip` under the same lock as the stack snapshot and strip durably before rendering.
* **Test sketch.** Exists; after the fix the three tests must pass with `REVIEW_STRICT=1`.

## R3. The compactor fork changes `tool_choice`: on Anthropic it forfeits the whole conversation cache

* **Evidence.** `kv/fork.go:29-31` sets `Params.ToolChoice = "none"` and caps `MaxTokens` at 3000; the existing
  test enshrines it (`kv/compact_test.go:419`). `prompt-caching.md` "Invalidation hierarchy" (row `tool_choice,
  images`: tools and system caches survive, **messages cache does not**) and research 02 §3.3
  (`tool_choice ... | messages`). Sleipnir puts G1-G5 (shared pin, role pin, notes, spine, thread) in the messages
  tier, so the fork re-writes **all of it at 1.25x instead of reading at 0.1x**. CACHE-DESIGN lines 84-87 claim "the
  provider serves the whole shared prefix from cache, so the compactor pays for the instruction and a short JSON
  answer".
* **Numbers.** `TestCacheEcon_SimBillsForksAsCacheReadsButToolChoiceInvalidatesMessages` bills one fork with the
  simulator's own cache model (`sim.go: cacheSim.request`) both ways. Thread 30k: as billed by the sim 7.3k ITE;
  with the messages tier re-keyed 44.2k (+36.9k) if another agent's fork has kept the shared pins warm under that
  regime, 75.3k (+68.0k) if nothing is shared. The reference run has 110 forks (`sleipnir sim`, 20 workers): **+4.1M
  ITE (+26% on the 15.9M layered bill; headline -37% vs plain becomes about -21%) to +7.5M (+47%; about -8%)**.
  At Opus 5.5 input pricing ($4/MTok) that is about $16 (favourable) to $30 (upper bound) per 40-task, 20-worker run. The simulator cannot see this: it keys
  entries by segment id only (`sim/policies.go:402-410`); the mock ignores `tool_choice` too (R16). OpenAI documents
  `tool_choice` as cache-safe (research 02 §4.1), so this is Anthropic-specific; vLLM behaviour depends on
  `--exclude-tools-when-tool-choice-none` (unverified).
* **Related.** `agent/compact.go:203` parses `resp.Turn.PlainText()`, which concatenates thinking text ahead of the
  answer; `ParsePatch` takes the first balanced `{...}` (`TestCacheEcon_CompactorReplyIncludesThinkingText`:
  a reasoning model that drafts `{"keep_from": ...}` in its thinking makes every model patch fail and silently
  falls back to the mechanical patch). `max_tokens=3000` also has to cover adaptive thinking.
* **Minimal fix.** Leave every request parameter identical to the parent's (fork = parent bytes + instruction).
  Keep "do not call tools" in the instruction only; if the reply has a tool_use treat it as failure -> mechanical
  patch. Parse text blocks only. Keep `max_tokens` equal to the parent's.
* **Test sketch.** Assert `fork.Params == parent.Params` except `MaxTokens` (in `TestForkPromptSharesParentPrefixExactly`
  replace the current `ToolChoice == "none"` assertion).

## R4. A held patch blocks compaction forever; the hard limit is measured on the snapshot

* **Evidence.** `agent/compact.go:57-75`: while `a.comp.ready != nil` the boundary evaluates `ShouldCommit` with
  `cs.ThreadTokens = rp.snapTotal` (line 61) and **returns**: no new compaction starts and the 85%-of-window
  emergency (lines 79, 103) is unreachable. `kv/policy.go:165` (hard limit) therefore compares the frozen snapshot
  size, not the live thread, and the warm-branch economics (`policy.go:175-176`) never change while the patch waits.
  Design text: "A ready patch may be *held* until the economics or a cold moment justify the rewrite" (line 137).
* **Demonstration.** `TestCacheEcon_HeldMarginalPatchBlocksCompactionForever`: warm agent, hard limit 3000, a cautious
  compactor that folds only the first eight turns. One compaction starts, "commit? ... would not pay back within 25
  turns; waiting for a cold moment" is evaluated 44 times, **0 commits, live thread reaches ~6,300 tokens** (2x the
  hard limit) and keeps growing. A continuously warm agent never becomes cold, so the "cold moment" never comes.
* **Why it matters.** Unbounded context growth for a warm agent after a single marginal patch: every request reads
  more, the eventual emergency compaction is mechanical, and the fork prompt (whole thread) grows too.
* **Minimal fix.** Evaluate hard limit and pressure on the live thread; discard and re-propose a held patch when the
  live thread has grown by max(20%, 4k) since its snapshot or its age exceeds N steps; run the emergency check even
  when a patch is pending.

## R5. The planner measures reasoning that is never sent

* **Evidence.** `kv/thread.go:119-126` (`BlockTokens`) counts `Text` of thinking blocks; `agent/compact.go`
  (`plannerStateLocked`) uses `Snapshot.Tokens`. `kv/render.go:150-170` (`renderBlocks`) drops a thinking block unless
  `Caps.ReplayThinking` and it carries `Wire` in the matching dialect. `openaichat/stream.go` builds plain
  `reasoning` text without `Wire` (only `reasoning_details` items get `Wire`), so for every model that returns plain
  `reasoning`/`reasoning_content` (DeepSeek, Qwen, GLM, Kimi style) the block is stored, counted, and never sent.
* **Demonstration.** `TestCacheEcon_PlannerCountsReasoningThatIsNeverSent` (mock, 8 steps of ~1,500 reasoning tokens):
  **planner thread 12,274 tokens, provider actually received 152**.
* **Why it matters.** With `SoftThreadTokens` 20k a reasoning model compacts about every 13 steps although its real
  prompt is a few hundred tokens. Each compaction is a fork call (prefix read plus 1-3k output at ~5x weight, roughly
  5-15k ITE), a rebase (thread rewrite), and hides tool results behind `recall`. `Apply.RemovedTokens` includes the
  phantom tokens too, so `ShouldCommit` sees savings that do not exist.
* **Minimal fix.** Size the thread from what `Render` sends (`Rendered` already has cumulative tokens in
  `Guard.digest`), or drop non-replayable thinking blocks at append time (keep them in the archive/log only).

## R6. "Warm" is inferred from the last hit ratio; the low-hit alarm silences itself

* **Evidence.** `agent/request.go:262-272` (`isWarmLocked`): after the first request, warm iff `lastHit >= 0.3`. The hit
  ratio is `read/total`, i.e. how much of the prompt is OLD, not whether the cache is warm. `request.go:108`: the
  `low_hit` alarm is gated on `warmBefore` from the same function.
* **Demonstrations.** `WarmHeuristicMisreadsABigToolResult`: the provider serves 100% of the previous prefix, a
  14k-token `cat big.log` drops the ratio to 0.11, the agent is declared cold and the cold-path compaction starts
  ("cache is cold: shrink for free by masking"), a rebase that rewrites a warm cache when something is maskable.
  `NonReportingProviderLooksColdForever`: a provider that hides cache usage (ARCHITECTURE.md "Providers": the
  marketplace's Anthropic-style route does not report it) is cold forever; 3 cold-path compactions fire on a
  thread of a few thousand tokens. `LowHitAnomalyIsSilentOnLargeMisses`: a persistent 70% miss for 6 requests fires
  **once** (after the first low ratio the agent is "cold" and the alarm is off).
* **Minimal fix.** Derive warmth from the guard's expectation (`cache_read >= 0.7 x expected`, tracked per request),
  not from `hit_ratio`; treat "provider never reports reads" as unknown -> never take the cold path and never
  suppress the alarm; keep TTL as the only positive cold signal.

## R7. `ModeMask` (new) has no fallback and can storm

* **Evidence.** `kv/policy.go:119-130`: any `Yes` from `ShouldStart` becomes `ModeMask` when `!Warm`, including the
  **hard limit** and window-pressure reasons. `agent/compact.go:84-102` calls `maskCommit`; on error it only emits
  `compact.reject`. `kv/apply.go:514-543` (`MaskOnly`) counts stripped thinking as progress (`RemovedTokens > 0`).
* **Demonstrations (cache hidden, so permanently "cold").** `ColdModeCommitsEveryStepByStrippingThinking`: 52 of 60
  steps end in a `mask:` commit that masked **zero** results (each is a declared rebase that rewrites the thread and
  drops the model's reasoning). `ColdModeWithNothingToMaskNeverFolds` (no thinking, small results): 111 failed mask
  attempts, 0 commits, **0 forks**, thread ~10,950 tokens against a hard limit of 3,000 (only the 85%-of-window
  emergency would ever fold it).
* **What is right.** The intent is sound and the original defect is fixed: cold starts no longer race a fork against
  the main request (`ColdStartNoLongerForksAModelCall` is a regression test; before the change both requests
  prefilled the cold prompt with zero cache reads).
* **Minimal fix.** `MaskOnly` must not count thinking strip as progress unless something is masked; if the cold path
  cannot shrink below the soft limit (or pressure is hard), fall through to the fork (or mechanical patch); rate-limit
  mask commits (at most one per N steps); fix R6 so "cold" is rare.

## R8. The commit economics: formula omissions with numbers

`Planner.ShouldCommit` (`policy.go:159-182`) and CACHE-DESIGN §4.2 use penalty `w(A+R) - rT`, saving `r(T-A-R)` per
turn. The algebra is internally consistent and the doc's example arithmetic is right (40k -> 6.8k: 1.36 turns).
The model behind it is not what an explicit-breakpoint cache charges:

1. **Old spine.** The spine is one text block; appending a line changes the block, and no marker sits inside or
   after it, so the whole spine is re-written. The formula counts only the added lines `A`.
2. **Tail.** Turns appended while the compactor ran (or while a patch is held) sit after the changed prefix and are
   re-written too (`compact.go:58-59` comment says they "ride along unchanged"; in cost terms they do not).
3. **Notes.** Any commit that folds a user turn edits notes (`applyNotes`), re-writing notes as well.
4. **T is inconsistent.** `snapTotal = RemovedTokens + RetainedTokens` (`compact.go:225`) where Retained is measured
   after masking and stripping, so masking savings never enter the decision.
5. **Compactor cost is never used.** `Outcome.CompactorITE` is assigned (`compact.go:63`) and never read;
   `ShouldStart` has no economics at all (thresholds only); `State.Remaining` is never set by the agent.

* **Numbers** (`TestCacheEcon_PlannerPenaltyOmitsTailSpineAndNotes`, `...AcceptsMoneyLosingCommit`,
  `...SnapTotalExcludesMaskingSavings`, driven through `Render`/`Apply`/`Commit` and the explicit-cache model):
  thread 42.5k -> 6.1k with a 3k-token old spine and 3 more exchanges (~7.5k tokens) arriving during compaction:
  planner penalty **3,361 ITE, actual 12,597 (3.7x)**. Marginal case (T=13.6k, shrink 5.7k, 6k old spine, 4 tail
  exchanges of 1.1k): planner says commit, net **+5,568 ITE** over 25 turns; the cache charges **-5,084**. A patch that is all masking: planner T = 56.5k vs live thread 96.4k.
* **Automatic providers.** Same structure with (w-r)=0.75: the tail term is `0.75 D`, the old spine survives
  (byte-append), so the error is smaller but the T and compactor-cost problems remain.
* **Minimal fix.** `penalty = w(P_old_spine + A + R + D_tail [+ N_notes if changed]) - r(P + T + D)` for explicit
  caches (drop `P` for automatic ones); use the live pre-mask `T`; add `CompactorITE` in `ShouldStart` against
  remaining-turns x saving; feed `Remaining` (task steps left or observed steps per task).
* **Test sketch.** Property test: for random states compare `ShouldCommit.Yes` with the sign of simulated net
  (`cxSim`); allow only false negatives.

## R9. Breakpoint policy vs. "one trie, many leaves"

* **Evidence.** `kv/plan.go:44-56` and `kv/stack.go:38-44`: markers only when the layer is at least
  `MinLayerForBreakpoint` = 1500 tokens; G0 (constitution + tools) never gets a marker; candidates are exactly four
  (thread, shared, role, notes) for four slots, so the floor is the only size heuristic that ever removes one (the
  other test is the provider minimum). The comment's
  rationale ("the extra cache entry would save less than it costs to maintain") does not hold on Anthropic: writes are
  billed on tokens from the highest hit to the last marker, not per entry. Built-in role pins are
  **74-278 tokens** (`swarm/roles.go`; measured with the default estimator: backend 162, manager 278, docs 74), the
  constitution ~1,045, so no `role` marker ever appears with the shipped roles, and a young project's shared pin
  is often under 1500 too.
* **Demonstration.** `TestCacheEcon_SmallLayersLoseCrossAgentSharing` (const 3000, shared 900, role 200, notes 300):
  markers `[thread]` only; the second agent reads **0 of ~4,100** identical prefix tokens (control with the floor
  removed: 4,178 read); after a commit changes the spine the same agent reads **0 and re-writes 4,722**.
* **Second-order.** The spine is one block, so every commit re-writes it (R8 item 1). Splitting it into one block
  per commit generation, with the `notes` marker moved to the end of notes+spine, makes a commit re-write only the
  new lines.
* **Minimal fix.** Apply the floor only when slots are contested; when a slot is free add a marker at the end of
  the system prompt (G0); render the spine as append-only blocks and put the private stable-prefix marker after them.
* **Impact.** Config dependent: with shared >= 1500 tokens G0+G1 is shared and the loss is small; with a small shared
  pin every new agent and every commit pays the whole G0..G3 at 1.25x (about 10k ITE per commit for a 9k G0).

## R10. The 20-position lookback is never planned for

* **Evidence.** `LookbackBlocks` is carried into `kv.Caps` (`stack.go:21-23`) and never read (grep). `agent.takeInbox`
  turns every queued message into its own text block (`agent.go:390-402`); the router allows
  up to 8 mails/min per sender and 3 per (sender, recipient) pair per minute (`swarm/mailbox.go:57-59`), so a manager
  with 24 workers can receive dozens in one step.
* **Demonstration.** `TestCacheEcon_LookbackWindowIsNotPlannedFor`: after 5 mail blocks the next request reads
  18,650 of 18,650 tokens; after 25 mail blocks it reads **9,586 and writes 9,231** (the whole thread) because the
  previous rolling entry is 26 positions back and no intermediate marker exists.
* **Minimal fix.** Coalesce an inbox drain into one text block; when a turn adds more than ~15 positions insert an
  intermediate marker (a slot is available when `MinLayerForBreakpoint` no longer eats one, R9).

## R11. Guard and low-hit telemetry: false negatives (no false positives found)

* **Evidence and tests.** `TestCacheEcon_GuardBlindSpots`: changing `effort`, `thinking`, `tool_choice`, the model,
  the hot tail, message boundaries or roles produces `Drift:false` (`guard.go` hashes only `Block` JSON, skips
  ephemeral blocks, and ignores `Params`, `Prompt.Model`, roles, message splits; adapter-level serialisation is
  never seen either). `request.go:108`: alarm only if `warmBefore` (per-agent; false on an agent's first request,
  so the swarm's headline claim "spawning a worker costs a warm read of the shared prefix" is never checked) and
  `read < 0.7 x expected`. `LowHitAnomalyIsSilentOnLargeMisses`: a persistent **25% miss (the recent thread re-written
  at 1.25x every step) never fires**; 70% relative hides up to 30% of the prompt. Claude Code's rule for comparison
  (research 02 §3.10): miss = more than 5% **and** at least 2,000 tokens re-processed that could have been read.
* **Minimal fix.** Alarm on absolute miss (`expected - read > max(2000, 5%)`); compute `expected` from
  `max(guard, gate.Warm(GlobalKey) prefix)` so first requests are checked; hash `Params` (effort, thinking,
  tool_choice), model and roles into the guard digest; do not gate on the hit-ratio warm heuristic (R6).

## R12. The warm gate: sound idea, wrong key, generous timing

* **Evidence.** `agent/request.go:75` gates on `stack.GlobalKey()` (model, tools, constitution, shared pin).
  Routing (`request.go:243-256`) also includes the shard, and the physical prefix also includes the role pin.
  `Stack.PrefixKey` exists "for ... fan-out gating" (`stack.go:80-82`) and is only logged. The simulator models gate
  keys `G/<shard>` and `R:worker/<shard>` (`sim/policies.go:280-285`): **the sim credits a gate the code does not
  implement.** `swarm.go:180` builds the gate with the model TTL and `warmUntil = first byte + ttl`
  (`gate.go:80-95`), while Anthropic measures the entry lifetime from the request **start**.
* **Demonstrations.** `GateKeyIgnoresShardAndRole`: 8 agents, 2 roles, 4 shards: **1 gate key, 4 routing keys**;
  followers whose engine/prefix is still cold are released after the primer's first byte.
  `GateWarmWindowOutlivesTheProviderEntry`: with a 45 s first byte the gate still says warm at t0+5m20s
  (the provider entry ended at t0+5m). `GateStampedesWhenColdPrefillOutlastsMaxWait`: all 7 followers are released
  before the primer's first byte when TTFT exceeds `maxWait` (default 45 s; the sim's own TTFB model gives ~13 s for
  200k tokens, so this needs a loaded engine or a larger context).
* **No deadlock or starvation found.** `Enter` is bounded by `ctx` and `maxWait`; the primer's finisher is always
  called (`requestOnce` calls `started(err == nil)` after `call`); a failed primer hands over; a follower can wait up to one
  `maxWait` per failed primer ahead of it. Race detector clean.
  Priority inversion is possible (a worker primer elected for a manager's key waits in the governor queue while a
  higher-priority follower is held).
* **Minimal fix.** Key the gate by the routing key (`Prompt.CacheKey`) and add a second gate on `PrefixKey`; set
  `warmUntil = requestStart + ttl - margin` (as `IsCold` does with `ColdMargin`); derive `maxWait` from observed
  `TTFB` rather than a constant.

## R13. Affinity and marketplace claims

* CACHE-DESIGN lines 171-174 say affinity works ("tested"). It is tested only against `internal/provider/mock`, whose
  `Router` hard-codes pinning by `session_id` > `prompt_cache_key` > `X-Session-Id` (`mock/server.go` parseChat,
  `mock/engine.go` Router). `docs/research/02-provider-caching.md` §0 and §6 state that every marketplace-gateway
  behaviour is UNVERIFIED and prescribe the canary suite D11 first. The claim is circular.
* Default `AffinityShards` is 1 (`agent.New`): one routing key for the whole swarm. OpenAI's documented ceiling is
  ~15 requests/min per prefix+key before spill to other machines (research 02 §4.1; D10 says shard so no key exceeds
  ~12 RPM). At the swarm's own RPM cap (500) that is 40 shards, not 1. On a marketplace, one key also pins every
  agent to one engine (queueing, LRU pressure); nothing sizes the shard count.
* The gate's "warm" is time based (4-5 min); per-engine LRU eviction, restarts and autoscaling are invisible to it.
  A follower released onto an evicted prefix reads nothing (and R11 does not check first requests).
* **Fix.** Reword to "designed for"; run the D11 canary; default shards = ceil(RPM/12) for OpenAI-type providers.

## R14. Instructions and task text

`TestCacheEcon_UserInstructionGuarantees` (three subtests). CACHE-DESIGN lines 108-109 ("User instructions survive
verbatim ... never entrusted to a summariser") is false in three ways:

1. **Steering is not preserved.** `applyNotes` copies only `Origin == OriginUser` turns (`apply.go:449`). Steering sent
   with `Agent.Send` rides in the tool-results turn with `Origin: OriginTool` (`agent.go:360-363`) or an `OriginMail`
   turn (`drainInbox`). Folding it leaves `instructions="- build it"` and a mechanical spine line; "never touch the
   billing package" is gone (recoverable only via `recall`, which the model does not know to do).
2. **Truncation without a pointer.** `capTokens(txt, 600, ...)` (`apply.go:457`): a 5,000-token task (20,027 bytes) is
   cut to 2,204 bytes ending `...[truncated]`; no "recall t1" hint.
3. **Model-writable.** A later compactor patch with `{"op":"set","key":"instructions"}` replaced the segment; only the
   turns folded in the *current* commit are re-added. `assignment` is equally editable.
* **Fix.** Give steering its own `Origin` and treat it like user text; add a recall pointer to truncated lines;
  reject note ops on harness-owned keys (`instructions`, `assignment`); cap and evict with an archive pointer (R18).

## R15. A reused worker never receives the new task

`swarm.go:397-413` (reuse path) starts the run with `taskCard(task, id, false)` = "Begin task T2: <title>. Your
assignment and scope are in <my-notes>." (`swarm.go:500`), but `<my-notes>` was built once at first spawn
(`swarm.go:443`) and still says "Your assignment is task T1". `TestCacheEcon_ReusedWorkerNeverSeesTheNewTasksBrief`: the
reused worker's prompt contains T1's assignment and none of T2's description. (Changing notes would cost a
G3+G4+G5 rewrite, which is presumably why it was skipped.) Fix: put the full card in the kickoff turn and fold it
into `assignment` at the next commit.

## R16. Is the mock faithful enough that passing tests mean something?

* **For automatic prefix caching, mostly yes**: block-chain hashing over real prompt bytes, entries published at the
  first token, TTL with refresh-on-read, per-engine caches. It caught nothing wrong in steady state and is a fair
  check for append-only stability, ordering and no-drift claims.
* **For everything Anthropic-specific, no.** `mock/server.go:389-414` hashes tools + message text only:
  `cache_control` parts are flattened, `tool_choice`, `reasoning_effort` and thinking config are not part of the key
  (`TestCacheEcon_MockIsBlindToCacheKeyParameters`: 768 of 779 tokens still served after both changed), no write
  premium and no `cache_write_tokens` (`mock/chat.go:96-101`), no thinking binding, no lookback. The whole explicit-breakpoint
  path (`planBreakpoints`, TTL ordering, lookback, R3, R9, R10, R17) is therefore untested end to end, and the agent
  tests configure `cost.OpenAICacheModel()`.
* **Eviction is inverted.** `mock/engine.go:114,120-138`: hits and inserts walk the chain root-first, leaving the
  root least recently used, so capacity eviction removes roots first. vLLM V1 frees tail blocks first (research 02
  §7). `TestCacheEcon_MockEvictsRootsBeforeLeaves`: a 6-block chain loses its root, hits 0 tokens, and 4 unreachable
  blocks keep squatting in the budget. Any capacity-pressure test overstates misses.
* **The simulator (CACHE-DESIGN §8) shares the same blind spots and adds three.** (a) Warm detection is a TTL oracle
  (`sim/policies.go:346`), not the agent's hit-ratio heuristic (R6), so the 5-14% cold-masking win is measured with a
  detector the agent does not have. (b) Fork billed as a cache read (R3). (c) Gate keys per shard and role (R12). (d) It feeds the planner `Remaining = stepsLeft` (an oracle, `sim/policies.go:354,377`) while the agent feeds 0 and gets the 25-turn default (R8).
  Also its default role pin is 10k tokens against ~150 in the shipped roles (R9), and the sim has no thinking, no
  lookback and one breakpoint layout ("layeredBP") that skips `planBreakpoints`. The doc already says "a model, not a
  benchmark" (§9): the headline percentages should not be quoted until R3 and R12 are modelled.

## R17. `openaichat` drops the rolling marker on tool results

`openaichat/wire.go:256-270`: with `CacheControlParts` (documented for gateways fronting Anthropic) markers are looked up
only for text parts of user messages; a `tool_result` becomes a `role: tool` message with string content
(`toolResultText`), and assistant messages carry no parts. In every tool loop the last persistent block is a
tool_result, so the rolling `thread` marker is never emitted (`TestCacheEcon_RollingBreakpointOnToolResultIsDropped`:
3 of 4 markers rendered; with a trailing text block 4 of 4). Only the pinned layers would be cached explicitly.
Fix: render the tool message content as a one-part array carrying the marker (or move the marker to the nearest
preceding text/assistant block).

## R18. Unbounded growth

`TestCacheEcon_SpineAndInstructionsGrowWithoutBound`: after 150 commits the spine is 3,637 tokens (+~24/commit) and
`instructions` 5,411 tokens (+~36/user turn; 3.6x its `MaxSectionTokens`); `NotesOverBudget` was true on 109 commits and
nothing reads it (`compact.go:274` only logs it). The spine block is re-written at each commit (R8 item 1) and the
whole spine and notes are read on every request. CACHE-DESIGN line 110-111 marks eviction as "planned". Also the
archive keeps a 4 KB in-memory search preview per turn forever (`archive.go` `previewCap`). Fix: cap+evict behind a
pointer line (the doc's own plan), consume `NotesOverBudget`, page the preview to disk.

## R19. CACHE-DESIGN.md: factual errors and overclaims

Line numbers are for the current file.

1. Line 3 "Status: implemented ... (gate, hot view)": there is no epoch scheduler (`SetShared` has no caller and its
   comment at `swarm.go:736` refers to a non-existent `Epoch`; the curator is "planned" in ARCHITECTURE), so "priced, batched, declared"
   epochs are not implemented.
2. Line 20 (OpenAI row "none (1x)"): research 02 §1 item 1 and §4.2 record that GPT-5.6 and later have a **1.25x write
   premium**, explicit `prompt_cache_breakpoint`, and a 30-minute minimum lifetime. `cost.openaiCache()` and the planner
   (`agent/compact.go` `write = 1`) model none of it.
3. Line 33 / lines 66-68: "never persisted" is incompatible with the documented `clear_at` mechanism, which must stay
   in the transcript verbatim (R1).
4. Lines 84-87: the fork "reads the shared prefix from cache ... pays for the instruction and a short JSON answer".
   False on Anthropic while `tool_choice` differs (R3); also pays a write for the turns since the last request and
   ~1-3k output tokens at 5x.
5. Lines 98-99 "Turns that arrived meanwhile ride along verbatim": correct for structure, wrong for cost (R8) and for
   thinking (R2).
6. Lines 108-109 user instructions "survive verbatim": R14.
7. Lines 112-114 "Published agent-context studies find masking roughly halves cost at equal solve rate": the repo's own
   research (03, A3.2, arXiv:2607.12161) measured that removing 38% of tool-output tokens *raised* paired cost 6.8%
   (95% CI +2.8..+11.3) because masking edits invalidate the cache; the claim should say "when batched into an
   existing commit".
8. Lines 125-127: penalty formula and "pays back in ~1.4 turns": arithmetic right, model incomplete (R8), and it counts
   only the sunk fork call; total payback including the fork is longer.
9. Lines 128-134 (cold cache): now consistent with the ModeMask fix; but "commit is free" only holds for a truly cold
   agent (R6/R7).
10. Lines 140-141: `isWarm` "measured hit ratio" is the flawed signal of R6.
11. Lines 154-158: "Between rebases the prompt is append-only ... `kv.Guard` verifies this" (R1, R11).
12. Lines 165-166 "one trie, many leaves ... a warm read of the shared prefix" depends on markers that the size
    floor removes (R9).
13. Lines 167-170, 174: gate and affinity "tested" against the mock/hand-rolled fake only (R12, R13, R16).
14. Lines 187-188 "cache boundaries make usage reports a free tokenizer": the estimator is calibrated on total
    `PromptBytes` vs total input tokens (`request.go:105`); nothing uses cache boundaries, `PromptBytes` ignores framing,
    tool wrappers and thinking-only signatures, and the first accepted sample overwrites the ratio (`core/tokens.go:73-76`).
15. Lines 274-277 (§9) "cache-faithful mock ... evicts under memory pressure": eviction order is inverted (R16); the
    hit-ratio thresholds it reports (>=0.2 after a commit) are weak.
16. §8 tables: see R16 (oracle warm, fork billed as a read, gate keys, 10k role pin).


## R20. Low-severity items

* **Render merge guard** (`render.go:116`, `n > 1`): a thread that opens with two user turns renders two adjacent user
  messages (`RenderLeavesTwoUserMessagesAtStart`; e.g. retry after a failed first request). Accepted by Anthropic and
  OpenAI; strict chat templates (Mistral style) reject it; the guard hashes blocks, not messages.
* **Rolling marker on a thinking block** when the last persistent block is thinking (thinking-only tail turn).
* **Estimator.** `BlockTokens` adds fixed overheads (+6/+8) that the byte-ratio calibration already absorbs;
  `PromptBytes` omits framing; the first accepted sample replaces the ratio; one estimator is shared by all agents.
  Errors are a few percent and do not reach the 70% alarm threshold on their own.
* **Keys.** `GlobalKey`/`PrefixKey` omit `ToolSpec.Strict` and all `Params`; on Anthropic effort/thinking changes
  invalidate the messages tier, so per-role effort would split the shared pins (all agents share `Deps.Params` today).
  Note that placing G1/G2 in the messages tier makes them subject to tool_choice, image and thinking-config changes
  that a system-tier pin would survive (R3).
* **Config footguns.** `agent.New` (`agent.go:233`) replaces the apply policy only if it equals the zero value, so a partially specified
  `ApplyPolicy` silently gets `StripThinking=false` and `MaxSpineLineChars=0`.
* **Profile defaults.** `openaichat.DefaultProfile` assumes a 1 h TTL and a 64-token minimum (real OpenAI: 1024, so
  `low_hit` false positives on small prompts); the planner reads `cost.Model.Cache`, the renderer reads
  `Profile.Cache` (two sources of truth; `cost.Fallback` is explicit-cache).
* **String/array flip.** `renderUser` sends a single text part as a plain string and several as an array, so the same
  persistent message can serialise differently when the hot block attaches to it; harmless for engines that flatten parts,
  a possible prefix divergence on those that do not.
* **Token capture** (`Profile.CaptureTokens`) adds `logprobs`/`return_token_ids` request members; not cache-relevant on
  vLLM completion logprobs, but worth a canary on each engine.

---

## Test index

The tables above name the review-time tests; the permanent regression tests that replaced them are (all `TestCacheEcon_`
unless noted; "(sound)" = was never a defect, kept always on):

| File | Tests |
|---|---|
| `internal/kv/cache_regress_test.go` | `SmallLayersStillShareThePrefixAcrossAgents`, `LookbackWindowIsPlannedFor`, `PlannerPenaltyMatchesWhatTheCacheCharges`, `PlannerNeverApprovesAMoneyLosingCommit`, `SnapTokensIncludeMaskingSavings`, `CommitStripsThinkingFromTheCarriedTail`, `UserInstructionGuarantees`, `ForkKeepsEveryRequestParameter`, `GuardSeesWhatTheProviderKeysOn`, `ApplyCommitRenderStructuralSoundness` (sound), `RenderMergesAdjacentUserTurnsAtStart`, `SpineAndInstructionsStayBounded`, `EvictionOnlyReplacesItsOwnPointers`, `BreakpointRulesHoldOnRandomStacks` (sound), `RollingBreakpointNeverLandsOnAThinkingBlock`, `EstimatorMovesBreakpointsButNeverBytes` (sound), `CompactorReplyIsParsedFromAnswerTextOnly` |
| `internal/kv/hot_test.go`, `internal/kv/planner_test.go` (no prefix) | `TestResolveHotPicksTheMechanismForTheRoute`, `TestApplyKeepsOnlyTheNewestPersistedNoticeAndNeverCopiesOneIntoInstructions`, `TestTurnScopedHotIsAPersistedSystemMessage`, `TestSizerCountsWhatRenderSends`, `TestMaskOnlyNeedsSomethingToMask`, `TestPlanMarksSpendsSlotsInPriorityOrder`, `TestThreadRewriteAndCommitWithLeaveSnapshotsUntouched`, `TestApplyPolicyWithDefaultsFillsBoundsButKeepsDeliberateZeros`, `TestNotesAreHeldToTheirBudgetAndTheCompactorIsAskedToConsolidate`, `TestTurnScopedSystemMessagesObeyThePlacementRules`, `TestSizerCountsFoldedBoardViewsAndSkipsClearedOnes`, `TestForkPromptOnATurnScopedThreadEndsInTheInstruction`, `TestPlannerStartMatrix`, `TestShouldCommitJudgesTheLiveThread`, `TestStalePatchesAreDiscarded` |
| `internal/kv/sim/cache_regress_test.go` (no prefix) | `TestSimPlacesMarkersWithTheRealPlanner`, `TestSimForkReadsTheParentsPrefix`, `TestSimGateIsKeyedAndTimedLikeTheRealOne`, `TestSimHeadlineWithTheFixesStaysInTheDocumentedRange`, `TestSimPersistedHotOnAPreservedThinkingRoute` |
| `internal/agent/cache_regress_test.go` | `HotTailKeepsPreservedThinkingValid`, `TurnScopedHotIsPersistedAsClearAtSystemMessages`, `InlineHotRemainsTheDefaultWhereNothingIsBound`, `BindingRejectionIsRecoveredOnce`, `BackgroundCommitStripsThinkingOfCarriedTurns`, `SyncSharedStripsInTheSameStepAsTheSwap`, `EpochDuringARequestStripsTheResponsesThinking`, `BigToolResultIsNotAColdCache`, `NonReportingProviderIsNotCold`, `LowHitAlarmFiresOnEveryLargeMiss`, `FirstRequestIsCheckedAgainstAWarmSharedPrefix`, `GateKeyCoversShardAndRole`, `ColdStartNoLongerForksAModelCall` (sound), `HeldPatchCannotBlockCompaction`, `HiddenCacheIsNotACommitStorm`, `ColdAgentAboveTheHardLimitStillFolds`, `ColdMaskCommitsAreRateLimitedAndNeverEmpty`, `PlannerIgnoresReasoningThatIsNeverSent`, `InboxBurstIsOneBlockPerClass`, `SteeringSurvivesCompactionAndMailDoesNot`, `MailBehindATurnScopedBoardViewRidesTheNextTurn` |
| `internal/agent/native_e2e_test.go` | the real agent against the native Anthropic adapter and the enforcing Anthropic mock: `NativeAdapterAgentKeepsThinkingAndTheCache` (turn-scoped and persisted routes), `NativeAdapterAgentSurvivesACompactionCommit`, `NativeAdapterUnflaggedRouteIsLearnedFromOneRejection` |
| `internal/provider/mock/cache_regress_test.go` | `MockEvictsTailsBeforeRoots`, `AutomaticModeKeysOnPrefixBytesOnly` |
| `internal/provider/openaichat/cache_regress_test.go` | `RollingBreakpointOnToolResultIsRendered`, `UnmarkedToolResultsStayPlainStrings`, `SystemBlockMarkerIsRendered`, `AssistantTextMarkerIsRendered` |
| `internal/swarm/cache_regress_test.go` | `GateWarmWindowEndsBeforeTheProviderEntry`, `GateStaggersReleaseWhenThePrimerIsStuck`, `GateRecoversFromAPrimerThatNeverReports`, `GateElectsOnePrimerPerLevel`, `GateNestedKeysStress`, `ReusedWorkerReceivesTheNewTasksBrief` |

---

## Answers to the eight questions

1. **Planner formulas.** No sign errors; the warm and cold algebra is right for a cache that keeps the "surviving prefix"
   and nothing else. It is wrong about what survives on explicit-breakpoint providers (old spine, notes), ignores the
   tail, uses an inconsistent T, ignores compactor cost and `Remaining` (R8). The cold-start "free" claim was wrong for
   *starting* a fork (fixed by `ModeMask`); it now misfires through the warm heuristic (R6, R7). Numeric
   counterexamples: R8.
2. **Breakpoints.** Positions are ascending, at most 4, longer TTL first, the hot tail always follows the last marker,
   markers never sit on empty blocks (500 random stacks, `BreakpointRulesHoldOnRandomStacks`). Defects: lookback not
   planned (R10), floor and missing G0 marker (R9), rolling marker can land on a thinking block
   (`RollingBreakpointCanLandOnAThinkingBlock`, low), minimum sizes rely on the calibrating estimator. Folding the first
   user turn into message 0 causes no hidden invalidation: everything after the spine is re-written anyway.
3. **Byte stability.** Two renders of the same state are byte-identical (no map iteration reaches bytes, no clocks,
   ids, floats; `Layer.With`, notes ops and spine appends are deterministic and append-only at the byte level). The
   calibrating estimator moves only breakpoints, never bytes (`EstimatorMovesBreakpointsButNeverBytes`); flicker near
   a layer's size floor means the notes entry is not refreshed on requests where its marker is skipped: minor. Undeclared
   prefix changes that do exist are the hot tail and the commit tail (thinking bindings, R1/R2), not cache bytes.
4. **Guard/telemetry.** No false positives found. False negatives: R11; alarm self-silencing: R6.
5. **Compaction correctness.** Structurally sound under random thread/patch/tail shapes (the committed test runs 400; I also ran the same generator for 30,000) (atomic units, `keep_from`
   mid-unit folds the whole unit, unanswered tool call protected, no orphan results, alternation preserved) with one
   exception: two adjacent user messages when a thread opens with two user turns (R20). Losses and growth:
   R14, R15, R18; stuck patch: R4.
6. **Preserved thinking.** Stripping at commit is not sufficient (R2); the hot tail breaks it on every request (R1); the
   fork does not disturb the parent's bindings (append-only) but does disturb its cache (R3); with `error` enforcement the
   agent dies at R1/R2 events and has no recovery path.
7. **Gate and affinity.** Sound in concept for Anthropic (entry readable after the first byte); depends on markers
   existing at the shared boundary (R9), on a correct key (R12) and on realistic TTL/TTFT. For marketplace engines the
   affinity premise is unverified and shard sizing is absent (R13). No deadlock or starvation in `gate.go`.
8. **Doc errors.** R19.

## Things I checked and found sound

* Byte determinism of layers, `SortTools`/`Canonical`, notes-op ordering, spine text, mask placeholders (fixed at apply
  time), tool-call/`reasoning_details` replay (`Wire` verbatim), `PrefixKey`/`GlobalKey` invariance to private layers.
* `Layer.With` of equal content has an equal hash; version never reaches the prompt.
* `Thread.Commit` CAS: stale epoch rejected; a stale ready patch after an emergency commit is dropped safely.
* Units/`Validate`/`Apply` clamps and in-flight protection (property test, 400 committed / 30k scratch cases); masking keeps `ToolID` and
  `IsError`; `MaskRef` indices consistent between compactor view and `retain`.
* Breakpoint ordering/TTL/limit/hot-tail rules on random stacks; read/write weights and minimum prefixes in
  `cost/defaults.go` match the bundled reference (0.025/0.05/0.1 reads, 1.25/2 writes, 512/1024/2048/4096 minimums;
  `PreservedThinking` flags for Fable 5.1, Opus 5.5, Sonnet 5.5 only).
* `openaichat` usage normalisation (input = prompt - cached - written), stable request bodies, fixed field order,
  `tool_choice` mapping; the OpenAI-style path caches the spine at the byte level (append-only text).
* `Guard`: no false drift on append-only growth, declared commits, SyncShared in the normal order.
* `WarmGate`: single primer, failure hand-over, `ctx` cancellation, no deadlock; race detector clean.
* The cold-start fork race is fixed (regression test `ColdStartNoLongerForksAModelCall`).
