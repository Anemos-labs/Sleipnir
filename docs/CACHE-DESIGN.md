# Sleipnir cache design: layered, generational, cost-aware prompt state

Status: implemented in `internal/kv`, `internal/agent`, `internal/swarm` (gate, hot view). Numbers below are
input-token equivalents (ITE): 1 ITE = the price of one plain input token.

## 1. The constraint everything follows from

Provider prompt caches are **byte-prefix matches**. Any change at byte *p* invalidates everything after *p*, for
every request that shared it. So the cost of a change is proportional to how much prompt follows it, times how
many agents carry that prompt. Harnesses that treat the conversation as one growing list get an easy win from
"cache the end of the last turn" and then lose it whenever they trim, summarise, swap tools, or inject
per-turn text. A swarm multiplies the loss by the number of agents.

Sleipnir's answer is to make **layout follow volatility** and to treat every change to a stable layer as a
priced, batched, *declared* event.

| Provider family | Cache | Read | Write | Readable when | Placement control |
|---|---|---|---|---|---|
| Anthropic | explicit, ≤4 breakpoints, 20-block lookback, TTL 5m/1h | 0.1x (0.05x Opus 5.5, 0.025x Fable 5.1) | 1.25x / 2x | after the first response byte | markers |
| OpenAI / marketplace engines (vLLM/SGLang-like) | automatic block-hash prefix, LRU | ~0.25x (Heimdall catalogue) | none (1x) | after prefill of the first request | ordering + routing key (`prompt_cache_key`, `X-Session-Id`) |

`internal/cost` models both (`CacheModel`, `Weights`); the planner never hard-codes a provider.

## 2. Layers

```
G0 constitution + tools   frozen for the session; identical bytes for every agent and role
G1 shared pin             project knowledge; identical for every agent; changes only at *epochs*
G2 role pin               conventions shared by all agents of one role; epochs only
G3 notes                  one agent's tenured facts/decisions/instructions/assignment; edited at major commits
G4 spine                  one agent's append-only one-line history digests; appended at minor commits
G5 thread                 verbatim recent turns; strictly append-only between commits
G6 hot                    always-fresh board view; never cached, never persisted
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

At most four; priority `thread > shared > role > notes`. A marker is skipped when its prefix is under the
provider minimum or the layer it closes is too small to justify a separate entry (`MinLayerForBreakpoint`).
Automatic-cache providers get no markers; the layout alone does the work, plus a routing key (§6).

## 3. The hot layer

The board and mailbox view sit **after the last breakpoint** and are regenerated every request: a personalised,
deterministic, token-capped rendering (`swarm.RenderHot`, ≤900 tokens for workers, ≤2200 for the manager):
own status, alerts, own and related tasks, relevant teammates first, pending shared notes. It is billed at the
uncached rate on every request of every agent, so it is *the* cost to keep small: at 50 agents an unbudgeted board
is ~75k uncached tokens per fleet round.

Mail is **not** hot: a delivered message becomes a turn in the thread (append-only, cached after one turn,
compacted later), so it is paid for once.

Providers with turn-scoped system messages (`clear_at: next_user_message`, Claude Opus 5.5 / Fable 5.1 / Sonnet 5.5)
can carry the hot block natively, with zero input cost after the turn and thinking bindings intact; the
adapter interface (`Block.Ephemeral`) already distinguishes ephemeral blocks for that.

## 4. Compaction = generational collection, not summarisation

The thread is the *nursery*. Old material moves rightwards through cheaper-to-maintain generations, and the
deeper a generation, the more rarely and the more carefully it is touched.

| Event | Touches | Frequency | Cache rewrite |
|---|---|---|---|
| append turn | G5 | every step | delta only |
| **minor commit** | fold old units of G5 into G4 spine lines; mask bulky results; strip stale thinking | when thread pressure and economics allow | G4 + G5 |
| **major commit** | G3 notes ops (add/replace/remove/set) | rarely; batched with a minor commit, ideally at a cold moment | G3 + G4 + G5 |
| **shared epoch** | G1/G2 | session start, phase boundaries, idle swarm | everything below G1, for every agent |

### 4.1 What a compactor produces: a *patch*, never prose

A compactor is an LLM call that is a **fork of the agent's own request**: same model, tools, constitution, layers
and thread bytes, with one instruction block appended at the tail (`kv.ForkPrompt`). The provider serves the
whole shared prefix from cache, so the compactor pays for the instruction and a short JSON answer, not for
re-reading the conversation. It replies with a patch (`kv.Patch`):

```json
{"keep_from":"t30",
 "spine":[{"turns":"t12-t19","line":"Reproduced the refresh race; wrote failing test"}],
 "mask":["t22.0"],
 "notes":[{"op":"add","key":"decisions","text":"mutex, not channel, in refresh()"}],
 "promote":[{"scope":"shared","key":"conventions","text":"tests: make test-unit"}]}
