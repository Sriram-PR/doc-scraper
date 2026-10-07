.PHONY: fmt fmt-check lint test test-cover build clean check

BIN := doc-scraper

# golangci-lint can't type-check stdlib from a newer Go than it was built with.
LINT_GOTOOLCHAIN ?= $(shell golangci-lint version 2>/dev/null | sed -n 's/.*built with \(go[0-9.]*\).*/\1/p')

fmt:
	GOTOOLCHAIN=$(LINT_GOTOOLCHAIN) golangci-lint fmt ./...

fmt-check:
	GOTOOLCHAIN=$(LINT_GOTOOLCHAIN) golangci-lint fmt --diff ./...

lint:
	GOTOOLCHAIN=$(LINT_GOTOOLCHAIN) golangci-lint run ./...

test:
	go test ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@rm -f coverage.out

build:
	go build -o $(BIN) ./cmd/doc-scraper

clean:
	rm -f $(BIN) coverage.out

check: fmt-check lint test
