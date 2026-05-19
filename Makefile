.PHONY: build test test-integration lint tidy clean

BINARY := metabase-mcp

build:
	go build -o $(BINARY) .

test:
	go test -race -count=1 ./...

test-integration:
	go test -race -tags=integration -count=1 ./test/...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt issues:" && gofmt -l . && exit 1)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping"; \
	fi

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
	rm -rf dist
