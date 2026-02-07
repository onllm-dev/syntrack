# Development Guide

Build and run SynTrack from source on any platform.

---

## Prerequisites

- Go 1.25 or later
- Git
- Make (optional, for convenience targets)

---

## Quick Build

```bash
git clone https://github.com/onllm-dev/syntrack.git
cd syntrack
make build
```

This reads the version from the `VERSION` file and injects it via ldflags.

---

## Platform-Specific Setup

### macOS

```bash
brew install go
make build
```

### Ubuntu / Debian

```bash
sudo apt update && sudo apt install -y golang-go git make
make build
```

### CentOS / RHEL / Fedora

```bash
sudo dnf install -y golang git make
make build
```

### Windows

Install Go from https://go.dev/dl/ or use a package manager:

```powershell
# Chocolatey
choco install golang git

# Or Winget
winget install GoLang.Go
```

Build:

```powershell
go build -ldflags="-s -w" -o syntrack.exe .
```

---

## Make Targets

```bash
make build          # Build production binary with version from VERSION file
make test           # Run all tests with race detection and coverage
make run            # Build and run in debug/foreground mode
make clean          # Remove binary, coverage files, dist/, and database files
make dev            # Run with --debug --interval 10 (fast polling for dev)
make lint           # Run go fmt and go vet
make coverage       # Generate HTML coverage report
make release-local  # Cross-compile for all 5 platforms into dist/
```

---

## Versioning

The `VERSION` file at the project root is the single source of truth. The Makefile reads it:

```makefile
VERSION := $(shell cat VERSION)
```

To bump the version, edit `VERSION` and rebuild. The GitHub Actions workflow and `make release-local` both read from this file.

---

## Cross-Compilation

SynTrack uses pure Go SQLite (`modernc.org/sqlite`), so cross-compilation works without CGO:

```bash
make release-local
```

This produces binaries in `dist/`:

| Platform | Binary |
|----------|--------|
| macOS ARM64 | `syntrack-darwin-arm64` |
| macOS AMD64 | `syntrack-darwin-amd64` |
| Linux AMD64 | `syntrack-linux-amd64` |
| Linux ARM64 | `syntrack-linux-arm64` |
| Windows AMD64 | `syntrack-windows-amd64.exe` |

Manual cross-compilation:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.version=$(cat VERSION)" -o syntrack-linux-amd64 .
```

---

## Development Workflow

### 1. Clone and Setup

```bash
git clone https://github.com/onllm-dev/syntrack.git
cd syntrack
cp .env.example .env
```

### 2. Configure Providers

Edit `.env` with at least one API key:

```bash
SYNTHETIC_API_KEY=syn_your_actual_key
ZAI_API_KEY=your_zai_key
```

Both providers can run simultaneously. Configure one or both.

### 3. Run in Dev Mode

```bash
make dev    # Runs with --debug --interval 10
```

Or manually:

```bash
go run . --debug --interval 10
```

---

## Testing

```bash
go test ./...              # Run all tests
go test -race ./...        # With race detection (run before every commit)
go test -cover ./...       # With coverage
go test ./internal/store/  # Single package
make coverage              # Generate HTML coverage report → coverage.html
```

---

## Multi-Provider Architecture

SynTrack supports two providers: Synthetic and Z.ai. When both API keys are set, both agents run in parallel goroutines, each polling its respective API and storing snapshots in the shared SQLite database.

The dashboard switches between providers via the `?provider=` query parameter. The frontend reloads the page when the user selects a different provider from the dropdown.

Key source files:

| File | Purpose |
|------|---------|
| `internal/api/client.go` | Synthetic API client |
| `internal/api/zai_client.go` | Z.ai API client |
| `internal/agent/agent.go` | Synthetic polling agent |
| `internal/agent/zai_agent.go` | Z.ai polling agent |
| `internal/store/store.go` | Shared SQLite store |
| `internal/store/zai_store.go` | Z.ai-specific queries |
| `internal/web/handlers.go` | Provider-aware route handlers |

---

## Production Build

Strip debug symbols for a smaller binary:

```bash
make build    # Equivalent to: go build -ldflags="-s -w -X main.version=$(VERSION)" -o syntrack .
```

Binary sizes: ~12-13 MB per platform.

---

## Release Pipeline

### Local

```bash
make release-local
ls -lh dist/
```

### GitHub Actions

The workflow at `.github/workflows/release.yml` triggers on:

- **Tag push** (`v*`): Builds all platforms and creates a GitHub Release
- **Manual dispatch**: Optionally creates a release with the `publish` input

To release:

```bash
# Update VERSION file
echo "1.2.0" > VERSION

# Commit, tag, push
git add VERSION
git commit -m "chore: bump version to 1.2.0"
git tag v1.2.0
git push && git push --tags
```

The workflow builds, tests, and publishes binaries automatically.

---

## Dependencies

| Package | Purpose |
|---------|---------|
| `modernc.org/sqlite` | Pure Go SQLite driver (no CGO) |
| `github.com/joho/godotenv` | `.env` file loading |

Install or update:

```bash
go mod tidy
```

---

## Docker Build (Optional)

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -ldflags="-s -w" -o syntrack .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/syntrack .
COPY .env.example .env
CMD ["./syntrack"]
```

```bash
docker build -t syntrack .
docker run -p 8932:8932 -v $(pwd)/.env:/root/.env syntrack
```

---

## Troubleshooting

### "go: command not found"

Install Go for your platform. See https://go.dev/dl/.

### "cannot find module"

```bash
go mod download
```

### Permission denied (Unix)

```bash
chmod +x syntrack
```

### Port already in use

```bash
./syntrack stop           # Stop existing instance
./syntrack --port 9000    # Or use a different port
```
