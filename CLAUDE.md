# SynTrack - CLAUDE.md

## Overview

**SynTrack** is a minimal, ultra-lightweight Go CLI tool that tracks [Synthetic API](https://synthetic.new) and [Z.ai](https://z.ai) usage by polling quota endpoints at configurable intervals, storing historical data in SQLite, and serving a Material Design 3 web dashboard with dark/light mode for usage visualization. Supports multiple providers running in parallel.

**This is an open-source project.** No secrets, no waste, clean repo.

**Core Design Principles:**
- **Ultra-lightweight:** Minimal RAM footprint (~8-10 MB idle). This app runs as a background agent — memory efficiency is paramount. Every design decision must consider RAM impact.
- **Single binary:** No external dependencies at runtime. All templates and static assets embedded via `embed.FS`.
- **TDD-first:** Every feature is built test-first. Red → Green → Refactor. No exceptions.
- **Efficient polling:** The `/v2/quotas` endpoint does NOT count against quota, but we still poll responsibly (default 60s).
- **Visual clarity:** Users must instantly see which quotas approach limits and when they reset.

## RAM Efficiency Guidelines

Since SynTrack runs as a background daemon, RAM is our primary constraint:

| Guideline | Implementation |
|-----------|---------------|
| No goroutine leaks | Always use `context.Context` for cancellation |
| Bounded buffers | Limit in-memory snapshot cache (last 100 max) |
| No ORM | Raw SQL with `database/sql` interface — no reflection overhead |
| Embedded assets | `embed.FS` — no runtime file reads |
| System fonts preferred | Google Fonts lazy-loaded only when dashboard viewed |
| Chart.js via CDN | Not bundled in binary — loaded by browser only |
| SQLite WAL mode | Enables concurrent reads without memory-heavy locks |
| Minimal dependencies | Only `modernc.org/sqlite` and `godotenv` |
| No JSON marshaling cache | Use `encoding/json` directly, no struct caching |
| HTTP server idle cost | `net/http` idles at ~1 MB — acceptable |
| Periodic GC hint | After large operations, `runtime.GC()` if needed |

**Target RAM budget:**

| Component | Budget |
|-----------|--------|
| Go runtime | 5 MB |
| SQLite (in-process) | 2 MB |
| HTTP server (idle) | 1 MB |
| Agent + polling buffer | 1 MB |
| Template rendering | 1 MB |
| **Total idle** | **~10 MB max** |
| **During dashboard render** | **~15 MB max** |

## Tech Stack

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| Language | Go 1.25+ | Low memory, single binary, fast startup |
| Database | SQLite via `modernc.org/sqlite` | Pure Go (no CGO), zero config, ~2 MB memory |
| HTTP | `net/http` (stdlib) | No framework overhead |
| Templates | `html/template` + `embed.FS` | Embedded in binary, zero disk I/O |
| Static assets | `embed.FS` | CSS/JS embedded, served from memory |
| Charts | Chart.js 4.x (CDN) | Not in binary — browser loads on demand |
| Config | `.env` + CLI flags | Simple, standard |
| Auth | HTTP Basic Auth | Minimal, sufficient for single-user dashboard |
| Design | Material Design 3 | Dark + Light mode, clean, professional |
| Logging | `log/slog` (stdlib) | Structured, zero-alloc, built-in |

## Project Structure

```
syntrack/
├── CLAUDE.md                           # THIS FILE — project guide for agents
├── IMPLEMENTATION_PLAN.md              # Phased TDD build plan (source of truth)
├── README.md                           # User-facing documentation
├── .env.example                        # Template (safe to commit)
├── .env                                # Local secrets (NEVER committed)
├── .gitignore                          # Excludes secrets, db, binaries, screenshots
├── go.mod / go.sum                     # Go module
├── main.go                             # Entry point: CLI parsing, wiring, lifecycle
├── Makefile                            # build, test, clean, run targets
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
│   │   ├── store.go                    # SQLite CRUD + schema migrations
│   │   ├── store_test.go
│   │   ├── zai_store.go               # Z.ai-specific queries
│   │   ├── migrations.go              # Schema creation + versioning
│   │   └── queries.go                 # Named SQL query constants
│   ├── tracker/
│   │   ├── tracker.go                  # Reset detection + usage delta calculation
│   │   └── tracker_test.go
│   ├── agent/
│   │   ├── agent.go                    # Synthetic background polling loop
│   │   ├── agent_test.go
│   │   └── zai_agent.go               # Z.ai background polling loop
│   └── web/
│       ├── server.go                   # HTTP server setup + graceful shutdown
│       ├── server_test.go
│       ├── handlers.go                 # Route handlers (HTML + JSON API)
│       ├── handlers_test.go
│       ├── middleware.go               # Basic Auth middleware
│       ├── middleware_test.go
│       └── templates/                  # Go html/template files (embedded)
│           ├── layout.html             # Base layout with theme toggle
│           ├── dashboard.html          # Main dashboard page
│           └── login.html              # Login prompt page
│
├── static/                             # Frontend assets (embedded via embed.FS)
│   ├── style.css                       # Material Design 3 theme (dark + light)
│   └── app.js                          # Dashboard logic: charts, countdowns, refresh
│
├── design-system/                      # UI/UX design specifications
│   └── syntrack/
│       ├── MASTER.md                   # Global design tokens, colors, typography
│       └── pages/
│           └── dashboard.md            # Dashboard-specific component specs
│
└── LICENSE                             # MIT
```

## API Reference

### Synthetic API — GET /v2/quotas

**Endpoint:** `https://api.synthetic.new/v2/quotas`
**Auth:** `Authorization: Bearer <SYNTHETIC_API_KEY>`
**Rate Limit:** This endpoint does NOT count against quota (safe to poll frequently).
**Docs:** https://dev.synthetic.new/docs/synthetic/quotas

**Real response (captured 2026-02-06):**
```json
{
  "subscription": {
    "limit": 1350,
    "requests": 154.3,
    "renewsAt": "2026-02-06T16:16:18.386Z"
  },
  "search": {
    "hourly": {
      "limit": 250,
      "requests": 0,
      "renewsAt": "2026-02-06T13:58:14.386Z"
    }
  },
  "toolCallDiscounts": {
    "limit": 16200,
    "requests": 7635,
    "renewsAt": "2026-02-06T15:26:41.390Z"
  }
}
```

**Critical observations for implementation:**
1. `requests` is `float64` (e.g., 154.3) — use `float64` in Go, not `int`
2. Three independent quota types, each with its own `renewsAt`
3. `subscription` resets approximately every ~5 hours
4. `search.hourly` resets every ~1 hour
5. `toolCallDiscounts` has its OWN independent reset cycle (different from subscription!)
6. All `renewsAt` timestamps are ISO 8601 UTC
7. The API may add new fields in the future — struct should handle unknown fields gracefully

## Commands

```bash
# Development
go test ./...                      # Run all tests
go test -race ./...                # Race detection (ALWAYS run before commit)
go test -cover ./...               # Tests with coverage report
go test ./internal/store/ -v       # Run specific package tests

# Make targets
make build                         # Production binary (version from VERSION file)
make test                          # go test -race -cover ./...
make run                           # Build + run in debug mode
make dev                           # go run . --debug --interval 10
make clean                         # Remove binary + test artifacts + dist/ + db files
make lint                          # go fmt + go vet
make coverage                      # Generate HTML coverage report
make release-local                 # Cross-compile for all 5 platforms → dist/

# Running
./syntrack                         # Background: daemonize, log to .syntrack.log
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
| `syntrack stop` | Stop running instance (PID file + port fallback) |
| `syntrack status` | Show status of running instance |

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--interval` | Polling interval in seconds | `60` |
| `--port` | Dashboard HTTP port | `8932` |
| `--db` | SQLite database file path | `./syntrack.db` |
| `--debug` | Run in foreground, log to stdout | `false` |
| `--version` | Print version and exit | - |
| `--help` | Print help and exit | - |

**Background mode (default):** Daemonizes, logs to `.syntrack.log`, writes PID file. Use `syntrack stop` to stop.

**Debug/foreground mode (`--debug`):** Stays in foreground, logs to stdout.

## Session Tracking

Every time the SynTrack agent starts, it creates a **session**:

- Each session gets a unique **session ID** (UUID v4)
- The session is recorded in the `sessions` database table
- During a session, the agent tracks the **maximum quota count** observed
- This max count = the total consumption during that session
- Sessions allow comparing usage across different work periods

**Why session tracking matters:**
- Synthetic doesn't tell you "how many tokens did I use in the last 30 days"
- But if SynTrack is running, every session records its peak consumption
- By summing `max_requests` across sessions since the last quota reset, you get the total
- Since quotas reset every ~5 hours, sessions that span resets are tracked accurately

**Session lifecycle:**
1. Agent starts → new session created (UUID, start time, initial request counts)
2. Every poll → update session's `max_sub_requests`, `max_search_requests`, `max_tool_requests`
3. Agent stops (SIGINT/SIGTERM) → session closed (end time recorded)
4. Dashboard shows session history and per-session consumption

## Development Rules

### TDD Protocol (MANDATORY)

Every feature follows strict Red → Green → Refactor:

1. **Write the failing test FIRST** — never write implementation without a test
2. **Run the test, see it fail** — confirm test is testing the right thing
3. **Write minimal implementation** — just enough to make the test pass
4. **Run the test, see it pass** — confirm green
5. **Refactor** — clean up, but only if tests stay green
6. **Test file lives next to source** — `foo.go` → `foo_test.go`
7. **Table-driven tests** — use Go's idiomatic `[]struct{ name, input, want }` pattern
8. **No mocks unless necessary** — prefer real SQLite (`:memory:`) and `httptest.NewServer`
9. **Test names describe behavior** — `TestTracker_DetectsQuotaReset_WhenRenewsAtChanges`
10. **Run with `-race` before every commit** — race conditions are bugs

### Security Rules (NON-NEGOTIABLE)

- **NEVER commit `.env`** — only `.env.example` with placeholder values
- **NEVER log API keys** — redact in all log output (`syn_***...***`)
- **NEVER embed secrets in code** — always load from env/flags
- **NEVER commit database files** (`.db`, `.db-journal`, `.db-wal`, `.db-shm`)
- **NEVER commit screenshots** (`.png`, `.jpg`, `.gif`, etc.)
- **NEVER commit binaries** (`syntrack`, `*.exe`, `/bin/`, `/dist/`)
- **ALWAYS use parameterized SQL** — never string interpolation in queries
- **ALWAYS use `subtle.ConstantTimeCompare`** for password comparison
- **ALWAYS set HTTP timeouts** — 10s for API client, 5s for shutdown
- **ALWAYS validate user input** — sanitize query params before SQL

### Code Style

- Follow `gofmt` / `govet` / `golint` conventions
- Use `internal/` to prevent external imports of private packages
- Keep packages small and focused (single responsibility)
- Error wrapping: `fmt.Errorf("store.Save: %w", err)`
- Structured logging: `slog.Info("poll complete", "requests", resp.Subscription.Requests)`
- Constants over magic numbers
- Named return values only when they aid readability

### Git Hygiene

- Conventional commits: `feat:`, `fix:`, `test:`, `docs:`, `refactor:`, `perf:`
- Atomic commits — one logical change per commit
- Always run `go test -race ./...` before committing
- Always `git diff --staged` before every commit
- Never commit generated files, temp files, or IDE config

## Database Schema

```sql
-- Pragma settings (set on connection open)
PRAGMA journal_mode=WAL;           -- Write-Ahead Logging for concurrent reads
PRAGMA synchronous=NORMAL;         -- Balance safety and speed
PRAGMA cache_size=-2000;           -- 2MB cache (controls RAM usage)
PRAGMA foreign_keys=ON;

-- Schema version tracking
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

-- Raw API snapshots (append-only log — NEVER update or delete)
CREATE TABLE IF NOT EXISTS quota_snapshots (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    captured_at      TEXT NOT NULL,      -- ISO 8601 UTC timestamp
    -- Subscription quota
    sub_limit        REAL NOT NULL,
    sub_requests     REAL NOT NULL,      -- float64! (e.g., 154.3)
    sub_renews_at    TEXT NOT NULL,       -- ISO 8601 UTC
    -- Search hourly quota
    search_limit     REAL NOT NULL,
    search_requests  REAL NOT NULL,
    search_renews_at TEXT NOT NULL,
    -- Tool call discounts quota (HAS ITS OWN RESET CYCLE!)
    tool_limit       REAL NOT NULL,
    tool_requests    REAL NOT NULL,
    tool_renews_at   TEXT NOT NULL        -- Independent from subscription!
);

-- Detected reset cycles (one row per cycle per quota type)
CREATE TABLE IF NOT EXISTS reset_cycles (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    quota_type      TEXT NOT NULL,       -- 'subscription' | 'search' | 'toolcall'
    cycle_start     TEXT NOT NULL,       -- ISO 8601 UTC
    cycle_end       TEXT,                -- NULL = current active cycle
    renews_at       TEXT NOT NULL,       -- The renewsAt that started this cycle
    peak_requests   REAL NOT NULL DEFAULT 0,
    total_delta     REAL NOT NULL DEFAULT 0  -- Accumulated usage in this cycle
);

-- Agent sessions (one row per agent start/stop)
CREATE TABLE IF NOT EXISTS sessions (
    id                  TEXT PRIMARY KEY,     -- UUID v4
    started_at          TEXT NOT NULL,        -- ISO 8601 UTC
    ended_at            TEXT,                 -- NULL = currently running
    poll_interval       INTEGER NOT NULL,     -- seconds
    -- Max observed request counts during this session
    max_sub_requests    REAL NOT NULL DEFAULT 0,
    max_search_requests REAL NOT NULL DEFAULT 0,
    max_tool_requests   REAL NOT NULL DEFAULT 0,
    -- Snapshot count during session
    snapshot_count      INTEGER NOT NULL DEFAULT 0
);

-- Indexes for efficient queries
CREATE INDEX IF NOT EXISTS idx_snapshots_captured ON quota_snapshots(captured_at);
CREATE INDEX IF NOT EXISTS idx_snapshots_sub_renews ON quota_snapshots(sub_renews_at);
CREATE INDEX IF NOT EXISTS idx_snapshots_tool_renews ON quota_snapshots(tool_renews_at);
CREATE INDEX IF NOT EXISTS idx_cycles_type_start ON reset_cycles(quota_type, cycle_start);
CREATE INDEX IF NOT EXISTS idx_cycles_type_active ON reset_cycles(quota_type, cycle_end)
    WHERE cycle_end IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at);
```

## Dashboard Design

See `design-system/syntrack/MASTER.md` for complete design tokens and component specs.
See `design-system/syntrack/pages/dashboard.md` for dashboard-specific layout and components.

**Key design decisions:**
- **Material Design 3** — clean, professional, Google-style aesthetic
- **Dark + Light mode** — toggle in header, respects `prefers-color-scheme`, persists in `localStorage`
- **Three quota cards** — one per quota type, each with progress bar, countdown, status
- **All three quotas show reset time** — including Tool Call Discounts (independent reset!)
- **Color-coded thresholds** — green (0-49%), yellow (50-79%), red (80-94%), critical (95%+)
- **Accessibility** — color + icon + text for all status indicators (never color alone)
- **Live countdown** — updates every second for all 3 quota types
- **Usage insights** — plain English description of what current usage means
- **Time-series chart** — Chart.js area chart, all 3 quotas as % of limit
- **Reset cycle history** — table showing historical cycles with stats

## Quota Reset Tracking Logic

This is the core intelligence of SynTrack — tracking usage across reset boundaries:

1. **Detection:** Compare consecutive snapshots' `renewsAt` timestamps
   - If `curr.renewsAt != prev.renewsAt` → reset occurred
   - Each quota type is tracked independently
2. **Cycle management:**
   - On reset: close current cycle (set `cycle_end`), open new cycle
   - Record `peak_requests` (highest seen in the cycle)
   - Accumulate `total_delta` (sum of request increments within cycle)
3. **Usage calculation:**
   - `delta = curr.requests - prev.requests` (if positive and same cycle)
   - If `delta < 0` → reset happened between polls (requests went down)
   - Handle missed polls gracefully (agent was down for a while)
4. **Insights derivation:**
   - Average usage per cycle = sum of `total_delta` / number of completed cycles
   - Current rate = `current.requests / time_since_cycle_start`
   - Projected usage = `current_rate * time_until_reset`
   - "30-day usage" = sum of all `total_delta` for cycles in last 30 days
