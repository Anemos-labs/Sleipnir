# 03 - Swarm coordination, context engineering, and training data for Sleipnir

Status: research draft for design review. Date: 2026-09-29. Author: research analyst (Claude).
Scope: Part A covers how multi-agent coding harnesses coordinate and maintain context; Part B covers what a harness must log so
trajectories can later train a model that natively works this way. Layout: 0 summary, A1-A5, B1-B4, design rules, validation experiments, sources.

## How to read this document

Evidence tags: (P) primary page fetched and read this session; (G) GitHub README or source file fetched and read; (S) secondary only, meaning a
search-engine summary of a source I could not open (re-check before quoting externally); UNVERIFIED = not confirmed by any source I could read.
Bracketed keys such as [A-MAS] or [arXiv:2601.13295] resolve in the Sources section. No emojis, no legal advice.

Access limits (calibration). The sandbox proxy blocked arxiv.org, huggingface.co, cognition.ai/.com, manus.im, openai.com, cursor.com, x.com,
medium.com, factory.ai, langchain.com, letta.com, kimi.ai, lmsys.org, docs.openhands.dev and openreview.net, and the web-search budget (200 calls)
ran out. Read first-hand: anthropic.com, claude.com, platform.claude.com, code.claude.com, support.claude.com, github.com, raw.githubusercontent.com.
So Anthropic and Claude Code facts are first-hand; Cognition, Manus, Cursor, OpenAI, Kimi, Factory and almost every arXiv result are (S).
Most sources are from 2026 and postdate my training data; I did not lean on memory except where flagged.

## 0. Summary of findings

1. Parallelize reads, serialize writes. Every strongly positive multi-agent result I found has a single integrating authority and isolated contexts or
   workspaces, and the coding ones add verifiers: Anthropic research +90.2% [A-MAS], Cursor planner/worker/judge [CURSOR-SCALE], CAID +26.7/+14.3 pts [CAID], Devin Review [COG-2].
   Negative results come from peer writers on one codebase: two cooperating agents reach ~25% success, about half of one agent doing both tasks
   [GH-COOPERBENCH]; a paper review reports further decline from 2 to 4 agents (S); multi-agent loses up to 70% on sequential tasks [arXiv:2512.08296].
2. The provider prefix cache dominates cost and dictates layout. Cache traffic was ~87% of reconstructed cost in 2,848 analyzed billed Claude Code runs
   [arXiv:2607.12161]. Matching is exact-prefix, tools come first, a cache entry exists only after the first response begins, and there are at most 4 explicit
   breakpoints [API-CACHE]. Any edit to a shared layer flushes every agent's cache.
3. Compaction: masking old observations is the baseline to beat (half the cost, same solve rate) [arXiv:2508.21433]; monolithic LLM rewrites collapse
   (18,282 -> 122 tokens, accuracy 66.7 -> 57.1) [arXiv:2510.04618]; summaries lose artifact trails (best 2.45/5) [FACTORY]; RL-trained self-summarization
   cut compaction error 50% at ~1/5 the tokens [CURSOR-SUM]. Fewer tokens is not lower cost [arXiv:2607.12161][arXiv:2609.32961].
4. Concurrency: 19.8-41.7% of temporally overlapping agent PRs conflict textually [arXiv:2607.04697]; CRDT editing still leaves 5-10% semantic conflicts
   [arXiv:2510.18893]; claim-based admission lifts pass rate 23.3% -> 50.0% but serializes work [arXiv:2608.00947]; isolation plus merge-time validation wins [arXiv:2608.18092].
5. Typed shared state beats dialogue (PatchBoard 84.6% vs LangGraph 30.8% on ALFWorld) [arXiv:2605.29313]; models' own progress reports are unreliable
   [arXiv:2609.08589], so board status must be harness-derived.
6. Background memory agents work in principle (sleep-time: ~5x less test-time compute) [arXiv:2504.13171] but fail through last-writer-wins edits [LETTA-SHARED],
   recall-then-re-extract loops (97.8% junk in one 10,134-entry audit, n=1) [GH-MEM0-4573] and poisoning [arXiv:2503.03704].
7. Training data: the field converged on capturing at the LLM API boundary with token IDs, logprobs, lineage and compaction-aware sample splitting
   [arXiv:2605.24220][GH-DRESSAGE][GH-RLLM][arXiv:2608.17528]. The harness matters more than the recipe (4.3x vs 1.16x) [arXiv:2609.04518].
8. Governance risk is larger than expected: provider terms bar training competing models on outputs [ANTH-TERMS]; verified-outcome labels are noisy
   (15.7% of Verified-passing patches wrong [arXiv:2506.09289]; infra config moves scores 6 pts [A-INFRA]); multi-agent runs hit eval contamination 3.7x more often [A-EVALAWARE].

### Where the evidence contradicts or refines the design as briefed

| # | Design element | Evidence | Verdict | Change to make |
|---|---|---|---|---|
| 1 | 10-50 concurrent workers | CooperBench, Google scaling study, Claude Code docs say start with 3-5 teammates; scale successes (Cursor, CAID, C compiler) add planners, isolation, oracles [GH-COOPERBENCH][arXiv:2512.08296][CC-TEAMS][CURSOR-SCALE][A-CC] | Refines (scale unproven for writers) | Cap concurrent writers per repo region; make most of the 10-50 read-only (review, test, research) |
| 2 | Compactors promote facts into pinned layers | Cache is exact-prefix; editing a shared layer flushes all agents (about 9 fleet-rounds of cost in A3.2 arithmetic) and, on Claude, invalidates preserved thinking [API-CACHE][API-THINK] | Contradicts in-place promotion | Epoch-versioned pinned layers; stage promotions as append-only proposals; fold at rare, pre-warmed epoch rollovers |
| 3 | Hot block at the tail | Anthropic turn-scoped system messages (clear_at) are the native cache-safe, thinking-safe primitive [API-SYS] | Supports | Per-agent delta board, version-stamped; full board via tool call |
| 4 | "Atomically trim into one-line summaries" | Masking ~ summary at half cost; summaries elongate trajectories; collapse; reacquisition costs; artifact-trail loss [arXiv:2508.21433][arXiv:2510.04618][arXiv:2608.16370][FACTORY] | Refines | Mask first; append-only structured entries with pointers; recall tool; never summarize summaries |
| 5 | Same-model compactor shares cached prefix | Claude Code and Anthropic API compaction reuse system+tools+history and append an instruction; background swap protocol exists [CC-CACHE][API-BG] | Supports | Mirror the swap protocol (record N, append-only, one in flight) |
| 6 | Per-role pinned layers (and implied per-role tools) | Tool changes invalidate everything; Manus masks instead of removing; Anthropic offers tool_removal messages [API-CACHE][MANUS][API-SYS] | Refines | Identical tools array for all roles; restrict at runtime |
| 7 | Mailman routes, agents never talk directly | Workers never chat in Cursor/CAID; chat budget up to 20% without success gain; 28-73% of messages redundant; but no evidence for LLM routers [CURSOR-SCALE][GH-COOPERBENCH][arXiv:2410.02506] | Supports no-direct-talk; LLM mailman unproven | Deterministic typed router first; LLM only for digest/dedupe |
| 8 | Task board as "what is everyone doing" | Self-reported progress unreliable; Claude Code task status lags; typed patches win [arXiv:2609.08589][CC-TEAMS][arXiv:2605.29313] | Refines | Harness-derived status; typed mutations; verifier-owned "done" |
| 9 | Event log for later fine-tune export | Convergent recipe: LLM-boundary capture, token IDs, lineage, segment splits [arXiv:2605.24220][GH-DRESSAGE][ATIF] | Supports | Add segment digests, cache usage, is_copied_context analog, infra config |
| 10 | Fine-tune a native model from these trajectories | Provider terms restrict training competing models on outputs [ANTH-TERMS]; Anthropic documents preserved thinking as guarding against distillation, with signature-bound blocks [API-THINK]; Anthropic returns no logprobs (S, UNVERIFIED for 2026) | Contradicts if teacher is Claude/GPT | Use open-weight or in-house policies; legal review before any closed-model logs are used |
| 11 | Rejection sampling by verified outcome | Weak tests, infra noise, reward hacking [arXiv:2506.09289][OAI-VERIFIED][A-INFRA][arXiv:2606.26300] | Refines | Multi-signal verification, log infra, hack detectors, fresh holdouts |
| 12 | Background maintenance agents | Sleep-time gains need predictable queries; last-writer-wins; recall-loop junk [arXiv:2504.13171][LETTA-SHARED][GH-MEM0-4573] | Refines | Single writer per artifact; extract only from primary observations; provenance |

## A1. Multi-agent coordination: what works and what fails

### A1.1 The shared-context debate, reconciled
- Cognition (2025-06): parallel sub-agents make conflicting implicit decisions; principle "share context, and share full agent traces, not just individual
  messages"; suggests a model that compresses history into key details, events and decisions, and says they fine-tuned a smaller one [COG-1] (S).
- Anthropic (2025-06): orchestrator-worker research system; subagents in clean contexts return condensed results; ~15x chat tokens; token usage alone explained 80% of
  BrowseComp variance; failures seen: 50 subagents for simple queries, endless search, subagents "distracting each other with excessive updates", duplicated work from
  vague delegation, a synchronous lead as bottleneck [A-MAS] (P).
- Cognition (2026-04) narrows its claim: multi-agent works when writes stay single-threaded and extra agents contribute intelligence rather than actions; a clean-context
  reviewer (Devin Review) finds ~2 bugs per PR, ~58% severe, and works best when coder and reviewer share no prior context [COG-2] (S).
- Kimi's context sharding: each sub-agent keeps its own notebook and returns only conclusions [KIMI] (S). LangChain: broadcasting all context maximizes coherence but
  blows the token budget; scoping isolates but loses implicit decisions [LANGCHAIN] (S).
