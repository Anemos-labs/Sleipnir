#!/bin/bash
# run-all.sh: every verification script, one browser at a time, against the scratch builds (dist/test.html without the data pack, dist/pack-test.html with it).
# Output of each script goes to dist/results/BUILD-NAME.txt; the last line of this script prints the files with their first lines. Run ./go.sh first.
. ~/.local/sleipnir-toolchains.sh
HERE="$(cd "$(dirname "$0")/.." && pwd)"; cd "$HERE/test"; mkdir -p "$HERE/dist/results"
for b in pack-test; do
  [ -f "$HERE/dist/$b.html" ] || continue
  for t in numbers governor lifecycle input func layout fit motion prune pack; do
    node "t-$t.mjs" "../dist/$b.html" > "$HERE/dist/results/$b-$t.txt" 2>&1 || echo "t-$t on $b: exit $?"
  done
done
node t-shipped.mjs /home/thanos/Documents/Sleipnir/.claude/worktrees/sleipnir-web-interface-cc5a2f/docs/design/web-mocks/v3/sleipnir-web.html > "$HERE/dist/results/shipped.txt" 2>&1 || echo "t-shipped: exit $?"   # the shipped file itself: no hooks, real frame loop
ls "$HERE/dist/results"
