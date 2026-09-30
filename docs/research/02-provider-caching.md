# 02 - Provider prompt/KV caching: verified facts for Sleipnir's layered-cache harness

*Researched 2026-09-29/30. Prices, model IDs and beta headers are point-in-time; this area changes monthly.*
*Machine-readable companion: `docs/research/02-provider-caching.json` (21 provider profiles, per-fact confidence).*

## 0. Legend, method, access limits

Confidence tags used on every fact:

- **[H]** high: read in this session from the vendor's own docs, spec or source code.
- **[M]** medium: primary but indirect (SDK type stubs, OpenAPI descriptions without the prose guide, vendor cookbook, LiteLLM cost map) or two consistent secondary sources.
- **[L]** low: one secondary, community or search-summary source.
- **UNVERIFIED**: could not be established; do not build on it.

Access:

- Reached: platform.claude.com, code.claude.com, claude.com and anthropic.com posts; raw GitHub (vLLM, SGLang, TensorRT-LLM, LMCache, Portkey, Codex, pi-mono, LiteLLM cost map); package contents from PyPI/npm (openai 3.22.0 with OpenAPI 2.3.0 spec, anthropic 1.9.0, google-genai 2.25.0, litellm 1.103.1, sglang 0.5.20, @openrouter/sdk 1.4.7, @ai-sdk/gateway 4.0.100).
- Egress-blocked and NOT bypassed: developers.openai.com, platform.openai.com, openrouter.ai, ai.google.dev, api-docs.deepseek.com, docs.x.ai, platform.moonshot/kimi, alibabacloud.com, docs.mistral.ai, docs.z.ai, platform.minimax.io, docs.vllm.ai, vercel.com, docs.aws.amazon.com, manus.im, medium.com.
- For the blocked sites I used specs, SDK types, source code and web-search summaries (search budget ran out at 200/200), so those rows are capped at [M]/[L].
- Nothing here was measured against the "marketplace" gateway; every statement about it is UNVERIFIED. Section 6 and D11 give a canary suite to establish what it preserves.

## 1. Headline findings (read first)

Items marked **CONTRADICTS BRIEF** overturn an assumption in the task brief.

1. **CONTRADICTS BRIEF - OpenAI is no longer "automatic prefix, free writes" everywhere.** GPT-5.6 and later (GPT-5.6 Sol/Terra/Luna, GPT-6 Astra/Sol/Luna, GPT-6.1 Sol) add explicit `prompt_cache_breakpoint` on content parts and `prompt_cache_options{mode,ttl,prewarm,comparison_response_id}`, a 30-minute minimum lifetime, a **1.25x write premium** on implicit and explicit writes, `cache_write_tokens` in usage and `prompt_cache_diagnostics` on responses. GPT-5.5 and earlier keep the old regime (automatic, no write fee). [H parameters (spec 2.3.0); M prices]
2. **CONTRADICTS BRIEF - `prompt_cache_retention` is deprecated** in favour of `prompt_cache_options.ttl`; they are independent (maximum-retention policy vs minimum lifetime). For gpt-5.5, gpt-5.5-pro and later only `24h` is accepted. [H]
3. **CONTRADICTS BRIEF - current OpenAI flagships** are GPT-6 Astra ($10/$50), GPT-6 Sol ($2/$10), GPT-6 Luna, GPT-6.1 Sol (2026-09-29, $2/$10, cached $0.10) and the GPT-5.6 tier. The newest "codex"-named ID in the spec and cost map is gpt-5.3-codex; coding is now served by the general models (press: GPT-6/6.1 Sol are the Codex models) [L]. [H IDs; M prices]
4. **CONTRADICTS BRIEF - Anthropic cache diagnostics is GA, not beta**: request field `diagnostics:{previous_message_id}`, no header needed (`cache-diagnosis-2026-04-07` is still accepted). Claude API only. [H]
5. **Anthropic isolation is per workspace** (Claude API, Foundry, Claude Platform on AWS); per organization on Bedrock/Google Cloud. A workspace is a hard cache boundary for a fleet. [H]
6. **The read discount is not a constant 0.1x**: 0.05x on Opus 5.5, 0.025x on Fable 5.1 and Mythos 5.1, 0.1x elsewhere. Writes stay 1.25x (5m) / 2x (1h). The minimum cacheable prefix is 512 tokens on the newest models and 4096 on Opus 4.6/4.5 and Haiku 4.5. [H]
7. **Concurrency bites a fan-out design.** An Anthropic entry is readable only after the writing request's response begins, so N agents launched together on a cold prefix all pay the write; pre-warm (`max_tokens:0`) or stagger (Claude Code holds all but one workflow agent for up to 5 s). OpenAI adds a ~15 requests/min ceiling per prefix+`prompt_cache_key`, so one shared key for dozens of agents overflows onto new machines. [H]
8. **Preserved thinking is the central tension with a compactor-plus-pinned-layer design.** On Fable 5.1, Opus 5.5 and Sonnet 5.5 a thinking block is valid only if `system`, `tools` and every earlier message are unchanged since it was produced; the API enforces this as a 400 by default for accounts created on/after 2026-08-31 00:00 UTC. Client-side rewrites of the transcript or pinned layers invalidate all later thinking. Safe patterns: append-only `role:system` messages, turn-scoped `clear_at` messages, server-signed on-demand compaction, "simple compaction" (new session from a summary). [H]
9. **New cache-preserving primitives exist** (mid-conversation `role:system`, `clear_at:"next_user_message"`, `tool_addition`/`tool_removal`, per-message effort, `defer_loading`, `max_tokens:0`, diagnostics). They map almost 1:1 onto Sleipnir's hot-tail block and promotion needs (section 9). [H]
10. **Gateways break caching silently.** LiteLLM drops any `anthropic-beta` value missing from its allow-list (debug log only); Portkey drops `ttl:"1h"`, reports `prompt_tokens` without cache tokens and load-balances randomly; Anthropic's gateway guide says forward `anthropic-beta` verbatim and never turn block-form `system` into a string. The failure is silent: no error, just uncached billing. [H]
11. **Compaction is a priced cache event**: Anthropic estimates ~$0.25 for a warm 150K-token Opus 5.5 conversation, break-even after ~10 turns; a cold-cache compaction costs ~$0.75 on input alone. Run it as a same-prefix fork. [H]
12. **A cross-organization "global" cache scope probably exists but is undocumented**: Claude Code calls its static prompt+tools "globally cached" and reportedly sends `cache_control.scope:"global"` under beta `prompt-caching-scope-2026-01-05`. Semantics UNVERIFIED [L]; do not design around it.

## 2. Comparison tables

### 2.1 Hosted APIs (details and per-fact confidence in the JSON)

| Provider | Mode | Min prefix | Breakpoints / granularity | TTL | Read x | Write x | Usage fields | Isolation / routing | Conf |
|---|---|---|---|---|---|---|---|---|---|
| Anthropic | explicit `cache_control` on blocks and/or top-level auto | 512 / 1024 / 2048 / 4096 by model | max 4; one entry per breakpoint; 20-block lookback | 5m default, 1h; free refresh on read | 0.1 (0.05 Opus 5.5; 0.025 Fable/Mythos 5.1) | 1.25 (5m), 2.0 (1h) | `cache_read_input_tokens`, `cache_creation_input_tokens` (+`cache_creation.ephemeral_5m/1h_input_tokens`); `input_tokens` = uncached tail only | workspace; no routing control | H |
| OpenAI <=GPT-5.5 | automatic | 1024 | 128-token increments; no breakpoints | `in_memory` (~5-10 min idle, up to 1h [M]) or `24h`; 5.5+: 24h only | 0.5 (4o, o1) / 0.25 (4.1, o3) / 0.1 (5.x) | 1.0 | chat `prompt_tokens_details.cached_tokens`; responses `input_tokens_details.cached_tokens` (inside the input total) | org; `prompt_cache_key` + hash of first ~256 tokens; ~15 RPM per prefix+key | H/M |
| OpenAI >=GPT-5.6 | implicit + explicit `prompt_cache_breakpoint` | 1024 | 4 written per request, latest 80 matched, no block lookback, exact boundary | 30m minimum (only value) | 0.1 (0.05 GPT-6.1 Sol) | 1.25 | + `cache_write_tokens`; `prompt_cache_diagnostics` | org [M]; same key routing | H/M |
| Gemini implicit | automatic (2.5+) | 4096 (3.x), 2048 (2.5) | not documented | not documented | ~0.1 | 1.0 | `usageMetadata.cachedContentTokenCount` | project [UNVERIFIED] | M |
| Gemini explicit | `cachedContents` resource | 4096 / 2048 | whole named resource; must end on a user turn | default 60 min; `ttl` or `expire_time` | ~0.1 | 1.0 + storage per token-hour | same | project resource bound to a model | M |
| DeepSeek | automatic disk cache | 64 | 64-token units, exact prefix from token 0 | best-effort, hours to days | 0.1 (V3.2); ~0.02-0.033 (V4) | 1.0 | `prompt_cache_hit_tokens`, `prompt_cache_miss_tokens` | UNVERIFIED | M/L |
| xAI Grok | automatic | not stated | exact prefix | best-effort, evicted under memory pressure | 0.15-0.25 by model | 1.0 | `prompt_tokens_details.cached_tokens` | `x-grok-conv-id` header / `prompt_cache_key` routes to same server | M |
| Moonshot Kimi | automatic | not stated | prefix | not user-selectable [L] | 0.1-0.2 by model | 1.0 [L] | `usage.cached_tokens` and `prompt_tokens_details.cached_tokens` | `prompt_cache_key` = scheduling hint only | L |
| Alibaba Qwen | explicit + implicit | 1024 | <=4 markers, 20-block lookback, message-level on Qwen3.5+ | 5 min (explicit) | 0.1 explicit / ~0.2 implicit | 1.25 explicit / 1.0 implicit | `prompt_tokens_details.cached_tokens`, `cache_creation_input_tokens` | UNVERIFIED | M |
| Mistral | automatic + `prompt_cache_key` | not stated | - | not stated | 0.1 | 1.0 | `prompt_tokens_details.cached_tokens` | key is a hint | L |
| Z.ai GLM | automatic | not stated | - | not stated | ~0.18-0.2 | 1.0 (+ unexplained "cached input storage") | `prompt_tokens_details.cached_tokens` | none documented | L |
| MiniMax | passive (OpenAI endpoint) + explicit `cache_control` (Anthropic endpoint) | 512 [L] | - | not stated | 0.1 (M2.x) / ~0.2 (M3) | 1.25 explicit | `cache_read_input_tokens`/`cache_creation_input_tokens` or `cached_tokens` | none documented | L |

