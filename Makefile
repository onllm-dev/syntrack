.PHONY: build test run clean integration dev lint release-local

VERSION := $(shell cat VERSION)
BINARY := onwatch
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) .

test:
	go test -race -cover -count=1 ./...

run: build
	./$(BINARY) --debug

clean:
	rm -f $(BINARY) coverage.out coverage.html
	rm -rf dist/
	go clean -testcache

integration:
	go test -v -tags=integration ./...

dev:
	go run . --debug --interval 10

lint:
	go fmt ./...
	go vet ./...

coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

release-local:
	@echo "Building onWatch v$(VERSION) for all platforms..."
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/onwatch-darwin-arm64       .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/onwatch-darwin-amd64       .
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/onwatch-linux-amd64        .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/onwatch-linux-arm64        .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/onwatch-windows-amd64.exe  .
	@echo "Done. Binaries in dist/"
	@ls -lh dist/
