# SynTrack - CLAUDE.md

## Overview

**SynTrack** is a minimal, ultra-lightweight Go CLI tool that tracks [Synthetic API](https://synthetic.new) and [Z.ai](https://z.ai) usage by polling quota endpoints at configurable intervals, storing historical data in SQLite, and serving a Material Design 3 web dashboard with dark/light mode for usage visualization. Supports multiple providers running in parallel.

**This is an open-source project.** No secrets, no waste, clean repo.

**Core Design Principles:**
- **Ultra-lightweight:** Minimal RAM footprint (~25-30 MB idle). This app runs as a background agent -- memory efficiency is paramount.
- **Single binary:** No external dependencies at runtime. All templates and static assets embedded via `embed.FS`.
- **TDD-first:** Every feature is built test-first. Red -> Green -> Refactor. No exceptions.
- **Efficient polling:** The `/v2/quotas` endpoint does NOT count against quota, but we still poll responsibly (default 60s).
- **Visual clarity:** Users must instantly see which quotas approach limits and when they reset.

## RAM Efficiency Guidelines

Since SynTrack runs as a background daemon, RAM is our primary constraint:

| Guideline | Implementation |
|-----------|---------------|
| No goroutine leaks | Always use `context.Context` for cancellation |
| Bounded buffers | Limit in-memory snapshot cache (last 100 max) |
| No ORM | Raw SQL with `database/sql` interface -- no reflection overhead |
| Embedded assets | `embed.FS` -- no runtime file reads |
| System fonts preferred | Google Fonts lazy-loaded only when dashboard viewed |
| Chart.js via CDN | Not bundled in binary -- loaded by browser only |
| SQLite WAL mode | Enables concurrent reads without memory-heavy locks |
| Minimal dependencies | Only `modernc.org/sqlite` and `godotenv` |
| HTTP server idle cost | `net/http` idles at ~1 MB -- acceptable |

**Target RAM budget:**

| Component | Budget |
|-----------|--------|
| Go runtime | 5 MB |
| SQLite (in-process) | 2 MB |
| HTTP server (idle) | 1 MB |
| Agent + polling buffer | 1 MB |
| Template rendering | 1 MB |
| **Total idle** | **~30 MB max** |
| **During dashboard render** | **~50 MB max** |

## Tech Stack

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| Language | Go 1.25+ | Low memory, single binary, fast startup |
| Database | SQLite via `modernc.org/sqlite` | Pure Go (no CGO), zero config, ~2 MB memory |
| HTTP | `net/http` (stdlib) | No framework overhead |
| Templates | `html/template` + `embed.FS` | Embedded in binary, zero disk I/O |
| Static assets | `embed.FS` | CSS/JS embedded, served from memory |
| Charts | Chart.js 4.x (CDN) | Not in binary -- browser loads on demand |
| Config | `.env` + CLI flags | Simple, standard |
| Auth | Session cookies + Basic Auth fallback | SHA-256 password hashing, DB-persisted credentials |
| Design | Material Design 3 | Dark + Light mode, clean, professional |
| Logging | `log/slog` (stdlib) | Structured, zero-alloc, built-in |

## Project Structure

```
syntrack/
├── CLAUDE.md                           # THIS FILE
├── README.md                           # User-facing documentation
├── DEVELOPMENT.md                      # Build guide + perf monitoring
├── .env.example                        # Template (safe to commit)
├── .env                                # Local secrets (NEVER committed)
├── .gitignore
├── go.mod / go.sum                     # Go module
├── main.go                             # Entry point: CLI, wiring, lifecycle, DB migration
├── platform_unix.go                    # Unix-specific: daemonize, PID dir
├── platform_windows.go                 # Windows-specific: daemonize, PID dir
├── Makefile                            # build, test, clean, run targets
├── VERSION                             # Single source of truth for version
│
├── internal/
│   ├── config/
│   │   ├── config.go                   # Load .env + CLI flags, multi-provider config
│   │   └── config_test.go
│   ├── api/
│   │   ├── types.go                    # Synthetic: QuotaResponse, QuotaInfo structs
│   │   ├── types_test.go
│   │   ├── client.go                   # HTTP client for Synthetic API
│   │   ├── client_test.go
│   │   ├── zai_types.go               # Z.ai response types + snapshot conversion
│   │   └── zai_client.go              # HTTP client for Z.ai API
│   ├── store/
│   │   ├── store.go                    # SQLite schema, CRUD, users table, auth tokens
│   │   ├── store_test.go
│   │   └── zai_store.go               # Z.ai-specific queries
│   ├── tracker/
│   │   ├── tracker.go                  # Synthetic reset detection + usage delta
│   │   ├── tracker_test.go
│   │   ├── zai_tracker.go             # Z.ai reset detection + usage delta
│   │   └── zai_tracker_test.go
│   ├── agent/
│   │   ├── agent.go                    # Synthetic background polling loop
│   │   ├── agent_test.go
│   │   └── zai_agent.go               # Z.ai background polling loop
│   └── web/
│       ├── server.go                   # HTTP server setup + route registration
│       ├── server_test.go
│       ├── handlers.go                 # Route handlers (HTML + JSON API + password change)
│       ├── handlers_test.go
│       ├── middleware.go               # Session auth, Basic Auth, SHA-256 hashing
│       ├── middleware_test.go
│       ├── static/                     # Embedded static assets
│       │   ├── style.css
│       │   ├── app.js
│       │   └── favicon.svg
│       └── templates/                  # Go html/template files (embedded)
│           ├── layout.html
│           ├── dashboard.html          # Main page + footer + password modal
│           └── login.html
│
├── static/                             # Sync copy of internal/web/static/ (keep in sync)
│   ├── style.css
│   └── app.js
│
├── screenshots/                        # Dashboard screenshots
│   └── INDEX.md                        # Screenshot descriptions
│
├── design-system/                      # UI/UX design specifications
│   └── syntrack/
│       ├── MASTER.md
│       └── pages/
│           └── dashboard.md
│
├── tools/
│   └── perf-monitor/                   # RAM + HTTP performance measurement tool
│
└── LICENSE                             # MIT
```

## API Reference

### Synthetic API -- GET /v2/quotas

**Endpoint:** `https://api.synthetic.new/v2/quotas`
**Auth:** `Authorization: Bearer <SYNTHETIC_API_KEY>`
**Rate Limit:** Does NOT count against quota.

**Response shape:**
```json
{
  "subscription": { "limit": 1350, "requests": 154.3, "renewsAt": "2026-02-06T16:16:18.386Z" },
  "search": { "hourly": { "limit": 250, "requests": 0, "renewsAt": "..." } },
  "toolCallDiscounts": { "limit": 16200, "requests": 7635, "renewsAt": "..." }
}
```

Key facts: `requests` is `float64`. Three independent quotas with independent `renewsAt` timestamps.

### Z.ai API -- GET /monitor/usage/quota/limit

**Endpoint:** `{ZAI_BASE_URL}/monitor/usage/quota/limit`
**Auth:** `Authorization: <ZAI_API_KEY>` (no Bearer prefix)
**Base URLs:** `https://api.z.ai/api` (default) or `https://open.bigmodel.cn/api` (mirror, identical data)

**Response shape:**
```json
{
  "code": 200,
  "data": {
    "limits": [
      { "type": "TIME_LIMIT", "usage": 1000, "currentValue": 19, "remaining": 981, "percentage": 1,
        "usageDetails": [{ "modelCode": "search-prime", "usage": 16 }] },
      { "type": "TOKENS_LIMIT", "usage": 200000000, "currentValue": 200112618, "remaining": 0,
        "percentage": 100, "nextResetTime": 1770398385482 }
    ]
  }
}
```

**Key facts and gotchas:**
- **Field naming is confusing:** `usage` = quota budget (limit), `currentValue` = actual consumption
- `nextResetTime` is epoch milliseconds (not ISO 8601) -- only present on `TOKENS_LIMIT`
- `TIME_LIMIT` has no `nextResetTime` -- reset cycle unclear
- `currentValue` can exceed `usage` (no hard cap)
- Auth errors return HTTP 200 with `{"code": 401, "msg": "token expired or incorrect"}` in body
- `usageDetails` (per-model breakdown) only present on `TIME_LIMIT`
- `unit` and `number` fields appear in responses but their meaning is unknown

**Additional time-series endpoints (not used for polling):**
- `GET /monitor/usage/model-usage?startTime={}&endTime={}` -- hourly API call counts + token usage
- `GET /monitor/usage/tool-usage?startTime={}&endTime={}` -- hourly per-tool breakdown

### Provider Mapping

| SynTrack Concept | Synthetic API | Z.ai Equivalent |
|-----------------|---------------|-----------------|
| Real-time snapshot | `GET /v2/quotas` | `GET /monitor/usage/quota/limit` |
| Primary quota | `subscription` (requests/limit) | `TOKENS_LIMIT` (currentValue/usage) |
| Secondary quota | `search.hourly` | `TIME_LIMIT` (tool calls) |
| Reset time | `renewsAt` (ISO 8601) | `nextResetTime` (epoch ms) |

## Commands

```bash
# Development
go test ./...                      # Run all tests
go test -race ./...                # Race detection (ALWAYS run before commit)
go test -cover ./...               # Coverage report
go test ./internal/store/ -v       # Specific package

# Make targets
make build                         # Production binary (version from VERSION file)
make test                          # go test -race -cover ./...
make run                           # Build + run in debug mode
make dev                           # go run . --debug --interval 10
make clean                         # Remove binary + test artifacts + dist/ + db files
make lint                          # go fmt + go vet
make coverage                      # Generate HTML coverage report
make release-local                 # Cross-compile for 5 platforms -> dist/

# Running
./syntrack                         # Background: daemonize, log to ~/.syntrack/.syntrack.log
./syntrack --debug                 # Foreground: log to stdout
./syntrack --interval 30           # Poll every 30 seconds
./syntrack --port 9000             # Dashboard on port 9000
./syntrack stop                    # Stop running instance
./syntrack status                  # Check if running

# Environment setup
cp .env.example .env               # Create local config
```

## CLI Reference

### Subcommands

| Command | Description |
|---------|-------------|
| `syntrack` | Start agent (background mode) |
| `syntrack stop` or `--stop` | Stop running instance (PID file + port fallback) |
| `syntrack status` or `--status` | Show status of running instance |

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--interval` | Polling interval in seconds | `60` |
| `--port` | Dashboard HTTP port | `9211` |
| `--db` | SQLite database file path | `~/.syntrack/data/syntrack.db` |
| `--debug` | Run in foreground, log to stdout | `false` |
| `--test` | Test mode: isolated PID/log, won't kill production | `false` |
| `--version` | Print version and exit | - |
| `--help` | Print help and exit | - |

## Authentication & Password Management

- Passwords stored as SHA-256 hex hashes in the `users` table
- On first run, the `.env` password is hashed and stored in the DB
- On subsequent runs, the DB hash takes precedence (`.env` is not re-read for password)
- `PUT /api/password` lets the user change their password from the dashboard
- Password changes invalidate all sessions (force re-login)
- `HashPassword()` in `middleware.go` produces the SHA-256 hex hash
- `NewSessionStore` and `NewServer` accept a password hash, not plaintext

## Development Rules

### TDD Protocol (MANDATORY)

1. Write the failing test FIRST
2. Run the test, see it fail
3. Write minimal implementation to pass
4. Run the test, see it pass
5. Refactor if tests stay green
6. Test file lives next to source: `foo.go` -> `foo_test.go`
7. Table-driven tests: `[]struct{ name, input, want }`
8. Prefer real SQLite (`:memory:`) and `httptest.NewServer` over mocks
9. Test names describe behavior: `TestTracker_DetectsQuotaReset_WhenRenewsAtChanges`
10. Run with `-race` before every commit

### Security Rules (NON-NEGOTIABLE)

- **NEVER commit `.env`** -- only `.env.example` with placeholder values
- **NEVER log API keys** -- redact in all log output
- **NEVER embed secrets in code** -- always load from env/flags
- **NEVER commit database files** (`.db`, `.db-journal`, `.db-wal`, `.db-shm`)
- **NEVER commit binaries** (`syntrack`, `*.exe`, `/bin/`, `/dist/`)
- **ALWAYS use parameterized SQL** -- never string interpolation in queries
- **ALWAYS use `subtle.ConstantTimeCompare`** for credential comparison
- **ALWAYS set HTTP timeouts** -- 10s for API client, 5s for shutdown

### Code Style

- Follow `gofmt` / `govet` conventions
- Use `internal/` to prevent external imports
- Error wrapping: `fmt.Errorf("store.Save: %w", err)`
- Structured logging: `slog.Info("poll complete", "requests", resp.Subscription.Requests)`
- Constants over magic numbers

### Git Hygiene

- Conventional commits: `feat:`, `fix:`, `test:`, `docs:`, `refactor:`, `perf:`
- Atomic commits -- one logical change per commit
- Always run `go test -race ./...` before committing
- Never commit generated files, temp files, or IDE config

### Static File Sync

Static files exist in TWO places. Always keep them in sync:
- `internal/web/static/` -- embedded in binary (source of truth)
- `static/` -- sync copy

After editing `internal/web/static/`, copy to `static/`:
```bash
cp internal/web/static/style.css static/style.css
cp internal/web/static/app.js static/app.js
```

## Database Schema

All tables created in `store.go:createTables()`:

```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA cache_size=-2000;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;

-- Version tracking
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);

