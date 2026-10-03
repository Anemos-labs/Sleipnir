# Prompt and cache design

Sleipnir controls prompt content and request structure. The provider controls
cache writes, placement, retention, and reads. Shared bytes make reuse possible;
they do not guarantee it.

## Layers

| Layer | Content | Change policy |
|---|---|---|
| G0 | Constitution and universal tool list | Session configuration |
| G1 | Project map and instruction files | Shared epoch |
| G2 | Role instructions | Role epoch |
| G3 | Private notes | Compaction |
| G4 | Summary spine | Compaction |
| G5 | Conversation and tool results | Append between rebases |
| G6 | Task board and relevant team updates | Route-dependent |

Stable layers come first. All agents send the same tool list; permissions apply
at execution time. Shared and role context must remain concise because it is
sent on every request.

## Rendering

`internal/kv.Render` produces the constitution, pinned context, thread, and
live notices. The provider adapter turns this into the final API payload.

Text identity is insufficient when a provider caches only at particular
boundaries. A message ending can be lost by extending or shortening a message,
even if most of its text remains unchanged.

### Live notices

- `HotInline` appends a temporary tail after persistent context. Use it only
  where the cache can reuse shorter prefixes and reasoning bindings permit it.
- `HotPersist` writes changed notices into history. Earlier notices stay
  unchanged until compaction. The update interval limits context growth.
- `HotTurnScoped` uses persistent system messages with provider-supported
  turn scope.

Responses routes select `HotPersist`. Removing a temporary user message on the
next request can remove the implicit cache boundary that was just written.
The integration test in `internal/agent/responses_cache_test.go` compares actual
Responses input items across a tool loop.

[OpenAI cache semantics](https://developers.openai.com/api/docs/guides/prompt-caching)
describe message-boundary lookup on supported models. Feature availability
must still be checked on the selected account and endpoint.

## Explicit cache markers

The marker planner uses provider capabilities: minimum prefix length, marker
count, lookback, and supported lifetimes. Anthropic-style routes can place
markers after stable layers and at a rolling thread boundary.

Automatic caching is not interchangeable with explicit-marker caching.
A mock that stores every token prefix cannot validate an API that writes only
at message endings. Gate expectations and hit estimates must reflect those
differences.

## Compaction

The planner chooses whether and when to replace old context. A compactor
proposes a patch; deterministic validation protects user instructions, tool
pairs, and retained history. Mechanical masking provides a fallback.

Committing a patch changes private prefix bytes and can require a cache rewrite.
The archive and `recall` retain older conversation content. Compaction can reduce
total input even when the immediate hit rate falls.

Compaction, shared-context updates, and reasoning-binding recovery are declared
rebases. Bound reasoning is removed atomically with incompatible history changes.

## Team requests

A warm gate limits simultaneous cold starts. It waits for generated response
content or completion, rather than a Responses queue/acceptance event.
This is a scheduling heuristic; it does not confirm a cache entry exists.
Independent server slots or replicas may each need a cold prefill. Warming one
does not ensure the next concurrent request reaches that cache.

Cache keys group related requests. They use the whole session identity and a
stable shared-prefix hash. Providers may use them as routing hints; they do not
pin requests to a machine. Shards distribute requests across key groups.

The Responses adapter sends the same key in `prompt_cache_key` and the
`session-id` header, matching the session routing mechanism in
[OpenAI's client](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/requests/headers.rs).
Sending only the body field is insufficient to establish session affinity on
every Responses route.

Workers share only the context before their first difference. Role instructions,
private notes, assignments, model choice, and provider serialization can limit
reuse. A new worker does not automatically inherit the manager's private cache.

## Accounting and diagnostics

Usage separates ordinary input, cache reads, cache writes, and output. Cache-hit
ratio is:

```text
cache_read_tokens / total_input_tokens
```

Aggregate counts before computing the session ratio; do not average percentages
from differently sized requests. Cache writes are part of total input, not
additional input tokens.

The guard compares internal prompt blocks and reports undeclared changes. Its
expected reads are estimates, not measured server behavior. A low-hit event
without internal drift leaves several possible causes: API serialization,
missing boundaries, eviction, routing, or unsupported cache behavior.

For message-boundary routes, the guard only estimates reuse of a complete
eligible ending from the previous request. It does not assume every logical
layer has an implicit server entry. Explicit-marker and token-prefix routes
retain their own partial-prefix calculations.

Only provider-reported cached tokens count as observed hits. Unknown prices
must not be presented as evidence that work was free. Subscription allowances
are separate from API-dollar estimates.

## Validation

Check stable request fields, complete message boundaries, reasoning replay,
tool-result order, and the selected provider profile. Compare sequential
requests before increasing concurrency. Test cancellation, compaction, resume,
and worker fan-out separately.

`sleipnir sim` models synthetic workloads. Its assumptions and cost tables are
in [Cache economics](CACHE-ECONOMICS.md). Use [Validation](VALIDATION.md) for real
endpoints and [Benchmarks](BENCHMARKS.md) for task comparisons. Neither internal
hash agreement nor a mock hit rate proves real savings.
