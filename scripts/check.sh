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

echo "== README's simulator block is current"
readme_before=$(mktemp)
trap 'rm -f "$readme_before"' EXIT INT TERM
cp README.md "$readme_before"
sh scripts/readme-sim.sh
if ! cmp -s "$readme_before" README.md; then
  diff -u "$readme_before" README.md | head -40 || true
  cp "$readme_before" README.md
  echo "README.md's sim block is stale: run make readme-sim" >&2
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

echo "== cross-compile and vet, for every target .goreleaser.yaml ships"
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -trimpath -o /dev/null ./cmd/sleipnir
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go vet ./...
  echo "ok $t"
done
echo "all checks passed"
