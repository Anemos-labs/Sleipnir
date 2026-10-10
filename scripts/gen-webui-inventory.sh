#!/bin/sh
# Regenerate the inventory of the page (internal/parity/webui.json) from the scripts of internal/web/ui.
#
#   scripts/gen-webui-inventory.sh            rewrite internal/parity/webui.json
#   scripts/gen-webui-inventory.sh --check    change nothing; exit 1 if the inventory is out of date
#
# The inventory lists what the page offers (views, rail, palette, slash handlers, Settings controls, the New session dialog, the
# command runner's forms) for the Go tests of the parity guard (internal/parity, cmd/sleipnir), which read the checked-in file and
# need no Node. It also depends on internal/web/clispec/clispec.json (sh scripts/gen-clispec.sh). Needs Node 18 or later.
set -eu
cd "$(dirname "$0")/.."

case "${1:-}" in
  --check) exec node internal/web/uidev/inventory.mjs --check ;;
  "") exec node internal/web/uidev/inventory.mjs ;;
  -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
  *) echo "gen-webui-inventory: unknown argument: $1 (try --help)" >&2; exit 2 ;;
esac
