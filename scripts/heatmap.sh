#!/bin/sh
# Generate the committed file/line age audit using Git and the project's Go toolchain.
# Usage: scripts/heatmap.sh [-rev REV] [-out DIR] [-jobs N] [-require-full-history]
# The JSON and HTML reports default to .sleipnir/tmp/code-age. See docs/MAINTENANCE.md.
set -eu
cd "$(dirname "$0")/.."
exec go run ./cmd/codeage "$@"
