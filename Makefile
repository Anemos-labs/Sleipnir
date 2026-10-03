.PHONY: build test race lint fmt sim readme-sim check release-plan clean

build:
	go build -trimpath -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.commit=$$(git rev-parse --short HEAD 2>/dev/null || echo none)" -o bin/sleipnir ./cmd/sleipnir

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

# The quick static checks: format, vet, the dependency allow-list and the action pins.
lint:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	sh scripts/check-deps.sh
	sh scripts/check-pins.sh

fmt:
	gofmt -w cmd internal

# What layering buys and where it stops paying (see docs/CACHE-ECONOMICS.md).
sim:
	go run ./cmd/sleipnir sim --mode scenarios
	go run ./cmd/sleipnir sim --mode pins

# Refresh the simulator results in docs/CACHE-ECONOMICS.md.
readme-sim:
	scripts/readme-sim.sh

# Everything CI runs: gofmt, go mod tidy and verify, the dependency allow-list, action pins, declared prompt bytes,
# generated docs, the tests of the scripts, vet, build, race tests (with internal/repocheck), cross-compile and vet for
# every platform that is released.
check:
	scripts/check.sh

# What a merge to main would release from this checkout: the version, and why or why not. Writes nothing, asks nothing.
release-plan:
	sh scripts/release-plan.sh --dry-run

clean:
	rm -rf bin