-- Synthetic snapshots (append-only)
CREATE TABLE IF NOT EXISTS quota_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL DEFAULT 'synthetic',
    captured_at TEXT NOT NULL,
    sub_limit REAL NOT NULL, sub_requests REAL NOT NULL, sub_renews_at TEXT NOT NULL,
    search_limit REAL NOT NULL, search_requests REAL NOT NULL, search_renews_at TEXT NOT NULL,
    tool_limit REAL NOT NULL, tool_requests REAL NOT NULL, tool_renews_at TEXT NOT NULL
);

-- Reset cycles (one row per cycle per quota type)
CREATE TABLE IF NOT EXISTS reset_cycles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL DEFAULT 'synthetic',
    quota_type TEXT NOT NULL, cycle_start TEXT NOT NULL, cycle_end TEXT,
    renews_at TEXT NOT NULL, peak_requests REAL NOT NULL DEFAULT 0,
    total_delta REAL NOT NULL DEFAULT 0
);

-- Agent sessions
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL DEFAULT 'synthetic',
    started_at TEXT NOT NULL, ended_at TEXT, poll_interval INTEGER NOT NULL,
    max_sub_requests REAL NOT NULL DEFAULT 0,
    max_search_requests REAL NOT NULL DEFAULT 0,
    max_tool_requests REAL NOT NULL DEFAULT 0,
    snapshot_count INTEGER NOT NULL DEFAULT 0
);