```

The harness — not the model — validates and applies it (`kv.Apply`, a pure function) against the snapshot the
compactor saw, then commits with a compare-and-swap on the thread epoch while the agent kept running. Turns that
arrived meanwhile ride along verbatim.

Safety properties (all tested):

* **Nothing is silently dropped.** Every folded unit is covered by a spine line; uncovered units get a
  harness-written mechanical line. The originals stay in the archive (`kv.Archive`) and come back through the
  `recall` tool by turn range, handle or query. Compaction is reversible.
* **Atomic units.** A tool call and its result are never separated; an in-flight exchange is never folded; the
  newest units always stay verbatim.
* **User instructions survive verbatim.** User-authored turns are copied into the `instructions` notes segment
  by the harness, never entrusted to a summariser.
* **Append-only spine; never summarise summaries.** Old spine lines are immutable. (Planned: when the spine
  exceeds its budget the oldest lines are *evicted to the archive behind a pointer line*, not re-written.)
* **Masking before summarising.** Bulky old tool results are replaced by `⟦masked: bash(go test) · ~4.1k tokens ·
  recall t22.0⟧` deterministically (`AutoMaskAfterUnits`, `MaskMinTokens`). Published agent-context studies find
  masking roughly halves cost at equal solve rate; LLM summarisation is reserved for what masking cannot shrink.
* **Fallback.** If the model's patch is unparseable or invalid, `MechanicalPatch` compacts without a model.
  A triggered compaction always yields something applicable.
* **Promotions are proposals.** `promote` entries go to a queue (board notes), visible to everyone immediately in
  the hot block, and are folded into G1/G2 only at an epoch.

### 4.2 When to compact: the planner (`kv.Planner`)

Let `S` be the prefix that survives a commit, `T` the thread tokens the patch covers, `A` the spine tokens it
adds, `R` the thread tokens retained, `r` the read weight and `w` the write weight (1.0 with no write premium).

* **Warm cache.** The next request costs `w·(A+R) − r·T` *more* than without the commit; every later request
  saves `r·(T−A−R)`. Commit when `N·r·(T−A−R) > w·(A+R) − r·T` for the expected remaining turns `N`.
  Example (Anthropic-like `r=0.1, w=1.25`; 40k → 6.8k): pays back in ~1.4 turns. (Marketplace `r=0.25, w=1`: faster.)
* **Cold cache** (the prefix will be re-prefilled anyway, e.g. after an idle gap past the TTL): a commit is
  *free* and saves `w·(T−A−R)` immediately, so cold moments are the preferred compaction windows. A model fork
  is the wrong tool then: it would pay a cold write for the prefix just to read the thread. The planner
  therefore returns `ModeMask` for a cold agent, and the harness applies deterministic masking of bulky old
  results (`kv.MaskOnly`) with no model call; the next request re-prefills a smaller prompt for the price of a
  bigger one. Warm agents get `ModeFork` (the high-quality path, whose cost is the instruction and the answer).
  In the simulator this rule is worth 5–14% when tool calls sometimes outlive the cache (§8).
* **Pressure.** Start at `SoftThreadTokens`; force at `HardThreadTokens` or 85% of the context window (mechanical
  emergency compaction if the model path is too slow).
* **Prepare early, commit late.** A ready patch may be *held* until the economics or a cold moment justify the
  rewrite (`compact.plan` events record every decision and reason).

`agent.isWarm` is estimated from provider TTL when modelled, else from the measured hit ratio of the last
request (a first request wrote the prefix, so it counts as warm).

## 5. Preserved thinking and declared rebases

Newer Claude models bind each thinking block to the exact prefix that produced it; editing history invalidates
later blocks (HTTP 400 on accounts created on/after 2026-08-31). Sleipnir therefore has exactly three kinds of
transcript change and treats each as a **declared rebase** (epoch counter):

1. minor/major compaction commit,
2. shared-layer epoch (`Agent.SyncShared`),
3. `StripThinking` (thinking blocks removed durably from the thread at the boundary; never a render-time toggle,
   which would flip back and forth and mis-hit).

Between rebases the prompt is append-only, so cache and thinking bindings both stay valid. `kv.Guard` verifies
this on every request: it remembers the previous request as a chain of block hashes, measures the surviving exact
prefix, and if it shrinks with no declared rebase raises `cache.anomaly{kind: drift, diverged: <layer>}` naming
the layer that changed. Low-hit anomalies compare provider-reported cache reads to the guard's expectation. This
is the standing check that turns silent cost regressions into events.

(Providers with an API-native background compaction primitive can implement the same `Patch` semantics through
it; the client-side path above is the portable one.)

## 6. Swarm-level cache behaviour

* **One trie, many leaves.** All agents share G0+G1; agents of a role share G2; only G3–G6 are private. Spawning
  a worker costs a warm read of the shared prefix plus a tiny assignment: no briefing needed.
* **Warm gate** (`swarm.WarmGate`). A provider serves a prefix only after some request began responding, so a
  fan-out over a cold prefix would pay N full prefills. The gate elects one *primer* (the first real request),
  holds the rest until its first byte, then releases them; warm prefixes pass straight through. Tested: eight
  concurrent agents produce one primer and seven followers.
* **Affinity.** Engines behind a marketplace cache per engine. Requests carry a routing key
  (`sl:<session>:<GlobalKey>:<shard>`) as `prompt_cache_key` and `X-Session-Id`, so agents that share a prefix
  land on the engine that holds it; `AffinityShards` spreads very large swarms over several engines.
  Without a key, k engines cost k−1 extra cold prefills per prefix (tested).
* **Epochs are rare and self-warming.** A new shared layer changes `GlobalKey`; the gate makes the first agent
  after the change the primer for all the rest.
* **Rate limit is the real cap.** Marketplaces limit requests, not tokens (600/min/key on Heimdall). The
  governor paces admission by priority (manager > workers > background), honours `Retry-After`, and adapts
  (multiplicative decrease on 429, additive increase on success).

## 7. Accounting and self-calibration

Every request emits a *recipe* (`model.request`): layer names/hashes/token estimates, breakpoints, thread range,
hot-block hash, routing key, params. Layer texts are content-addressed blobs stored once, so 50 agents sharing
a prefix log it once and any prompt is reconstructible. Every response records normalised usage
(`input/cache_read/cache_write/output`), the gateway's exact `usage.cost` when reported, hit ratio and the guard's
expectation. Provider-reported input tokens against prompt bytes calibrate the token estimator (`BytesEstimator`)
online: cache boundaries make usage reports a free tokenizer.

## 8. Where layering pays, and where it does not (simulation)

`internal/kv/sim` (`sleipnir sim`) replays one synthetic swarm under three policies and prices every request
against an explicit cache model (exact prefix chains, TTL/LRU eviction, entries readable only after the first
byte, per-engine caches, optional routing affinity). It uses the real `kv.Planner`. Tasks are drawn per task
index from the seed, so every policy faces identical work (common random numbers); only the policy differs.

* **plain**: every subagent has its own growing history cached end to end, is briefed by the manager, and must
  explore the project to get its bearings (default: 10 extra tool calls returning 30k tokens). Constitution and
  tools are shared, as in real harnesses.
* **plain + summary compaction**: the same, summarising a subagent's history into one message past 100k tokens.
* **Sleipnir**: G0 + 8k shared pin + 10k role pin + notes + spine + thread + a 700-token hot tail; planner-driven
  compaction with cold-moment masking; warm gate; routing affinity. Agents skip most orientation (2 targeted
  reads of 3.6k tokens instead of 10 of 3k).

Every parameter is an assumption printed with the result. The event log records exactly the quantities the
simulator consumes; **calibrate against real traces before quoting absolute savings**. Cost is in input-token
equivalents (Anthropic-like: read 0.1x, write 1.25x, TTL 5m; marketplace-like: read 0.25x, no write premium,
3 engines). 20 workers, 40 tasks, seed 1:

| scenario | plain | plain + summary | Sleipnir | vs plain | vs plain + summary |
|---|---:|---:|---:|---:|---:|
| typical (8k+10k pins, 30k orientation) | 25.4M | 22.1M | 15.9M | −37% | −28% |
| short tasks (~8 steps) | 8.5M | 8.5M | 4.8M | −44% | −44% |
| long tasks (~90 steps) | 96.8M | 50.7M | 45.6M | −53% | −10% |
| cold launches (3 swarms, 12 min idle) | 25.6M | 22.3M | 16.1M | −37% | −28% |
| small repo (6k orientation) | 17.1M | 16.4M | 15.2M | −11% | −7% |
| **bloated pins (40k+20k)** | 25.4M | 22.1M | 22.4M | −12% | **+2%** |
| huge exploration (90k orientation) | 50.8M | 32.5M | 17.5M | −65% | −46% |

Marketplace-like pricing is less forgiving (cheap writes make re-exploration cheaper): typical −33% / −22%,
small repo −2% / +4%, **bloated pins +16% / +35%**.

### 8.1 The design rule this yields: pins must be dense

Layering trades *re-exploration by every agent* for *a larger prefix read at every request*. The shared and role
pins are read on every step of every agent; the exploration they replace is paid once per task. Sweeping the total
pin size against a fixed 30k-token orientation budget (20 workers):

| pins / orientation | Anthropic-like vs plain | vs plain + summary | marketplace vs plain | vs plain + summary |
|---:|---:|---:|---:|---:|
| 0.13 (4k) | −46% | −38% | −49% | −41% |
| 0.6 (18k) | −37% | −28% | −33% | −22% |
| 1.0 (30k) | −30% | −20% | −19% | −6% |
| 1.5 (45k) | −21% | −9% | −2% | +14% |
| 2.3 (70k) | −6% | +8% | +27% | +47% |
| 3.3 (100k) | +12% | +29% | +61% | +87% |

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

* **Generational compaction**: removing it raises cost by 23–28% and the average context by ~65% (about 70k tokens
  instead of 43k, the attention load). Against plain + summary compaction the margin is smaller (−10% on long
  tasks) because a
  summary at 100k is also a compaction; the difference is *when* and *what*: deterministic masking first, folded
  units kept as one-liners, nothing summarised twice, and commits scheduled for cheap moments.
* **Cold-moment masking**: 5% on cold launches, 14% when one in five tool calls outlives the cache. It is nearly
  free to implement and removes the "compact while cold" trap where a fork pays a second cold write.
* **Warm gate**: 1–2% of cost in these runs (one primer instead of N cold writes of the role layer per launch);
  its real value is not stampeding the provider with N simultaneous cold prefills (latency, 429s).
* **Routing affinity** (multi-engine marketplaces): removing it raises the bill by 20% at 50 workers (the −33%
  advantage over plain falls to −20%). Without a key the shared prefix is prefilled on every engine.
* **Hot tail**: 700 uncached tokens per request are 4–6% of the layered bill. It is the price of coordination and
  the reason it is budgeted and personalised rather than a dump of the board.

### 8.3 Effects the cost table does not show

Orientation skipped is also *time*: in the typical case the swarm finishes in ~81 minutes instead of ~100. The
average context shrinks by ~40% (43k vs 68k), which matters for long-context quality and latency but has no line in
a token bill. Coordination benefits (no briefing written by the manager, visible board, mail) are outside the
model entirely.

## 9. What is measured, and what is not yet

Measured in tests against the cache-faithful mock (`internal/provider/mock`, which hashes real prompt bytes into
block chains, evicts under memory pressure, publishes entries only at first token, and routes by affinity): steady
state hit ratios ≥ 0.8 from the third request; a commit costs one partial rewrite that still keeps the deep prefix
warm (≥ 0.2 immediately after, ≥ 0.6 within the epoch); no false drift alarms; fan-out reads instead of writes.

Measured in the simulator: the tables above, guarded by tests (`TestPinDensityRule`, `TestAblationsMatterInTheRightDirection`,
`TestColdMaskingHelpsAfterLongWaits`).

Not yet measured against real providers (blocked on credentials): real block sizes and TTLs (the `doctor` probe
measures them), real hit ratios per model, the real size of orientation reads with and without a pin, and the
end-to-end savings against a naive whole-history harness. Until then §8 is a model, not a benchmark.
