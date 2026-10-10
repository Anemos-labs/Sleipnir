#!/bin/bash
# all.sh: the whole battery, serially, against the scratch build with hooks (dist/pack-test.html) and the shipped file. Output in dist/final/.
. ~/.local/sleipnir-toolchains.sh
HERE="$(cd "$(dirname "$0")/.." && pwd)"; cd "$HERE/test"; mkdir -p "$HERE/dist/final"
SHIPPED=/home/thanos/Documents/Sleipnir/.claude/worktrees/sleipnir-web-interface-cc5a2f/docs/design/web-mocks/v3/sleipnir-web.html
bash run-all.sh > "$HERE/dist/final/run-all.log" 2>&1
for t in v3 quiet-scan dock dock2 a11y layout-dock safe perf ten; do timeout 400 node t-$t.mjs ../dist/pack-test.html > "$HERE/dist/final/$t.txt" 2>&1 || echo "t-$t exit $?" >> "$HERE/dist/final/log.txt"; done
timeout 120 node t-shipped.mjs "$SHIPPED" > "$HERE/dist/final/shipped.txt" 2>&1 || echo "t-shipped exit $?" >> "$HERE/dist/final/log.txt"
echo done > "$HERE/dist/final/DONE"
