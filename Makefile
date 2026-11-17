.PHONY: build lint test check clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/stellatinus-installer ./cmd/stellatinus-installer

lint:
	golangci-lint run

test:
	go vet ./...
	go test -race ./...

# What CI runs on every pull request.
check: lint test

clean:
	rm -rf bin dist