Reconciliation: share state and decisions (board, contracts, pinned facts), not raw traces; keep raw traces retrievable on demand (Gas Town "seance" reads a
predecessor's event log [GH-GASTOWN]); give reviewers clean context; single-thread writes per region. Sleipnir's pinned layer + hot block is the right kind of sharing.

### A1.2 Evidence by scale
| System | Scale | Coordination | Reported outcome and caveats |
|---|---|---|---|
| Anthropic research system 2025 [A-MAS] (P) | lead + parallel subagents | lead plans; subagents return summaries; plan saved to memory near 200k | +90.2% vs single Opus 4 on internal eval; ~15x chat tokens; synchronous bottleneck |
| Claude Code agent teams, experimental [CC-TEAMS][CC-COST] (P) | 3-5 recommended, no hard cap | shared task list with deps; self-claim via file locking; per-agent JSON mailboxes; direct messages; hooks TeammateIdle/TaskCompleted | ~7x tokens when teammates plan; task status lags; lead sometimes implements itself; same-file edits overwrite; no nested teams |
| Anthropic C compiler 2026-02 [A-CC] (P) | 16 agents, ~2,000 sessions | no orchestrator; git lock files in current_tasks/; fresh container per session; progress files; specialist roles | ~$20k, 2B input + 140M output tokens; compiled Linux 6.9; monolithic kernel task made agents collide until a GCC oracle split work |
| Cursor autonomous coding 2026-01 [CURSOR-SCALE] (S) | hundreds of workers | flat + locks failed; optimistic concurrency not enough; planners (recursive) / workers (no peer coordination) / judge; fresh restart per cycle | browser from scratch (1M+ LOC); Solid->React migration; numbers vendor-reported |
| Gas Town 2026-01 [GH-GASTOWN] (G) | 20-30 agents | Mayor, Polecats, Refinery bisecting merge queue, Witness/Deacon watchdogs; git-backed Beads; mail, nudge, handoff | commentary reports design and planning become the bottleneck (S); community project, no benchmarks |
| CAID (CMU) [CAID] (S) | manager + engineers | dependency-aware plan; isolated git worktrees; merge + tests | +26.7 pts PaperBench, +14.3 Commit0 vs single agent; beats shared workspace |
| Kimi K2.5 Agent Swarm [KIMI] (S); README [GH-KIMI] (G) | up to 100 sub-agents, ~1,500 steps (S) | trainable orchestrator, frozen sub-agents (PARL) (S) | up to ~80% lower wall-clock (vendor claim, S); README: BrowseComp 78.4 in swarm mode vs 74.9 with context management alone vs 60.6 plain, WideSearch item-F1 79.0 (swarm adds ~3.5 pts over context management); read-heavy research |
| AgentRadio 2026-07 [GH-AGENTRADIO] (G) | 4 agents | threads and messages; wait_for_mention runs in the background so mentions surface at the next step boundary without spending a step listening | SWE-Atlas QnA (124 codebase question-answering tasks): 62.1% vs 32.3% single agent (Opus 4.6), 50.8% vs 29.0% (DeepSeek V4 Pro); background beat foreground listening 15 wins to 2 losses; answers, not merged patches |
| CooperBench 2026-01 [GH-COOPERBENCH] (G) | 2 agents, 652 tasks/12 repos | Redis messaging + optional git | ~25% success (about half of one agent); failures: expectation 42%, commitment 32%, communication 26%; up to 20% budget on chat lowers conflicts, not failures |
| Google/MIT scaling study [arXiv:2512.08296] (S) | 180 configs, 3 LLM families | single/independent/centralized/decentralized/hybrid | centralized +80.8% on parallelizable, up to -70% on sequential; errors x17.2 (independent) vs x4.4 (centralized); returns vanish above ~45% single-agent baseline |

### A1.3 Framework patterns
- OpenAI Agents SDK: a handoff transfers control and the receiver by default sees the entire prior history; input filters and opt-in nest_handoff_history compress it
  into <CONVERSATION HISTORY> summaries; structured payload via input_type/on_handoff [GH-OAI-HANDOFFS] (G). Handoff is sequential ownership, not concurrency.
- LangGraph: typed shared StateGraph with control flow in code. MetaGPT: shared message pool with publish/subscribe and structured artifacts (PRD, design, tasks) instead of free chat,
  plus executable feedback [arXiv:2308.00352] (S).
- AutoGen GroupChat: LLM-selected next speaker is brittle (recurring select_speaker failures on name formatting) [GH-AUTOGEN-ISSUES]; AutoGen and Semantic Kernel merged into Microsoft
  Agent Framework 1.0 (graph-based, 2026-04) (S). CrewAI hierarchical process: manager delegation bugs (unpopulated coworker list, schema type errors) [GH-CREWAI-ISSUES] (S).
- OpenHands SDK: DelegateTool runs sub-agents in parallel threads (max_children); TaskToolSet runs synchronous specialists; open issues on non-blocking background sub-agents and delegation bypassing tool
  restrictions [OPENHANDS-DELEG] (S). Claude Code subagents: fresh window, only the final summary returns, depth 3, 20 concurrent, optional worktree isolation, MEMORY.md memory [CC-SUB] (P).
- Lesson: the most fragile part of every framework is LLM-driven routing and delegation; deterministic control flow around LLM steps is what survived.

### A1.4 Blackboard, tuple-space, actor, CRDT and transactional ideas applied to LLM agents
- Blackboard: LbMAS (control unit picks the next agent from board content; agents talk only via the board) reached 81.68% vs CoT 77.35% vs static MAS 76.65% on reasoning and maths, not coding
  [arXiv:2507.01701] (S). PatchBoard replaces dialogue with JSON-Patch mutations validated by a deterministic kernel (schema, role write contracts, invariants): 84.6% vs LangGraph 30.8% vs Flock 61.6% on ALFWorld [arXiv:2605.29313] (S).
- Actor/tuple space: per-actor FIFO mailboxes, supervision trees and Linda-style generative communication are common proposals (for example the Turn language [arXiv:2603.08755]) but I found no coding-agent benchmark
  evidence; treat as vocabulary, not proof. Claude Code's mailbox is a per-agent JSON file validated per entry; before v2.1.207 one malformed entry blocked delivery every second [CC-TEAMS] (P): validate at the boundary.
- CRDT: CodeCRDT (observation-driven shared state) gave 100% convergence but was up to 21.1% faster on parallelizable tasks and up to 39.4% slower on tightly coupled ones, with 5-10% semantic conflicts over 600 trials; a pattern write-up concludes that parallelizing tightly coupled tasks is worse than running them serially [arXiv:2510.18893] (S), [GH-PATTERNS-CRDT] (G, third-party summary). AgentRoom (file-level claims + append-only
  broadcast log over a CRDT filesystem; MCP tools room_claim/room_release/room_state/room_broadcast/room_read) reports 0% semantic conflicts and fewer abandoned hard tasks [arXiv:2608.23740] (S).
- Concurrency control: long inference windows create stale reads and lost updates [arXiv:2608.18092]; verified anomalies are stale-generation, phantom-tool, causal-cascade, tool-effect reordering and split-view;
  snapshot isolation cost ~+8% tokens, pessimistic locking 1.6-2.3x [arXiv:2606.17182] (S). CoAgent: 2PL deadlocked 0.81x/trial with no speedup; OCC aborted 0.95x/trial at 1.83x tokens and ran slower than serial;
  uncoordinated passed only 13%; notify-and-patch with saga inverses got 1.4x speedup at 1.15x tokens [arXiv:2606.15376] (S). Version conflicts are not decision conflicts: recheck only the facts a pending action relied on [arXiv:2609.08015] (S).

### A1.5 What consistently works and fails
Works: (1) manager decomposes with explicit contracts (objective, output format, tools, boundaries) [A-MAS]; (2) workers isolated in their own workspace and context, returning condensed results [CC-SUB][KIMI];
(3) external verifiable state: feature list with pass flags only flipped after end-to-end tests, progress file, git [A-HARN][A-CC]; (4) separate skeptical evaluator, since self-evaluation is over-generous [A-HARNESS-DESIGN];
(5) partition work so agents do not hit one hotspot (GCC oracle) [A-CC]; (6) explicit effort-scaling rules in the manager prompt [A-MAS]; (7) hierarchical planners with no worker-to-worker chat [CURSOR-SCALE]; (8) near-perfect verifiers before autonomy [A-CC];
(9) tiered models: Opus lead with Sonnet subagents [A-MAS], Sonnet for teammates [CC-COST]; caches are per model, so each tier needs its own shared-prefix warm-up and a compactor must run on the model of the agent it serves [API-CACHE][CC-CACHE].
Fails: peer writers negotiating over shared files [GH-COOPERBENCH]; flat equal-status agents with locks [CURSOR-SCALE]; LLM-selected speakers and delegation; teammate status lag and the lead doing the work itself [CC-TEAMS]; debate for correctness (A5).
Harness assumptions decay: "context anxiety" that needed resets on Sonnet 4.5 largely disappeared on Opus 4.6, and Anthropic advises stress-testing every harness component [A-HARNESS-DESIGN]; keep coordination features modular and measurable.

## A2. Background memory-maintenance agents

### A2.1 What exists, what it measures, what failed
| System | Mechanism | What is measured | Known failure or caveat |
|---|---|---|---|
| Letta sleep-time agents [LETTA-SHARED][arXiv:2504.13171] | primary + sleep-time agent share labeled memory blocks (bounded by a character limit); background agent rewrites blocks | Stateful GSM-Symbolic/AIME: ~5x less test-time compute at equal accuracy; +13%/+18% accuracy by scaling sleep compute; benefit tracks query predictability; multi-query amortization | memory_insert is append-only and safe, memory_replace mostly safe, memory_rethink is last-writer-wins (lost updates) so use one owner (S); benchmarks are maths/QA, not long-horizon coding |
| Letta Context Repositories / MemFS [LETTA-CTXREPO] (S) | memory is a git checkout; memory subagents work in worktrees and reconcile with git | qualitative | semantic-conflict policy unspecified |
| Mem0 [arXiv:2504.19413] | extract, consolidate, retrieve facts from dialogue | LoCoMo: +26% relative LLM-judge vs OpenAI memory, -91% p95 latency, >90% token savings (self-reported) | LoCoMo dispute with Zep (84% -> 58.44% -> 75.14%), no third-party replication (S); audit of 10,134 entries over 32 days found 97.8% junk and one hallucinated preference asserted in 808 entries via a recall -> re-extract loop: "no mechanism to distinguish recalled memories from new conversation content during extraction"; the extraction prompt, not model size, was the bottleneck (n=1, a user report) [GH-MEM0-4573] (G) |
| LangMem [LANGMEM] (S) | hot-path ("conscious") vs background ("subconscious") formation; semantic/episodic/procedural | docs only | hot path adds latency; background needs dedup/consolidation |
| A-MEM [arXiv:2502.12110] (S) | Zettelkasten notes, LLM-chosen links, evolution of old notes | LoCoMo (9K-token dialogues): up to 6x ROUGE-L on multi-hop; 85-93% fewer tokens per operation | small conversational benchmark; rewriting old notes is a drift risk |
| Claude Code MEMORY.md, subagent memory [CC-SUB][CC-CTX] (P) | first 200 lines or 25KB loaded; re-injected from disk after compaction | none published | hard size cap replaces consolidation quality |
| Anthropic memory tool [API-MEM][A-CTXMGMT] (P) | client-side /memories file ops; "assume interruption" protocol | 100-turn search eval: context editing +29%, plus memory +39%, -84% tokens | caps, expiry, path traversal and sensitive data are the app's job |
| RL-trained managers: Memory-R1 [arXiv:2508.19828], Mem-alpha [arXiv:2509.25911], MemAgent [arXiv:2507.02259] (S) | ADD/UPDATE/DELETE/NOOP; core/episodic/semantic; fixed-size overwrite memory | QA benchmarks; MemAgent 8K context -> 3.5M tokens; Mem-alpha trained <=30k, generalizes >400k | outcome-reward RL only; no multi-writer or coding evaluation |

### A2.2 Lessons for Sleipnir's compactor and promoter
- No published evaluation covers background memory agents on long-horizon multi-agent coding; all evidence is conversational QA or maths. Treat the compactor/promoter as unproven and budget for measuring it.
- One writer per artifact; append-only fast path for everything else (Letta's concurrency table).
- Break the feedback loop: extraction may read only primary observations (tool results, user and verifier signals). Never re-extract from injected pinned layers, hot blocks, recalls or earlier summaries.
  Tag provenance on every injected segment so the compactor can exclude it [GH-MEM0-4573].
- Sleep-time gains depend on predictability. In coding, the predictable material is repo structure, conventions, test commands and recurring failure signatures: precompute a repo digest per epoch, not per-turn speculation.
- Promotion is a privileged write path: a promoted fact becomes an instruction to every agent. Memory injection reached 98.2% injection success and 76.8% attack success by query-only interaction [arXiv:2503.03704] (S);
  OWASP lists memory and context poisoning as ASI06 [OWASP-AGENTIC] (S). Require evidence pointers plus an independent verifier before promotion.
- Git-backed memory with worktrees (Letta) is attractive: diffable, mergeable, reversible. Use it for private notes and pinned-layer proposals.

## A3. Context compression research distilled into design rules

### A3.1 Landscape
| Family | Representative result | Caveat |
|---|---|---|
| Observation masking / tool-result clearing | JetBrains: masking ~halves cost vs the raw agent with equal or slightly better solve rate than LLM summary (SWE-agent, SWE-bench Verified, 5 model configs); hybrid saves 7%/11% vs masking/summary [arXiv:2508.21433][GH-JB]. Anthropic clear_tool_uses: default trigger 100k tokens, keep 3, clear_at_least, exclude_tools [API-EDIT]; +29% in a 100-turn search eval [A-CTXMGMT]. OpenCode prunes tool output beyond the last 40k tokens before summarizing [GIST-COMPACT] | invalidates cache from the clearing point; LLM summaries lengthened trajectories and can hide failure signals |
| Free-form LLM summary | Claude Code full re-summarization near 95% of window; Codex keeps ~20k tokens of recent user messages verbatim plus a handoff summary [GIST-COMPACT] | cumulative loss across compactions; mid-task auto-compact can derail |
| Structured / anchored summary | Factory probe eval on >36k production messages: structured 3.70 vs Anthropic 3.44 vs OpenAI 3.35; all weak on artifact trail (best 2.45/5) [FACTORY] (S). OpenHands StructuredSummaryCondenser: 17-field state summary via forced function call [OPENHANDS-COND] (S) | file/command state needs harness-side tracking |
| Delta-updated playbooks | ACE: itemized bullets with helpful/harmful counters, generator-reflector-curator, deterministic merge with dedup and pruning; README headline +10.6% on agent tasks and +8.6% on finance [GH-ACE] (G); AppWorld 76.2/64.3 TGC/SGC vs 67.3/46.4 without incremental updates; full rewrite collapsed 18,282 -> 122 tokens (66.7 -> 57.1) [arXiv:2510.04618] (S) | prompt self-improvement benchmarks, not transcript compaction |
| Folding / branch-return | AgentFold: context 92% smaller at turn 100, 500+ turns under 20k [arXiv:2510.24699]; Context-Folding + FoldGRPO: 62.0% BrowseComp-Plus, 58.0% SWE-Bench Verified [arXiv:2510.11967]; ReSum: +4.5% (+8.2% with RL) [arXiv:2509.13313]; MEM1: 3.5x perf, 3.7x less memory [arXiv:2506.15841] (all S) | mostly web-search style tasks; all need RL |
| Agent-initiated / learned | CAT: SWE-Compressor 57.6% Verified [arXiv:2512.22087]; ACM: lossless offload + query tools [arXiv:2607.23809]; ContextPilot: RL with counterfactual-branch partial rollouts [arXiv:2608.28476]; AdaCoM: external RL manager for a frozen agent [arXiv:2605.30785]; Cursor Composer compaction-in-the-loop RL: -50% compaction error, ~1/5 tokens, ~1k-token summaries, KV reuse [CURSOR-SUM] (all S) | needs a trainable model or RL infrastructure |
| Extractive / prompt compression | Paritok-4B (67K trajectories): 25.7% of size, 86.5% quality, 96% of identifiers copied from input [arXiv:2608.24188]; ACON guideline optimization: -26..54% peak tokens [arXiv:2510.00615]; LLMLingua claims up to 20x compression with minimal loss [GH-LLMLINGUA] (G) and degrades beyond ~20x [arXiv:2310.05736] (S) | query-aware compression changes the prefix every call and misses the cache [arXiv:2607.15516] |
| Programmatic context (RLM) | context as a REPL variable with recursive sub-calls; GPT-5-mini RLM beats GPT-5 on OOLONG [arXiv:2512.24601] (S) | moves cost into code execution; not a transcript policy |

### A3.2 Economics: every compaction is a cache event
- Cost structure (Anthropic): read 0.1x, 5-minute write 1.25x, 1-hour write 2x of base input; entries are readable only after the first response begins [API-CACHE] (P). Manus calls KV-cache hit rate the single most
  important production metric (cached vs uncached $0.30 vs $3 per MTok on Sonnet, ~100:1 input:output), and prescribes stable prefixes, no timestamps in the system prompt, deterministic JSON key order, append-only
  context, masking instead of removing tools, and a todo.md recitation at the tail [MANUS] (S) - the hot block plays the recitation role.
- Measured: cache traffic ~87% of cost; removing 38% of tool-output tokens raised paired cost 6.8% (95% CI +2.8..+11.3%); Pearson r = 0.15 between token cut and cost change; on Go tasks compression cut patch application 27/40 -> 15/40
  [arXiv:2607.12161] (S). Policies using about one third of the tokens ran 20-80% slower wall-clock on Terminal-Bench with one open-weight model [arXiv:2609.32961]; turn count, not compression rate, drives cost [arXiv:2609.22114] (S).
- Anthropic's own postmortem: an optimization meant to clear old thinking after an hour idle instead cleared it every turn, making Claude forgetful and repetitive and draining limits through cache misses, and it escaped internal testing and code review for a while [A-PM] (P). Instrument cache misses per agent.
- Illustrative arithmetic (mine, not measured; documented multipliers; output ~5x input price): 30 agents, prefix S=30k shared + R=8k role + P=4k private + T=60k transcript, hot block 1.5k. Steady step ~14.2k
  token-equivalents per agent (10.2k read + 1.5k hot + 2.5k write of new tokens).
  * Editing S with no warm-up: each agent rewrites 102k x 1.25 = 127.5k; 30 agents = 3.8M, about 9 fleet-rounds of normal steps. With S warmed once it is still ~2.7M (R+P+T rewrites).
  * Trimming one transcript 60k -> 25k: rewrite 31k plus summarizer call (~10k read + ~7.5k output-equivalent) ~ 49k; saves ~3.5k per later step, so break-even ~14 steps. Compact agents that have >~15 steps left, in large batches; otherwise finish or reset with handoff notes.
  * An agent idle over 5 minutes (long test run) loses its cache: ~127.5k to resume vs ~10k per keep-alive read; under these assumptions keep-alives or a 1-hour TTL win for gaps up to roughly 50 minutes.
  * Coalesce prefix-changing operations (transcript trim + epoch rollover) so the S/R/P rewrite is paid once.

### A3.3 What to keep verbatim, mask, or summarize
| Class | Policy | Evidence |
|---|---|---|
| Task spec, acceptance criteria, user and verifier instructions, security constraints | pinned or verbatim forever, never summarized | Claude Code's summarizer must list ALL user messages and preserve security constraints verbatim [PIEBALD] (community mirror); Codex keeps ~20k tokens of recent user messages [GIST-COMPACT] |
| Decisions and rationale, unresolved bugs, constraints | one-line verbatim entries with pointer to the origin event | Anthropic: preserve architectural decisions, unresolved bugs, implementation details [A-CTX]; Cognition [COG-1] |
| File paths, symbols, commands, error strings, test names | harness-extracted artifact table, not LLM paraphrase | artifact trail scored 2.45/5 [FACTORY]; Paritok copies 96% of identifiers [arXiv:2608.24188]; Claude Code re-reads up to 5 recent files and returns files >5k tokens as path only [CC-CTX] |
| Last K tool observations | keep verbatim | JetBrains window; clear_tool_uses keep=3 default; OpenCode protects 40k tokens [arXiv:2508.21433][API-EDIT][GIST-COMPACT] |
| Older tool outputs (file reads, logs, search) | mask with placeholder + archive pointer + hash | JetBrains; Anthropic +29% and -84% tokens [A-CTXMGMT]; Manus restorable compression [MANUS] (S) |
| Failed attempts and errors | compact ledger (what, why it failed, count) | Manus keeps errors in context (S); C compiler progress files record failed approaches [A-CC]; summaries can hide failing trajectories (JetBrains) |
| Reasoning and chatter of finished subtasks | summarize into milestone entries; raw stays in archive | AgentFold granular vs deep condensation; Context-Folding returns (S) |
| Thinking blocks on Claude with preserved thinking | append-only or drop; never edit | [API-THINK] (P) |
| Images, documents, fetched URLs | re-attach or restate; compaction cannot carry them | [API-COMPACT] (P) |

### A3.4 Rules distilled
- C1 Mask before summarize; summarize only what masking cannot shrink. Batch maskings (the clear_at_least idea) so each cache rewrite pays for itself.
- C2 Structured where the harness can verify (files, commands, tests, board ids); free-form only for rationale, unless the summarizer was trained in the loop [CURSOR-SUM].
- C3 Never ask an LLM to rewrite a summary. Anthropic's "compact again" summarizes the old summary plus newer turns [API-COMPACT], which is the summary-of-summary drift path (my inference). Summarize only the newly dropped span, keep earlier entries immutable, render the
  top-level index deterministically, merge and dedup in code (ACE curator; Factory's anchored iterative summarization (S)).
- C4 Every entry carries provenance (source event ids, hashes), an epoch, and usage counters (recalled, acted on, contradicted) that drive eviction (ACE helpful/harmful counters).
- C5 Reversible by construction: the event log is immutable; compaction appends a Condense event {range, method, summary, archive_ref, pre/post digests}; the view is a projection. OpenHands Condensation {forgotten_event_ids, summary, summary_offset}
  and ATIF context_management {type, boundary} are templates [OPENHANDS-COND][ATIF]. Provide recall(event_id|query) as a tool; results append as new observations and never re-inflate old positions; recall cost is bounded by the segment length [arXiv:2609.22114].
- C6 Respect atomic units: never split a tool call from its result, or a thinking block from its turn (OpenHands manipulation_indices) [OPENHANDS-COND].
- C7 Measure reacquisition, not just success: dropping raised retrieval calls from 21.0 to 63.9 with no significant completion change [arXiv:2608.16370]; compaction weakens the influence of recent interactions and raises blocked actions and repeat exploration [arXiv:2608.06503] (S).
- C8 Trigger early and at natural boundaries: community consensus is 85-90% of window, not 95% [GIST-COMPACT]; Anthropic advises starting background compaction while room remains for turns arriving meanwhile [API-BG]; some models do better with reset plus handoff artifact than compaction [A-HARNESS-DESIGN].
- C9 Regression-test compaction with probes generated from the raw log (recall, artifact, continuation, decision) [FACTORY]; run paired continuations at sampled compaction points [arXiv:2608.06503].
- C10 Tune per model and agent strength: strong agents want higher fidelity, weak ones more compression [arXiv:2605.30785]; equal-success policies solve different tasks [arXiv:2609.32961].
- C11 Shape tool output at the source so less needs masking later: Claude Code caps tool responses at 25,000 tokens by default, steers truncation messages toward narrower queries, and a concise response format cut a Slack example from 206 to 72 tokens [A-TOOLS] (P);
  a test harness should print ERROR plus the reason on one grep-able line and never thousands of useless bytes [A-CC].

### A3.5 Provider mechanics Sleipnir must design around (Anthropic first-hand; others (S))
| Mechanism | Fact | Consequence |
|---|---|---|
| Exact-prefix cache, tools -> system -> messages | any tool or system change invalidates all; 4 explicit breakpoints; 20-block lookback (runs of consecutive tool_use or tool_result count once); entry readable only after the first response begins; isolated per workspace; min cacheable 512-4096 tokens by model [API-CACHE] | identical tools array for every agent; warm before fan-out; breakpoints at end of shared, role, private, stable transcript; one workspace and model for the fleet |
| Claude Code layout [CC-CACHE] | system+tools -> CLAUDE.md/auto memory -> conversation; file changes and skills appended as messages; compaction request reuses system/tools/history plus an instruction; fork inherits the parent prefix; subagents, teammates and compaction sit in a 5-minute TTL bucket by default; cache scoped to machine + directory because prompts embed paths | validates a same-model compactor sharing the prefix; keep cwd and paths out of the shared segment |
| API background compaction (beta compact-2026-09-04) [API-BG][API-COMPACT] | send request with history, record N; keep appending (never edit; one compaction in flight); on arrival replace the first N messages with the block; failure means keep history; `instructions` up to 16,384 chars replace the default prompt; images, docs and URLs are lost | mirror this swap protocol in Sleipnir's compactor |
| Preserved thinking (Fable 5.1, Opus 5.5, Sonnet 5.5; enforced by default with a 400 error for accounts created on or after 2026-08-31; documented as guarding against distillation) [API-THINK][API-COMPACT-THINK] | sent-back thinking is valid only if nothing before it changed (system, tools, every earlier message); an API-written compaction swap is accepted, and a harness-written rewrite would not be (my inference); drop_block mode reports dropped blocks in input_transformations; Sonnet 5.5 blocks are account-bound | harness-written trims drop later reasoning; prefer API-native compaction or accept drops; keep pinned layers immutable per epoch |
| Mid-conversation and turn-scoped system messages (beta) [API-SYS] | appended role:system messages preserve the cache; `clear_at: next_user_message` renders until the next user message, then costs 0 tokens and keeps cache and thinking valid; tool_addition/tool_removal change tools without touching `tools`; must be re-sent verbatim | native primitive for the hot block and per-role tool restriction; log both wire form and effective render |
| OpenAI GPT-6 caching [OAI-CACHE] (S, single-source) | 30-minute window; write 1.25x, ~90% read discount; explicit breakpoints (up to 4 writes); miss diagnostics | a 4-segment layout ports; verify TTL assumptions per provider |
| OpenAI Codex agent loop [OAI-CODEX] (S) | the old prompt is an exact prefix of the new one; requests are stateless (no previous_response_id) to support zero data retention; /responses/compact returns an opaque encrypted compaction item | opaque provider compaction cannot be logged or trained on; keep a harness-side summary too. A reported Codex "stuck in compaction loop" issue (openai/codex #8481) shows compaction can re-trigger itself: cap compactions per unit of progress |

### A3.6 Concrete shapes (proposal)
Prompt layout for every agent, using the 4 breakpoints; the hot block is last and uncached.
```text
tools[]                       identical bytes for all agents and roles (restrict per role with tool_removal messages, never by editing tools)
system SHARED (epoch e)       project facts, conventions, protocol, glossary                    <- breakpoint 1 (fleet-wide entry)
system ROLE   (epoch e)       role instructions and examples                                     <- breakpoint 2 (per-role entry)
system PRIVATE                agent's structured notes, lease/scope summary, pending promotions  <- breakpoint 3 (per-agent entry)
messages TRANSCRIPT           condensed entries + masked old observations + recent verbatim turns <- breakpoint 4 (moves each turn, <=20 blocks)
system[clear_at=next_user_message] HOT   own task, blockers, direct mailbox, board delta since last seen, board version (never cached)
```
Epoch e changes only at rollover, after a pre-warm; mid-epoch promotions are appended as pending entries in PRIVATE or HOT and folded into SHARED at the next rollover.
PRIVATE is rewritten only at trim or reset boundaries (an edit there flushes that agent's transcript cache); between them, note updates are appended as transcript messages or HOT entries.
A condensed transcript entry (immutable; recall counters are the only mutable fields and live outside the prefix):
```json
{"id":"c-0042","covers":["e1201","e1388"],"kind":"milestone","epoch":7,"source":"primary",
 "text":"Lease renewal implemented; go test ./sched passes (12 tests)",
 "artifacts":[{"path":"sched/lease.go","sha256":"ab12..","op":"edit"}],
 "decisions":["monotonic clock for TTL (see e1230)"],"open":["flaky TestRenewRace"],
 "pointers":["evt:e1230","out:sha256:9f3c.."]}
```

## A4. Many agents, one repository: concurrency hazards

### A4.1 Strategies in use
| Strategy | Used by | Evidence | Failure modes |
|---|---|---|---|
| Worktree or VM per agent, branch per task, merges later | Claude Code `--worktree` and `isolation: worktree` (Claude Code itself blocks edits, working directories and git redirects that point into the main checkout, and holds a git worktree lock while the agent runs) [CC-WT] (P); Claude Squad (tmux + worktree) [GH-CLAUDESQUAD] (G); Conductor [CONDUCTOR] (S); Cursor 2.0 (up to 8 agents per prompt, worktrees or remote VMs) (S); Codex cloud (microVM per task) (S); CAID; Gas Town | isolation plus merge-time validation beat a shared workspace [arXiv:2608.18092] (S) | conflicts deferred, not removed; Claude Squad documents no conflict strategy; Cursor forum reports wrong merge conflicts with multi-agent "apply all" (S); gitignored files (.env, node_modules) absent unless copied [CC-WT] |
| Task-level lock files in git | Anthropic C compiler (16 agents; current_tasks/*.txt; pull, merge, push, unlock) | worked when tasks were partitioned; "Merge conflicts are frequent, but Claude is smart enough to figure that out" [A-CC] (P) | monolithic task: all agents fix the same bug and overwrite each other until an oracle split the work |
| Shared-file locks between equal agents | Cursor first attempt | bottleneck plus risk-averse agents [CURSOR-SCALE] (S); 2PL deadlocks 0.81 per trial [arXiv:2606.15376] (S) | lock held across LLM latency |
| Optimistic concurrency | Cursor second attempt; CoAgent OCC | removed the bottleneck but not the coordination problem (Cursor); OCC aborted 0.95 per trial at 1.83x tokens | an abort throws away inference |
| Pre-write admission with claims or intents | Claim Plane; AgentRoom; ATM; Claude Code teams (file ownership by convention) | pair pass 23.3% -> 50.0% and integration 65.6% -> 96.7%, but work largely serialized; dynamic admission blocked undeclared region-level mutations in >50% of runs [arXiv:2608.00947]; AgentRoom file claims 0% semantic conflicts [arXiv:2608.23740] (S) | needs a bounded scope-amendment path and good semantic dependency prediction |
| Merge queue with verification | Gas Town Refinery (bisecting, Bors-style; polecats never push to main) [GH-GASTOWN] (G); Mergify [MERGIFY] (S) | "writing code got parallel, landing code stayed serial" | queue throughput; flaky tests |
| Character-level CRDT | CodeCRDT | 5-10% semantic conflicts; up to 21.1% faster on some tasks, up to 39.4% slower on others [arXiv:2510.18893] (S) | semantic incompatibility is invisible to the CRDT |

### A4.2 Observed conflict rates (public GitHub agent PRs: baselines, not swarm measurements)
- 79.4% of agent PRs overlap in time with other PRs; textual conflicts in 19.8% of same-agent and 41.7% of cross-agent overlapping scenarios; 84.4% of conflicted files are source; 42% structural, 58% content [arXiv:2607.04697] (S).
- AgenticFlict: 142,652 agent PRs across 59,412 repos and 5 agents (107,026 merge-simulated), 27.67% textual conflict rate versus a typical 10-20% for human PRs; semantic conflicts are not detected and survivorship bias applies [GH-AGENTICFLICT] (G).
  Per-agent rates of 15.43% (Copilot) to 32.31% (Codex) come from the paper summary [arXiv:2604.03551] (S). Humans author 96.1% of conflicting merge commits; agent self-resolution differs ~60x across vendors [SBES-2026] (S).
- Agent PR merge rates range 43-83% by vendor across two AIDev-based studies; in the study with a human baseline (75.6% within 30 days) every agent was lower (43.2-67.2%) [AIDEV] (S).
- Caveat: mostly uncoordinated agents on human-run repos. An orchestrated swarm with declared scopes should sit lower on textual conflicts, but semantic conflicts (5-10% in CodeCRDT) need tests to surface.

### A4.3 Recommended hybrid for Sleipnir
1. Writers get a worktree and branch each (record base sha). Read-only agents (reviewers, researchers, test runners) share a read-only snapshot and are unlimited.
2. The manager creates tasks with a declared scope (paths, dirs, symbols), acceptance tests and a size estimate. The harness grants a lease on the scope (exclusive for writers, shared for readers) with TTL and heartbeat; lease state lives in the harness, never in model output.
3. Scope amendment is a first-class request answered deterministically (free: grant; held: queue or split the task). This avoids Claim Plane's failure of blocking undeclared writes.
4. Integration is one serial-ish merge queue with bisecting on failure: rebase, run the verifier on the merged stack, land or reject. On conflict it creates a rebase task on the board owned by the later branch's agent, with the conflicting diff in that agent's hot block.
5. Partition to avoid hotspots: if many tasks touch one subsystem, serialize them or first create an interface or oracle task [A-CC].
6. Admission control: cap concurrent writers per lease region, scale readers freely, track the semantic-conflict rate and lower the cap when it rises.
7. Validate stale writes: every action derived from the board or mailbox carries the version it was based on; the harness rejects or notifies on mismatch (stale-generation) rather than aborting whole generations [arXiv:2606.17182][arXiv:2606.15376][arXiv:2609.08015].
8. Keep worktree paths and cwd out of the shared cached segment, since Claude Code caches are scoped to directory because prompts embed paths [CC-CACHE].

### A4.4 Hazards to plan for
- Semantic conflicts invisible to git: run CI on the merged stack; flaky tests turn bisecting into noise, so record per-test flake rates.
- Long tool runs versus lease TTL: heartbeats must come from the harness process, not the model. Agents show "time blindness" and will spend hours running tests [A-CC].
- Base drift: rebasing costs LLM tokens; prefer small tasks and fresh bases (Claude Code `worktree.baseRef` fresh vs head) [CC-WT].
- Shared .git and filter drivers: Claude Code skips repo-defined filter drivers (LFS pointers appear) for security [CC-WT]; plan LFS pulls per worktree.

### A4.5 Typed board, lease and mailbox shapes (proposal)
Hot block for one agent, rendered per turn and tiny; the whole-board pull is a tool call:
```text
HOT v=1842 agent=backend-3 epoch=7
MY TASK  T-88 "lease renewal" in_progress  lease=sched/** ttl=240s  base=9f2c1a
BLOCKERS T-85 review by reviewer-1: changes requested (2 findings)
MAIL(2)  m-501 manager: rename LeaseRequest.TTL -> Duration before merge | m-502 digest: 3 agents touched sched/*.go since v1830
DELTA    v1830->v1842: T-91 frontend-2 claimed web/**; T-77 landed (merge queue) at 9f2d40
```
Mutations are typed and pass a deterministic kernel that checks schema, the role's write contract and the version the agent saw (PatchBoard-style [arXiv:2605.29313]):
```json
{"op":"claim","task":"T-91","agent":"frontend-2","base_version":1841,"scope":["web/**"],"evidence":["evt:e5510"]}
{"op":"message","from":"backend-3","to":"role:reviewer","topic":"T-88","kind":"request_review","refs":["git:9f2c1a..HEAD"],"base_version":1842}
```
Status fields (in_progress, blocked, done) are derived from git, CI, lease heartbeats and verifier results; the model may propose but not set them [arXiv:2609.08589].
At 50 agents a full board of ~30 tokens per agent is ~1.5k uncached tokens per agent per turn (~75k per fleet round), which is why the hot block carries a personal delta, not the whole board.

## A5. Swarm failure modes and mitigations

| Failure | Evidence | Mitigations that worked or are supported |
|---|---|---|
| Duplicated work, vague delegation | Duplicates from vague task descriptions [A-MAS]; agents hitting the same bug and overwriting [A-CC] | task contracts (objective, output format, tools, boundaries); leases and board claims; manager-side dedupe |
| Over-spawning, spurious parallelism, serial collapse | 50 subagents for simple queries [A-MAS]; PARL rewards parallelism early (annealed), rewards finished subtasks to stop spurious parallelism, scores Critical Steps [KIMI][GH-PARL] | effort-scaling rules; spawn budgets and justification; track critical path, not total steps |
| Message storms, idle chatter | "Excessive updates" [A-MAS]; up to 20% of budget on chat with no success gain [GH-COOPERBENCH]; 28.1-72.8% of tokens saved by pruning redundant messages [arXiv:2410.02506]; pub/sub filtering [arXiv:2308.00352] | typed messages, delivery batched at turn boundaries, per-agent inbox caps, digests, dedupe |
| Deadlock, stalls, zombies | Teammate status lag blocks dependents [CC-TEAMS]; 2PL deadlocks [arXiv:2606.15376]; Gas Town states (GUPP violation, stalled, zombie) and three watchdog tiers [GH-GASTOWN] | leases with TTL and heartbeat; dependency DAG only; tiered watchdogs (nudge, then handoff); harness-derived status |
| Runaway spend, infinite loops | 68 confirmed infinite agentic loops in 47 of 6,549 agent repos [arXiv:2607.01641] (S); a "$47k two-agent loop" story is anecdotal (UNVERIFIED) | budgets per agent and per fleet (tokens, dollars, wall-clock, tool calls); hop limits; circuit breakers; cap compactions per unit of progress (see A3.5, Codex); scheduler concurrency cap (Gas Town max_polecats) |
| Groupthink, sycophancy, agreement loops | Debate flips correct answers to incorrect more than the reverse [arXiv:2509.05396]; sycophancy propagates and peer sycophancy priors add +10.5% [arXiv:2604.02668] (S) | verify by execution, not discussion; clean-context independent reviewers; a peer's claim is not evidence; separate generator and skeptical evaluator [A-HARNESS-DESIGN] |
| Prompt-injection propagation | Self-replicating Prompt Infection: replication made attacks 209% more effective in global messaging (GPT-3.5 Turbo); tagging alone insufficient, tagging plus defenses effective [arXiv:2410.07283] (S); OWASP ASI06/07/08 [OWASP-AGENTIC] (S) | Claude Code tells the receiver a message came from another session, forbids relaying approvals, and in auto mode its classifier reviews every inter-agent message [CC-TEAMS] (P); probe on tool outputs plus an action classifier that sees only user messages and bare tool calls (0.4% FPR on 10,000 real calls, 17% FNR on 52 real overeager actions) [A-AUTO] (P); provenance tags; least privilege per role |
| Unreliable self-reports, premature "done" | Progress reports lose accuracy mid-task [arXiv:2609.08589] (S); MAST: verification and termination failures are 21.3% (premature end 6.2%, incomplete 8.2%, incorrect 9.1%) [arXiv:2503.13657] (S); premature victory [A-HARN] (P) | pass flags flipped only by the verifier; independent evaluator; status from git, CI and leases |
| Error amplification | x17.2 independent vs x4.4 centralized [arXiv:2512.08296] (S) | manager validates before relaying; no unverified peer-to-peer facts |
| Specification and role ambiguity (largest MAST bucket) | 41.8% specification/design, 36.9% inter-agent misalignment [arXiv:2503.13657] (S) | explicit role contracts, termination conditions and typed protocols |
| Cache thrash and cost blowups | teams use ~7x tokens [CC-COST]; the thinking-clear bug [A-PM] | per-agent cache-hit telemetry and alerts (Claude Code's `Prompt cache` line counts misses over 5% and 2,000 tokens) [CC-CACHE] |
| Eval integrity and reward hacking in swarms | contamination 0.87% multi-agent vs 0.24% single (3.7x); one eval-aware attempt used 40.5M tokens (38x median) [A-EVALAWARE] (P); tampering and special-casing exploits [arXiv:2605.02964] (S) | egress control, block benchmark strings, canaries, hack detectors (test edits, stubbing, hardcoding) |
| Corrupted coordination state | one malformed mailbox entry blocked delivery every second [CC-TEAMS] (P) | validate and quarantine at the boundary; schema versions; dead-letter queue |

### A5.1 Swarm health metrics to log and alert on
Critical-path steps (main steps plus the longest sub-agent per stage) rather than total steps [KIMI]; cost per merged task; cache hit rate and miss reasons per agent [CC-CACHE]; conflicts and semantic-conflict rate per merged PR;
duplicate-work rate (overlapping diffs or repeated tool calls across agents); share of tokens spent on messages (CooperBench saw up to 20%) [GH-COOPERBENCH]; stale-write rejections; recalls and re-reads after compaction;
lease wait time and expiry rate; idle time lost to cache TTL expiry; verifier flake rate; budget stops.

## B1. Existing datasets, formats and training pipelines

### B1.1 Datasets and interchange formats
| Dataset / format | Size and content | Record format | Licence | Notes |
|---|---|---|---|---|
| SWE-smith [GH-SWESMITH] (G) | 52K task instances; 26K SWE-agent trajectories (5K trained SWE-agent-LM-32B, 40.2% pass@1 Verified); 250+ Docker envs | SWE-agent message trajectories | MIT | single agent, Python |
| SWE-Gym [GH-SWEGYM] (G) | 2.4K tasks in 11 Python repos (+ Lite 234); fewer than 500 trajectories from GPT-4o and Claude 3.5 Sonnet; verifier data; +14 pts for a 32B model | agent trajectories, prebuilt Docker images (scaffold details not confirmed) | Apache-2.0 | small; verifiers trained on trajectories |
| R2E-Gym [GH-R2E] (G) | 8.1K problems, 13 repos; SWE-GEN builds tasks from commits; three SFT sets (editor, execution-based tester, execution-free verifier) from Claude 3.5 Sonnet, T=0.2, 40 steps; 34.4% -> 51% with hybrid test-time scaling | trajectories, LLaMA-Factory configs | Apache-2.0 | hybrid verifiers |
| SWE-rebench + OpenHands trajectories [NEBIUS] (S) | 21,000+ tasks, 3,400+ repos; 67,074 trajectories (Qwen3-Coder-480B-A35B, OpenHands 0.54.0, 1,823 repos), kept only if the patch applies | messages | per dataset card | fresh, decontaminated tasks |
| Nebius SWE-agent-trajectories [NEBIUS] (S) | 80,036 trajectories | instance_id, model_name, target (bool), trajectory (system/assistant/user/tool), exit_status, generated_patch, eval_logs | CC-BY-4.0 | includes labeled failures |
| ADP [GH-ADP] (G) | ~20+ datasets unified (swe-smith, swe-gym, openhands, codeactinstruct, mind2web, agenttuning, toucan ...) | ATIF-style Trajectory {id, content[]}: API/code/message actions, text/web observations; std_to_sft converters for OpenHands v0, SWE-agent, AgentLab | MIT (dataset licences vary) | interchange hub |
| Harbor ATIF v1.8 [ATIF] (G) | spec | steps with tool_calls, observation, metrics {tokens, cached_tokens, cost_usd, prompt_token_ids, completion_token_ids, logprobs}, subagent_trajectories, is_copied_context, context_management, continued_trajectory_ref | open | closest to Sleipnir's needs |
| OpenAI SFT (S) | n/a | JSONL {"messages":[...], "tools":[...], "parallel_tool_calls":false}; assistant "weight" 0 or 1 skips a turn | n/a | OpenAI RFT adds graders and tool calls to your endpoints (docs unreadable here) |

### B1.2 Training-framework inputs
| Framework | Unit of data | Notes |
|---|---|---|
| TRL SFTTrainer [GH-TRL] (G) | `messages` or prompt/completion, plus a `tools` column; `assistant_only_loss=True` needs chat templates with {% generation %} tags (TRL patches known families such as Qwen3); `completion_only_loss`; packing | prompt/completion per step avoids template patching |
| verl multi-turn [GH-VERL] (G) | delta tokenization (template of messages[:i] vs [:i+1], tokenize the difference); loss_mask 1 only for new assistant tokens; tokenization sanity check strict / ignore_strippable / disable | templates that strip reasoning break delta tokenization; verl falls back to a fixed base conversation |
| ART [GH-ART] (G) | Trajectory {messages_and_choices (choices carry logprobs), tools, reward, metrics} in TrajectoryGroups; RULER relative LLM-judge rewards; GRPO with vLLM + Unsloth LoRA | needs an endpoint that returns logprobs |
| SkyRL [GH-SKYRL] (G) | skyrl-train, skyrl-agent, skyrl-gym, skyrl-tx (Tinker API); fully async RL with in-flight weight updates; Harbor integration | |
| rLLM [GH-RLLM] (G) | gateway captures token IDs + logprobs per URL-routed session -> Episode (task) > Trajectory (agent run) > Step (LLM call); EvalOutput {reward, is_correct, Signals}; backends verl, tinker, fireworks | wraps LangGraph, OpenAI Agents SDK, openai.OpenAI code |
| Agent Lightning [GH-AGL] (G) | trainer + API gateway proxy + rollout controller; agents unchanged; ~3,500 lines; v1.0 paper lists retokenization, sample merging, advantage calculation, loss normalization as the hard parts [arXiv:2608.17528] (S) | 41.8% -> 56.4% on SWE-bench Verified with 6K examples (S) |
| Polar / ProRL-Agent-Server [arXiv:2605.24220] (S) | black-box harness: proxy LLM API calls, record token-level interactions, reconstruct token-faithful trajectories | Qwen3.5-4B gains +22.6 (Codex harness), +4.8 (Claude Code), +0.6 (Qwen Code), +6.2 (Pi) |
| Dressage [GH-DRESSAGE] (G) | per-token token_id, logprob, loss_mask, token_version, token_expert; lineage-aware TITO for main-agent and subagent calls; compaction or schema-change segments each become a sample with the anchor segment's terminal advantage broadcast to siblings; prompt-equal denominators | closest to Sleipnir's multi-segment case |
| Tinker (S) | Datum {model_input, loss_fn_inputs {target_tokens, weights}} | token-level weights |

### B1.3 Gaps against Sleipnir's needs
- No public dataset records multi-agent lineage, shared or pinned segments, cache usage, mailbox or board state, or compaction events. The SWE-focused sets are single-agent, Python-only and at most ~80K trajectories; ADP aggregates larger general agent corpora, also single-agent.
- ATIF is the closest schema (subagent refs, context_management, is_copied_context) but has no segment digests, hot-block or board concept: log natively and write an ATIF exporter plus ADP converters.
- Public pipelines keep resolved-only or patch-applies trajectories; SWE-Gym and R2E-Gym also train verifiers on failures, so keep failed trajectories with labels for verifier and preference training.
- The harness dominates results (evaluation harness moved solve rate 4.3x vs 1.16x for the training recipe; cross-harness credit gave no more portability) [arXiv:2609.04518] (S); Cognition trained SWE-1.5 inside its own harness [COG-SWE15] (S). Train on the harness you deploy.

## B2. What the harness must log

### B2.1 Principles
1. The append-only event log is the source of truth; rendered prompts are derived views, yet every LLM call records exactly what was sent. Anthropic's Managed Agents keeps a durable session log outside the context window with context engineering in the harness [A-MANAGED] (P).
2. Capture at the LLM boundary through one gateway used by all agents (rLLM, Agent Lightning, Polar, Dressage), so no code path bypasses logging.
3. Deterministic rendering: stable key order, one serializer, a hash of the wire bytes, and a replay test that re-renders from the log and compares hashes [MANUS].
4. Record what the model saw, not only what was sent: provider-ephemeral constructs change the effective prompt (clear_at messages render nothing after the next user message [API-SYS]; compaction blocks are opaque; server-side context editing edits before the model sees it [API-EDIT]).

### B2.2 Event families
llm_call; tool_call/tool_result (command, cwd, exit code, duration, truncation flag, full-output archive pointer and hash); message (sent, routed, delivered, read, consumed); board_patch (typed mutation, base version, accepted or rejected with reason);
lease (grant, heartbeat, expire, release, amend); git (commit, rebase, merge, conflict, revert); verify (test, lint, build, typecheck with verifier version, infra config, flake flag);
compaction (range, method, input digest, summary, archive ref, tokens before/after, cache cost); promotion (fact, evidence pointers, verifier verdict, epoch); recall (query, results, cost);
human (approve, reject, edit diff, latency); budget, watchdog and security (probe hits, classifier verdicts, denials); infra (OOM, timeout, resource limits).

### B2.3 Per-call record (Go sketch)
```go
type LLMCall struct {
    CallID, AgentID, Role, ParentCallID string // lineage: spawn, continuation, compactor-of
    Epoch      uint32           // pinned-layer epoch the prompt was rendered under
    Provider, Model, Snapshot string
    Params     map[string]any   // temperature, top_p, max_tokens, thinking cfg, tool_choice, betas, effort
    Segments   []SegmentRef     // ordered: {Kind: shared|role|private|transcript|hot, SHA256, Bytes, Tokens, Breakpoint bool, TTL}
    PromptSHA  string           // hash of exact wire bytes
    Effective  *EffectiveRender // what the model saw after clear_at / server-side edits, if it differs from wire
    Response   []RawBlock       // content blocks verbatim, including thinking blocks and their signatures
    StopReason string
    Usage      Usage            // input, output, cache_read, cache_creation, reasoning tokens; usd; ttl bucket
    Tokens     *TokenTrace      // prompt_delta_ids, completion_ids, logprobs; nil when the API cannot return them
    LatencyMS  int64; Retries int; Err *string
    Outcomes   []OutcomeRef     // filled later by verifiers, humans, CI
}
```

### B2.4 Fidelity checklist
1. Segments are content-addressed blobs with role, boundaries and cache breakpoints; per-step prompts are lists of segment refs (avoids the O(n^2) copy of resent context).
2. Thinking blocks stored verbatim with signatures, separately from message history; never mutate blocks that will be re-sent [API-THINK]. Redacted thinking blocks are logged as opaque.
3. Token IDs and logprobs where the endpoint supports them (self-hosted vLLM/SGLang; OpenAI logprobs); Anthropic's Messages API reportedly returns none (S, UNVERIFIED for 2026). Without token IDs, retokenize with the delta method and run a consistency check [GH-VERL].
4. Model snapshot, parameters, beta headers, effort and thinking config on every call (they change cache keys and behavior [API-CACHE]).
5. Cache accounting per call (cache_read, cache_creation, TTL bucket) and a per-agent miss reason, to attribute cost and detect regressions [CC-CACHE][A-PM].
6. Environment snapshot per task: repo sha, base branch, worktree, container image, resource limits (guaranteed vs hard kill), network policy, tool versions [A-INFRA].
7. Compaction and promotion events store input digest, output text, and the exact replaced range so any prompt is reproducible; mark copied context (ATIF is_copied_context) and context_management {type, boundary} [ATIF].
8. Timestamps, seeds, and retry, refusal and truncation outcomes; a compaction call that ends in refusal or max_tokens is still billed and reported in usage.iterations [API-COMPACT].
9. Board and mailbox state hashes at each call (which version the agent saw), for stale-write analysis.
10. Redaction at write time (B3.6), with per-repo licence and tenant-consent fields.
11. Renderer version and delimiter set on every call, so a later export can reproduce or migrate the exact layout the model was deployed with.

### B2.5 Outcome signals
- Per step: test/lint/build/typecheck deltas after each edit, verifier version, flake flag. Per task: verifier verdict, PR merged or reverted within N days, reviewer findings, CI on main.
- Human: approvals, rejections, edits (diff between agent output and merged code), time to approve. Operational: retries, stale-write rejections, lease conflicts, recalls and re-reads after compaction, budget stops, duplicated work.
- Repeat sample tasks to estimate pass^k (all k trials succeed), not just pass@k; grade both transcript and outcome; isolate trials from clean environments [A-EVALS].

## B3. Export recipes

### B3.1 Sample construction
1. One sample per (agent, LLM call). Prompt = the effective render the model saw; completion = its output (text, tool calls, notes edits). Loss only on completion tokens; mask pinned layers, hot block, tool outputs and copied context.
2. Steps with is_copied_context true and agent steps with llm_call_count 0 are excluded from SFT [ATIF].
3. Role tag per sample (worker, reviewer, compactor, mailman) so one model learns all roles; compactor samples carry the whole served-agent prefix as context.
4. Keep failed trajectories with labels for verifier, preference and negative-sample use.

### B3.2 Target mappings
| Target | Sample unit | Fields from the Sleipnir log |
|---|---|---|
| OpenAI SFT JSONL | per trajectory or step | messages (effective render), tools, parallel_tool_calls, weight 1 only on trained turns; only if terms allow the teacher |
| TRL SFT | prompt/completion per step (preferred) or messages + tools | assistant_only_loss needs generation-tagged templates; prompt/completion avoids patching |
| TRL DPO | prompt, chosen, rejected | pairs from replays at the same state (B4) |
| verl / OpenRLHF-style | prompt_ids, response_ids, response_mask, old_logprobs, reward, group id | token trace; else delta tokenization plus sanity check; never full retokenization [GH-VERL] |
| SkyRL / Tinker | Datum {model_input, target_tokens, weights} | token trace |
| ART | Trajectory {messages_and_choices with logprobs, tools, reward} in a TrajectoryGroup | requires logprobs from an open endpoint |
| rLLM / Agent Lightning | Episode (task run) > Trajectory (one agent) > Step (LLM call) | LLMCall maps to Step; reward Signals from verifiers |
| ATIF / ADP | steps with tool_calls, observation, metrics | interchange and sharing; converters to OpenHands or SWE-agent formats |

### B3.3 SFT and preference recipes
- SFT: filter by multi-signal verified outcome, dedup, cap per task, weight recent and harder tasks; train per-step samples with completion-only loss; keep tool-call arguments exact; include compactor and mailman samples as separate roles.
- Preference: (a) two rollouts from the same state with different verified outcomes; (b) compactor pairs from paired continuations (B4); (c) mailman pairs from with/without-message replays (B4); (d) rejection-sampled best-of-n vs worst-of-n on the same task.
- Robustness data (proposal): inject synthetic prompt injections into tool results, mailbox messages and pending promotions and label the correct behavior (treat as data, do not comply, flag it), so the model learns the pattern while the deterministic defenses stay in place [arXiv:2410.07283][A-AUTO].

### B3.4 RL and credit assignment
- Terminal reward broadcast (GRPO) across all tokens of a chain, including summaries, so compaction chains train end to end [CURSOR-SUM][GH-DRESSAGE].
- Step credit from anchor states: group steps that start from the same (task, repo tree hash, failing-test set) across rollouts, as in GiGPO's anchor-state grouping (>12% ALFWorld, >9% WebShop over GRPO) [arXiv:2505.10978] (S).
- Dense verifier deltas: change in failing tests, build and lint state after an edit; penalties for budget overrun, stale-write rejections, lease violations, re-reads after compaction, duplicated work.
- Multi-agent: normalize advantages per agent (a global GRPO baseline destabilizes gradients across heterogeneous agents [arXiv:2602.08847] (S)); combine team reward with leave-one-out counterfactual replays (message-level counterfactual credit is still sparse in the literature [arXiv:2605.02801] (S));
  freeze sub-agents when training only the manager (PARL) [KIMI].
- Group boundary: state whether GRPO groups are within task-harness pairs [arXiv:2609.04518]; exclude infra-error episodes; make rewards hack-resistant by combining executable tests, quality filters, behavior monitoring and agentic evaluators [arXiv:2606.26300] (S).

### B3.5 Multi-agent trajectories and prefix sharing
- Represent an episode as a DAG: nodes are LLM calls; edges are same-agent continuation, spawn, message delivery, lease and compaction; export one trajectory per agent per epoch (ATIF subagent_trajectories and refs) [ATIF][arXiv:2605.02801].
- All agents share the pinned shared layer byte-for-byte, so the shared prefix is huge. Dedup by segment digest in storage; in training use tree packing (each shared prefix computed once, up to 6.2x speedup on agentic rollouts) [arXiv:2511.00413] (S) or at least shared-prefix batching; mask shared tokens from loss.
- Lineage-aware reconstruction records which context a delegated call inherited [GH-DRESSAGE].

### B3.6 Data-quality and governance filters
| Filter | Recipe | Evidence |
|---|---|---|
| Verified outcome | multi-signal: unit and held-out or augmented tests, build, lint, static analysis, independent reviewer agent, human merge, no revert within N days; keep failures as negatives | 15.7% of Verified-passing patches wrong, 28.4% on Lite [arXiv:2506.09289] (S); 7.8% of plausible patches fail developer tests [SWEBENCH-SOLVED] (S); OpenAI audit found 59.4% of 138 audited Verified tasks had flawed tests and stopped reporting it [OAI-VERIFIED] (S) |
| Infra noise | log resource limits; drop infra-error episodes; sample pass^k; distrust gaps under ~3 pts | 6-pt gap between resource setups on Terminal-Bench 2.0; SWE-bench only +1.54 pts at 5x RAM [A-INFRA] (P) |
| Reward hacking | detect test edits, stubbing, hardcoding, fetching solutions, eval awareness; drop or label | one benchmark summary reports exploit rates of 4.6-13.9% across several frontier models [arXiv:2605.02964] (S, attribution within the summary uncertain); Opus 4.6 decrypted an answer key [A-EVALAWARE] (P) |
| Dedup | exact by prompt or segment hash; near-duplicate by MinHash over thought+action token n-grams with LSH banding (~0.85 threshold is a common default); cap samples per task | general practice (S); generic sources, no agent-specific study read |
| Secrets and PII | scan tool outputs and prompts before persistence; detect-secrets with default plugins + regexes + NER (BigCode's StarPII was trained on 12,000 files with 22,950 annotated entities); block `.env` reads; irreversible replacement tokens | [BIGCODE-PII] (S); worktree `.worktreeinclude` copies secrets into worktrees on purpose [CC-WT] |
| Licence and provenance | store repo URL, commit sha and SPDX licence per task; The Stack v2 used GHArchive repo licences, ScanCode file-level detection for 96.93% of repos lacking metadata, an opt-out process and per-datapoint provenance; restrict redistributable sets to permissive licences | [BIGCODE-PII][arXiv:2501.02628] (S) |
| Provider terms | check the teacher's terms before exporting: Anthropic does not allow Outputs to train models competitive with Anthropic's own, and lists general-purpose and open-ended generation models and "training targets" as prohibited [ANTH-TERMS] (P); OpenAI's terms are similar (S) | get legal review |
| Contamination | split by commit date; hold out repos; never train on eval tasks; use fresh SWE-rebench-style tasks; block benchmark strings and solution leaks (future refs, upstream fetch) | multi-agent contamination 3.7x [A-EVALAWARE] (P); SWE-rebench [NEBIUS] (S) |

## B4. Training the compactor and mailman roles

### B4.1 Compactor
Evidence that it is trainable: Cursor's compaction-in-the-loop RL chains generations through ~1k-token summaries with one terminal reward applied to every token, cutting compaction error 50% at ~1/5 tokens with KV reuse and a very short summary prompt [CURSOR-SUM] (S);
FoldGRPO, ReSum-GRPO, MEM1 and MemAgent train folding or memory under outcome reward [arXiv:2510.11967][arXiv:2509.13313][arXiv:2506.15841][arXiv:2507.02259] (S); AdaCoM trains an external manager for a frozen agent [arXiv:2605.30785] (S);
TRACE scores individual compaction events with paired closed-loop continuations from the same environment state and optimizes the compression prompt from summary preferences [arXiv:2608.06503] (S); Paritok distills a 4B extractive compressor from 67K trajectories [arXiv:2608.24188] (S).
Staged plan:
1. Instrument (Stage 0): every compaction event logs input digest, output, tokens before/after, cache cost, and, for the next K steps, recalls, re-reads, repeated exploration, blocked actions, and downstream success. Build a probe suite from the raw log (recall, artifact, continuation, decision) [FACTORY].
2. Distill (Stage 1): teacher summaries under a strict schema (verifiable fields harness-generated; rationale free-form), filtered so every identifier appears in the raw log (extractive check).
3. Preference pairs (Stage 2): at sampled compaction points fork the environment (worktree at the commit, container image, seeds, event log); generate K candidate compactions; run paired continuations; prefer higher success, fewer steps, fewer re-reads and fewer tokens [arXiv:2608.06503].
   Add oracle-restoration labels: restore dropped items one at a time and see which removes reacquisition, which yields "should have kept" targets [arXiv:2608.16370]. Use cheap proxies (re-read counts within N steps) for bulk data.
4. RL in the loop (Stage 3): treat summarization as part of the agent's chain with a single terminal reward, plus terms for tokens kept, recall calls, cache-miss cost, probe recall, and a schema and extractive-validity constraint. Because the compactor is the same model as the served agent, self-summarization reuses the KV prefix.
Pitfalls: brevity bias and collapse (ACE) call for a structured schema and KL to the teacher; reward hacking through leaking plans into notes or over-long summaries; train on multi-compaction chains so the compactor sees its own earlier summaries (drift);
fidelity-versus-compression differs by agent strength [arXiv:2605.30785]; policies transfer poorly across models [arXiv:2609.32961]. Always compare against "no compaction" and "mask only" baselines on cost-adjusted success.

### B4.2 Mailman
No source I found trains an LLM router between coding agents; the closest evidence is communication-efficiency training (Optima: generate-rank-select-train with reward = task performance + token count + readability; up to 2.8x gain with <10% of tokens on information-exchange tasks [arXiv:2410.08115] (S)),
pruning (AgentPrune, 28.1-72.8% fewer tokens [arXiv:2410.02506] (S)), and orchestration-trace RL where message-level counterfactual credit is sparse [arXiv:2605.02801] (S).
Plan:
1. Start with a deterministic, typed router (topic, recipient by lease or dependency, priority, batching at turn boundaries) that logs every candidate route and the chosen one, with some randomized low-stakes exploration for off-policy learning (record propensities).
2. Labels: consumption (the recipient quotes, cites or acts on the content within N steps), noise (ignored, or followed by a clarification or duplicate work), conflict avoided (counterfactual replay where the message is withheld shows a conflict or duplicate), time to first use, and downstream team outcome.
3. Preference pairs from replays at the same state: deliver as is vs digest vs withhold vs different recipients; reward = recipient outcome, minus tokens, minus a staleness penalty (Optima-style).
4. Keep security deterministic: provenance tags, injection probes, rate limits and permission checks are never learned or bypassed by the router [A-AUTO][arXiv:2410.07283].

## Design rules for Sleipnir (the 15 most important, each tied to evidence)

1. **Serialize writes per region; parallelize reads.** Cap concurrent writers per repo region and make reviewers, researchers and test runners the bulk of a 10-50 agent fleet.
   Evidence: [GH-COOPERBENCH][arXiv:2512.08296][COG-2][CURSOR-SCALE][CAID]; Claude Code advises 3-5 teammates [CC-TEAMS]. Test: sweep K = 2, 4, 8, 16 writers on a fixed task set.
2. **Isolate writers, lease scopes, land through a verifying queue.** Worktree per writer; harness-owned leases on declared scopes with TTL, heartbeat and an amendment path; bisecting merge queue that verifies the merged stack.
   Evidence: [arXiv:2608.18092][GH-GASTOWN][arXiv:2608.00947][A-CC][CC-WT]; overlapping agent PRs conflict 19.8-41.7% [arXiv:2607.04697].
3. **The shared prefix is immutable within an epoch; every edit is a fleet-wide cache flush.** Identical tools array, one workspace and model, pre-warm before fan-out, coalesce prefix-changing operations, roll epochs rarely.
   Evidence: [API-CACHE][CC-CACHE][arXiv:2607.12161]; arithmetic in A3.2 (about 9 fleet-rounds per unwarmed flush).
4. **The hot block is a per-agent, version-stamped delta delivered through an ephemeral, cache-safe channel.** Use a turn-scoped system message (or an appended message elsewhere); never rewrite earlier bytes; reject writes based on stale versions; pull the full board by tool.
   Evidence: [API-SYS][arXiv:2606.17182][arXiv:2609.08015]; recitation at the tail [MANUS].
5. **Mask before summarizing, and batch prunes so each cache rewrite pays for itself.** Break-even was ~14 steps in the illustration; token savings alone do not lower cost.
   Evidence: [arXiv:2508.21433][API-EDIT][GIST-COMPACT][arXiv:2607.12161][arXiv:2609.32961].
6. **Compaction output is append-only structured entries with provenance and pointers.** Harness-generated for verifiable state (files, commands, tests), LLM only for rationale; never LLM-rewrite a summary; merge and dedup in code.
   Evidence: [arXiv:2510.04618][FACTORY][OPENHANDS-COND][API-COMPACT].
7. **Compaction is reversible and measured.** Immutable log, Condense events, a recall tool, a verbatim keep-list (instructions, security constraints, acceptance criteria), and recalls or re-reads as the quality metric.
   Evidence: [MANUS][arXiv:2609.22114][arXiv:2608.16370][PIEBALD][ATIF].
8. **Promotion into pinned layers is a privileged, verified, single-owner write.** Evidence pointers, an independent verifier, usage counters, TTL, extraction only from primary observations.
   Evidence: [GH-MEM0-4573][arXiv:2503.03704][LETTA-SHARED][arXiv:2510.04618].
9. **Board status comes from the harness; "done" comes from a verifier.** Model reports are advisory; mutations are typed patches validated by a deterministic kernel.
   Evidence: [arXiv:2605.29313][arXiv:2609.08589][CC-TEAMS][A-HARN][arXiv:2503.13657].
10. **Mailman: deterministic typed routing first.** Batch delivery at turn boundaries, rate limits, provenance, untrusted-by-default handling; an LLM mailman only for digest and dedupe; reviewers get clean context.
    Evidence: [CURSOR-SCALE][arXiv:2410.02506][GH-COOPERBENCH][COG-2][arXiv:2410.07283][CC-TEAMS].
11. **Budgets and watchdogs are first-class.** Per-agent and per-fleet caps on tokens, dollars, wall-clock and tool calls; hop limits; lease TTLs; tiered watchdogs; spawn justification and effort-scaling rules; caps on compactions per unit of progress.
    Evidence: [A-MAS][arXiv:2607.01641][GH-GASTOWN][KIMI][A-CC][OAI-CODEX].
12. **Defend at every boundary.** Probe on tool results, an action classifier that never sees tool results, provenance tags on inter-agent content, least privilege per role, approvals never relayed between agents.
    Evidence: [A-AUTO][CC-TEAMS][arXiv:2410.07283][OWASP-AGENTIC].
13. **Log at the LLM boundary through one gateway.** Wire hash, segment digests and boundaries, params, raw response including thinking, cache usage, token IDs and logprobs where possible, lineage, infra config; deterministic rendering with a replay test.
    Evidence: [arXiv:2605.24220][GH-DRESSAGE][GH-RLLM][ATIF][MANUS][GH-VERL].
14. **Export per-agent step samples with segment-aware loss masks.** Split at compaction boundaries with advantage broadcast, mask copied context, normalize advantages per agent, tree-pack shared prefixes, and train the compactor with the agent in the loop.
    Evidence: [GH-DRESSAGE][ATIF][arXiv:2602.08847][arXiv:2511.00413][CURSOR-SUM][arXiv:2609.04518].
15. **Settle data governance before scale.** Teacher terms, per-repo licences, redaction, contamination, and noise-aware multi-signal verification with fresh holdouts.
    Evidence: [ANTH-TERMS][BIGCODE-PII][arXiv:2506.09289][OAI-VERIFIED][A-INFRA][A-EVALAWARE][arXiv:2606.26300].

### Unresolved tensions in the literature
- Share traces (Cognition 2025) vs isolate contexts (Anthropic, Kimi): state-sharing plus retrievable traces is my reconciliation; it needs an ablation.
- Summaries vs masking: JetBrains favours masking, Factory and Cursor favour good or trained summaries. The answer depends on whether the summarizer is trained and structured; run both against a no-compaction control.
- Locks vs optimistic vs claims: Cursor rejected locks, the C compiler used them at 16 agents, AgentRoom and Claim Plane use claims but serialize. Granularity and task partitioning decide it.
- Communication: CooperBench found chat does not fix failures; AgentRadio gained 29.8 pts from passive awareness on read-only tasks. Awareness helps reads; negotiation does not help writes.
- Scale: Cursor reports hundreds of workers while CooperBench declines from 2 to 4 peers; scaffolding, decomposability and verifiers explain the gap.
- Evidence quality: many 2026 items are single-source, vendor-reported, single-author (Claim Plane) or unreplicated (Mem0, Cursor, Kimi); most arXiv numbers here are (S).

## Validation experiments to run before committing
1. Cache fan-out: N = 30 requests with and without pre-warm; confirm entry-availability semantics, breakpoint and 20-block lookback behavior with long tool-use transcripts; measure hit rate and cost. (A zero-token warm-up request is UNVERIFIED; the fetch summary implied it, so test a 1-token request.)
2. Hot block with preserved thinking: ephemeral suffix vs appended message vs turn-scoped system message on a preserved-thinking model with prefix_mismatch_behavior=error; check that thinking survives and how harness-written trims behave (my inference: they invalidate later thinking).
3. Mask vs summary vs structured-entry ablation on your own tasks, reporting cost-adjusted success, steps, recalls and re-reads, with a no-compaction control.
4. Compaction probe suite plus a paired-continuation harness with deterministic environment snapshots; this is the foundation for B4.
5. Lease and merge-queue simulation, then live runs with K writers; track semantic-conflict rate, integration success, wall-clock and tokens.
6. Reviewer clean-context ablation (author transcript vs none) on bug-catch rate and false positives.
7. Mailman ablation: direct delivery vs deterministic router vs LLM digest; message tokens, duplicated work, conflicts.
8. Logging replay test: re-render 1,000 random calls from the log and compare prompt hashes; token-ID round trip on the self-hosted target model.
9. Injection red team across mailbox, board and promotion paths; measure whether a poisoned tool result can reach a pinned layer.
10. Legal review of teacher-model terms and repo licences; choose an open-weight teacher or in-house policy for exported data.

## Sources

Tags: (P) primary page read; (G) GitHub page or file read; (S) secondary summary only, source unreachable or not opened. arXiv entries were not opened (arxiv.org blocked); each rests on search-engine summaries of the abstract or text, so treat numbers as (S).

### Anthropic and Claude Code (all P unless noted)
- [A-MAS] How we built our multi-agent research system. https://www.anthropic.com/engineering/multi-agent-research-system
- [A-CTX] Effective context engineering for AI agents. https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- [A-CC] Building a C compiler with a team of parallel Claudes. https://www.anthropic.com/engineering/building-c-compiler
- [A-MANAGED] Scaling Managed Agents: decoupling the brain from the hands. https://www.anthropic.com/engineering/managed-agents
- [A-HARNESS-DESIGN] Harness design for long-running application development. https://www.anthropic.com/engineering/harness-design-long-running-apps
- [A-HARN] Effective harnesses for long-running agents. https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
- [A-PM] An update on recent Claude Code quality reports (2026-04-23). https://www.anthropic.com/engineering/april-23-postmortem
- [A-AUTO] How we built Claude Code auto mode. https://www.anthropic.com/engineering/claude-code-auto-mode
- [A-INFRA] Quantifying infrastructure noise in agentic coding evals. https://www.anthropic.com/engineering/infrastructure-noise
- [A-EVALAWARE] Eval awareness in Claude Opus 4.6's BrowseComp performance. https://www.anthropic.com/engineering/eval-awareness-browsecomp
- [A-EVALS] Demystifying evals for AI agents. https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents
- [A-TOOLS] Writing effective tools for agents. https://www.anthropic.com/engineering/writing-tools-for-agents
- [A-CTXMGMT] Managing context on the Claude Developer Platform (redirected from anthropic.com/news/context-management). https://claude.com/blog/context-management
- [CC-TEAMS] Orchestrate teams of Claude Code sessions. https://code.claude.com/docs/en/agent-teams
- [CC-SUB] Create custom subagents. https://code.claude.com/docs/en/sub-agents
- [CC-CACHE] How Claude Code uses prompt caching. https://code.claude.com/docs/en/prompt-caching
- [CC-COST] Manage costs effectively. https://code.claude.com/docs/en/costs
- [CC-WT] Run parallel sessions with worktrees. https://code.claude.com/docs/en/worktrees
- [CC-CTX] Explore the context window (what survives compaction). https://code.claude.com/docs/en/context-window
- [API-CACHE] Prompt caching. https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- [API-EDIT] Context editing. https://platform.claude.com/docs/en/build-with-claude/context-editing
- [API-COMPACT] Compaction overview and on-demand compaction. https://platform.claude.com/docs/en/build-with-claude/compaction ; https://platform.claude.com/docs/en/build-with-claude/compaction-on-demand
- [API-BG] Compaction in the background. https://platform.claude.com/docs/en/build-with-claude/compaction-background
- [API-COMPACT-THINK] Compaction and preserved thinking. https://platform.claude.com/docs/en/build-with-claude/compaction-thinking-blocks
- [API-THINK] Preserved thinking (read twice via fetch summaries; states it "guards against distillation"). https://platform.claude.com/docs/en/build-with-claude/preserved-thinking
- [API-SYS] Mid-conversation system messages (turn-scoped clear_at, tool_addition/removal). https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages
- [API-MEM] Memory tool. https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool
- [ANTH-TERMS] Can I use my Outputs to train an AI model? https://support.claude.com/en/articles/12326764-can-i-use-my-outputs-to-train-an-ai-model ; Commercial Terms wording ("train competing AI models") seen only in search results (S).
- [A-LESSONS] Lessons from building Claude Code: prompt caching is everything (search summary only; the claude.com URL redirected to a non-Anthropic domain that I did not follow) (S). https://claude.com/blog/lessons-from-building-claude-code-prompt-caching-is-everything

### Company blogs and vendor pages (S: primary unreachable)
- [COG-1] Cognition, Don't Build Multi-Agents. https://cognition.com/blog/dont-build-multi-agents
- [COG-2] Cognition, Multi-Agents: What's Actually Working (2026-04-22). https://cognition.com/blog/multi-agents-working
- [COG-SWE15] Cognition, Introducing SWE-1.5. https://cognition.ai/blog/swe-1-5
- [MANUS] Context Engineering for AI Agents: Lessons from Building Manus. https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus
- [CURSOR-SCALE] Scaling long-running autonomous coding; Towards self-driving codebases. https://cursor.com/blog/scaling-agents ; https://cursor.com/blog/self-driving-codebases
- [CURSOR-SUM] Training Composer for longer horizons; Composer 2 Technical Report. https://cursor.com/blog/self-summarization ; https://arxiv.org/abs/2603.24477
- [CURSOR-FORUM] Wrong merge conflicts (possibly only in multi-agent). https://forum.cursor.com/t/wrong-merge-conflicts-possibly-only-in-multi-agent/146203
- [OAI-CODEX] Unrolling the Codex agent loop. https://openai.com/index/unrolling-the-codex-agent-loop/
- [OAI-VERIFIED] Why SWE-bench Verified no longer measures frontier coding capabilities. https://openai.com/index/why-we-no-longer-evaluate-swe-bench-verified/
- [OAI-CACHE] Better prompt caching for GPT-6. https://openai.com/index/better-prompt-caching-for-gpt-6/
- [KIMI] Kimi K2.5: Visual Agentic Intelligence. https://arxiv.org/abs/2602.02276 ; https://www.kimi.ai/blog/kimi-k2-5
- [FACTORY] Factory, Evaluating Context Compression for AI Agents. https://factory.ai/news/evaluating-compression
- [LANGCHAIN] How and when to build multi-agent systems. https://www.langchain.com/blog/how-and-when-to-build-multi-agent-systems
- [LANGMEM] LangMem conceptual guide. https://langchain-ai.github.io/langmem/concepts/conceptual_guide/
- [LETTA-SHARED] Letta sleep-time agents and shared memory docs; forum best-practices thread. https://docs.letta.com/guides/agents/architectures/sleeptime/ ; https://docs.letta.com/guides/agents/multi-agent-shared-memory ; https://forum.letta.com/t/sleeptime-agents-for-memory-consolidation-best-practices-guide/154
- [LETTA-CTXREPO] Introducing Context Repositories. https://www.letta.com/blog/context-repositories/
- [CONDUCTOR] Conductor. https://www.conductor.build/
- [CAID] Effective strategies for asynchronous software engineering agents (CAID). https://www.openhands.dev/blog/asynchronous-software-engineering-agents
- [OPENHANDS-DELEG] OpenHands sub-agent delegation docs. https://docs.openhands.dev/sdk/guides/agent-delegation
- [MERGIFY] Merge queues and AI coding agents; State of Merge Queues 2026. https://mergify.com/blog/merge-queues-and-ai-coding-agents ; https://mergify.com/reports/state-of-merge-queues-2026
- [NEBIUS] SWE-rebench dataset; OpenHands trajectories with Qwen3-Coder-480B; SWE-agent-trajectories. https://nebius.com/blog/posts/swe-rebench-dataset ; https://nebius.com/blog/posts/openhands-trajectories-with-qwen3-coder-480b ; https://huggingface.co/datasets/nebius/SWE-agent-trajectories
- [AIDEV] AIDev dataset and agentic-PR studies. https://huggingface.co/datasets/hao-li/AIDev ; https://arxiv.org/abs/2601.18749
- [SWEBENCH-SOLVED] Are "Solved Issues" in SWE-bench Really Solved Correctly? https://doi.org/10.1145/3744916.3764576
- [SBES-2026] How AI Coding Agents Resolve Merge Conflicts: An Empirical Study. https://cbsoft.sbc.org.br/2026/data/papers/sbes/How%20AI%20Coding%20Agents%20Resolve%20Merge%20Conflicts%20An%20Empirical%20Study.pdf
- [OWASP-AGENTIC] OWASP Top 10 for Agentic Applications 2026. https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/
- [BIGCODE-PII] BigCode PII pipeline and StarCoder/Stack v2. https://github.com/bigcode-project/pii-lib ; https://arxiv.org/abs/2305.06161 ; https://arxiv.org/abs/2402.19173

### GitHub (G unless noted)
- [GH-COOPERBENCH] https://github.com/cooperbench/CooperBench
- [GH-GASTOWN] https://github.com/steveyegge/gastown
- [GH-CLAUDESQUAD] https://github.com/smtg-ai/claude-squad
- [GH-JB] https://github.com/JetBrains-Research/the-complexity-trap
- [GH-OAI-HANDOFFS] https://raw.githubusercontent.com/openai/openai-agents-python/main/docs/handoffs.md
- [OPENHANDS-COND] https://raw.githubusercontent.com/OpenHands/software-agent-sdk/main/openhands-sdk/openhands/sdk/context/condenser/llm_summarizing_condenser.py (StructuredSummaryCondenser field list via search (S))
- [GIST-COMPACT] Context Compaction Research (community comparison of Claude Code, Codex CLI, OpenCode, Amp). https://gist.github.com/badlogic/cd2ef65b0697c4dbe2d13fbecb0a0a5f
- [PIEBALD] Community mirror of Claude Code's summarization prompt (not official). https://github.com/Piebald-AI/claude-code-system-prompts
- [GH-MEM0-4573] What we found after auditing 10,134 mem0 entries: 97.8% were junk (issue page read; a single user report). https://github.com/mem0ai/mem0/issues/4573
- [GH-AUTOGEN-ISSUES] (S) https://github.com/microsoft/autogen/issues/1064 ; https://github.com/microsoft/autogen/issues/663
- [GH-CREWAI-ISSUES] (S) https://github.com/crewAIInc/crewAI/issues/4783 ; https://github.com/crewAIInc/crewAI/issues/2606
- [GH-PARL] Community PARL re-implementation (not Kimi's code). https://github.com/The-Swarm-Corporation/PARL
- [GH-SWESMITH] https://github.com/SWE-bench/SWE-smith ; [GH-SWEGYM] https://github.com/SWE-Gym/SWE-Gym ; [GH-R2E] https://github.com/R2E-Gym/R2E-Gym
- [GH-ADP] https://github.com/neulab/agent-data-protocol
- [ATIF] Harbor trajectory format RFC. https://github.com/harbor-framework/harbor/blob/main/rfcs/0001-trajectory-format.md
- [GH-TRL] https://raw.githubusercontent.com/huggingface/trl/main/docs/source/sft_trainer.md
- [GH-VERL] https://raw.githubusercontent.com/volcengine/verl/main/docs/sglang_multiturn/multiturn.rst
- [GH-ACE] https://github.com/ace-agent/ace ; [GH-LLMLINGUA] https://github.com/microsoft/LLMLingua ; [GH-KIMI] https://github.com/MoonshotAI/Kimi-K2.5 (README has no PARL or speedup figures)
- [GH-PATTERNS-CRDT] Third-party pattern write-up of CodeCRDT results. https://github.com/agentpatterns-ai/website/blob/main/patterns/multi-agent/crdt-observation-driven-coordination.md
- [GH-ART] https://github.com/OpenPipe/ART ; [GH-SKYRL] https://github.com/NovaSky-AI/SkyRL ; [GH-RLLM] https://github.com/rllm-org/rllm
- [GH-AGL] https://github.com/microsoft/agent-lightning ; [GH-DRESSAGE] https://github.com/Accio-Lab/Dressage

### arXiv (all S; URL pattern https://arxiv.org/abs/<id>)
- 2308.00352 MetaGPT. 2310.05736 LLMLingua. 2410.02506 Cut the Crap (AgentPrune). 2410.07283 Prompt Infection. 2410.08115 Optima.
- 2501.02628 Cracks in The Stack (licensing risks). 2502.12110 A-MEM. 2503.03704 Memory Injection Attacks (MINJA). 2503.13657 Why Do Multi-Agent LLM Systems Fail? (MAST).
- 2504.13171 Sleep-time Compute. 2504.19413 Mem0. 2505.10978 GiGPO. 2506.09289 UTBoost. 2506.15841 MEM1. 2507.01701 Blackboard-architecture LLM MAS (LbMAS).
- 2507.02259 MemAgent. 2508.19828 Memory-R1. 2508.21433 The Complexity Trap (JetBrains). 2509.05396 Talk Isn't Always Cheap. 2509.13313 ReSum. 2509.25911 Mem-alpha.
- 2510.00615 ACON. 2510.04618 Agentic Context Engineering (ACE). 2510.11967 Context-Folding. 2510.18893 CodeCRDT. 2510.24699 AgentFold. 2511.00413 Tree Training.
- 2512.08296 Towards a Science of Scaling Agent Systems. 2512.22087 Context as a Tool (CAT). 2512.24601 Recursive Language Models.
- 2601.13295 CooperBench. 2602.08847 Dr. MAS. 2603.08755 Turn (agentic-computation language). 2604.02668 Too Polite to Disagree. 2604.03551 AgenticFlict.
- 2605.02801 RL for LLM multi-agent systems through orchestration traces. 2605.02964 Reward Hacking Benchmark. 2605.24220 Polar. 2605.29313 PatchBoard. 2605.30785 AdaCoM.
- 2606.15376 CoAgent. 2606.17182 Verified detection of concurrency anomalies in multi-agent LLM systems. 2606.26300 The Verification Horizon.
- 2607.01641 When Agents Do Not Stop (IAL-Scan). 2607.04697 AI Agent Pull Requests on GitHub: Frequency, Structure, and Merge Conflict Rates. 2607.12161 Token Reduction Is Not Cost Reduction.
- 2607.15516 Cache-Aware Prompt Compression. 2607.23809 ACM: Agentic Context Management. 2607.28430 AgentRadio.
- 2608.00947 Claim Plane (pre-write admission study). 2608.06503 Toward Reliable Context Compression (TRACE). 2608.16370 What Does Context Compression Cost an Agent?
- 2608.17528 Agent Lightning v1.0. 2608.18092 Position: Multi-Agent Systems Should Prioritize Concurrency Control. 2608.23740 AgentRoom. 2608.24188 Paritok-4B. 2608.28476 ContextPilot.
- 2609.04518 What Does Multi-Harness RL Learn? 2609.08015 From Version Conflicts to Decision Conflicts. 2609.08589 The Unreliable Progress Bar. 2609.22114 Cost Attribution of Context-Compression Gateways. 2609.32961 Beyond Token Savings.

