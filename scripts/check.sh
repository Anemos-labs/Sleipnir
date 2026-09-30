#!/bin/sh
# Everything CI runs, in the order that fails fastest. Usage: scripts/check.sh [go test flags/packages]
set -eu
cd "$(dirname "$0")/.."

echo "== gofmt"
bad=$(gofmt -l cmd internal || true)
[ -z "$bad" ] || { echo "not gofmt-clean:"; echo "$bad"; exit 1; }

echo "== go.mod is tidy"
go mod tidy -diff

echo "== vet"
go vet ./...

echo "== build"
go build ./...

echo "== test (race)"
if [ "$#" -gt 0 ]; then go test -race -count=1 "$@"; else go test -race -count=1 -timeout 20m ./...; fi

echo "== cross-compile"
for t in linux/amd64 linux/arm64 darwin/arm64 windows/amd64; do
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -o /dev/null ./cmd/sleipnir
  echo "ok $t"
done
echo "all checks passed"
