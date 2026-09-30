# Sleipnir cache design: layered, generational, cost-aware prompt state

Status: the layout, renderer, breakpoint planner, drift guard, compaction (patch, apply, planner, fork), the three
hot-tail mechanisms, the fan-out gate and the hot view are implemented in `internal/kv`, `internal/agent` and
`internal/swarm`. Not implemented: an **epoch scheduler** that batches shared/role promotions (`Swarm.SetShared`
installs a new shared layer everywhere but nothing decides when; the curator is planned), the OpenAI newer-model write
premium (§1), and a native Anthropic adapter with turn-scoped system messages (the contract it must meet is §3.3).
Numbers below are input-token equivalents (ITE): 1 ITE = the price of one plain input token.

This document was revised after an adversarial review (`docs/reviews/cache-economics.md`, findings R1-R20). Where a
claim changed, the review number is given.

## 1. The constraint everything follows from

Provider prompt caches are **byte-prefix matches**. Any change at byte *p* invalidates everything after *p*, for
every request that shared it. So the cost of a change is proportional to how much prompt follows it, times how
many agents carry that prompt. Harnesses that treat the conversation as one growing list get an easy win from
"cache the end of the last turn" and then lose it whenever they trim, summarise, swap tools, or inject
per-turn text. A swarm multiplies the loss by the number of agents.

Sleipnir's answer is to make **layout follow volatility** and to treat every change to a stable layer as a
priced, *declared* event. (Batching those events into scheduled epochs is designed, not built: see the status line.)

| Provider family | Cache | Read | Write | Readable when | Placement control |
|---|---|---|---|---|---|
| Anthropic | explicit, ≤4 breakpoints, 20-position lookback (a run of consecutive `tool_use` or `tool_result` blocks is one position), TTL 5m/1h, per workspace; `tool_choice`, images, thinking/effort changes invalidate the whole messages tier | 0.1x (0.05x Opus 5.5, 0.025x Fable 5.1) | 1.25x / 2x | after the first response byte | markers |
| OpenAI / marketplace engines (vLLM/SGLang-like) | automatic block-hash prefix, LRU | ~0.25x (Heimdall catalogue) | none (1x) on the engines measured; **GPT-5.6 and later charge a 1.25x write premium, offer explicit `prompt_cache_breakpoint` and a 30-minute minimum lifetime** (research 02 §1, §4.2). `cost.openaiCache()` and the planner model none of that yet | after prefill of the first request | ordering + routing key (`prompt_cache_key`, `X-Session-Id`) |

`internal/cost` models both families (`CacheModel`, `Weights`); the planner never hard-codes a provider.

## 2. Layers

```
G0 constitution + tools   frozen for the session; identical bytes for every agent and role
G1 shared pin             project knowledge; identical for every agent; changes only at *epochs*
G2 role pin               conventions shared by all agents of one role; epochs only
G3 notes                  one agent's tenured facts/decisions/instructions/assignment; edited at major commits
G4 spine                  one agent's append-only one-line history digests; appended at minor commits
G5 thread                 verbatim recent turns; strictly append-only between commits
G6 hot                    always-fresh board view; delivered one of three ways (§3)
```

Rules, all enforced by `internal/kv` and tested:

1. **Deepest = most stable.** Volatility classes (`Frozen < Epoch < Slow < Fast`) order segments *inside* a
   layer as well, so an edit lands as late in the layer as its change rate allows.
2. **Bytes are a pure function of content.** Layer text has no version number, timestamp or id; tool schemas
   pass through canonical JSON; tools are sorted; `Layer.With` of equal content has an equal hash.
3. **Only G0 is a system prompt.** Pins, notes and spine are labelled *context* in the first user message.
   Text distilled from tool output must never gain operator authority (prompt-injection posture).
4. **Universal tool list.** Every agent, every role, sends the same `tools` array. Restrictions are enforced at
   run time (permissions, leases, role gates). Per-role tool lists would fork the prefix at byte 0.
5. **Wire fidelity.** Provider-native blocks (thinking signatures, `reasoning_details`, tool-call JSON) are kept
   verbatim in `Block.Wire` and replayed byte-for-byte; adapters never re-serialise history.

### Breakpoints (explicit-cache providers)

`kv.PlanMarks` spends at most four markers (`Caps.MaxBreakpoints`), in this priority order:

1. **thread**: the rolling marker on the newest block that can carry one (never a thinking block, an empty text
   block or a turn-scoped system message). Every request of every agent reads through it.
