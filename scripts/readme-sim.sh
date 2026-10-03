#!/bin/sh
# Rewrites the block between <!-- SIM:BEGIN --> and <!-- SIM:END --> in docs/CACHE-ECONOMICS.md with
# fresh `sleipnir sim` output. The numbers come from a model with printed assumptions
# (internal/kv/sim), not from a benchmark; docs/VALIDATION.md is the protocol for real endpoints.
#   scripts/readme-sim.sh          (or: make readme-sim)
set -eu
cd "$(dirname "$0")/.."
bin=$(mktemp)
trap 'rm -f "$bin" "$bin.block" "$bin.doc"' EXIT
go build -o "$bin" ./cmd/sleipnir

{
  echo '<!-- SIM:BEGIN -->'
  echo '`sleipnir sim` replays one synthetic swarm workload (20 workers, 40 tasks, about 32 work steps per task; an agent'
  echo 'without a shared pin spends 30k tokens orienting itself in the repository) under a plain harness, the same with'
  echo 'summary compaction, and the layered policy, pricing every request against an explicit model of the provider'"'"'s cache.'
  echo 'Costs are in millions of input-token equivalents, lower is better. Regenerate with `make readme-sim`.'
  echo
  echo 'Provider with explicit cache breakpoints (reads 0.1x, writes 1.25x, 5 minute TTL):'
  echo
  echo '```'
  "$bin" sim --mode scenarios | sed 1,3d
  echo '```'
  echo
  echo 'Provider with an automatic prefix cache (reads 0.25x, no write premium, 3 engines behind a marketplace):'
  echo
  echo '```'
  "$bin" sim --mode scenarios --provider marketplace | sed 1,3d
  echo '```'
  echo
  echo 'Columns: `ctx` is the average context per request in thousands of tokens and `wall` the simulated duration in minutes,'
  echo 'each as naive/sleipnir. The rows: `short tasks` (about 8 work steps each: pins and orientation dominate), `long tasks` (about 90: compaction'
  echo 'dominates), `cold launches` (three launches 12 idle minutes apart, so every launch starts cold), `small repo` (a cold'
  echo 'agent needs only 6k tokens to orient, so there is little for a pin to replace), `bloated pins` (a sloppy 40k shared and'
  echo '20k role pin against the same 30k of exploration) and `huge exploration` (a monorepo where a cold agent reads 90k).'
  echo
  echo 'Layering wins when the shared pin is dense. The pin replaces each agent'"'"'s own orientation; once it is larger than'
  echo 'that orientation it costs every request of every agent more than it saves (`bloated pins` above loses on the'
  echo 'marketplace cache and roughly ties on the explicit one). Sweeping the total pin size (`pins(k)`, split into shared and'
  echo 'role pin in thousands of tokens) against a fixed 30k orientation on the explicit-cache provider:'
  echo
  echo '```'
  "$bin" sim --mode pins | sed 1,3d
  echo '```'
  echo
  echo 'The simulator is a model, not a benchmark: its assumptions are printed with every run, and it exists so that a change'
  echo 'to the policy has to survive an explicit cost comparison (`go test ./internal/kv/sim` guards the shape of these results).'
  echo '<!-- SIM:END -->'
} > "$bin.block"

awk -v blockfile="$bin.block" '
  /<!-- SIM:BEGIN -->/ { while ((getline line < blockfile) > 0) print line; skip = 1; next }
  /<!-- SIM:END -->/   { skip = 0; next }
  !skip { print }
' docs/CACHE-ECONOMICS.md > "$bin.doc"
cat "$bin.doc" > docs/CACHE-ECONOMICS.md
echo "docs/CACHE-ECONOMICS.md updated"
