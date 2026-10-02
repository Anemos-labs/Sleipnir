# What the layering buys, and where it does not

<!-- SIM:BEGIN -->
`sleipnir sim` replays one synthetic swarm workload (20 workers, 40 tasks, about 32 work steps per task; an agent
without a shared pin spends 30k tokens orienting itself in the repository) under a plain harness, the same with
summary compaction, and the layered policy, pricing every request against an explicit model of the provider's cache.
Costs are in millions of input-token equivalents, lower is better. Regenerate with `make readme-sim`.

Provider with explicit cache breakpoints (reads 0.1x, writes 1.25x, 5 minute TTL):

```
scenario              naive  naive+sum   sleipnir  vs naive  vs n+sum  ctx n/s wall n/s
typical               25.4M      22.1M      16.2M      -36%      -26%  68/43 k 100/81 m
short tasks            8.5M       8.5M       4.7M      -45%      -45%  38/38 k  48/26 m
long tasks            96.8M      50.7M      45.3M      -53%      -11% 135/45 k 214/201m
cold launches         25.6M      22.3M      16.4M      -36%      -26%  68/43 k 203/179m
small repo            17.1M      16.4M      15.5M       -9%       -5%  51/42 k  86/81 m
bloated pins          25.4M      22.1M      22.8M      -10%       +3%  68/85 k 100/81 m
huge exploration      50.8M      32.5M      18.0M      -64%      -44% 106/45 k 130/81 m
```

Provider with an automatic prefix cache (reads 0.25x, no write premium, 3 engines behind a marketplace):

```
scenario              naive  naive+sum   sleipnir  vs naive  vs n+sum  ctx n/s wall n/s
typical               33.1M      28.4M      22.4M      -32%      -21%  68/43 k  99/81 m
short tasks            9.7M       9.7M       6.6M      -33%      -33%  38/39 k  48/26 m
long tasks           143.8M      68.5M      62.3M      -57%       -9% 135/46 k 210/199m
cold launches         33.0M      28.4M      22.0M      -33%      -23%  68/43 k 201/178m
small repo            21.6M      20.5M      21.4M       -1%       +4%  51/42 k  85/81 m
bloated pins          33.1M      28.4M      38.5M      +17%      +36%  68/85 k  99/81 m
huge exploration      67.6M      39.8M      24.0M      -65%      -40% 106/45 k 128/81 m
```

Columns: `ctx` is the average context per request in thousands of tokens and `wall` the simulated duration in minutes,
each as naive/sleipnir. The rows: `short tasks` (about 8 work steps each: pins and orientation dominate), `long tasks` (about 90: compaction
dominates), `cold launches` (three launches 12 idle minutes apart, so every launch starts cold), `small repo` (a cold
agent needs only 6k tokens to orient, so there is little for a pin to replace), `bloated pins` (a sloppy 40k shared and
20k role pin against the same 30k of exploration) and `huge exploration` (a monorepo where a cold agent reads 90k).

Layering wins when the shared pin is dense. The pin replaces each agent's own orientation; once it is larger than
that orientation it costs every request of every agent more than it saves (`bloated pins` above loses on the
marketplace cache and roughly ties on the explicit one). Sweeping the total pin size (`pins(k)`, split into shared and
role pin in thousands of tokens) against a fixed 30k orientation on the explicit-cache provider:

```
pins(k)    shared/role vs pin/explore   vs naive   vs n+sum
4             1/2     0.13                 -44%       -36%
9             4/4     0.30                 -41%       -33%
18            8/9     0.60                 -36%       -26%
30           13/16    1.00                 -29%       -18%
45           20/24    1.50                 -20%        -8%
70           31/38    2.33                  -5%       +10%
100          45/55    3.33                 +14%       +31%
```

The simulator is a model, not a benchmark: its assumptions are printed with every run, and it exists so that a change
to the policy has to survive an explicit cost comparison (`go test ./internal/kv/sim` guards the shape of these results).
<!-- SIM:END -->

The result is a model with printed assumptions, not a benchmark, and it says where layering loses: **the shared pin must
be dense** (roughly no larger than the orientation it replaces). `docs/VALIDATION.md` is the protocol for measuring the real
thing on your endpoint, and holds the first real measurements (one marketplace, one model family: prompts that do not drift and
workers that start warm, on an endpoint whose own cache is erratic).