### 2.2 Anthropic models (all [H] from the pricing, caching and models pages, 2026-09-29)

| Model ID | Context | In / out $ per MTok | 5m write | 1h write | Read | Min prefix | Preserved-thinking check | Mid-conv `system` / per-message effort |
|---|---|---|---|---|---|---|---|---|
| `claude-fable-5-1` | 1M | 10 / 50 | 12.50 | 20 | 0.25 (0.025x) | 512 | yes | yes / yes |
| `claude-mythos-5-1` (limited) | 1M | 10 / 50 | 12.50 | 20 | 0.25 (0.025x) | 512 | exempt | yes / yes |
| `claude-fable-5` | 1M | 10 / 50 | 12.50 | 20 | 1.00 | 512 | no | yes / no |
| `claude-opus-5-5` | 1M | 4 / 20 | 5.00 | 8 | 0.20 (0.05x) | 512 | yes | yes / yes |
| `claude-opus-5` | 1M | 5 / 25 | 6.25 | 10 | 0.50 | 512 | no | yes / yes |
| `claude-opus-4-8` | 1M | 5 / 25 | 6.25 | 10 | 0.50 | 1024 | no | yes / no |
| `claude-opus-4-7` / `-4-6` | 1M | 5 / 25 | 6.25 | 10 | 0.50 | 2048 / 4096 | no | no / no |
| `claude-sonnet-5-5` | 1M | 2 / 10 | 2.50 | 4 | 0.20 | 512 | yes | yes / yes |
| `claude-sonnet-5` | 1M | 2 / 10 | 2.50 | 4 | 0.20 | 1024 | no | no / no |
| `claude-sonnet-4-6` | 1M | 3 / 15 | 3.75 | 6 | 0.30 | 1024 | no | no / no |
| `claude-haiku-4-5-20251001` | 200K | 1 / 5 | 1.25 | 2 | 0.10 | 4096 | no | no / no |

- Max output 128K (Haiku 4.5: 64K). Opus 5.5 default effort is `medium`, others `high`.
- No long-context premium on 4.6+ (the full 1M is billed at the standard rate).
- `inference_geo:"us"` = 1.1x on every category including cache reads/writes. Batch API = 50% off and stacks with cache multipliers.
- Fast mode (Opus 5.5 $8/$40) is a cache-key input.

### 2.3 OpenAI models (IDs [H] from the spec's model enum; prices [M] from LiteLLM cost map + press)

| Model ID | Context (in cap / out) | In / out $ per MTok | Cached read | Cache write | Note |
|---|---|---|---|---|---|
| `gpt-6-astra` | 922K / 128K (1.05M window) | 10 / 50 | 1.00 | 12.50 | released 2026-09-03/04 |
| `gpt-6-sol` | 922K / 128K | 2 / 10 | 0.20 | 2.50 | 2026-09-22 |
| `gpt-6-luna` | 922K / 128K | 0.10 / 0.50 | 0.01 | 0.125 | 2026-09-22 |
| `gpt-6.1-sol` | 922K / 128K | 2 / 10 | 0.10 (0.05x) | 2.50 | 2026-09-29 (DevDay) |
| `gpt-5.6-sol` / `-terra` / `-luna` | 922K / 128K | 4/20 (promo), 2/12, 0.20/1.20 | 0.40 / 0.20 / 0.02 | 5.00 / 2.50 / 0.25 | Sol price is promotional; a secondary table lists a $6.25 write |
| `gpt-5.5` | 1.05M / 128K | 5 / 30 | 0.50 | none | only `24h` retention |
| `gpt-5.4`, `gpt-5.3-codex`, `gpt-4.1` | 1.05M, 272K, 1.05M | 2.5/15, 1.75/14, 2/8 | 0.25, 0.175, 0.50 | none | 4.1 read = 0.25x |

Input above 272K tokens is priced at 2x input (cost map `..._above_272k_tokens`) for gpt-5.4 and later. [M]

### 2.4 Concurrency, routing, invalidators, rate-limit interplay

| Provider | Entry readable when | Routing / stickiness control | Main invalidators | Rate-limit interplay |
|---|---|---|---|---|
| Anthropic | after the writing request's response begins [H] | none exposed (`metadata.user_id` is abuse detection only) [M] | tools > system > messages hierarchy, model, thinking/effort, `tool_choice`, images, speed | reads excluded from ITPM (not Haiku 3.5); writes and uncached input count [H] |
| OpenAI <=5.5 | not documented (UNVERIFIED) | `prompt_cache_key`; ~15 RPM per prefix+key per engine | tools order/schema, instructions, `text.format`, effort, truncation, timestamps | not documented in sources reached; Flex tier lets you control RPM [M] |
| OpenAI >=5.6 | UNVERIFIED (use `prewarm`) | `prompt_cache_key`, `prewarm`, `comparison_response_id` | nine documented miss reasons (section 4.2) | UNVERIFIED |
| Gemini | UNVERIFIED | none documented | prefix change; explicit cache expiry | UNVERIFIED |
| DeepSeek | UNVERIFIED | none | any change before the last full 64-token unit | UNVERIFIED |
| xAI | UNVERIFIED | `x-grok-conv-id` / `prompt_cache_key`; entries stored per server [M] | editing/reordering earlier messages; omitting `reasoning_content` | UNVERIFIED |
| Kimi / Qwen / Mistral / GLM / MiniMax | UNVERIFIED | Kimi/Mistral `prompt_cache_key` hint; others none | prefix change | UNVERIFIED |
| vLLM | a block as soon as it is full, even for requests in the same batch [H] | external router only | any earlier token; different salt/LoRA | n/a |
| TensorRT-LLM | blocks enter the radix tree "as soon as they are filled" [H] | KV events for an external router | eviction by priority | n/a |
| SGLang | tree insert timing not verified | scheduler `lpm`/`dfs-weight`; gateway routing keys | eviction policy | n/a |

### 2.5 Gateways and open-source serving

| Gateway | Effect on caching | Verified failure modes | Routing control | Conf |
|---|---|---|---|---|
| OpenRouter | passes provider-native caching; top-level and block `cache_control` (`5m`/`1h`) | unrecognised `provider.options` keys silently dropped [M] | `session_id` (<=256 chars) or `x-session-id` = sticky key; `provider.order/only/ignore/allow_fallbacks/sort` | M |
| LiteLLM | maps `cache_control` and `prompt_cache_breakpoint`; `cache_control_injection_points` | unknown `anthropic-beta` values dropped; unsignable thinking blocks dropped | router affinity checks (`session_affinity`, `prompt_caching_deployment_check`, ...) | H |
| Portkey (OSS gateway) | re-creates Anthropic `cache_control` | `ttl` dropped (always 5m); `prompt_tokens` excludes cache tokens; random load balancing | none in OSS gateway | M |
| Vercel AI Gateway | `caching:"auto"` adds breakpoints (5m only) | cannot request 1h; provider fail-over fragments caches | `order`/`only` | L |
| Cloudflare AI Gateway | provider-native endpoints pass through; its "Caching" is an exact-match response cache | not KV caching; useless for agent loops | none | M |

| Engine | Granularity | Eviction / pinning | Isolation | Metrics | Conf |
|---|---|---|---|---|---|
| vLLM (V1) | 16-token full blocks, sha256 hash chain | LRU only; no pin primitive | global unless `cache_salt` | not verified | H |
| SGLang | radix tree, page_size 1 by default | `lru/lfu/slru/priority`; HiCache tiers | `cache_salt`/extra key | `cached_tokens` only with `--enable-cache-report` | H |
| TensorRT-LLM | user-set power-of-two blocks, radix tree | priority 0-100 (default 35) + `duration_ms` per token range; host offload | `cache_salt` | KV events | H |
| LMCache | chunked KV store over engines | tiered CPU/disk/remote; CacheBlend non-prefix reuse | deployment-defined | n/a | M |

## 3. Anthropic (Claude API)

### 3.1 Mechanics [H - prompt-caching page]

Prefix and lookback:

