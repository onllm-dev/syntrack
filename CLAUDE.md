# onWatch - CLAUDE.md

## Overview

**onWatch** is a minimal, ultra-lightweight Go CLI tool that tracks [Anthropic](https://anthropic.com) (Claude Code), [Synthetic API](https://synthetic.new), and [Z.ai](https://z.ai) usage by polling quota endpoints at configurable intervals, storing historical data in SQLite, and serving a Material Design 3 web dashboard with dark/light mode for usage visualization. Supports multiple providers running in parallel.

**This is an open-source project.** No secrets, no waste, clean repo.

**Core Design Principles:**
- **Ultra-lightweight:** Measured RAM footprint of ~28 MB idle with all three agents (Synthetic, Z.ai, Anthropic) polling in parallel. This app runs as a background agent -- memory efficiency is paramount.
- **Single binary:** No external dependencies at runtime. All templates and static assets embedded via `embed.FS`.
- **TDD-first:** Every feature is built test-first. Red -> Green -> Refactor. No exceptions.
- **Efficient polling:** The `/v2/quotas` endpoint does NOT count against quota, but we still poll responsibly (default 60s).
- **Visual clarity:** Users must instantly see which quotas approach limits and when they reset.

## RAM Efficiency Guidelines

Since onWatch runs as a background daemon, RAM is our primary constraint:

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

**Measured RAM (2026-02-08, all three agents polling in parallel):**

| Metric | Measured | Budget |
|--------|----------|--------|
| Idle RSS (avg) | 27.5 MB | 30 MB |
| Idle RSS (P95) | 27.5 MB | 30 MB |
| Load RSS (avg) | 28.5 MB | 50 MB |
| Load RSS (P95) | 29.0 MB | 50 MB |
| Load delta | +0.9 MB (+3.4%) | <5 MB |
| Avg API response | 0.28 ms | <5 ms |
| Avg dashboard response | 0.69 ms | <10 ms |
| Load test throughput | 1,160 reqs / 15s | -- |

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
onwatch/
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
│   │   ├── zai_client.go              # HTTP client for Z.ai API
│   │   ├── anthropic_types.go         # Anthropic: dynamic quota response types
│   │   ├── anthropic_client.go        # HTTP client for Anthropic OAuth usage API
│   │   ├── anthropic_token.go         # Shared token detection entry point
│   │   ├── anthropic_token_unix.go    # macOS Keychain + Linux keyring/file detection
│   │   └── anthropic_token_windows.go # Windows file-based detection
│   ├── store/
│   │   ├── store.go                    # SQLite schema, CRUD, users table, auth tokens
│   │   ├── store_test.go
│   │   ├── zai_store.go               # Z.ai-specific queries
│   │   ├── anthropic_store.go         # Anthropic snapshot/cycle queries
│   │   └── anthropic_store_test.go
│   ├── tracker/
│   │   ├── tracker.go                  # Synthetic reset detection + usage delta
│   │   ├── tracker_test.go
│   │   ├── zai_tracker.go             # Z.ai reset detection + usage delta
│   │   ├── zai_tracker_test.go
│   │   └── anthropic_tracker.go       # Anthropic utilization tracking + cycle mgmt
│   ├── agent/
│   │   ├── agent.go                    # Synthetic background polling loop
│   │   ├── agent_test.go
│   │   ├── zai_agent.go               # Z.ai background polling loop
│   │   └── anthropic_agent.go         # Anthropic background polling loop
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
│   └── onwatch/
│       ├── MASTER.md
│       └── pages/
│           └── dashboard.md
│
├── tools/
│   └── perf-monitor/                   # RAM + HTTP performance measurement tool
│
└── LICENSE                             # GPL-3.0
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

### Anthropic OAuth Usage API -- GET /api/oauth/usage

**Endpoint:** `https://api.anthropic.com/api/oauth/usage`
**Auth:** `Authorization: Bearer <ANTHROPIC_TOKEN>`, `anthropic-beta: oauth-2025-04-20`
**Token source:** Auto-detected from Claude Code credentials (macOS Keychain, Linux keyring, `~/.claude/.credentials.json`)

**Response shape:**
```json
{
  "five_hour": { "utilization": 45.2, "resets_at": "2026-02-07T14:00:00Z", "is_enabled": true },
  "seven_day": { "utilization": 12.8, "resets_at": "2026-02-10T00:00:00Z", "is_enabled": true },
  "seven_day_sonnet": { "utilization": 5.1, "resets_at": "2026-02-10T00:00:00Z", "is_enabled": true },
  "monthly_limit": { "utilization": 67.3, "resets_at": "2026-03-01T00:00:00Z", "is_enabled": true, "monthly_limit": 100.0, "used_credits": 67.3 },
  "extra_usage": null
}
```

**Key facts:**
- Response is a `map[string]*QuotaEntry` — **dynamic keys** (not fixed). Null entries are skipped.
- `utilization` is a percentage (0-100), not an absolute count
- `extra_usage` with `is_enabled: false` is filtered out
- `resets_at` is ISO 8601 (same as Synthetic, unlike Z.ai)
- New quota types may appear in the future — the DB schema uses normalized key-value storage

### Provider Mapping

| onWatch Concept | Anthropic API | Synthetic API | Z.ai Equivalent |
|-----------------|--------------|---------------|-----------------|
| Real-time snapshot | `GET /api/oauth/usage` | `GET /v2/quotas` | `GET /monitor/usage/quota/limit` |
| Primary quota | `five_hour` (utilization %) | `subscription` (requests/limit) | `TOKENS_LIMIT` (currentValue/usage) |
| Secondary quotas | `seven_day`, `monthly_limit`, etc. | `search.hourly` | `TIME_LIMIT` (tool calls) |
| Reset time | `resets_at` (ISO 8601) | `renewsAt` (ISO 8601) | `nextResetTime` (epoch ms) |
| Quota structure | Dynamic key-value | Fixed 3 quotas | Fixed 2-3 limits |

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
./onwatch                         # Background: daemonize, log to ~/.onwatch/.onwatch.log
./onwatch --debug                 # Foreground: log to stdout
./onwatch --interval 30           # Poll every 30 seconds
./onwatch --port 9000             # Dashboard on port 9000
./onwatch stop                    # Stop running instance
./onwatch status                  # Check if running

# Environment setup
cp .env.example .env               # Create local config
```

## CLI Reference

### Subcommands

| Command | Description |
|---------|-------------|
| `onwatch` | Start agent (background mode) |
| `onwatch stop` or `--stop` | Stop running instance (PID file + port fallback) |
| `onwatch status` or `--status` | Show status of running instance |

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--interval` | Polling interval in seconds | `60` |
| `--port` | Dashboard HTTP port | `9211` |
| `--db` | SQLite database file path | `~/.onwatch/data/onwatch.db` |
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
- **NEVER commit binaries** (`onwatch`, `*.exe`, `/bin/`, `/dist/`)
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

-- Anthropic snapshots (normalized key-value for dynamic quotas)
CREATE TABLE IF NOT EXISTS anthropic_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    captured_at TEXT NOT NULL, raw_json TEXT, quota_count INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS anthropic_quota_values (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id INTEGER NOT NULL REFERENCES anthropic_snapshots(id),
    quota_name TEXT NOT NULL, utilization REAL NOT NULL, resets_at TEXT
);
CREATE TABLE IF NOT EXISTS anthropic_reset_cycles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    quota_name TEXT NOT NULL, cycle_start TEXT NOT NULL, cycle_end TEXT,
    resets_at TEXT NOT NULL, peak_utilization REAL NOT NULL DEFAULT 0,
    total_delta REAL NOT NULL DEFAULT 0
);
```

## Dashboard Design

See `design-system/onwatch/MASTER.md` for design tokens and component specs.
See `design-system/onwatch/pages/dashboard.md` for dashboard-specific layout.

**Key design decisions:**
- Material Design 3 with dark + light mode
- Three quota cards per provider (Synthetic/Z.ai), dynamic cards for Anthropic
- Anthropic cards rendered dynamically from API response (variable quota count)
- Color-coded thresholds: green (0-49%), yellow (50-79%), red (80-94%), critical (95%+)
- Provider accent colors: Synthetic (primary), Z.ai (teal), Anthropic (coral #D97757)
- Accessibility: color + icon + text for all status indicators
- Live countdown updating every second
- Provider-specific usage insights
- Chart.js area chart with time range selector
- Footer with version display and "Change Password" button
- Password change modal with current/new/confirm fields

**Provider tab order:** Anthropic (default), Synthetic, Z.ai, All. Controlled by `AvailableProviders()` in `config.go`.

**Anthropic display names** (must match in both Go `anthropic_types.go` and JS `app.js`):

| API Key | Display Name |
|---------|-------------|
| `five_hour` | 5-Hour Limit |
| `seven_day` | Weekly All-Model |
| `seven_day_sonnet` | Weekly Sonnet |
| `monthly_limit` | Monthly Limit |
| `extra_usage` | Extra Usage |

**Anthropic chart colors** (key-based map in `anthropicChartColorMap`, not index-based):

| Quota | Color | Hex |
|-------|-------|-----|
| `five_hour` | Coral | `#D97757` |
| `seven_day` | Emerald | `#10B981` |
| `seven_day_sonnet` | Blue | `#3B82F6` |
| `monthly_limit` | Violet | `#A855F7` |
| `extra_usage` | Amber | `#F59E0B` |

## Quota Reset Tracking Logic

Core intelligence of onWatch -- tracking usage across reset boundaries:

1. **Detection:** Compare consecutive snapshots' `renewsAt` timestamps. If changed, a reset occurred. Each quota type tracked independently.
2. **Cycle management:** On reset, close current cycle (set `cycle_end`), open new cycle. Record `peak_requests` and accumulate `total_delta`.
3. **Usage calculation:** `delta = curr.requests - prev.requests` (if positive and same cycle). Negative delta means reset happened between polls.
4. **Insights derivation:** Average usage per cycle, current rate, projected usage before reset, 30-day total.
