# Development Guide

Build and run onWatch from source on any platform.

---

## Prerequisites

- Go 1.25 or later
- Git
- Make (optional, for convenience targets)

---

## Quick Build

```bash
git clone https://github.com/onllm-dev/onwatch.git
cd onwatch
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
go build -ldflags="-s -w" -o onwatch.exe .
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

onWatch uses pure Go SQLite (`modernc.org/sqlite`), so cross-compilation works without CGO:

```bash
make release-local
```

This produces binaries in `dist/`:

| Platform | Binary |
|----------|--------|
| macOS ARM64 | `onwatch-darwin-arm64` |
| macOS AMD64 | `onwatch-darwin-amd64` |
| Linux AMD64 | `onwatch-linux-amd64` |
| Linux ARM64 | `onwatch-linux-arm64` |
| Windows AMD64 | `onwatch-windows-amd64.exe` |

Manual cross-compilation:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.version=$(cat VERSION)" -o onwatch-linux-amd64 .
```

---

## Development Workflow

### 1. Clone and Setup

```bash
git clone https://github.com/onllm-dev/onwatch.git
cd onwatch
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

onWatch supports three providers: Synthetic, Z.ai, and Anthropic. When multiple API keys are set, all agents run in parallel goroutines, each polling its respective API and storing snapshots in the shared SQLite database.

The dashboard switches between providers via the `?provider=` query parameter. Each provider renders its own quota cards, insight cards, and stat summaries. Synthetic insights focus on cycle utilization and billing periods; Z.ai insights show plan capacity (daily/monthly token budgets), tokens-per-call efficiency, and top tool analysis; Anthropic insights show burn rate forecasting, window averages, and projected exhaustion.

Key source files:

| File | Purpose |
|------|---------|
| `internal/api/client.go` | Synthetic API client |
| `internal/api/zai_client.go` | Z.ai API client |
| `internal/api/anthropic_client.go` | Anthropic OAuth API client |
| `internal/agent/agent.go` | Synthetic polling agent |
| `internal/agent/zai_agent.go` | Z.ai polling agent |
| `internal/agent/anthropic_agent.go` | Anthropic polling agent |
| `internal/store/store.go` | Shared SQLite store |
| `internal/store/zai_store.go` | Z.ai-specific queries |
| `internal/store/anthropic_store.go` | Anthropic-specific queries |
| `internal/web/handlers.go` | Provider-aware route handlers |

---

## Production Build

Strip debug symbols for a smaller binary:

```bash
make build    # Equivalent to: go build -ldflags="-s -w -X main.version=$(VERSION)" -o onwatch .
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
RUN go build -ldflags="-s -w" -o onwatch .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/onwatch .
COPY .env.example .env
CMD ["./onwatch"]
```

```bash
docker build -t onwatch .
docker run -p 9211:9211 -v $(pwd)/.env:/root/.env onwatch
```

---

## Performance Monitoring

A built-in performance monitoring tool tracks onWatch's RAM consumption and HTTP response times. This helps validate memory efficiency and identify performance regressions.

### Building the Tool

```bash
cd tools/perf-monitor
go build -o perf-monitor .
```

### Running Performance Tests

**Monitor existing instance (default 1 minute):**
```bash
./perf-monitor
```

**With custom port and duration:**
```bash
./perf-monitor 9211 2m
```

**With restart (stops and restarts onWatch for clean baseline):**
```bash
./perf-monitor --restart 9211 1m
```

### What It Measures

The tool runs two phases:

1. **Idle Phase (50% of duration):** Samples memory every 5 seconds with no HTTP requests
2. **Load Phase (50% of duration):** Makes continuous requests to all endpoints while sampling memory

### Output

The tool generates:
- Console summary with RAM statistics and HTTP performance
- JSON report: `perf-report-YYYYMMDD-HHMMSS.json`

Example results (all three agents -- Synthetic, Z.ai, Anthropic -- polling in parallel):
```
IDLE STATE (3 agents polling concurrently):
  Avg RSS: 27.5 MB
  P95 RSS: 27.5 MB

LOAD STATE (1,160 requests in 15s while agents poll):
  Avg RSS: 28.5 MB
  P95 RSS: 29.0 MB
  Delta:   +0.9 MB (+3.4%)

HTTP PERFORMANCE:
  /                    145 reqs  avg: 0.69ms
  /api/current         145 reqs  avg: 0.29ms
  /api/history         145 reqs  avg: 0.29ms
  /api/cycles          145 reqs  avg: 0.28ms
  /api/insights        145 reqs  avg: 0.28ms
  /api/summary         145 reqs  avg: 0.27ms
  /api/sessions        145 reqs  avg: 0.27ms
  /api/providers       145 reqs  avg: 0.32ms
```

### Latest Benchmark (2026-02-08)

Measured with the built-in `tools/perf-monitor` while all three provider agents (Synthetic, Z.ai, Anthropic) ran in parallel, each polling its respective API every 60 seconds and writing snapshots to the shared SQLite database:

| Metric | Idle | Under Load | Budget |
|--------|------|------------|--------|
| Avg RSS | 27.5 MB | 28.5 MB | 30 MB (idle) / 50 MB (load) |
| P95 RSS | 27.5 MB | 29.0 MB | -- |
| Load delta | -- | +0.9 MB (+3.4%) | <5 MB |
| Total requests | -- | 1,160 in 15s | -- |
| Avg API response | -- | 0.28ms | <5 ms |
| Avg dashboard response | -- | 0.69ms | <10 ms |

### Interpreting Results

**Healthy metrics:**
- Idle RAM: <30 MB (with all three agents)
- Load overhead: <5 MB
- API response: <5 ms
- Dashboard response: <10 ms

**Investigate if:**
- Idle RAM >35 MB
- Load overhead >10 MB
- Response times >50 ms

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
chmod +x onwatch
```

### Port already in use

```bash
./onwatch stop           # Stop existing instance
./onwatch --port 9000    # Or use a different port
```
