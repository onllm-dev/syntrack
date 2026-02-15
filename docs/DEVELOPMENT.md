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
./app.sh --build    # or: make build
```

This reads the version from the `VERSION` file and injects it via ldflags.

---

## Platform-Specific Setup

### macOS

```bash
./app.sh --deps     # auto-installs Go + git via Homebrew
./app.sh --build
```

### Ubuntu / Debian

```bash
./app.sh --deps     # auto-installs Go + git via apt
./app.sh --build
```

### CentOS / RHEL / Fedora

```bash
./app.sh --deps     # auto-installs Go + git via dnf
./app.sh --build
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

## Commands

`app.sh` is the primary entry point. `make` targets are thin wrappers.

```bash
./app.sh --build          # Build production binary (or: make build)
./app.sh --test           # Tests with race detection and coverage (or: make test)
./app.sh --build --run    # Build + run in debug mode (or: make run)
./app.sh --clean          # Remove binary, coverage, dist/ (or: make clean)
./app.sh --smoke          # Quick validation: vet + build + short tests
./app.sh --release        # Cross-compile all 5 platforms (or: make release-local)
./app.sh --deps           # Install Go + git for your platform
make dev                  # go run . --debug --interval 10
make lint                 # go fmt + go vet
make coverage             # HTML coverage report
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
ANTHROPIC_TOKEN=your_anthropic_token      # Auto-detected from Claude Code if not set
COPILOT_TOKEN=ghp_your_github_token       # GitHub PAT with 'copilot' scope (Beta)
```

All four providers can run simultaneously. Configure any combination.

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

onWatch supports four providers: Synthetic, Z.ai, Anthropic, and GitHub Copilot (Beta). When multiple API keys are set, all agents run in parallel goroutines, each polling its respective API and storing snapshots in the shared SQLite database.

The dashboard switches between providers via the `?provider=` query parameter. Each provider renders its own quota cards, insight cards, and stat summaries. Synthetic insights focus on cycle utilization and billing periods; Z.ai insights show plan capacity (daily/monthly token budgets), tokens-per-call efficiency, and top tool analysis; Anthropic insights show burn rate forecasting, window averages, projected exhaustion, and cross-quota ratio analysis (5-Hour vs Weekly); GitHub Copilot (Beta) insights are under active development.

A dedicated settings page (`/settings`) provides tabbed configuration for provider controls, notification thresholds, and SMTP email alerts. The notification engine (`internal/notify/`) checks quota statuses against thresholds and dispatches alerts for warning, critical, and reset events via email (SMTP) and/or browser push notifications (Web Push). Delivery channels are configurable per user preference.

Key source files:

| File | Purpose |
|------|---------|
| `internal/api/client.go` | Synthetic API client |
| `internal/api/zai_client.go` | Z.ai API client |
| `internal/api/anthropic_client.go` | Anthropic OAuth API client |
| `internal/api/copilot_client.go` | GitHub Copilot API client (Beta) |
| `internal/agent/agent.go` | Synthetic polling agent |
| `internal/agent/zai_agent.go` | Z.ai polling agent |
| `internal/agent/anthropic_agent.go` | Anthropic polling agent |
| `internal/agent/copilot_agent.go` | GitHub Copilot polling agent (Beta) |
| `internal/agent/session_manager.go` | Cross-agent session lifecycle |
| `internal/store/store.go` | Shared SQLite store + settings |
| `internal/store/zai_store.go` | Z.ai-specific queries |
| `internal/store/anthropic_store.go` | Anthropic-specific queries |
| `internal/store/copilot_store.go` | GitHub Copilot-specific queries (Beta) |
| `internal/notify/notify.go` | Notification engine: thresholds + alerts |
| `internal/notify/smtp.go` | SMTP mailer: TLS/STARTTLS delivery |
| `internal/notify/push.go` | Web Push sender: VAPID + RFC 8291 encryption |
| `internal/notify/crypto.go` | AES-GCM encryption for SMTP passwords |
| `internal/web/handlers.go` | Provider-aware route handlers + settings |
| `internal/web/templates/settings.html` | Settings page template |

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
./app.sh --release    # or: make release-local
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