- Render order is `tools`, `system`, `messages`. A breakpoint writes exactly one entry: the hash of the cumulative prefix ending at that block. Nothing is written for earlier positions.
- On each request the system computes the hash at every breakpoint and, if absent, walks backward at most 20 positions (the breakpoint is position 1), looking only for entries earlier requests wrote.
- On the Claude API a run of consecutive `tool_use` blocks counts as one position, as does a run of `tool_result` blocks.
- If one turn adds 20 or more blocks the previous write falls out of the window; add an earlier breakpoint so a write already exists there.
- Put the breakpoint on the last block identical across requests, never on a block holding a timestamp or the incoming message (it would write every time and never read).

Breakpoint budget:

- Up to 4 breakpoints per request. Top-level automatic `cache_control` uses one slot.
- 4 explicit plus auto returns 400; an explicit last-block marker with a different TTL returns 400.
- Auto silently walks back to the nearest eligible block and skips turn-scoped system messages. Auto is unavailable on legacy Bedrock (Opus 4.6 and earlier).

TTL and payback:

- 5 minutes by default, 1 hour at 2x. Each read refreshes at no charge.
- The lifetime is measured from the START of the request that wrote or read the entry, so a 4-minute response leaves ~1 minute.
- A 1h entry must precede any 5m entry in a request. Mixed-TTL billing uses positions A (highest hit), B (highest 1h breakpoint after A), C (last breakpoint): read A, write B-A at 1h, write C-B at 5m.
- A 5m write pays off after 1 read (1.25 + 0.1 < 2), a 1h write after 2 reads.

Limits and side effects:

- Not cacheable directly: thinking blocks (they are cached inside later prefixes and count as input when read), empty text blocks, citation sub-blocks.
- Concurrency: "a cache entry only becomes available after the first response begins"; parallel requests must wait for the first response to get hits.
- Isolation: per workspace on Claude API, Platform on AWS and Foundry; per organization on Bedrock and Google Cloud; never across organizations.
- ZDR-eligible: only KV representations and hashes, held in memory. Entries live at least 5 min / 1 h, then are "promptly, though not immediately" deleted.
- Server tools (web search/fetch/code execution) get an automatic 5m breakpoint whenever the request already has any `cache_control`, so `ephemeral_5m` writes appear even if you only set 1h.
- `defer_loading` tools cannot carry `cache_control` (400). Message Batches: best-effort hits (30%-98% typical); use 1h TTL and one warm-up request.

Rate limits:

- `input_tokens` and `cache_creation_input_tokens` count toward ITPM; `cache_read_input_tokens` do not (except Haiku 3.5).
- Docs example: 2M ITPM at an 80% hit rate processes ~10M input tokens/min. Acceleration limits can 429 sharp ramps.

### 3.2 Usage fields and streaming [H]

- Total input = `input_tokens` + `cache_read_input_tokens` + `cache_creation_input_tokens`; `input_tokens` counts only tokens after the last breakpoint.
- `cache_creation_input_tokens` equals `cache_creation.ephemeral_5m_input_tokens` + `ephemeral_1h_input_tokens`.
- Thinking output is inside `output_tokens`; `output_tokens_details.thinking_tokens` decomposes it. Also present: `service_tier` (`standard`, `priority`, `batch`), `inference_geo`, `server_tool_use`.
- Streaming: `message_start.message.usage` carries the input/cache fields; `message_delta.usage` is CUMULATIVE. There is no `stream_options.include_usage` (usage is always streamed).
- `diagnostics` and `input_transformations` arrive on `message_start` (the latter again on the final `message_delta` after a mid-stream fallback).
- With compaction or fallback, sum `usage.iterations[]` (types `message`, `compaction`, `fallback_message`); top-level fields describe only the answering attempt.
- Events: `message_start`, `content_block_start/delta/stop`, `message_delta`, `message_stop`, `ping`, `error`; unknown events must be tolerated.
- Deltas: `text_delta`, `input_json_delta` (partial JSON string; parse at `content_block_stop`), `thinking_delta`, `signature_delta` (just before the block closes).
- On Fable 5.1 the `thinking` string is empty and the reasoning lives in the signature: a serializer that skips "empty" blocks silently deletes reasoning.
- Compaction blocks stream whole (one `content_block_start`, no deltas); `fallback` blocks are start+stop with no deltas.

### 3.3 What invalidates the cache [H]

| Change | Invalidates |
|---|---|
| Any tool definition (name, description, schema, order); non-deterministic JSON key order (Go/Swift) | tools + system + messages |
| Web search / citations toggle; `speed:"fast"` toggle | system + messages |
| `tool_choice`, `disable_parallel_tool_use`, image added/removed anywhere | messages |
| Thinking configuration or top-level `output_config.effort` (explicit default = omitted) | messages always; tools/system too on models that render the config ahead of them |
| Model change (cache is per model) | everything |
| Dropped thinking block (API dropped it: unreadable by the model, prefix mismatch) | from that block onward |
| Client edit of any earlier message, tool_result clearing, image re-encode | from the edit onward (and later thinking, section 3.6) |

### 3.4 Cache-preserving mutation primitives [H - mid-conversation-system-messages, effort, tool-search pages]

| Need | Mechanism | Beta header | Models / notes |
|---|---|---|---|
| New instruction mid-session | append `{"role":"system"}` after a `user` turn (after `tool_result` is fine; never between `tool_use` and its result); permanent, cacheable | none | Fable 5.1/5, Mythos 5.1/5, Opus 5.5/5/4.8, Sonnet 5.5; NOT Sonnet 5; not the first entry of `messages`; content has operator authority, so never put tool output or web text in it |
| Per-turn reminder that must not pile up | `clear_at:"next_user_message"` system message; renders until a later user message (a tool_result-only user message counts), stays in the array | `mid-conversation-system-clear-at-2026-08-21` | text only, no `cache_control`/`output_config`/tool blocks; re-send verbatim forever or cache and later thinking break; auto-caching skips it |
| Add/remove/redefine tools | `tool_addition` / `tool_removal` blocks in a `role:system` message; by value via `tool_definition`; up to 10,000 tools / 4 MB | `inline-tools-2026-09-15` (older `mid-conversation-tool-changes-2026-07-01` = by reference); plus `mcp-client-2026-09-15` for MCP toolsets | keep >=1 non-deferred tool in `tools` or the first inline tool costs a full miss; Claude API only |
| Change effort | `output_config.effort` inside a system message (empty content is legal anywhere) | `mid-conversation-output-config-2026-07-01` | Fable 5.1, Mythos 5.1, Opus 5.5, Opus 5, Sonnet 5.5; a top-level change restarts the cache |
| Many tools, few used | `defer_loading:true` + tool search (`tool_search_tool_regex_20251119`, `tool_search_tool_bm25_20251119`); definitions arrive as `tool_reference` blocks | none | prefix untouched; Claude Code keeps the first request's tool list for the whole conversation |
| Trim old results | server-side `clear_tool_uses_20250919` (default trigger 100K input tokens, keep 3, `clear_at_least`) | `context-management-2025-06-27` | invalidates the prefix from the clear point; `clear_thinking_20251015` with `keep:"all"` preserves cache; on preserved-thinking models server-side edits never invalidate thinking |

### 3.5 Pre-warm and diagnostics [H]

Pre-warm:

- `max_tokens:0` writes the cache at any breakpoint and returns `content:[]`, `stop_reason:"max_tokens"` with full `usage`. No output is billed; a write is billed if the prefix was not cached.
- Put the breakpoint on the last block shared with the follow-up (not on the placeholder user message), so use an explicit breakpoint, and reuse the same thinking config and `effort`.
- Rejected with `stream:true`, `thinking.type:"enabled"`, `output_config.format`, forced `tool_choice`, and inside Message Batches.

Diagnostics (GA):

- Send `diagnostics:{previous_message_id: <id or null>}` every turn. Response `diagnostics` is `null` (no divergence or not opted in), `{cache_miss_reason:null}` (comparison still running) or `{cache_miss_reason:{type,cache_missed_input_tokens}}`.
- `type` is `model_changed`, `system_changed`, `tools_changed`, `messages_changed`, `previous_message_not_found` or `unavailable`.
- It compares request structure, not the actual hit, so read it together with `usage`. Same workspace only; fingerprints are hashes with short retention; Claude API only.

### 3.6 Preserved thinking (the crux) [H - preserved-thinking page]

Scope and checks:

- Applies to Fable 5.1, Opus 5.5, Sonnet 5.5 (Mythos 5.1 and all earlier models are exempt).
- On send-back the API checks (a) the current model can read the block and (b) nothing in `system`, `tools` or earlier `messages` changed.
- Readability: Fable 5.1 reads Opus 5 and, on the Claude API, Opus 5.5 blocks; Opus 5.5 does not read Fable/Mythos; Sonnet 5.5 reads Sonnet 5, Opus 4.8, Haiku 4.5 and earlier. Unreadable blocks are dropped silently and unbilled.
- Sonnet 5.5 blocks are also bound to the producing account (drop reason `organization_binding_mismatch`).

Enforcement:

- Default is a 400 `invalid_request_error` naming the first failing block, for accounts created on/after 2026-08-31 00:00 UTC. Older accounts are enforced only when `thinking.block_binding.prefix_mismatch_behavior` is set.
- Beta `thinking-binding-controls-2026-08-01` adds that field (`error` default, `drop_block`) and a top-level `input_transformations` array (`thinking_dropped` with reasons `prefix_binding_mismatch`, `model_binding_mismatch`, `organization_binding_mismatch`; `thinking_mismatch_allowed` when not enforced).
- `drop_block` removes the failing block and every later thinking block. Dropped blocks are unbilled, but the model may think more to recompute.
- Not part of the prefix: `effort`, `max_tokens`, `output_config`, `tool_choice`, `metadata`, `thinking.display`, `cache_control` markers, server-side compaction/context editing, a cleared turn-scoped message left in place.

