.PHONY: lint test test-cover build clean check

BIN := doc-scraper

lint:
	golangci-lint run ./...

test:
	go test ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@rm -f coverage.out

build:
	go build -o $(BIN) ./cmd/crawler

clean:
	rm -f $(BIN) coverage.out

check: lint test
