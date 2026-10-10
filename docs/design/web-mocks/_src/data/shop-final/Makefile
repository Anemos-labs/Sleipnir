.PHONY: build test lint run

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"

run:
	go run ./cmd/shop