Edits that invalidate later thinking:

- Changing `system`; adding, removing, renaming or editing a tool (a deferred tool nothing has referenced is fine).
- Editing, reordering or deleting any earlier message; clearing or shortening an earlier `tool_result`; adding or removing a text block in an earlier user turn.
- Re-rendering first-user-message context with a changed value; a URL whose bytes change.
- Deleting a `thinking` block from the middle (removing from the start, the end, or all is valid) and later re-adding it.

Handling rules:

- Send assistant `content` back exactly as returned: all block types, empty `thinking` fields, signatures. Persist exact request bytes for resume; do not re-render from inputs that may have changed.
- A branch that replays history unchanged up to the fork point keeps its thinking; a new conversation cannot inherit it.
- Only API-written (on-demand) compaction lets kept turns keep valid thinking. Conditions: every compaction since the block was produced ran on a preserved-thinking model, kept turns directly follow the summarized range unchanged, and `system` plus non-deferred `tools` match on the compaction request and later requests.
- The first kept message must have a different role from the last summarized one and cannot be a `role:system` message.
- Threshold compaction and self-written summaries cannot keep thinking: strip it from carried turns, send `drop_block`, or use "simple compaction". Do not compact mid tool round; do not cut turns from the middle.
- Gateway guidance from the same page: pass through `anthropic-beta` and `thinking.block_binding`, return `input_transformations`, leave a `role:system` message where the caller put it (moving it into `system` invalidates everything), use `tool_choice:{"type":"none"}` instead of removing `tools`.

### 3.7 Compaction, context editing, memory [H]

Threshold compaction (`context_management.edits[{type:"compact_20260112"}]`, beta `compact-2026-01-12`):

- `trigger` `input_tokens` >= 50,000; optional `pause_after_compaction` and `instructions`.
- Put `cache_control` on the compaction block and at the end of `system` so the system cache survives.
- Cannot be combined with `compaction` or a signed on-demand block. With tools defined, set `instructions` telling the model not to call tools, or the block can come back with `content:null`.

On-demand compaction (beta `compact-2026-09-04`, top-level `compaction:{type:"summarize"}`, optional `instructions` up to 16,384 chars):

- Send the conversation with the SAME model, `system`, `tools` and thinking. The response is one signed `compaction` block with `stop_reason:"compaction"`; cost is in `usage.iterations`.
- The block goes first in `messages`, summarized messages are removed (else 400 `compaction_block_misplaced`), exactly one block per request, never edit it. Compacting again summarizes the old summary too.
- Keep-tail: compact only the older messages; send the returned block plus the kept turns unchanged.
- Background: keep working on the full history (append only), then swap when the block arrives by dropping exactly the first N messages.
- Failure = any other `stop_reason` (`max_tokens`, `tool_use`, `refusal`, `end_turn`, `model_context_window_exceeded`): keep the full history and retry later.
- Lost in the summary: images, documents, fetched URLs; `role:system` instructions in the summarized range stop applying (restate them after the next user turn). Tool changes carry over via the block's `tool_changes` when `inline-tools-2026-09-15` is sent. Not available on Bedrock.

Memory tool `memory_20250818`:

- Client-side files under `/memories`; "standard client tool, no special caching interaction".
- Facts move out of the prompt into files the model re-reads via tool calls, so they enter the transcript, not the pinned prefix.

### 3.8 Stop reasons, refusals, fallback [H]

- `stop_reason` values: `end_turn`, `max_tokens`, `stop_sequence`, `tool_use`, `pause_turn`, `refusal`, `model_context_window_exceeded`.
- `pause_turn` = a server-tool loop hit its iteration cap (default 10): resend the assistant content unchanged. A client `tool_use` turn is never `pause_turn`.
- With an unresolved server tool the next user message must contain only `tool_result` blocks and `tools` must be unchanged.
- Refusal = HTTP 200 with `stop_details{category, explanation, recommended_model, fallback_credit_token, fallback_has_prefill_claim}`; category is `cyber`, `bio`, `frontier_llm`, `reasoning_extraction`, `general_harms` or null.
- Refusals in `bio`/`frontier_llm`/`reasoning_extraction` are billed even before output; mid-stream refusals bill what streamed. Applies to Fable 5.1/5, Opus 5.5/5, Sonnet 5.5.
- Server-side fallback (beta `server-side-fallback-2026-07-01`, Claude API only, not Batches): `fallbacks:"default"` or up to 3 models. The response has a `fallback` content block; top-level `model` is the answering model; sticky routing (~1 h, per organization) sends later turns straight to the fallback.
- Caches are per model, so a fallback rebuilds the prefix. For hand-rolled retries send `fallback_credit_token` with beta `fallback-credit-2026-07-01` within 5 minutes and an identical body (Fable 5.1/5 targets: Opus 4.8, Opus 5).
- Echo `fallback` blocks in place; drop earlier thinking and client `tool_use` before the last fallback block. `fallbacks` does not propagate into sub-agent calls: set it per request.

### 3.9 Tool-loop practices [H]

- Return every `tool_result` for one assistant turn in a single user message with the results first and text after; no messages between `tool_use` and results. Text right after results can teach the model to expect user input and yield empty `end_turn` responses.
- Parallel calls are on by default; Fable 5.1 may under-batch in long loops (add a batching instruction). `disable_parallel_tool_use` lives inside `tool_choice` and is a cache input.
- Fine-grained streaming: set `eager_input_streaming:true` per tool with `stream:true`. The API stops buffering/validating, so you can receive partial or invalid JSON (also at `max_tokens`): accumulate `partial_json`, guard the parse, return an `is_error` tool_result on failure. Claude Code leaves it off behind a custom base URL.
- Prefer Anthropic-schema client tools (`bash_20250124`, `text_editor_20250728` named `str_replace_based_edit_tool`, `memory_20250818`): trained-in signatures, no caching side effects.
- Opus 5.5, Sonnet 5.5, Fable 5.1 and Mythos 5.1 do not support `tool_choice` `any`/`tool`. Manual `budget_tokens` thinking is not accepted after Opus/Sonnet 4.6 (adaptive thinking only).

### 3.10 What Anthropic's own harness does [H - code.claude.com prompt-caching, workflows, gateway pages; blog 2026-04-30]

Layout:

- Layers, static to dynamic: system prompt + tools; project context (CLAUDE.md, auto memory); conversation.
- Updates go into messages as `<system-reminder>` blocks; plan mode and skills are appended messages.
- Tools are never added or removed mid-session: plan mode is the tools `EnterPlanMode`/`ExitPlanMode`; `defer_loading` stubs replace removal.

Compaction and forks:

- Compaction runs as a **cache-safe fork**: same system prompt, context and tools as the parent, history prepended, compaction prompt appended as a final user message, so it reads the warm prefix. Reserve a "compaction buffer" for the extra prompt and summary output.
- `/rewind` truncates back to a prefix that is already cached.
- Forks inherit the parent's exact prefix and read its cache; a fresh subagent has a different prefix and does not.
- Same-prefix workflow agents: all but the first are held until the first response begins (cap `CLAUDE_CODE_WORKFLOW_PREFIX_STAGGER_MS`, default 5000).

TTL, effort, scope:

- Two TTL buckets: main conversation vs "everything else" (subagents, workflows, teammates, forks, compaction). Default 5 min except the main conversation on a subscription (1 h).
- On most models each effort level has its own cache; Opus 5.5, Sonnet 5.5 and Fable 5.1 keep the cache via per-message effort (not on Bedrock, Google Cloud or a Claude apps gateway). Fast mode adds a header that is part of the cache key.
- Images are removed in batches (one slower turn per batch).
- Cache scope in practice is machine + directory (auto-memory path and cwd announcement live in the prefix); the SDK's `excludeDynamicSections:true` moves per-user context into the first user message so fleets share one system-prompt entry.

Telemetry and gateway hints:

- A request is a "miss" when it re-processed more than 5% and at least 2,000 tokens it could have read; compaction and tool-result clearing count as "expected rebuilds". Anthropic declares SEVs on low hit rate.
- Hint headers (Claude Code >= 2.1.273; sent by default only to api.anthropic.com): `x-claude-code-session-id`, `-agent-id`, `-parent-agent-id`, `-request-class` (`main`, `subagent`, `workflow`, `compaction`, `auxiliary`), `-agent-type`, `-compaction` (`auto`, `manual`, `reactive`), `-context-compacted`, `-prompt-id`, `-prev-tool-durations`.
- `x-claude-code-context-compacted` is documented as "the conversation prefix before this request is no longer used, so a cache keyed on it can be dropped".

Gateway behaviour and the postmortem:

- The two-block system prompt with separate breakpoints (`SYSTEM_PROMPT_DYNAMIC_BOUNDARY`) is only sent to the Claude API and Platform on AWS; behind any other endpoint or gateway Claude Code sends one system block.
- Claude Code's 1h TTL needs an `extended-cache-ttl` beta value forwarded through Messages-format gateways. Whether the upstream still requires it is UNVERIFIED (Anthropic's caching page shows only `ttl`).
- Anthropic's 2026-04-23 postmortem: a caching optimization ("clear old thinking after 1h idle" via `clear_thinking_20251015` with `keep:1`) fired on every turn instead of once. The agent became forgetful and burned usage limits through cache misses; the bug passed code review, unit/e2e tests and dogfooding.