-- Key-value settings
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);

-- Session auth tokens (persisted across restarts)
CREATE TABLE IF NOT EXISTS auth_tokens (token TEXT PRIMARY KEY, expires_at TEXT NOT NULL);

-- User credentials (SHA-256 password hashes)
CREATE TABLE IF NOT EXISTS users (
    username TEXT PRIMARY KEY, password_hash TEXT NOT NULL, updated_at TEXT NOT NULL
);

-- Z.ai snapshots, hourly usage, reset cycles (see store.go for full schema)
```

## Dashboard Design

See `design-system/syntrack/MASTER.md` for design tokens and component specs.
See `design-system/syntrack/pages/dashboard.md` for dashboard-specific layout.

**Key design decisions:**
- Material Design 3 with dark + light mode
- Three quota cards per provider, each with progress bar, countdown, status badge
- Color-coded thresholds: green (0-49%), yellow (50-79%), red (80-94%), critical (95%+)
- Accessibility: color + icon + text for all status indicators
- Live countdown updating every second
- Provider-specific usage insights
- Chart.js area chart with time range selector
- Footer with version display and "Change Password" button
- Password change modal with current/new/confirm fields

## Quota Reset Tracking Logic

Core intelligence of SynTrack -- tracking usage across reset boundaries:

1. **Detection:** Compare consecutive snapshots' `renewsAt` timestamps. If changed, a reset occurred. Each quota type tracked independently.
2. **Cycle management:** On reset, close current cycle (set `cycle_end`), open new cycle. Record `peak_requests` and accumulate `total_delta`.
3. **Usage calculation:** `delta = curr.requests - prev.requests` (if positive and same cycle). Negative delta means reset happened between polls.
4. **Insights derivation:** Average usage per cycle, current rate, projected usage before reset, 30-day total.
