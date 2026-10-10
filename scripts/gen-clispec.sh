#!/bin/sh
# Regenerate the CLI spec of `sleipnir web` (internal/web/clispec/clispec.json) from docs/CLI.md and the
# command tables of cmd/sleipnir.
#
#   scripts/gen-clispec.sh            rewrite internal/web/clispec/clispec.json
#   scripts/gen-clispec.sh --check    change nothing; exit 1 if the spec is out of date
#
# docs/CLI.md's flag blocks are the binary's own -h output (scripts/gen-cli-docs.sh keeps them current), so
# run that script first when a flag changed. The spec also fails to generate when `sleipnir --help` lists a
# command that has no entry, or a new command has no mode (internal/web/clispec/specgen).
set -eu
cd "$(dirname "$0")/.."

case "${1:-}" in
  --check) exec go run ./internal/web/clispec/gen -check ;;
  "") exec go run ./internal/web/clispec/gen ;;
  -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
  *) echo "gen-clispec: unknown argument: $1 (try --help)" >&2; exit 2 ;;
esac
