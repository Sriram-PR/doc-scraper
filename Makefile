.PHONY: fmt fmt-check lint test test-cover build clean check

BIN := doc-scraper

fmt:
	golangci-lint fmt ./...

fmt-check:
	golangci-lint fmt --diff ./...

lint:
	golangci-lint run ./...

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
