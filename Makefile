BINARY  := mail-muncher
PKG     := ./cmd/mail-muncher
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test lint fmt tidy clean

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

test:
	go test ./...

# Use golangci-lint when it is installed; otherwise fall back to go vet.
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint run"; \
		golangci-lint run; \
	else \
		echo "golangci-lint not found; falling back to go vet"; \
		go vet ./...; \
	fi

fmt:
	go fmt ./...

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
