# Endpoint validation

Use real endpoint requests to evaluate interoperability and cache behavior.
Probes consume the selected account's allowance. Mock providers and simulations
cannot establish production hit rates.

## Procedure

1. Run `sleipnir doctor --model PROVIDER/MODEL` to check basic communication.
   `--deep` sends additional cache, tool, reasoning, and concurrency probes.
2. Run a small task with several sequential tool calls. Verify the resulting
   files independently.
3. Inspect request and response events. Record total input, cached input, cache
   writes when reported, output, latency, and configured prices.
4. Repeat with a team, then with compaction and session resume. Change one
   variable at a time.
5. Compare task success and total resource use, not only cache percentage.

`scripts/validate.sh` automates fixture runs. Read its help and selected model
before running it. Its cache thresholds are diagnostic targets, not guaranteed
provider behavior.

## Cache investigation

- Compare the final API payload, including instructions, tool definitions,
  content-part ordering, complete message endings, and reasoning items.
- Confirm whether the route uses token-prefix caching, implicit message
  boundaries, or explicit cache markers.
- Preserve the boundary written by a preceding request. Removing or extending
  that message can prevent reuse without changing the earlier text.
- Test sequential requests before concurrent workers. Separate cold starts,
  compaction, idle expiry, and model changes.
- Treat routing keys as hints. A stable key does not guarantee placement.
- Use provider diagnostics where supported. Feature support can differ between
  API-key and subscription routes.

The internal prefix guard cannot rule out adapter or endpoint causes.
A repeated request reporting zero cached tokens is evidence of a miss, not
evidence that a particular party caused it.

For message-boundary providers, `doctor` keeps the system text and earlier
messages intact and appends each response and follow-up. It does not infer a
cache-write block size from these usage counts. Its concurrent warm-up bursts
repeat the same complete message prefix. Token-prefix providers retain
the uneven text-growth probe used to estimate their reported token increment.

## Reporting

Keep raw logs private: they can contain prompts, paths, tool output, and code.
A useful result states the binary revision, endpoint, exact model, account type,
workload, concurrency, request count, token totals, and verification outcome.

Use token-weighted cache ratios. Do not claim dollar savings without applicable
prices, or extrapolate a short protocol probe into an end-to-end benchmark.
Repeat paired measurements when endpoint conditions vary.

`doctor` records concurrent warm-up requests alongside its sequential requests.
Its JSON `total_usd` is the sum of reported costs, with `cost_complete` indicating
whether every recorded request succeeded and supplied a cost. Missing cost data
does not mean free usage. Failed or unreported warm-up samples leave the warm-up
requirement undetermined.

## Self-hosted endpoints

Record the server build, model digest, chat template, context limit, cache mode,
and number of request slots. An OpenAI-compatible API does not establish which
cache behavior its server implements.

Compare a cold request, an identical repeat, appended conversation and tool
history, a concurrent worker batch, and a second batch with the same worker
prefixes. A server with independent slot caches may reuse a sequential prefix
while other slots still require cold prefills. The warm gate does not guarantee
reuse across every slot or replica; routing and cache sharing belong to the
server. Confirm reuse with reported usage and server prefill diagnostics.

Include cold requests in the overall token-weighted ratio and report warm phases
separately. Repeat after history restoration and compaction. A tiny model with
prescribed tool history can check serialization and token accounting, but cannot
establish tool selection, coding quality, or another model's cache behavior.

[OpenAI caching](https://developers.openai.com/api/docs/guides/prompt-caching) ·
[OpenAI cache diagnostics](https://developers.openai.com/api/docs/guides/prompt-caching/diagnostics) ·
[Benchmarks](BENCHMARKS.md)
