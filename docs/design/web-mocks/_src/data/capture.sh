#!/bin/bash
# Captures the output of the real binary into real/*.cap (one file per section), from a fresh isolated home, deterministically.
# Never touches the owner's state: HOME and SLEIPNIR_HOME are scratch directories. No network: nothing here talks to a provider.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/env.sh"
export PATH=$HERE/bin:$PATH
OUT=$HERE/real; mkdir -p "$OUT"
san() { sed -e "s#$S/work/data/home2#~#g; s#$S/work/data/home#~#g; s#$S/work/data/shop-final#~/projects/shop#g; s#$S/work/data/shop#~/projects/shop#g; s#$S/work/data/demo-shop/session#~/.sleipnir/sessions/20260101-171204-7c1e3a#g; s#$S/work/data/demo-shop/shop#~/projects/shop#g; s#$S/work/data/rl#.#g; s#$S/work/data#<scratch>#g"; }
# run NAME ARGS...: appends "### sleipnir ARGS" + stdout/stderr interleaved + "[exit N]" to $CAP
cap() { CAP=$OUT/$1.cap; : > "$CAP"; }
run() { { echo "### sleipnir $*"; "$SL" "$@" 2>&1 </dev/null; echo "[exit $?]"; } | san >> "$CAP"; }
runin() { local cwd=$1; shift; { echo "### (in $cwd) sleipnir $*"; (cd "$cwd" && "$SL" "$@" 2>&1 </dev/null; echo "[exit $?]"); } | san >> "$CAP"; }

# a fresh home for the sample user
rm -rf "$HERE/home" "$HERE/home2"; mkdir -p "$HERE/home/.sleipnir/hooks" "$HERE/home2"
cp "$HERE/fixtures/user-config.json" "$HERE/home/.sleipnir/config.json"
printf '#!/bin/sh\n# refuse destructive commands.\nexit 0\n' > "$HERE/home/.sleipnir/hooks/block-dangerous.sh"; printf '#!/bin/sh\n# gofmt the Go files the last edit touched.\nexit 0\n' > "$HERE/home/.sleipnir/hooks/format.sh"; chmod +x "$HERE"/home/.sleipnir/hooks/*.sh
SHOP=$HERE/shop

cap basic
run --help
run version
run bogus
run chat --bogus
run logout
run logout nosuch
run sim --mode bogus

cap recon
runin $SHOP recon
runin $SHOP recon --budget 1000
runin $SHOP recon --budget 2000
runin $SHOP recon --budget 10000

cap config
runin $SHOP config
runin $SHOP config --trust-project
runin $SHOP config --json
runin $SHOP config --trust-project --json

cap trust
runin $SHOP trust
runin $SHOP trust list
runin $SHOP trust add --yes
runin $SHOP trust
runin $SHOP trust list
runin $SHOP trust forget
runin $SHOP trust list
runin $SHOP trust add --yes
cp "$HERE/home/trust.json" "$OUT/trust.json" 2>/dev/null || true

cap mcp
runin $SHOP mcp list
runin $SHOP mcp list --trust-project
runin $SHOP mcp approve tracker --yes
runin $SHOP mcp list --trust-project
runin $SHOP mcp test --trust-project
runin $SHOP mcp revoke tracker
runin $SHOP mcp test

cap schedule
runin $SHOP schedule
runin $SHOP schedule add --cron "0 9 * * 1-5" --cwd "$SHOP" "summarize yesterday's commits"
runin $SHOP schedule add --cron "@weekly" --model anthropic/claude-haiku-5-5 --mode accept-edits --budget-usd 0.5 "write the CHANGELOG entry for the week's merged work"
runin $SHOP schedule
runin $SHOP schedule rm j2
runin $SHOP schedule
runin $SHOP schedule add --cron "61 * * * *" "bad cron"
runin $SHOP daemon --once

cap models
runin $SHOP models fav list
runin $SHOP models fav add heimdall/demo-model anthropic/claude-sonnet-5-5 anthropic/claude-haiku-5-5
runin $SHOP models fav rm heimdall/demo-model
runin $SHOP models fav list
runin $SHOP models fav add notaref

cp "$HERE/fixtures/user-config.json" "$HERE/home/.sleipnir/config.json"

cap init
mkdir -p "$HERE/tmp/initproj" && rm -rf "$HERE/tmp/initproj" && mkdir -p "$HERE/tmp/initproj" && git -C "$HERE/tmp/initproj" init -q .
runin $HERE/tmp/initproj init
runin $HERE/tmp/initproj init
HOME=$HERE/home2 SLEIPNIR_HOME=$HERE/home2 run init --user --model anthropic/claude-sonnet-5-5 --local-url http://127.0.0.1:8000/v1
HOME=$HERE/home2 SLEIPNIR_HOME=$HERE/home2 run init --user
cat "$HERE/home2/.sleipnir/config.json" > "$OUT/init-user-config.json" 2>/dev/null
cat "$HERE/tmp/initproj/.sleipnir/config.json" > "$OUT/init-project-config.json"
cat "$HERE/tmp/initproj/AGENTS.md" > "$OUT/init-agents.md"

cap mock
( timeout 2 "$SL" mock --addr 127.0.0.1:18189 2>&1 || true ) | san | head -3 > "$OUT/mock.cap"

cap demo
echo "(see demo-shop.stdout / demo-handbook.stdout)" > "$CAP"

# the effective configuration at each layer, for the origin table: defaults (empty home, empty project), user only, user + project
mkdir -p "$HERE/tmp/empty-home" "$HERE/tmp/empty-proj"; git -C "$HERE/tmp/empty-proj" init -q . 2>/dev/null
( cd "$HERE/tmp/empty-proj" && HOME=$HERE/tmp/empty-home SLEIPNIR_HOME=$HERE/tmp/empty-home "$SL" config --json 2>/dev/null ) > "$OUT/config-defaults.json"
( cd "$HERE/tmp/empty-proj" && "$SL" config --json 2>/dev/null ) > "$OUT/config-user.json"
( cd "$SHOP" && "$SL" config --trust-project --json 2>/dev/null ) > "$OUT/config-project.json"
cp "$SHOP/.sleipnir/config.json" "$OUT/project-config.json"; cp "$HERE/fixtures/user-config.json" "$OUT/user-config.json"; cp "$SHOP/.mcp.json" "$OUT/project-mcp.json"
cp "$SHOP/AGENTS.md" "$OUT/project-agents.md"
echo captured