### 3.11 Other Anthropic platforms [H]

Bedrock and Google Cloud: organization-level isolation; per-model minimums and usage-field names follow AWS docs (not fetched); on-demand compaction, cache diagnostics and inline tools are unavailable; effort changes clear the cache; `fallbacks` is unavailable (use the SDK middleware).

## 4. OpenAI

### 4.1 Regime A: GPT-5.5 and earlier [H cookbook "Prompt Caching 101/201"; spec]

Mechanics:

- Automatic for prompts >= 1024 tokens; hits in 128-token increments. The whole prefix is cacheable (messages, images, audio, tool definitions, structured-output schemas). Org-scoped, ZDR-eligible; only K/V tensors are persisted, text stays in memory.
- Retention: `in_memory` (typically 5-10 min idle, up to 1 h [M, docs summary]) or `24h` (KV offloaded to GPU-local storage). Older models default to 24h unless the org is ZDR (then `in_memory`); gpt-5.5 and later accept only `24h`.
- Discounts by family: gpt-4o 50%, gpt-4.1 75%, gpt-5.x 90%, realtime audio 98.75%; no write fee.
- Usage: chat `usage.prompt_tokens_details.cached_tokens` (inside `prompt_tokens`); with `stream_options:{include_usage:true}` an extra chunk before `[DONE]` has empty `choices` and the whole-request `usage`. Responses: `usage.input_tokens_details.cached_tokens`, delivered in the `response.completed` event.

Routing:

- Requests go to engines by a hash of the first ~256 tokens; `prompt_cache_key` (<=64 chars) is combined with it.
- One engine handles ~15 requests/min per prefix+key. Overflow spills to other machines, each a one-time miss. Treat the key like a shard key.
- Per-user keys suit related conversations, per-conversation keys suit many unrelated threads, or use `hash(id) mod buckets` to stay near 15 RPM. Best-effort, never guaranteed.
- A coding customer went from 60% to 87% hit rate with `prompt_cache_key`.

Other facts:

- Flex tier (Responses, `service_tier:"flex"`) with extended retention and a key gave +8.5% hit rate vs Batch on 10,000 identical requests (-23% input cost). Batch lacks caching parity before GPT-5.
- Invalidators: tool schema key or order changes, instructions, `text.format`, reasoning effort, images, `truncation:"auto"` or any drop/summarize/compact of earlier turns, timestamps early in the prompt, Chat Completions with reasoning models (CoT not persisted).
- Put debugging timestamps in `metadata`; use the `allowed_tools` `tool_choice` to restrict tools per call without touching `tools`.

### 4.2 Regime B: GPT-5.6 and later (incl. GPT-6, GPT-6.1) [H parameters from spec 2.3.0 + SDK 3.22.0; M prices; L community]

Parameters:

- `prompt_cache_options{mode, ttl, prewarm, comparison_response_id}` on Chat Completions and Responses.
- `mode` is `implicit` (default: one implicit breakpoint at the end of the latest eligible message plus up to the latest 3 explicit) or `explicit` (no implicit; latest 4 explicit; with none, no caching).
- `ttl:"30m"` is the only value and is a MINIMUM lifetime for every breakpoint (the backend may keep entries longer). `prewarm:true` prepares the cache with no output (forces `generate:false`).
- `prompt_cache_breakpoint:{mode:"explicit"}` sits on content parts (text, image, file, audio, computer screenshot) and marks the exact end of a reusable prefix, not rounded to a token block.
- Matching considers the latest 80 breakpoints "without a content-block lookback limit". Minimum 1024 tokens [M].
- There is no breakpoint slot on `tools[]` or the `instructions` string; mark the end of pinned layers with a developer message built from content parts (inference).

Billing and usage [M]:

- Writes 1.25x input (implicit and explicit), reads 0.1x (0.05x on gpt-6.1-sol), input above 272K tokens 2x.
- `input_tokens_details.cache_write_tokens` (Responses) / `prompt_tokens_details.cache_write_tokens` (Chat) are counted inside the input total. Trackers that read only `cached_tokens` under-report cost.

Diagnostics:

- Send `comparison_response_id`; response `prompt_cache_diagnostics` is `cache_hit`, `cache_miss{reason,cache_missed_tokens,comparison_reusable_tokens}`, `comparison_response_not_found` or `unavailable`.
- Reasons: `model_changed`, `prompt_cache_key_changed`, `tools_changed`, `text_format_changed`, `reasoning_effort_changed`, `verbosity_changed`, `context_compacted`, `input_changed`, `service_tier_changed`.

Community reports [L]:

- A breakpoint on `function_call_output` is accepted but never writes; a strict `json_schema` response format suppressed writes on Chat Completions.
- Tool loops (several requests per user turn) easily exceed 15 RPM per key.
- One example reached 98.6% hits with a single explicit breakpoint on the last stable `input_text` (warm writes 123-128 tokens).

### 4.3 Responses vs Chat, reasoning replay [H spec; L error text]

- Responses persists reasoning via `previous_response_id` (with `store`, default true) or, statelessly, `include:["reasoning.encrypted_content"]`. OpenAI reports 40-80% better cache utilization than Chat Completions for reasoning models.
- With `previous_response_id`, earlier `instructions` are NOT carried over.
- A `function_call` must be replayed with its reasoning item (order: reasoning, function_call, function_call_output); dropping the reasoning item yields "function_call was provided without its required reasoning item" [L].
- `reasoning.context` (`auto`, `current_turn`, `all_turns`) controls which earlier reasoning items are rendered back. The GPT-5.6 family defaults to `all_turns`, earlier models to `current_turn`; the cache effect (older reasoning stops being rendered at a new user turn) is my inference.

### 4.4 Compaction and truncation [H spec]

- `context_management:[{"type":"compaction","compact_threshold":N}]` (N >= 1000) compacts server-side.
- `POST /v1/responses/compact` takes `model`, `input` (string or up to 131,072 items), `previous_response_id`, `instructions`, `prompt_cache_key`, `prompt_cache_options`, `service_tier` and returns a `compaction` item with opaque `encrypted_content` (works under ZDR). The miss reason afterwards is `context_compacted`.
- `truncation:"auto"` (marked deprecated in the spec) drops the oldest items and shifts the prefix every turn.
- The cookbook says outright: "context engineering and prompt caching are inherently at odds". Realtime `retention_ratio` (e.g. 0.7 drops ~30% in one event) shows the fix: evict in big, rare batches.

### 4.5 Effort, verbosity, tools [H spec]

- `reasoning.effort`: `none`, `minimal`, `low`, `medium` (default), `high`, `xhigh`, `max` (per-model support varies); `reasoning.summary` is `auto`, `concise` or `detailed`; `text.verbosity` is `low`, `medium` (default) or `high`. Effort and verbosity are cache-miss reasons.
- Tools: function (`strict`), `custom` (free-form; grammar `lark` or `regex`), `apply_patch` (create/delete/update files with unified diffs), `shell` (`environment` `container_auto`, `local`, `container_reference`), `tool_search`, namespaces, MCP.
- `allowed_tools` `tool_choice` is cache-safe. `service_tier` includes `flex`, `priority` and the access-controlled `ultrafast` (gpt-5.6-sol).
- A beta `responses_multi_agent=v1` (server-side sub-agents, default 6 concurrent) exists; its cache behaviour is UNVERIFIED.

### 4.6 WebSocket mode and what Codex does [M - beta SDK types, Codex source]

- WebSocket `response.create` continues via `previous_response_id`. Codex reuses that only if model, instructions, tools, tool_choice, parallel_tool_calls, reasoning, store, include, service_tier, `prompt_cache_key` and text are unchanged.
- A prewarm is `response.create` with `generate=false`. `x-codex-turn-state` (server-issued sticky-routing token) must be replayed for every request in a turn and not across turns.
- `response.steer` queues user input onto the active response (not for conversation-bound responses or automatic compaction; ends with `incomplete_details.reason:"steered"` then an automatic successor). Relevant to a hot-tail design.
- Codex uses `store:false` plus encrypted reasoning; `prompt_cache_key` = session id (internal helper sessions use `{source}:{parent_thread_id}`); "ChatGPT derives cache affinity from the session-id header".
- UNVERIFIED for 5.6+: concurrency semantics (when an entry is readable relative to an in-flight request), isolation scope (org vs project), implicit-breakpoint granularity, whether the 15 RPM guidance still holds.

## 5. Other hosted models (mostly [M]/[L]; vendor sites blocked)

Streaming usage placement was not verified for any provider in this section; OpenAI-compatible endpoints generally need `stream_options.include_usage` (UNVERIFIED per provider).

**Gemini** [M]

- Implicit caching is on by default for 2.5+ (no guarantee). Explicit `cachedContents` resources: `CreateCachedContentConfig` takes `ttl` (duration string such as "3.5s") or `expire_time`, plus `contents`, `system_instruction`, `tools`, `tool_config`.
- Default TTL 60 min. Minimum 4096 tokens on 3.x Pro/Flash entries, 2048 on 2.5. Storage is billed per token-hour (search summary: ~$4.50/M-token-hour on 3.1 Pro, ~$1.00 on Flash 3.5). Input above 200K is 2x (cost map).
- LiteLLM source: the API rejects cached contents that END on a model turn (assistant or tool result) with 400 "Requests ending with a model turn are not supported", so an explicit cache cannot be cut mid tool loop.

**DeepSeek** [M/L]

