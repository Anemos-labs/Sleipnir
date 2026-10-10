#!/bin/bash
# Builds the scratch shop project: commit "base" (main) and one commit per checkpoint step, authored by the agent that wrote it.
# Source of truth for file contents: shop-src/<step>/ (only the files that step changes). Deterministic dates.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/env.sh"
P=$HERE/shop
rm -rf "$P"; mkdir -p "$P"
cp -a "$HERE/shop-src/base/." "$P/"
cd "$P"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
git init -q -b main .
commit() { # name email date message
  GIT_AUTHOR_NAME="$1" GIT_AUTHOR_EMAIL="$2" GIT_AUTHOR_DATE="$3" GIT_COMMITTER_NAME="$1" GIT_COMMITTER_EMAIL="$2" GIT_COMMITTER_DATE="$3" git commit -q -m "$4"
}
git add -A
commit "Thanos" "dev@example.com" "2026-01-01T18:00:00+00:00" "shop: orders, customers, a cart and an empty page"
git tag base
step() { # step author date message files...
  local st=$1 who=$2 date=$3 msg=$4; shift 4
  for f in "$@"; do mkdir -p "$(dirname "$f")"; cp "$HERE/shop-src/$st/$f" "$f"; git add "$f"; done
  commit "$who" "$who@sleipnir.invalid" "$date" "$msg"
}
step c05 be-1 "2026-01-02T03:04:24+00:00" "T4: seed items in cents, and a loader" seed/items.json api/catalog/load.go
git tag c05
step c06 be-2 "2026-01-02T03:04:35+00:00" "T5: cart total in cents" api/cart/cart.go
git tag c06
step c07 be-1 "2026-01-02T03:04:39+00:00" "T4: catalogue page and GET /items" api/catalog/items.go api/server.go
step c07 fe-1 "2026-01-02T03:04:38+00:00" "T6: shop.js loads a page" web/shop.js
git tag c07
step c08 ts-1 "2026-01-02T03:04:52+00:00" "T7: table tests for the catalogue contract" api/catalog/items_test.go
git tag c08
step c09 be-2 "2026-01-02T03:04:56+00:00" "T5: Remove" api/cart/cart.go
git tag c09
step c10 fe-1 "2026-01-02T03:05:01+00:00" "T6: item cards, pager and styles" web/shop.js web/index.html web/shop.css web/package.json
git tag c10
step c11 ts-1 "2026-01-02T03:05:03+00:00" "T7: cart tests" api/cart/cart_test.go
git tag c11
step c12 be-1 "2026-01-02T03:05:05+00:00" "T4: document the last-page rule" api/server.go
git tag c12
git branch -m main final 2>/dev/null || true
git checkout -q -b main base 2>/dev/null || true
