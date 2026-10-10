#!/bin/sh
# Everything CI runs, in the order that fails fastest (.github/workflows/ci.yml: lint, test, cross, sim; internal/repocheck
# fails when the two lists drift apart). Usage: scripts/check.sh [go test flags/packages]
#   With arguments, only the test step is narrowed to them: scripts/check.sh ./internal/kv/...
#   BASE=<commit> is what the prompt-byte check compares with (default: the merge base with origin/main, else with main).
set -eu
cd "$(dirname "$0")/.."

echo "== gofmt"
bad=$(gofmt -l cmd internal || true)
[ -z "$bad" ] || { echo "not gofmt-clean:"; echo "$bad"; exit 1; }

echo "== go.mod is tidy"
go mod tidy -diff

echo "== go.sum matches the modules it names"
go mod download
go mod verify

echo "== dependencies are on the allow-list"
sh scripts/check-deps.sh

echo "== every uses is pinned to a commit SHA"
sh scripts/check-pins.sh

echo "== prompt-byte changes are declared in CHANGELOG.md"
base=${BASE:-$(git merge-base HEAD origin/main 2>/dev/null || git merge-base HEAD main 2>/dev/null || true)}
sh scripts/check-declared.sh "$base"

echo "== docs/CLI.md lists every flag and command of the binary"
sh scripts/gen-cli-docs.sh --check

echo "== the web interface's CLI spec matches docs/CLI.md"
sh scripts/gen-clispec.sh --check

echo "== the web interface's JavaScript tests"
if command -v node >/dev/null 2>&1; then node --test internal/web/uidev/test/*.test.mjs; else echo "node is not installed: skipped (CI runs them)"; fi

echo "== cache simulation documentation is current"
simulation_before=$(mktemp)
trap 'rm -f "$simulation_before"' EXIT INT TERM
cp docs/CACHE-ECONOMICS.md "$simulation_before"
sh scripts/readme-sim.sh
if ! cmp -s "$simulation_before" docs/CACHE-ECONOMICS.md; then
  diff -u "$simulation_before" docs/CACHE-ECONOMICS.md | head -40 || true
  cp "$simulation_before" docs/CACHE-ECONOMICS.md
  echo "docs/CACHE-ECONOMICS.md's sim block is stale: run make readme-sim" >&2
  exit 1
fi

echo "== the recorded demo matches what the interface draws"
if [ -f scripts/record-demo.sh ]; then sh scripts/record-demo.sh --check; fi

echo "== the tests of the scripts"
for t in scripts/*_test.sh; do sh "$t" || exit 1; done

echo "== vet"
go vet ./...

echo "== build"
go build ./...

echo "== test (race), with the repository's own invariants (internal/repocheck)"
if [ "$#" -gt 0 ]; then go test -race -count=1 "$@"; else go test -race -count=1 -timeout 25m ./...; fi

if [ "$#" -eq 0 ]; then
  echo "== tests that are not built under -race (the allocation gates)"
  go test -count=1 -timeout 10m -run 'Allocations' ./...
fi

echo "== cross-compile and vet, for every target .goreleaser.yaml ships"
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -trimpath -o /dev/null ./cmd/sleipnir
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go vet ./...
  echo "ok $t"
done
echo "all checks passed"
