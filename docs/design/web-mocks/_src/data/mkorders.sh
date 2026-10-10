#!/bin/bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/env.sh"
P=$HERE/orders
rm -rf "$P"; mkdir -p "$P"; cp -a "$HERE/orders-src/base/." "$P/"; cd "$P"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
git init -q -b main .
commit() { GIT_AUTHOR_NAME="$1" GIT_AUTHOR_EMAIL="$2" GIT_AUTHOR_DATE="$3" GIT_COMMITTER_NAME="$1" GIT_COMMITTER_EMAIL="$2" GIT_COMMITTER_DATE="$3" git commit -q -m "$4"; }
git add -A; commit "Ada" "ada@example.com" "2026-01-01T17:00:00+00:00" "orders: store, list and the http handler"; git tag base
step() { local st=$1 who=$2 date=$3 msg=$4; shift 4; for f in "$@"; do mkdir -p "$(dirname "$f")"; cp "$HERE/orders-src/$st/$f" "$f"; git add "$f"; done; commit "$who" "$who@sleipnir.invalid" "$date" "$msg"; }
step c01 main "2026-01-02T02:55:12+00:00" "turn 1: the pagination test in ./orders is failing, fix it" orders/list.go; git tag c01
step c02 main "2026-01-02T03:03:40+00:00" "turn 2: yes, add a test for page 0 and a negative size" orders/list_test.go; git tag c02
git branch -m main final; git checkout -q -b main base