2. **anchor**: only when the blocks added since the previous rolling marker span more than the provider's 20-position
   lookback (R10): intermediate markers keep every hop inside the window. Without them a burst of new blocks
   orphans the previous entry and the whole thread is re-written at the write premium. The agent tells the renderer
   where the previous marker was (`RenderOpts.PrevRolling`) and coalesces a mail burst into one block (R10).
3. **shared**: the end of the shared pin, or of the constitution when there is no pin. It needs only the prefix
   before it to reach the provider minimum, however small the layer is (R9: with the old 1500-token floor a young
   project's 900-token pin got no marker and a second agent read none of the identical prefix).
4. **role**: the end of the role pin, when the pin itself reaches the provider minimum.
5. **notes**: the end of the notes, when they reach `MinLayerForBreakpoint` (1500). A commit that only touches the
   spine and thread then re-writes nothing above it.
6. **const**: the end of the constitution and tools, when a shared pin follows and a slot is still free: it survives a
   shared-pin epoch.

A marker is skipped when the prefix before it is under the provider minimum. Shared and role markers carry
`Policy.SharedTTL` (1h entries must precede 5-minute ones). The planner is a pure function over "plan blocks", so the
simulator (§8) applies exactly the same policy. Automatic-cache providers get no markers; the layout alone does the
work, plus a routing key (§6). The spine is one block, so appending to it re-writes all of it on an explicit cache;
that is priced (§4.2) and bounded (§4.1), not avoided.

## 3. The hot layer

The board view is a personalised, deterministic, token-capped rendering (`swarm.RenderHot`, ≤900 tokens for
workers, ≤2200 for the manager): own status, alerts, own and related tasks, relevant teammates first, pending shared
notes. It is *the* cost to keep small: at 50 agents an unbudgeted board is ~75k uncached tokens per fleet round.

Mail is **not** hot: a delivered message becomes a turn in the thread (append-only, cached after one turn,
compacted later), so it is paid for once. A burst of queued messages travels as one block per class (steering,
mail), because every block is a position in the provider's lookback window.

How the board reaches the model is decided per route by `kv.ResolveHot` (`Caps.HotMode`, `agent.Config.HotMode`):

### 3.1 `HotInline` (default)

The hot blocks are appended, ephemeral, to the last user message after the last cache marker and rebuilt on every
request. They cost the uncached rate each time and are never persisted. This is safe only where earlier bytes are
bound to nothing. On a model that enforces **preserved thinking** (Fable 5.1, Opus 5.5, Sonnet 5.5) it is not: the
assistant turn produced against request N carries a signature bound to a prefix that contained the tail of request N,
and request N+1 has removed it, so every request after the first is a thinking-binding 400 (R1; the reference names
"injecting per-request text into an earlier turn that you remove or rebuild" as the invalidating pattern).

### 3.2 `HotPersist` (persist on change)

Forced when `cost.Model.PreservedThinking` and the profile replays thinking (`Caps.ReplayThinking`) and the provider
has no turn-scoped system messages. The agent writes the hot text into the thread as a **frozen notice block**
(`kv.Notice`) in the user turn it is about to send, and only when its content changed since the last one (the board
version stamp and context counter are ignored) and no more often than every `HotMinRequests` requests (default 3).
History stays append-only, so cache entries and thinking bindings survive. Each notice stays in the thread until a
commit folds it; `kv.Apply` keeps only the newest one in the retained region, and a notice is never copied into
`instructions`. In the simulator this costs about +2% of the layered bill against inline at the default interval and
+13% if every request wrote one (§8), which is why it is throttled. The constitution therefore says that the newest
`<live>` block is the current one and that a stale one is history.

### 3.3 `HotTurnScoped` (interface; needs a native adapter)

Providers with turn-scoped system messages (`clear_at: "next_user_message"`, beta
`mid-conversation-system-clear-at-2026-08-21`, Opus 5.5 / Fable 5.1 / Sonnet 5.5 and others) can carry the board with
zero input cost after its turn and thinking bindings intact. The pieces that exist:

* `core.Message.ClearAt` marks such a message; it is part of the manifest and wire hash.
* `kv.Caps.HotMode = HotTurnScoped`, set when the profile has `TurnScopedSystem`. The **agent** appends, after every
  user turn (input, tool results, mail), a persisted thread turn with `Role: system`, `Origin: system` holding the
  board text. It is thread history like any other: append-only, archived, folded by compaction (cleared ones are
  dropped at a commit; only the newest can still matter).
* `kv.Render` renders such a turn as its own `core.Message{Role: system, ClearAt: "next_user_message"}`, never merges
  it, and never puts a cache marker on it (the rolling marker moves to the block before it). Without
  `HotTurnScoped` the same turn is folded into the neighbouring user message, which is what an adapter without the
  feature would do anyway.
* `kv.Sizer` counts only the newest such message: a cleared one renders nothing.
* Placement. The API wants a system message after a user message and either last in the array or followed by an
  assistant turn (a system message followed by a user message is a 400). `kv.Render` sends a turn as `role: system`
  only where that holds and folds it into user text elsewhere; the normal loop never needs the fold (an assistant turn
  always follows), it covers a retried request. The agent leaves mail that lands behind a board view queued for the
  next tool-results turn, and the compactor fork (`kv.ForkPrompt`) drops the parent's trailing view, because its
  instruction is a user turn and it does not need the board.

What the native Anthropic adapter must do (and nothing else):

1. Render each `Message{Role: system, ClearAt}` as `{"role":"system","clear_at":"next_user_message","content":[text...]}`
   and send the beta header. Text blocks only: no `cache_control`, `output_config` or tool blocks (the planner never
   places a marker there).
2. **Re-send every earlier copy verbatim on every later request**, in place. The harness already persists them with
   frozen bytes; the adapter must not drop, reorder, re-serialise or "tidy" them. The provider keeps them in the array
   and renders nothing for them, which is what keeps the cache and later thinking blocks valid (reference:
   `prompt-caching.md:83`, `causes.md:45`).
3. Set `Profile.TurnScopedSystem` only after the endpoint has been shown to pass the field (canary D11 step 4).
   Otherwise leave it false and the harness uses persist-on-change.

### 3.4 Recovery

If a request is rejected with `provider.ErrThinkingBinding` (a route that enforces bindings although the model is not
flagged, a gateway, a bug) the agent strips all thinking from the thread durably, as a declared rebase (`cache.anomaly
{kind: thinking_binding}`, `layer.commit {scope: thinking-strip}`), and retries once; if the profile has
`BindingControls` it asks for `prefix_mismatch_behavior: drop_block` on that retry and on the first request after any
rebase. If the endpoint drops a block behind our back (`input_transformations`), the same durable strip follows. A
rejection with no thinking to strip is returned, not retried. The rejection is also evidence: from then on the agent
treats the route as one that enforces bindings whatever the model table says (`cache.anomaly {flagged: false}`), so
the board is persisted instead of inlined and an unflagged gateway costs one rejected request, not one per request.

## 4. Compaction = generational collection, not summarisation

The thread is the *nursery*. Old material moves rightwards through cheaper-to-maintain generations, and the
deeper a generation, the more rarely and the more carefully it is touched.

| Event | Touches | Frequency | Cache rewrite |
|---|---|---|---|
| append turn | G5 | every step | delta only |
| **minor commit** | fold old units of G5 into G4 spine lines; mask bulky results; strip thinking | when thread pressure and economics allow | G4 + G5 (+G3 if notes change) |
| **major commit** | G3 notes ops (add/replace/remove/set) | rarely; batched with a minor commit, ideally at a cold moment | G3 + G4 + G5 |
| **shared epoch** | G1/G2 | session start, phase boundaries, idle swarm | everything below G1, for every agent |

### 4.1 What a compactor produces: a *patch*, never prose

A compactor is an LLM call that is a **fork of the agent's own request**: same model, tools, constitution, layers
and thread bytes, with one instruction block appended at the tail (`kv.ForkPrompt`). It replies with a patch
(`kv.Patch`):

```json
{"keep_from":"t30",
 "spine":[{"turns":"t12-t19","line":"Reproduced the refresh race; wrote failing test"}],
 "mask":["t22.0"],
 "notes":[{"op":"add","key":"decisions","text":"mutex, not channel, in refresh()"}],
 "promote":[{"scope":"shared","key":"conventions","text":"tests: make test-unit"}]}
```

The fork reads the agent's cached prefix **only if every request parameter is identical to the parent's**. On
Anthropic a different `tool_choice`, thinking or effort setting invalidates the entire messages tier, where G1-G5 live,
so the fork would re-write the conversation at 1.25x instead of reading it at 0.1x (R3: +37k to +68k ITE per fork at a
30k thread). The fork therefore sends the parent's parameters unchanged (including `max_tokens`); "do not call tools"
is instruction text only, and a reply that calls a tool anyway, or whose answer text is not a patch, counts as failed
and falls back to `MechanicalPatch`. The reply is parsed from its **text blocks only**, never from thinking text. The
instruction rides in the last user message, after the rolling marker; on a turn-scoped route the parent's trailing board
view is dropped first (§3.3, placement), which only removes bytes the parent's last request never cached. What a
fork costs: a read of the prefix, plain input for the instruction (~1.5k), a write for turns added since the last
request, and ~1k output tokens at ~5x. The planner prices that (§4.2).

The harness, not the model, validates and applies the patch (`kv.Apply`, a pure function) against the snapshot the
compactor saw, then commits with a compare-and-swap on the thread epoch while the agent kept running. Turns that
arrived meanwhile are carried over: structurally unchanged, but in cost terms they sit behind the changed prefix and
are re-written (§4.2), and their thinking, produced against the old prefix, is stripped in the same atomic step
(`Thread.CommitWith`) (R2, R8).

Safety properties (all tested):

* **Nothing is silently dropped.** Every folded unit is covered by a spine line; uncovered units get a
  harness-written mechanical line. The originals stay in the archive (`kv.Archive`) and come back through the
  `recall` tool by turn range, handle or query. Compaction is reversible.
* **Atomic units.** A tool call and its result are never separated (nor a turn-scoped system message from the turn it
  follows); an in-flight exchange is never folded; the newest units always stay verbatim.
* **User words survive compaction, bounded, and the model cannot rewrite them** (R14). The harness copies
  human-authored text into the `instructions` notes: every user-typed turn (up to `TaskMaxTokens`, 2400) and every
  piece of human steering (`Agent.Steer`, or `Agent.Send` of text that is not router mail; up to 600 tokens), verbatim,
  each tagged with its turn id. An entry that had to be cut ends `…[truncated; full text: recall tN]`. Mail from other
  agents, the hot view and tool output are never copied. Patch ops on `instructions` and `assignment` are ignored with a
  warning. The section is bounded (`MaxInstructionTokens`, 4000): the oldest entries move to the archive behind one
  pointer line (`- (N older instructions archived; recall turns="t3-t41")`).
* **Append-only spine; never summarise summaries; bounded** (R18). Old spine lines are immutable. When the spine
  exceeds `MaxSpineTokens` (3000) the oldest digests are *evicted to the archive behind one pointer line*
  (`t1-t120 · (57 earlier digests archived; recall turns="t1-t120")`), not re-written. Compactor-written notes
  sections are held to `MaxSectionTokens` (1500) and, together, `MaxNotesTokens` (6000) by trimming their oldest lines
  behind a marker (those lines carry no turn provenance, so there is nothing to point at); the next compactor
  instruction asks for a consolidation pass once a section or the notes are at 80% of budget, and a commit that had to
  trim emits `cache.anomaly {kind: notes_over_budget}`.
* **Masking, batched into commits.** Bulky old tool results are replaced by `⟦masked: bash(go test) · ~4.1k tokens ·
  recall t22.0⟧` deterministically (`AutoMaskAfterUnits`, `MaskMinTokens`). Masking is not free: the repo's own research
  (03, A3.2, arXiv:2607.12161) measured that removing 38% of tool-output tokens as standalone edits *raised* paired cost
  by 6.8% (95% CI +2.8..+11.3%) because each edit invalidates the cache from that point; the "masking roughly halves
  cost" result of JetBrains is a token count, not a cached bill. Sleipnir therefore masks only inside a commit that
  rewrites the cache anyway, or when the cache is cold (§4.2), and a mask-only commit that masks nothing is refused.
* **Fallback.** If the model's patch is unparseable, invalid, or a tool call, `MechanicalPatch` compacts without a
  model. A triggered compaction always yields something applicable.
* **Promotions are proposals.** `promote` entries go to a queue (board notes), visible to everyone immediately in
  the hot block, and are folded into G1/G2 only at an epoch (the scheduler for which is not built).

### 4.2 When to compact: the planner (`kv.Planner`)

Every size is what the provider is **sent** (`kv.Sizer`): reasoning the profile does not replay is stored but is not
pressure (R5: a reasoning model's planner thread was 12,274 tokens against 152 actually sent). Let `T` be the thread
tokens the patch replaces, measured before masking, `A` the spine tokens it adds, `R` the thread tokens retained, `D'`
the turns appended since the snapshot that an earlier request already sent, `S`/`S'` the spine before/after, `N`/`N'`
the notes before/after, `r` the read weight, `w` the write weight (1.0 with no write premium).

* **Warm cache.** The first request after the commit costs more than it would without it, by
  * explicit-breakpoint caches: `w·(R + D' + S') − r·(T + D' + S)`, because the spine is one block and is re-written
    whole (plus `w·N' − r·N` if notes change, which puts everything behind them in the rewrite);
  * automatic caches: `w·(A + R + D') − r·(T + D')`, because the old spine survives byte for byte (unless eviction
    changed its front).

  Every later request then saves `r·(T + S − S' − R)`. Commit when `N·saving > penalty` over the expected remaining
  turns `N` (the agent feeds `min(HorizonTurns, MaxSteps − step)`, default horizon 25). Example (Anthropic-like `r=0.1,
  w=1.25`; T=40k → R=6k, A=0.8k, S=1.2k, D'=2k): penalty 8.2k ITE, saving 3.3k per turn, pays back in ~2.5 turns (the
  earlier formula `w(A+R) − rT` said ~1.4 and was off by 3.7x on the review's counterexample, R8; the new one matches a
  cache simulation to within 2% in the tests). The compactor call is sunk once a patch exists; it is counted when
  deciding to *start* (below).
* **Cold cache** (the prefix will be re-prefilled anyway): a commit is *free* and saves `w·shrink` immediately, so cold
  moments are the preferred compaction windows. A model fork is the wrong tool then (it would pay a cold write for the
  prefix just to read the thread), so a cold agent with something to mask takes `ModeMask`: deterministic masking of bulky
  old results (`kv.MaskOnly`), no model call. It is used only when the maskable saving reaches `MaskMinTokens` (2000)
  and at least `MaskMinInterval` (6) requests passed since the last one (R7: without that a hidden cache made every step
  a commit that masked nothing and dropped the model's reasoning).
* **Start.** Warm and over `SoftThreadTokens`: fork, but only if a typical patch (22% retained, 3% spine) pays for its
  own fork call within the remaining turns. Over `HardThreadTokens` or 60% of the window: something is folded,
  whatever it costs: a cold agent masks first when there is something to mask, otherwise a **fork runs even though the
  cache is cold** (nothing to mask must never mean nothing happens). At 85% of the window a mechanical patch is
  applied synchronously.
* **Prepare early, commit late, but never hold hostage.** A ready patch may be *held* until the economics or a cold
  moment justify the rewrite (`compact.plan` records every decision). Hard and window pressure are judged on the
  **live** thread, not the snapshot; a patch held for more than 8 requests, or overtaken by thread growth (more than 25%
  or 3k tokens), is discarded and proposed again; the 85% emergency runs even while a patch is pending (R4: one marginal
  patch used to block compaction for the rest of the run while the thread grew to twice its hard limit).

**Warmth** (`Agent.isWarmLocked`): with a modelled entry lifetime, warm means "within it since my last request" (every
request re-writes or refreshes what it sent, hit or miss). Without one (engines that evict under memory pressure) it
means "the previous request read what the guard said it could". A provider that never reports cache usage gives no
evidence and counts as **warm**, never as cold forever. The hit *ratio* is not used: it measures how much of the
prompt is old, so a 14k-token tool result used to make a warm agent look cold (R6).

## 5. Preserved thinking and declared rebases

Newer Claude models bind each thinking block to the exact prefix that produced it; editing history invalidates
later blocks (HTTP 400 on accounts created on/after 2026-08-31; removing *all* thinking is always allowed). Sleipnir
therefore has exactly three kinds of transcript change, and each is a **declared rebase** (epoch counter) that strips
thinking durably in the same atomic step:

1. compaction commit (retained region and carried tail, `kv.Apply` + `Thread.CommitWith`),
2. shared-layer epoch (`Agent.SyncShared` swaps the layers and strips under one lock, so a request snapshot sees both
   or neither; a response that was in flight when the epoch landed is stripped as it is appended) (R2),
3. binding recovery (§3.4).

There is no render-time strip toggle (it would flip back and forth and mis-hit). Between rebases the prompt is
append-only, **the hot tail included** (§3): with `HotPersist` or `HotTurnScoped` nothing that was sent is rewritten.
`kv.Guard` checks this on every request: it remembers the previous request as a chain of hashes (model, thinking,
effort and tool_choice first, then tools, system and every persistent block with its message role and position),
measures the surviving exact prefix and, if it shrinks with no declared rebase, raises `cache.anomaly{kind: drift,
diverged: <layer>}`. It does not see adapter-level serialisation (that is the adapter's contract) or the inline hot
tail (it sits after the last marker and is not cached).

Low-hit anomalies compare provider-reported cache reads with what the guard says was readable: the exact shared
prefix, up to the highest marker of the previous request inside it on explicit caches, scaled by how far the estimator
was off on the previous request. An alarm is a miss of more than 5% of the prompt and at least 2,000 tokens (Claude
Code's rule), judged only when the provider reports cache usage, the entry cannot be expected to have expired, and the
prefix was big enough to cache. A first request is checked too, when the gate says the shared prefix was already warm
(R11). This is the standing check that turns silent cost regressions into events.

(Providers with an API-native background compaction primitive can implement the same `Patch` semantics through
it; the client-side path above is the portable one.)

## 6. Swarm-level cache behaviour

* **One trie, many leaves.** All agents share G0+G1; agents of a role share G2; only G3–G6 are private. With the marker
  policy of §2, spawning a worker costs a warm read of the shared prefix plus a tiny assignment: no briefing needed
  (checked on every worker's first request, §5). A **reused** worker keeps its warm context; its new task's brief, scope
  and dependencies travel in the kickoff turn (`New assignment. It replaces the assignment in <my-notes>…`) because its
  notes still describe the first task, and the harness folds that user turn into `instructions` at the next commit (R15).
* **Warm gate** (`swarm.WarmGate`). A provider serves a prefix only after some request began responding, so a fan-out
  over a cold prefix would pay N full prefills. The gate elects one *primer* (the first real request) and holds the
  rest until its first byte; warm prefixes pass straight through. Keys are two nested levels, the shared prefix on the
  agent's routing shard (model, tools, constitution, shared pin, thinking/effort/tool_choice; the shard only where the
  provider routes by key) and the role prefix inside it, so agents of a second role, or on a second engine, are not
  released onto a prefix that is still cold for them (R12). A level is presumed warm from the **start** of the request
  that touched it for the entry lifetime less a margin (the provider measures from request start). A stuck primer is not
  waited for forever and not walked past all at once: after `maxWait` one follower is released as a co-primer, then two,
  then four, and the first byte of any of them warms the level for the rest. Tested: one primer and seven followers for
  eight concurrent agents; a second role waits for its own primer; a failed primer hands over; nested-key stress under the
  race detector. Not tested: behaviour against a real provider.
* **Affinity.** Engines behind a marketplace cache per engine. Requests carry a routing key
  (`sl:<session>:<GlobalKey>:<shard>`) as `prompt_cache_key` and `X-Session-Id`, so agents that share a prefix
  *should* land on the engine that holds it; `AffinityShards` spreads very large swarms over several engines. This is
  **designed for, not verified**: the only evidence is the in-repo mock, which hard-codes pinning by those keys.
  `docs/research/02-provider-caching.md` §0 and §6 record every marketplace-gateway behaviour as unverified and
  prescribe the canary suite (D11) first. The default is one shard (one key per swarm); OpenAI documents ~15
  requests/min per prefix+key before spill to other machines, so a busy swarm needs about RPM/12 shards, which nothing
  sizes yet (R13).
* **Epochs are rare, and self-warming once they happen.** A new shared layer changes `GlobalKey`; the gate makes the
  first agent after the change the primer for all the rest, and `SyncShared` strips thinking as part of it. What decides
  *when* an epoch happens is not built.
* **Rate limit is the real cap.** Marketplaces limit requests, not tokens (600/min/key on Heimdall). The
  governor paces admission by priority (manager > workers > background), honours `Retry-After`, and adapts
  (multiplicative decrease on 429, additive increase on success).

## 7. Accounting and self-calibration

Every request emits a *recipe* (`model.request`): layer names/hashes/token estimates, breakpoints, thread range,
hot-block hash, routing key, params. Layer texts are content-addressed blobs stored once, so 50 agents sharing
a prefix log it once and any prompt is reconstructible. Every response records normalised usage
(`input/cache_read/cache_write/output`), the gateway's exact `usage.cost` when reported, hit ratio, the guard's
expectation and the missed tokens. Provider-reported input tokens against prompt bytes calibrate the token estimator
(`BytesEstimator`) online: the first accepted sample sets the ratio and later ones move it by a moving average. (The
idea that cache boundaries could make usage reports an exact per-layer tokenizer is *not* implemented: the estimator
sees only whole-prompt totals, its byte count ignores framing, tool wrappers and thinking signatures, and one
estimator is shared by all agents; its error is a few percent.) The agent scales the guard's expectations by the
previous request's reported/estimated ratio before comparing them with reads, which removes most of that error.

## 8. Where layering pays, and where it does not (simulation)

`internal/kv/sim` (`sleipnir sim`) replays one synthetic swarm under three policies and prices every request
against an explicit cache model (exact prefix chains, TTL/LRU eviction, entries readable only after the first
byte, per-engine caches, optional routing affinity). Tasks are drawn per task index from the seed, so every policy faces
identical work (common random numbers); only the policy differs. The Sleipnir policy follows the code: its markers come
from `kv.PlanMarks`, its decisions from the real `kv.Planner` fed the state the agent feeds it (warmth judged by the
entry lifetime like the agent does; the planner's default horizon, not an oracle for the remaining work), masking hides
the bulky old results one by one so the cache keeps the prefix before the first, the gate is keyed and timed like the
real one, and the compactor fork reads the parent's prefix (true since R3 was fixed; before it the simulator assumed a
read the code did not earn).

* **plain**: every subagent has its own growing history cached end to end, is briefed by the manager, and must
  explore the project to get its bearings (default: 10 extra tool calls returning 30k tokens). Constitution and
  tools are shared, as in real harnesses.
* **plain + summary compaction**: the same, summarising a subagent's history into one message past 100k tokens.
* **Sleipnir**: G0 + 8k shared pin + 10k role pin + notes + spine + thread + a 700-token hot tail; planner-driven
  compaction with cold-moment masking; warm gate; routing affinity. Agents skip most orientation (2 targeted
  reads of 3.6k tokens instead of 10 of 3k).

Every parameter is an assumption printed with the result. The event log records exactly the quantities the
simulator consumes; **calibrate against real traces before quoting absolute savings**. It has no thinking blocks, one
compactor patch shape (22% retained, 3% spine) and no lookback. Cost is in input-token equivalents (Anthropic-like:
read 0.1x, write 1.25x, TTL 5m; marketplace-like: read 0.25x, no write premium, 3 engines). 20 workers, 40 tasks,
seed 1:

| scenario | plain | plain + summary | Sleipnir | vs plain | vs plain + summary |
|---|---:|---:|---:|---:|---:|
| typical (8k+10k pins, 30k orientation) | 25.4M | 22.1M | 16.2M | −36% | −26% |
| short tasks (~8 steps) | 8.5M | 8.5M | 4.7M | −45% | −45% |
| long tasks (~90 steps) | 96.8M | 50.7M | 45.3M | −53% | −11% |
| cold launches (3 swarms, 12 min idle) | 25.6M | 22.3M | 16.4M | −36% | −26% |
| small repo (6k orientation) | 17.1M | 16.4M | 15.5M | −9% | −5% |
| **bloated pins (40k+20k)** | 25.4M | 22.1M | 22.8M | −10% | **+3%** |
| huge exploration (90k orientation) | 50.8M | 32.5M | 18.0M | −64% | −44% |

The headline moved from the first version of this table (−37% / −28%) to −36% / −26% once the model followed the
code. (The review estimated that a fork that changed `tool_choice` would have added 26–47% to the layered bill and moved
the headline to about −21% / −8%; that is the bug, not the design, and it is fixed.) The code now does things the first
model ignored: the commit penalty includes the tail, spine and notes; mask commits are rate limited and need something
to mask; warmth is the entry-lifetime test; nothing is an oracle. Together they cost a point or two.

Marketplace-like pricing is less forgiving (cheap writes make re-exploration cheaper): typical −32% / −21%,
small repo −1% / +4%, **bloated pins +17% / +36%**.

**On a model that enforces preserved thinking** (the `HotPersist` route, §3.2) the board is written into the thread
instead of billed as an uncached tail: about +2% of the layered bill at the default interval of 3 requests (16.5M vs
16.2M in the typical case, still −35% vs plain), −1.6% at every 6th request, and +13% if every request wrote one
(18.3M, −28%). That is the price of correctness on that route; without it every request after the first fails.

### 8.1 The design rule this yields: pins must be dense

Layering trades *re-exploration by every agent* for *a larger prefix read at every request*. The shared and role
pins are read on every step of every agent; the exploration they replace is paid once per task. Sweeping the total
pin size against a fixed 30k-token orientation budget (20 workers):

| pins / orientation | Anthropic-like vs plain | vs plain + summary | marketplace vs plain | vs plain + summary |
|---:|---:|---:|---:|---:|
| 0.13 (4k) | −44% | −36% | −48% | −40% |
| 0.6 (18k) | −36% | −26% | −32% | −21% |
| 1.0 (30k) | −29% | −18% | −19% | −5% |
| 1.5 (45k) | −20% | −8% | −1% | +15% |
| 2.3 (70k) | −5% | +10% | +28% | +48% |
| 3.3 (100k) | +14% | +31% | +62% | +88% |

The break-even sits at roughly 1–2.3x the orientation saved, depending on provider pricing and baseline. So:

1. **Budget the pins.** Keep G1+G2 at or under about the size of what a fresh agent would read to orient (the
   defaults are 8k+10k). `recon` and the curator count tokens and refuse to grow a pin past its budget; a pin
   must earn its place by replacing more reading than it costs to carry.
2. **Dense, not comprehensive.** A repo map, conventions, commands, invariants, ownership. Not file contents;
   those stay behind `read`/`recall`.
3. **Small repos should run lean.** With little to explore the advantage shrinks to ~10% and can go negative on
   marketplace pricing. The harness measures the ratio after a few tasks (orientation tokens observed per agent
   against pin tokens carried) and can shrink or drop role pins for small projects.
4. **The prefix must not be the only lever.** With bloated pins the remaining gain comes from compaction and
   from avoiding cache-busting; that is why compaction, masking and the hot-tail budget matter independently.

### 8.2 What each mechanism is worth (ablations, Anthropic-like, 20–50 workers)

* **Generational compaction**: removing it raises cost by 20–23% and the average context by ~65% (about 71k tokens
  instead of 43k, the attention load). Against plain + summary compaction the margin is smaller (−11% on long
  tasks) because a summary at 100k is also a compaction; the difference is *when* and *what*: deterministic masking
  first, folded units kept as one-liners, nothing summarised twice, and commits scheduled for cheap moments.
* **Cold-moment masking**: about 5% on cold launches and typical runs, 5% when one in five tool calls outlives the cache
  and 11% for two workers with a quarter of their calls outliving it. It is free to run and removes the "compact while
  cold" trap where a fork pays a second cold write.
* **Warm gate**: about 3% of cost on cold launches of 30 workers (one primer instead of N cold writes of the role layer
  per launch); its real value is not stampeding the provider with N simultaneous cold prefills (latency, 429s).
* **Routing affinity** (multi-engine marketplaces): removing it raises the bill by 18–19% (the −33% advantage over plain
  falls to −20%). Without a key the shared prefix is prefilled on every engine. This is the simulator's premise, not a
  measurement of any gateway (§6).
* **Hot tail**: 700 uncached tokens per request are ~6% of the layered bill on the inline route. It is the price of
  coordination and the reason it is budgeted and personalised rather than a dump of the board.

### 8.3 Effects the cost table does not show

Orientation skipped is also *time*: in the typical case the swarm finishes in ~81 minutes instead of ~100. The
average context shrinks by ~37% (43k vs 68k), which matters for long-context quality and latency but has no line in
a token bill. Coordination benefits (no briefing written by the manager, visible board, mail) are outside the
model entirely.

## 9. What is measured, and what is not yet

Measured in tests against the in-repo mock (`internal/provider/mock`), which is an **automatic-prefix-cache** engine: it
hashes real prompt bytes into block chains, evicts under memory pressure the way paged-attention engines do (least
recently used sequence first, its newest blocks first), publishes entries only at first token, and routes by
affinity: steady-state hit ratios ≥ 0.8 from the third request; a commit costs one partial rewrite that still keeps the
deep prefix warm (≥ 0.2 immediately after, ≥ 0.6 within the epoch); no false drift alarms; fan-out reads instead of
writes. It is not a model of explicit-breakpoint caching (markers are flattened; `tool_choice`, effort and thinking
configuration are not part of any key), so the marker planner, TTL ordering, lookback and the invalidation hierarchy are
covered by the tests' own explicit-cache model (`cxSim` in `internal/kv`) and not by the mock.

Measured against a fake provider that enforces preserved-thinking bindings (a thinking block's signature is the hash of
everything before it): the hot tail in all three modes, background commits, shared epochs before, during and between
requests, and binding recovery.

Measured in the simulator: the tables above, guarded by tests (`TestPinDensityRule`,
`TestAblationsMatterInTheRightDirection`, `TestColdMaskingHelpsAfterLongWaits`,
`TestSimHeadlineWithTheFixesStaysInTheDocumentedRange`, `TestSimPersistedHotOnAPreservedThinkingRoute`).

Not yet measured against real providers (blocked on credentials): real block sizes and TTLs (the `doctor` probe
measures them), real hit ratios per model, whether a gateway passes `cache_control`, `clear_at` and
`block_binding` through, the real size of orientation reads with and without a pin, and the end-to-end savings against
a naive whole-history harness. Until then §8 is a model, not a benchmark.

Known limits, deliberately left: notes sections other than `instructions` are trimmed, not archived (their lines have
no turn provenance); the archive keeps a 4 KB in-memory search preview per turn; the notes marker still flickers with
the estimator near its 1500-token floor (bytes never do); `openaichat` sends one text part as a string and several as
an array, so a message can serialise differently when an inline hot block attaches to it (harmless on engines that
flatten parts); the default routing-shard count is 1; the OpenAI newer-model write premium is not priced.
