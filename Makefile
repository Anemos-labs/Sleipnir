.PHONY: build test race lint fmt sim clean

build:
	go build -trimpath -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.commit=$$(git rev-parse --short HEAD 2>/dev/null || echo none)" -o bin/sleipnir ./cmd/sleipnir

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

fmt:
	gofmt -w .

# What layering buys and where it stops paying (see docs/CACHE-DESIGN.md section 8).
sim:
	go run ./cmd/sleipnir sim --mode scenarios
	go run ./cmd/sleipnir sim --mode pins

clean:
	rm -rf bin