### Docker Container Registry

The workflow at `.github/workflows/docker-publish.yml` automatically builds and publishes multi-platform Docker images to GitHub Container Registry (GHCR) when a release is published.

**Features:**
- Multi-architecture support: `linux/amd64` and `linux/arm64`
- Published to `ghcr.io/kquinsland/onwatch`
- Tagged with version numbers and `latest`
- Uses Docker buildx with QEMU for cross-platform builds
- Implements GitHub Actions cache for faster builds

**Tags generated:**
- `ghcr.io/kquinsland/onwatch:latest` - Latest release
- `ghcr.io/kquinsland/onwatch:vX.Y.Z` - Specific version (e.g., v2.10.0)
- `ghcr.io/kquinsland/onwatch:X.Y` - Major.minor (e.g., 2.10)
- `ghcr.io/kquinsland/onwatch:X` - Major version (e.g., 2)

**Workflow triggers:**
- Automatically on release publication
- After the `release.yml` workflow creates a GitHub release

**Usage:**
```bash
# Pull the latest image
docker pull ghcr.io/kquinsland/onwatch:latest

# Or a specific version
docker pull ghcr.io/kquinsland/onwatch:vX.Y.Z

# Run it
docker run -d --name onwatch -p 9211:9211 \
  -v onwatch-data:/data \
  --env-file .env \
  ghcr.io/kquinsland/onwatch:latest
```

---

## Dependencies

| Package | Purpose |
|---------|---------|
| `modernc.org/sqlite` | Pure Go SQLite driver (no CGO) |
| `github.com/joho/godotenv` | `.env` file loading |
| `golang.org/x/crypto` | HKDF for Web Push encryption (RFC 8291) |

Install or update:

```bash
go mod tidy
```

---

## Docker Development

onWatch provides Docker support via `app.sh --docker` and a multi-stage Dockerfile with a distroless runtime image (~10-12 MB).

### app.sh Docker Commands

```bash
./app.sh --docker --build      # Build Docker image
./app.sh --docker --run        # Build image and start container
./app.sh --docker --stop       # Stop running container
./app.sh --docker --clean      # Remove container and image
```

### How It Works

- **Build stage:** `golang:1.25-alpine` compiles a static binary (`CGO_ENABLED=0`) with `-trimpath` and stripped debug symbols
- **Runtime stage:** `gcr.io/distroless/static-debian12:nonroot` — no shell, no package manager, minimal attack surface
- **Docker detection:** `config.IsDockerEnvironment()` checks for `/.dockerenv` or `DOCKER=true` env var. When detected, onWatch skips daemonization and logs to stdout
- **Data persistence:** SQLite database stored at `/data/onwatch.db` via volume mount
- **Non-root:** Container runs as UID 65532 (distroless `nonroot` user)

### Docker Development Workflow

```bash
# 1. Create .env from Docker template
cp .env.docker.example .env
# Edit .env — add at least one API key

# 2. Build and run
./app.sh --docker --run

# 3. View logs
docker logs -f onwatch

# 4. Access dashboard
open http://localhost:9211

# 5. Stop
./app.sh --docker --stop
```

### Manual Docker Build

If you need more control than `app.sh` provides:

```bash
docker build -t onwatch:latest \
  --build-arg VERSION=$(cat VERSION) \
  --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) .

docker run -d --name onwatch \
  -p 9211:9211 \
  -v ./onwatch-data:/data \
  --env-file .env \
  onwatch:latest
```

### Docker Compose

```bash
docker-compose up -d        # Start
docker-compose logs -f      # Follow logs
docker-compose down         # Stop and remove
```

The `docker-compose.yml` includes memory limits (64M limit, 32M reservation), log rotation, and `unless-stopped` restart policy. Data is persisted via bind mount at `./onwatch-data/`.

**Note:** For bind mounts, pre-create the directory with correct ownership:

```bash
mkdir -p ./onwatch-data && sudo chown -R 65532:65532 ./onwatch-data
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

Example results (all four agents -- Synthetic, Z.ai, Anthropic, GitHub Copilot -- polling in parallel):
```
IDLE STATE (4 agents polling concurrently):
  Avg RSS: 36.8 MB
  P95 RSS: 40.2 MB

LOAD STATE (4,584 requests in 60s while agents poll):
  Avg RSS: 44.2 MB
  P95 RSS: 45.8 MB
  Delta:   +7.5 MB (+20%)

HTTP PERFORMANCE:
  /                    573 reqs  avg: 1.15ms
  /api/current         573 reqs  avg: 0.41ms
  /api/history         573 reqs  avg: 0.39ms
  /api/cycles          573 reqs  avg: 0.36ms
  /api/insights        573 reqs  avg: 0.33ms
  /api/summary         573 reqs  avg: 0.35ms
  /api/sessions        573 reqs  avg: 0.34ms
  /api/providers       573 reqs  avg: 0.48ms
```

### Latest Benchmark (2026-02-15)

Measured with the built-in `tools/perf-monitor` while all four provider agents (Synthetic, Z.ai, Anthropic, GitHub Copilot) ran in parallel, each polling its respective API every 60 seconds and writing snapshots to the shared SQLite database. Includes server-side chart downsampling (max 500 data points per response).

| Metric | Idle | Under Load | Budget |
|--------|------|------------|--------|
| Avg RSS | 36.8 MB | 44.2 MB | 35 MB (idle) / 50 MB (load) |
| P95 RSS | 40.2 MB | 45.8 MB | -- |
| Load delta | -- | +7.5 MB (+20%) | <20 MB |
| Total requests | -- | 4,584 in 60s | -- |
| Avg API response | -- | 0.38ms | <5 ms |
| Avg dashboard response | -- | 1.15ms | <10 ms |

### Interpreting Results

**Healthy metrics:**
- Idle RAM: <40 MB (with all four agents)
- Load overhead: <20 MB (includes server-side downsampling allocations)
- API response: <5 ms
- Dashboard response: <10 ms

**Investigate if:**
- Idle RAM >45 MB
- Load overhead >25 MB
- Response times >50 ms

---

## Self-Update Mechanism

onWatch includes a self-update system that downloads new releases from GitHub and replaces the running binary. The update can be triggered from the dashboard (update badge in footer) or via `onwatch update`.

### Update Flow

1. **Check**: Queries `https://api.github.com/repos/onllm-dev/onwatch/releases/latest` (cached for 1 hour)
2. **Apply**: Downloads the platform-specific binary, validates magic bytes (ELF/Mach-O/PE), replaces the current binary using remove+rename (Unix) or backup-rename (Windows)
3. **Migrate**: Fixes the systemd unit file if running under systemd (`Restart=always`, `RestartSec=5`)
4. **Restart**: Uses `systemctl restart` under systemd, or spawns a new process in standalone mode

### systemd Integration

Under systemd, onWatch auto-detects its service name from `/proc/self/cgroup` and uses `systemctl restart` for proper lifecycle management. Three layers ensure reliability:

| Layer | When | Purpose |
|-------|------|---------|
| `Apply()` | After binary replacement | Fixes unit file before any restart attempt |
| `Restart()` | After apply | Runs `systemctl restart <service>` |
| Startup | Every boot | Safety net — re-checks unit file settings |

The startup migration runs before `stopPreviousInstance()`. This is critical for upgrades from older versions: when an old binary spawns the new binary as a post-update child, the child fixes the unit file while the parent is still alive, then kills the parent. systemd sees the main PID die, and `Restart=always` triggers an automatic restart with the new binary.

### Key Source Files

| File | Purpose |
|------|---------|
| `internal/update/update.go` | Version check, download, binary replacement, systemd migration |
| `internal/web/handlers.go` | `/api/update/check` and `/api/update/apply` endpoints |
| `main.go` | `MigrateSystemdUnit()` call on startup, `runUpdate()` CLI handler |

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
