# SynTrack — Implementation Plan

> **Source of truth** for the phased TDD build. Each phase has a Definition of Done.
> No phase proceeds until the previous phase's tests are green with `-race`.
>
> **Status:** Phases 1-7 complete. Multi-provider support (Synthetic + Z.ai) shipped.

---

## Real API Responses

### Synthetic API (captured 2026-02-06)

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

### Z.ai API (captured 2026-02-06)

```json
{
  "code": 200,
  "data": {
    "limits": [
      {
        "type": "TIME_LIMIT",
        "usage": 1000,
        "currentValue": 19,
        "remaining": 981,
        "percentage": 1,
        "usageDetails": [
          { "modelCode": "search-prime", "usage": 16 }
        ]
      },
      {
        "type": "TOKENS_LIMIT",
        "usage": 200000000,
        "currentValue": 200112618,
        "remaining": 0,
        "percentage": 100,
        "nextResetTime": 1770398385482
      }
    ]
  }
}
```

**Key facts:**
- Synthetic: `requests` is `float64`. Three independent quotas with independent `renewsAt` timestamps.
- Z.ai: `usage` is the budget, `currentValue` is actual consumption. `nextResetTime` in epoch ms. Auth errors return HTTP 200 with error in body.

---

## Phase 1: Foundation (Config + Types + Store) — COMPLETE

- Go module with `modernc.org/sqlite` and `godotenv`
- `.env` loading with CLI flag overrides
- Config validation: API key, interval, port ranges
- Background/foreground mode via `--debug`
- API response types matching real shapes
- SQLite store with WAL mode: snapshots, reset cycles, sessions
- Schema versioning and migrations

---

## Phase 2: API Client + Agent + Tracker — COMPLETE

- HTTP client for Synthetic with Bearer auth, 10s timeout, key redaction
- Tracker: independent reset detection for all three quota types
- Agent: polling loop with `time.Ticker`, session creation/closure
- Error resilience: API errors logged, agent continues

---

## Phase 3: Web Server + Auth + JSON API — COMPLETE

- HTTP server on configured port
- Session-based auth with cookie + Basic Auth fallback
- Login page at `/login`
- JSON API: `/api/current`, `/api/history`, `/api/cycles`, `/api/summary`, `/api/sessions`, `/api/insights`
- Time range filtering: 1h, 6h, 24h, 7d, 30d
- Graceful shutdown within 5 seconds

---

## Phase 4: Dashboard Frontend — COMPLETE

- Material Design 3 with dark and light mode
- Three quota cards with progress bars, countdowns, status badges
- Chart.js area chart with time range selector
- Usage insights as interactive cards
- Reset cycle history with time range and grouping pills
- Session history with accordion expansion
- Auto-refresh matching poll interval

---

## Phase 5: CLI Entry Point + Integration — COMPLETE

- `main.go` wiring: config, store, client, tracker, agent, server
- Signal handling: SIGINT/SIGTERM for graceful shutdown
- Background daemonization with PID file
- `stop` and `status` subcommands
- Startup banner with provider info
- `--version` and `--help` flags

---

## Phase 6: Documentation + Release — COMPLETE

- README, DEVELOPMENT.md, CLAUDE.md
- MIT License
- `VERSION` file as single source of truth
- `make release-local` for cross-compilation
- GitHub Actions release workflow (`.github/workflows/release.yml`)
- 5-platform matrix: darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, windows/amd64

---

## Phase 7: Z.ai Provider Integration — COMPLETE

Added Z.ai as a second provider running in parallel with Synthetic.

### What was built

| Component | File | Description |
|-----------|------|-------------|
| Types | `internal/api/zai_types.go` | Z.ai response structs, snapshot conversion |
| Client | `internal/api/zai_client.go` | HTTP client for `/monitor/usage/quota/limit` |
| Agent | `internal/agent/zai_agent.go` | Background polling loop for Z.ai |
| Store | `internal/store/zai_store.go` | SQLite CRUD for Z.ai snapshots and cycles |
| Config | `internal/config/config.go` | Multi-provider: `ZAI_API_KEY`, `ZAI_BASE_URL`, `AvailableProviders()` |
| Handlers | `internal/web/handlers.go` | Provider routing via `?provider=` parameter |
| Dashboard | `internal/web/templates/dashboard.html` | Provider-conditional quota cards |
| Frontend | `internal/web/static/app.js` | Provider switching, dynamic chart datasets |

### Architecture

Both agents run in parallel goroutines from `main.go`. Each polls its API at the configured interval. The dashboard switches between providers via the `?provider=` query parameter. At least one provider must be configured.

### Z.ai field mapping

The Z.ai API names fields confusingly:
- `usage` = quota budget (not actual usage)
- `currentValue` = actual consumption

The handlers map these correctly for the dashboard.

---

## Build Order

```
Phase 1 → Config + Types + Store
    ↓
Phase 2 → API Client + Tracker + Agent
    ↓
Phase 3 → Web Server + Auth + Handlers
    ↓
Phase 4 → Dashboard HTML/CSS/JS
    ↓
Phase 5 → main.go + CLI + Integration
    ↓
Phase 6 → Documentation + Release Pipeline
    ↓
Phase 7 → Z.ai Provider Integration
```

Each phase gate: `go test -race ./...` must pass with 0 failures.