- On by default; 64-token storage units; only exact prefixes from token 0 hit; best-effort; unused entries cleared after hours to days.
- Usage `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` (confirmed in LiteLLM source).
- V4.x prices (search summary): Flash off-peak $0.003 cached / $0.15 miss, Pro $0.022 / $0.66, peak = 2x (peak windows [L]).

**xAI Grok** [M]

- Automatic. `x-grok-conv-id` header (Chat Completions) or `prompt_cache_key` (Responses) routes a conversation to one server because entries are stored per server.
- Docs: never edit or reorder earlier messages; for reasoning models re-send `reasoning_content` (omitting it is "the top cause of cache misses"). Read 0.15-0.25x by model.

**Moonshot Kimi** [L]

- Automatic prefix cache for k2.x/k3; usage `cached_tokens` top-level and nested, plus `cache_write_tokens`.
- `prompt_cache_key` is a scheduling hint that routes to the same cluster and is ignored for cache decisions. TTL is not user-selectable (a feature request asks for it). k3 $3/$15 with $0.30 cached.
- A forum report says cached tokens drop with interleaved thinking plus tools.

**Alibaba Qwen (Model Studio)** [M]

- Anthropic-style explicit `cache_control:{type:"ephemeral"}`, at most 4 markers, 20-block lookback, 1024-token minimum, 5-minute validity. Qwen3.5+ supports only message-level breakpoints (several markers in one message collapse into one).
- Explicit write ~1.25x / read ~0.1x; implicit write 1.0x / read ~0.2x, hit not guaranteed. LiteLLM's dashscope provider keeps `cache_control`.

**Mistral** [L]

- Automatic; the same `prompt_cache_key` for likely-shared prefixes raises hit chance (no guarantee). Cached tokens billed at ~10%; `usage.prompt_tokens_details.cached_tokens`. Minimum, TTL, isolation UNVERIFIED.

**Z.ai GLM** [L]

- Automatic context caching (GLM-4.5 to 5.x), `usage.prompt_tokens_details.cached_tokens`; GLM-5 cached $0.20 vs $1.00 input. A "cached input storage" charge exists, unit UNVERIFIED.

**MiniMax** [L]

- M3 passive caching on `/v1/chat/completions`; explicit `cache_control` on the Anthropic-compatible endpoint (`cache_read_input_tokens`, `cache_creation_input_tokens`, >=512 tokens).
- Community report: the M3 Anthropic endpoint ignored `cache_control` (billed $0.60 instead of a $0.12 read). Test both endpoints. M3 list price conflicts between sources.

## 6. Gateways [H LiteLLM (source), M others]

