# Cache policy simulation

These synthetic scenarios compare policies under modeled cache behavior. They do
not predict an endpoint hit rate. See [validation](VALIDATION.md) for measurement.

<!-- SIM:BEGIN -->
`sleipnir sim` replays one synthetic swarm workload (20 workers, 40 tasks, about 32 work steps per task; an agent
without a shared pin spends 30k tokens orienting itself in the repository) under a plain harness, the same with
summary compaction, and the layered policy, pricing every request against an explicit model of the provider's cache.
Costs are in millions of input-token equivalents, lower is better. Regenerate with `make readme-sim`.

Provider with explicit cache breakpoints (reads 0.1x, writes 1.25x, 5 minute TTL):

```
scenario              naive  naive+sum   sleipnir  vs naive  vs n+sum  ctx n/s wall n/s
typical               25.4M      22.1M      14.9M      -41%      -33%  68/54 k 100/81 m
short tasks            8.5M       8.5M       4.3M      -49%      -49%  38/39 k  48/26 m
long tasks            96.8M      50.7M      47.2M      -51%       -7% 135/72 k 214/201m
cold launches         25.6M      22.3M      15.0M      -41%      -33%  68/54 k 203/179m
small repo            17.1M      16.4M      14.3M      -16%      -13%  51/52 k  86/81 m
bloated pins          25.4M      22.1M      20.8M      -18%       -6%  68/96 k 100/81 m
huge exploration      50.8M      32.5M      16.3M      -68%      -50% 106/60 k 130/81 m
```

Provider with an automatic prefix cache (reads 0.25x, no write premium, 3 engines behind a marketplace):

```
scenario              naive  naive+sum   sleipnir  vs naive  vs n+sum  ctx n/s wall n/s
typical               33.1M      28.4M      28.3M      -15%       -1%  68/71 k  99/81 m
short tasks            9.7M       9.7M       6.1M      -37%      -37%  38/41 k  48/26 m
long tasks           143.8M      68.5M     130.7M       -9%      +91% 135/131k 210/199m
cold launches         33.0M      28.4M      27.4M      -17%       -4%  68/70 k 201/178m
small repo            21.6M      20.5M      26.2M      +21%      +28%  51/65 k  85/81 m
bloated pins          33.1M      28.4M      42.8M      +30%      +51%  68/112k  99/81 m
huge exploration      67.6M      39.8M      33.3M      -51%      -16% 106/84 k 128/81 m
```

Columns: `ctx` is the average context per request in thousands of tokens and `wall` the simulated duration in minutes,
each as naive/sleipnir. The rows: `short tasks` (about 8 work steps each: pins and orientation dominate), `long tasks` (about 90: compaction
dominates), `cold launches` (three launches 12 idle minutes apart, so every launch starts cold), `small repo` (a cold
agent needs only 6k tokens to orient, so there is little for a pin to replace), `bloated pins` (a sloppy 40k shared and
20k role pin against the same 30k of exploration) and `huge exploration` (a monorepo where a cold agent reads 90k).

The layered policy compacts when an agent's conversation fills a quarter of its model's window (the default
`cache.compact_at_percent`); the simulated window is 1M tokens, so these tasks keep their whole conversation. On the
explicit cache that costs about what compaction did, since every commit re-writes the prefix at the write premium; on the
automatic cache each request reads the larger context at 0.25x, and `long tasks` and `small repo` cost more than
summary compaction at 100k: the price of keeping what an agent read in view. A lower `cache.compact_at_percent` trades
that memory back for cost.

Layering wins when the shared pin is dense. The pin replaces each agent's own orientation; once it is larger than
that orientation it costs every request of every agent more than it saves (`bloated pins` above loses on the
marketplace cache and roughly ties on the explicit one). Sweeping the total pin size (`pins(k)`, split into shared and
role pin in thousands of tokens) against a fixed 30k orientation on the explicit-cache provider:

```
pins(k)    shared/role vs pin/explore   vs naive   vs n+sum
4             1/2     0.13                 -49%       -42%
9             4/4     0.30                 -46%       -38%
18            8/9     0.60                 -41%       -33%
30           13/16    1.00                 -35%       -25%
45           20/24    1.50                 -27%       -16%
70           31/38    2.33                 -13%        +0%
100          45/55    3.33                  +4%       +19%
```

The simulator is a model, not a benchmark: its assumptions are printed with every run, and it exists so that a change
to the policy has to survive an explicit cost comparison (`go test ./internal/kv/sim` guards the shape of these results).
<!-- SIM:END -->
