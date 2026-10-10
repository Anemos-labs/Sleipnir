#!/bin/bash
# build the scratch builds (dist/pack.html shipped-like, dist/pack-test.html with the hooks) and, with --ship, the deliverable
. ~/.local/sleipnir-toolchains.sh
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO=/home/thanos/Documents/Sleipnir/.claude/worktrees/sleipnir-web-interface-cc5a2f
mkdir -p "$HERE/dist"
node "$HERE/build.mjs" --manifest "$HERE/manifest.pack.json" --out "$HERE/dist/pack.html" || exit 1
node "$HERE/build.mjs" --manifest "$HERE/manifest.pack.json" --test --out "$HERE/dist/pack-test.html" || exit 1
if [ "$1" = "--ship" ]; then mkdir -p "$REPO/docs/design/web-mocks/v3"; cp "$HERE/dist/pack.html" "$REPO/docs/design/web-mocks/v3/sleipnir-web.html"; ls -la "$REPO/docs/design/web-mocks/v3/sleipnir-web.html"; fi