**Rules any Messages-format gateway must follow (Anthropic's Claude Code gateway guide [H])**

- Forward `anthropic-beta` and `anthropic-version` verbatim; never allowlist values (the set changes with every Claude Code release).
- Forward `cache_control` wherever it appears (system blocks, messages, mid-conversation `role:system`); never convert block-form `system` or content into strings.
- Forward error bodies unmodified (Claude Code's capability-rejection retries match on the upstream wording). Do not buffer streams; forward `ping` events (Claude Code aborts after 300 s of silence).
- A gateway that strips a beta header but passes its body field produces hard 400s; stripping both silently disables the feature.
- If the gateway rejects `cache_control` on Claude Code's mid-conversation system-context block with a 400, Claude Code moves the marker onto the last conversation message (that block then bills uncached, the conversation stays cached). If markers are silently removed, all history bills uncached.
- The system-prompt attribution block (client version + fingerprint) is stripped positionally only on api.anthropic.com; a gateway that reshapes `system` should set `CLAUDE_CODE_ATTRIBUTION_HEADER=0`.

**LiteLLM** [H, v1.103.1]

- Header allow-list `litellm/anthropic_beta_headers_config.json`; unknown values are dropped with only a `verbose_logger.debug` line.
- Present: `compact-2026-01-12`, `context-management-2025-06-27`, `prompt-caching-scope-2026-01-05`, `thinking-binding-controls-2026-08-01`, `interleaved-thinking-2025-05-14`, `fine-grained-tool-streaming-2025-05-14`, `advanced-tool-use-2025-11-20`.
- Absent: `compact-2026-09-04`, `inline-tools-2026-09-15`, `mcp-client-2026-09-15`, `mid-conversation-system-clear-at-2026-08-21`, `mid-conversation-output-config-2026-07-01`, `mid-conversation-tool-changes-2026-07-01`, `server-side-fallback-2026-07-01`, `fallback-credit-2026-07-01`, `thinking-display-updates-2026-08-18`, `extended-cache-ttl-2025-04-11`.
- The map is fetched remotely by default (`LITELLM_LOCAL_ANTHROPIC_BETA_HEADERS=True` pins the local copy).
- OpenAI-format usage: `prompt_tokens` = input + cache read + cache creation; `prompt_tokens_details.cached_tokens`; `cache_creation_tokens`.
- `cache_control_injection_points` can inject markers; router pre-call checks give deployment/session affinity; `supports_prompt_cache_breakpoint` is set for the gpt-5.6/gpt-6 family.
- `_drop_unsignable_thinking_blocks` silently drops thinking blocks without a valid signature (a client-side edit, so it also breaks preserved thinking).

**Portkey OSS gateway** [M, main branch]

- Chat-completions to Anthropic emits `cache_control:{type:"ephemeral"}` only, so a requested `ttl:"1h"` is dropped.
- `prompt_tokens` = Anthropic `input_tokens` only while `total_tokens` adds cache tokens (inconsistent with OpenAI).
- `loadbalance` is random weighted (fragments caches across keys). `anthropic-beta` defaults to `messages-2023-12-15` unless configured. Thinking returns as non-standard `content_blocks` the client must echo. Hosted Portkey may differ.

**OpenRouter** [M]

- Sticky routing key `session_id` / `x-session-id` (stickiness starts after the first success; without it only after a cache hit is seen; sessions expire after ~10 min idle [L]).
- OpenRouter adds a top-level `cache_control` for `anthropic/` models [L]. Usage reports `cached_tokens` and `cache_write_tokens`.
- Its Anthropic-style `/messages` surface accepts `thinking.block_binding.prefix_mismatch_behavior`, `context_management`, `speed`, `fallbacks` (SDK 1.4.7); the chat surface returns `reasoning_details` that must be echoed back.

**Vercel AI Gateway** [L]

- `providerOptions.gateway.caching:"auto"` adds Anthropic breakpoints with the 5-minute TTL only; explicit `cache_control` passes through.
- `order`/`only` fail-over can move a conversation to another provider (cold cache); sticky behaviour UNVERIFIED.

**Cloudflare AI Gateway** [M]

- "Caching" is an exact-match response cache (SHA-256 over provider, endpoint, model, auth and full body; disabled by default; `cf-aig-cache-ttl` 60 s to 1 month; `cf-aig-cache-status`). It is not KV caching.
- Native provider endpoints forward bodies so provider caching still works; the unified/compat endpoint's handling of `cache_control` is UNVERIFIED.

## 7. Open-source serving [H unless noted]

**vLLM V1**

- Hash-chained blocks of 16 tokens (`hash(parent, block tokens, extras)`; extras = LoRA id, multimodal hashes, `cache_salt`). Only full blocks are cached, and a full block is reusable immediately, even by requests in the same batch.
- LRU free queue (tail blocks freed first). `--prefix-caching-hash-algo` is `sha256` (default since v0.11), `sha256_cbor`, `xxhash` or `xxhash_cbor` (non-cryptographic: collision/leak risk in multi-tenant).
- `cache_salt` isolates tenants; KV offload via a native or LMCache backend; no pin/priority primitive.

**SGLang 0.5.20**

- RadixAttention (`RadixKey` with extra key/salt, `page_size` default 1, matches rounded down to the page). Eviction `lru`, `lfu`, `slru`, `priority`.
- `--schedule-policy` choices `fcfs` (default), `lpm`, `dfs-weight`, `hrrn`, `random`, `lof`, `priority` (needs `--enable-priority-scheduling`), `routing-key`; HiCache host/storage tiers; a model gateway does cache-aware routing.
- `usage.prompt_tokens_details.cached_tokens` only with `--enable-cache-report` (default off).

**TensorRT-LLM**

- Radix tree of blocks (block size a power of two, `enable_block_reuse` default true); only leaf blocks are evictable.
- Prioritized LRU with priority 0-100 (default 35), per-token-range `TokenRangeRetentionConfig` plus `duration_ms` after which priority reverts, `decode_retention_policy` for generated tokens.
- Host offload (`host_cache_size`, `secondary_offload_min_priority` 35); `cache_salt` mixed into a SHA-256 block-key hash; KV events (`BlockStored`, `BlockRemoved`) for external routers.
- The only stack here with first-class pinning of layered prefixes.

**LMCache** [M, README only]

- Engine-independent tiered KV store (CPU, disk, Redis/Valkey, Mooncake, S3-compatible, NIXL, GDS) that survives engine restarts; P2P sharing; CacheBlend non-prefix reuse (recomputes selected tokens).

## 8. Empirical evidence on savings and compaction

**Manus** (secondary write-up of the primary post, which was blocked) [L]

- KV-hit rate is "the single most important metric"; input:output ~100:1; cached $0.30 vs uncached $3.00 per MTok on Claude Sonnet (10x).
- Practices: append-only context, deterministic JSON key order, no timestamps in the prompt, mask tools by logit instead of removing them, file system as memory.
- "Healthy agent sustains 80%+ hit rate after warm-up" is the write-up author's inference, not a Manus figure.

**Anthropic, Claude Code lessons (2026-04-30)** [H]

- "We run alerts on our prompt cache hit rate and declare SEVs if they're too low"; layered static-to-dynamic prompt; messages instead of system edits.
- Never switch models mid-session: a 100K-token Opus session costs more to answer on Haiku than to stay on Opus. Compaction as a cache-safe fork.

**Anthropic, Opus 5.5 posts (2026-09-24/25)** [H]

- Context per request grew 2.6x from March to September 2026; input:output moved from 189:1 to 324:1; cache reads are "the majority of agentic and coding work costs"; Claude Code cut uncached input by more than 50% while context grew.
- Worked example (Opus 5.5 list prices): 2.8M input tokens for a 40-turn task cost $11.20 uncached, $1.62 at a 90% hit rate, $0.99 at 96%.
- At 120K context a 5m write is ~$0.60 vs a read ~$0.02 (one write = 25 reads). 30 turns at 150K spend $0.90 on reads alone vs $0.12 at 20K.
- Compaction at 150K ~$0.25 with break-even ~10 turns; cold-cache compaction ~$0.75; a compaction right before finishing costs more than it saves.

**OpenAI cookbook** [H]

- 60% to 87% hit rate with `prompt_cache_key`; Responses API 40-80% better cache utilization; +8.5% hit rate for Flex vs Batch (-23% input cost).
- Batching truncation (`retention_ratio`) beats per-turn truncation.

## 9. Design implications for a layered-cache harness

### 9.1 Layer placement per provider

| Sleipnir layer | Anthropic | OpenAI >=5.6 | OpenAI <=5.5 / DeepSeek / xAI / Kimi / GLM / Mistral | Gemini |
|---|---|---|---|---|
| tools (frozen superset) | `tools`, sorted keys | `tools[]`, same order | `tools[]`, same order | `tools` |
| L0 shared context | `system` block 1, breakpoint 1 | developer message content part + `prompt_cache_breakpoint` | first messages; stable bytes | implicit (or explicit cache for a huge fixed document) |
| L1 role-pinned | `system` block 2, breakpoint 2 | content part + breakpoint | stable text; routing key per role | implicit |
| L2 agent-pinned | `system` block 3 or first user message, breakpoint 3 | content part + breakpoint | stable text | implicit |
| L3 rolling transcript | `messages`, moving breakpoint 4 | implicit breakpoint at latest message | append-only | append-only |
| L4 hot tail | turn-scoped `role:system` (`clear_at`) after the last breakpoint | text after the last breakpoint | text after the last stable point | text at the end |

### 9.2 Design items

**D1. Map layers onto the prefix order and the 4-breakpoint budget (Anthropic).**

- Order: tools (frozen superset), L0, L1, L2, L3, L4. Breakpoints: end of L0, end of L1, end of L2, and one moving breakpoint at the end of the transcript just before the hot tail (explicit, or the automatic one if L0-L2 use the other three).
- Do not spend a slot on tools alone. L4 sits after the last breakpoint, so it is billed as `input_tokens` and never enters a hash.
- L1/L2 can be extra `system` blocks or the first user message; either way they are prefix. [H]

**D2. Treat (tools, L0-L2, transcript) as append-only within an epoch; make every rewrite an epoch boundary.**

- On Fable 5.1, Opus 5.5 and Sonnet 5.5 any earlier-byte edit kills later thinking (400 on new accounts). Compactor and promotion agents must never patch in place.
- Allowed rewrites, best first: (a) server-signed on-demand compaction with a keep-tail cut at a request boundary, same `system`/`tools`, background swap; (b) "simple compaction": open a new conversation whose first user message is the summary, replay no thinking (Anthropic's recommended default); (c) `drop_block` and accept lost reasoning, logging every `input_transformations` entry; (d) server-side `clear_tool_uses` for old tool results (ignored by the check).
- Never cut middle turns, never self-write a summary and keep recent thinking, never compact between `tool_use` and `tool_result`. [H]

**D3. Promote facts by appending, fold at epochs.**

- A promoted fact becomes an appended `role:system` message ("promotion delta": cacheable, permanent) instead of an edit to L1/L2. At the next epoch boundary fold accumulated deltas into a fresh L1/L2.
- A pinned-layer rewrite costs a full re-write of everything behind it (1.25x/2x), so batch promotions. The memory tool is the zero-prefix-edit alternative: facts live in files read through tool calls (transcript region).
- Let the working agent emit promotion candidates as it goes (memory tool or a structured note) instead of a separate reader agent re-reading the whole transcript uncached (inference from per-model, per-prefix caches).
- Deltas inside a compacted range stop applying: restate durable ones right after the first new user turn. On models without mid-conversation system messages (Sonnet 5, Opus 4.7/4.6, Haiku 4.5) fall back to user-turn text or epoch rewrites. [H]

**D4. Hot tail = turn-scoped system message.**

- Append the always-fresh block after each `tool_result` message as `role:system` with `clear_at:"next_user_message"`. Earlier copies stay in the array byte-identical and render nothing, the cache keeps matching, and later thinking stays valid.
- Needs beta `mid-conversation-system-clear-at-2026-08-21`, a supporting model, and a gateway that passes the field.
- Without it, a text block appended and then deleted is an edit (invalid thinking): keep it permanently (append-only) or put it inside the tool result. On OpenAI use `response.steer` or a fresh developer item after the last breakpoint. [H]

**D5. Fan-out: single-flight pre-warm, then release.**

- Key = (model, thinking config, effort, tools, L0, L1). Send one `max_tokens:0` request with explicit breakpoints at the ends of L0 and L1, wait for its response to begin, then launch siblings; cap the hold (~5 s, like Claude Code).
- All siblings must be byte-identical up to the breakpoint or they fragment the cache.
- Use 1h TTL on L0/L1 (1h entries must come first) and 5m or 1h on the rest; ping shared layers before they idle out.
- Use forks (replay the parent's history unchanged) for helpers that need parent context: they read the parent's cache and keep valid thinking, while fresh agents with pasted summaries do not. [H]

**D6. Freeze what the prefix hashes.**

- One tools array for all roles (superset, sorted keys, deterministic serialization, `defer_loading` for rarely used tools) so L0 is shared across roles; enforce role differences with permissions and, if needed, `tool_addition`/`tool_removal` in the L1 message.
- Do not vary model, thinking config, `tool_choice`, `disable_parallel_tool_use`, image presence, `speed` or top-level effort inside a cached conversation; change effort with per-message effort.
- A server-side fallback is a model switch (rebuild, or fallback credit). Switch models with a sub-agent hand-off, not mid-conversation. [H]

**D7. Compaction economics.**

- Compact only when (turns remaining) x (read cost saved per turn) exceeds (summary call + re-write of the shorter prefix). Anthropic's Opus 5.5 numbers put break-even near 10 turns at 150K.
- Run it warm (right after a turn, not after an idle gap), same prefix, in the background, and rarely: few large evictions beat many small ones (OpenAI `retention_ratio`, Claude Code batching image removal). Reserve output headroom for the summary.
- Run the compactor on the SAME model with the SAME system/tools (cache-safe fork). A cheaper-model compactor or a fresh agent with its own system prompt pays a full uncached read of the transcript, and on preserved-thinking models it also breaks kept-turn thinking.
- Compaction loses images/documents and `role:system` instructions: restate them. [H]

**D8. Provider adapter contract.**

- Keep the harness cache-agnostic: layers with boundaries and a stability class. Each adapter renders a capability record (`mode`, `max_breakpoints`, `min_prefix`, `ttl_options`, read/write multipliers, usage-field mapping, `prewarm`, `diagnostics`, routing-key param, concurrency rule; see the JSON).
- Anthropic and Qwen render `cache_control` (Qwen message-level); OpenAI >=5.6 renders `prompt_cache_breakpoint` plus `prompt_cache_key` (+ `mode:"explicit"` to control writes); OpenAI <=5.5, DeepSeek, xAI, Kimi, Mistral and GLM get a stable prefix plus the routing hint; Gemini uses implicit caching, explicit `cachedContents` only for large long-lived documents that end on a user turn.
- Compute costs with per-model read/write multipliers, not a global 10%. [H/M]

**D9. Normalize and monitor usage.**

- Anthropic `input_tokens` excludes cached and written tokens; OpenAI/xAI/Gemini `prompt_tokens`/`input_tokens` include cached tokens (OpenAI >=5.6 also includes `cache_write_tokens`); Portkey deviates.
- Store {cache_read, cache_write_5m, cache_write_1h, uncached, output} per request and hit ratio per layer. Classify misses (Claude Code: >5% and >=2,000 tokens re-processed) apart from expected rebuilds (compaction, epoch fold).
- Use Anthropic `diagnostics.previous_message_id` and OpenAI `comparison_response_id` in canary sessions; count `input_transformations` per session.
- Alert on hit-rate drops like an outage: Anthropic declares SEVs, and the 2026-04-23 postmortem shows a silent cache-motivated edit costing quality and quota. [H]

**D10. OpenAI specifics.**

- Shard `prompt_cache_key` so no prefix+key exceeds ~12 RPM, a margin under the documented ~15 (agent tool loops multiply requests): key = tenant + role + `hash(agent_id) mod B`, B = ceil(expected RPM / 12).
- Keep tools, instructions, `text.format`, effort, verbosity and service tier constant; pre-warm with `prompt_cache_options.prewarm` and wait before fanning out (readable-after semantics are undocumented, so verify by measurement).
- In `explicit` mode place breakpoints at layer ends (max 4 written); in `implicit` mode the latest message end is written every turn (1.25x on the delta, 0.1x on the rest, same shape as Anthropic).
- Prefer Responses with `store:false` + `include:["reasoning.encrypted_content"]` and replay reasoning items in order; use `allowed_tools` for per-turn tool limits; use `/responses/compact` or `context_management` for compaction (`context_compacted` is a miss).
- Read `cache_write_tokens` so the cost model is not blind to the 1.25x premium. [H/M]

**D11. Gateway canary suite (run against the marketplace gateway before trusting any number).**

1. Two identical >=min-prefix requests: the second shows `cache_read_input_tokens` > 0.
2. Block-form `system` with `cache_control` survives (not stringified).
3. `ttl:"1h"` yields `cache_creation.ephemeral_1h_input_tokens` > 0.
4. New betas pass: send `thinking-binding-controls-2026-08-01` with `block_binding` and check `input_transformations` returns; same for `compact-2026-09-04`, `inline-tools-2026-09-15`, `mid-conversation-system-clear-at-2026-08-21`.
5. `max_tokens:0` is accepted and returns `usage` with a write.
6. `diagnostics` round-trips.
7. A mid-conversation `role:system` message stays where placed.
8. Streaming keeps `message_start.usage`, cumulative `message_delta.usage` and `ping`s.
9. Preserved-thinking three-request self-test: an append-only turn is fine, a `system` edit yields 400 or `input_transformations`.
10. Error bodies pass unmodified.
11. OpenAI-format models: `prompt_tokens_details.cached_tokens`, `cache_write_tokens`, `prompt_cache_key` and `prompt_cache_breakpoint` pass.
12. Routing: repeated requests from one session reach one upstream/key (no random load balancing) and an `x-session-id`-style sticky key is honoured.

Also emit your own hint headers (session, agent, parent, request class, compaction epoch) as Claude Code does. [H rules; the gateway itself UNVERIFIED]

**D12. Isolation.**

- Anthropic cache is per workspace: keep a fleet in one workspace/key group to share L0/L1; separate workspaces are separate caches (a feature for tenant isolation, a cost otherwise). OpenAI is org-scoped.
- On self-hosted engines set `cache_salt` per tenant, pick cryptographic hashing, and use TensorRT-LLM retention priorities (or SGLang `priority`) to pin L0/L1. [H]

**D13. Determinism and persistence.**

- Serialize with fixed key order; no clocks, request IDs or per-user paths in L0-L2 (Claude Code's SDK moves them into the first user message).
- Persist exact request bodies and assistant `content` (empty `thinking` fields, signatures, `compaction` and `fallback` blocks) and replay them on resume; reference images/documents by `file_id`, not by mutable URL. [H]

**D14. TTL choice.**

- Shared layers read constantly stay warm on 5m; layers idle across long tool runs or human waits need 1h (2x write, pays back after 2 reads).
- Compaction, sub-agents and forks default to 5m in Claude Code; set them deliberately. Server tools inject 5m writes automatically. Reads refresh, so a periodic cheap read beats a fresh write. [H]

**D15. Tool-loop hygiene.**

- Tool results in one user message, results first; validate streamed tool JSON when using `eager_input_streaming`; handle `pause_turn` by resending unchanged; never put untrusted text in `role:system`.
- Prefer Anthropic-schema `bash`/`text_editor`/`memory` tools; give every request its own `fallbacks` configuration. [H]

## 10. Not verified, or verified only weakly

- Marketplace gateway behaviour for every feature above.
- Anthropic `cache_control.scope:"global"` and `prompt-caching-scope-2026-01-05` semantics; whether the upstream still needs an `extended-cache-ttl` beta for 1h TTL (Claude Code sends it; Anthropic docs show only `ttl`).
- OpenAI 5.6+: concurrency semantics, isolation (org vs project), implicit-breakpoint granularity, whether ~15 RPM still applies, exact retention behaviour beyond the 30m minimum; GPT-5.6 Sol non-promotional price ($5 vs $4 input).
- Gemini isolation and implicit TTL, exact 3.x minimums per model; DeepSeek isolation and peak-pricing windows; xAI minimum length and isolation; Kimi TTL and write billing; Qwen refresh-on-hit semantics; Mistral, Z.ai and MiniMax minimums, TTL and isolation.
- OpenRouter sticky-session lifetime, Vercel stickiness, hosted Portkey behaviour (only OSS main-branch source read), Cloudflare unified endpoint `cache_control` handling.
- vLLM per-response cached-token reporting; SGLang insert timing; LMCache internals (README only).
- Manus figures beyond the secondary write-up; OpenAI `responses_multi_agent` cache behaviour.

## 11. Sources

**Anthropic docs (base `https://platform.claude.com/docs/en/`)**: `build-with-claude/prompt-caching`, `build-with-claude/cache-diagnostics`, `build-with-claude/mid-conversation-system-messages`, `build-with-claude/preserved-thinking`, `build-with-claude/compaction`, `build-with-claude/compaction-on-demand`, `build-with-claude/compaction-background`, `build-with-claude/compaction-keep-recent-turns`, `build-with-claude/compaction-thinking-blocks`, `build-with-claude/compaction-threshold`, `build-with-claude/context-editing`, `build-with-claude/handling-stop-reasons`, `build-with-claude/refusals-and-fallback`, `build-with-claude/fallback-credit`, `build-with-claude/streaming`, `build-with-claude/thinking`, `build-with-claude/effort`, `build-with-claude/batch-processing`, `agents-and-tools/tool-use/` (`tool-use-with-prompt-caching`, `fine-grained-tool-streaming`, `parallel-tool-use`, `handle-tool-calls`, `how-tool-use-works`, `manage-tool-context`, `memory-tool`, `tool-search-tool`), `about-claude/pricing`, `models/overview`, `api/rate-limits`, `api/beta-headers`.

**Anthropic harness docs and posts**: https://code.claude.com/docs/en/prompt-caching | https://code.claude.com/docs/en/llm-gateway-protocol | https://code.claude.com/docs/en/workflows | https://code.claude.com/docs/en/agent-sdk/modifying-system-prompts | https://code.claude.com/docs/en/costs | https://claude.com/blog/lessons-from-building-claude-code-prompt-caching-is-everything | https://claude.com/blog/what-a-task-costs-on-opus-5-5 | https://claude.com/blog/claude-opus-5-5-built-for-coding-sessions-that-use-more-context | https://www.anthropic.com/engineering/april-23-postmortem

**OpenAI**: https://raw.githubusercontent.com/openai/openai-openapi/master/openapi.yaml (spec 2.3.0) | https://pypi.org/project/openai/3.22.0/ | https://raw.githubusercontent.com/openai/openai-cookbook/main/examples/Prompt_Caching_201.ipynb | https://raw.githubusercontent.com/openai/openai-cookbook/main/examples/Prompt_Caching101.ipynb | https://developers.openai.com/api/docs/guides/prompt-caching (blocked; search summary only) | https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/src/client.rs | https://community.openai.com/t/5-6-prompt-caching-has-performance-issues/1390316 | https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json (pricing cross-check)

**Other hosted (docs blocked; search summaries and SDK/source)**: https://ai.google.dev/gemini-api/docs/caching | https://pypi.org/project/google-genai/2.25.0/ | https://api-docs.deepseek.com/guides/kv_cache/ | https://docs.x.ai/developers/advanced-api-usage/prompt-caching | https://docs.x.ai/developers/advanced-api-usage/prompt-caching/maximizing-cache-hits | https://platform.kimi.ai/docs/api/chat | https://www.alibabacloud.com/help/en/model-studio/context-cache | https://www.alibabacloud.com/help/en/model-studio/explicit-cache-best-practice | https://docs.mistral.ai/studio-api/conversations/advanced/prompt-caching | https://docs.z.ai/guides/capabilities/cache | https://platform.minimax.io/docs/api-reference/text-prompt-caching | https://platform.minimax.io/docs/api-reference/anthropic-api-compatible-cache | https://raw.githubusercontent.com/rwese/pi-minimax-m3-caching-fix/main/README.md

**Gateways**: https://pypi.org/project/litellm/1.103.1/ | https://www.npmjs.com/package/@openrouter/sdk | https://openrouter.ai/docs/guides/best-practices/prompt-caching | https://raw.githubusercontent.com/Portkey-AI/gateway/main/src/providers/anthropic/chatComplete.ts | https://vercel.com/docs/ai-gateway/models-and-providers/automatic-caching | https://www.npmjs.com/package/@ai-sdk/gateway | https://developers.cloudflare.com/ai-gateway/features/caching/ | https://developers.cloudflare.com/ai-gateway/usage/providers/anthropic/

**Open-source serving**: https://raw.githubusercontent.com/vllm-project/vllm/main/docs/design/prefix_caching.md | https://raw.githubusercontent.com/vllm-project/vllm/main/vllm/config/cache.py | https://pypi.org/project/sglang/0.5.20/ | https://raw.githubusercontent.com/sgl-project/sglang/main/python/sglang/srt/mem_cache/radix_cache.py | https://raw.githubusercontent.com/NVIDIA/TensorRT-LLM/main/docs/source/features/kvcache.md | https://raw.githubusercontent.com/LMCache/LMCache/dev/README.md

**Empirical (secondary)**: https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus (blocked) | https://github.com/akkupratap323/manus-decoded/tree/main/layers/08-context-engineering (write-up actually read)
