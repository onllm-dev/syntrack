# SynTrack

**Ultra-lightweight Synthetic API usage tracker.** A background agent that monitors your [Synthetic](https://synthetic.new) API quota consumption, stores historical data in SQLite, and serves a clean Material Design dashboard.

> Powered by [onllm.dev](https://onllm.dev)

---

## Why SynTrack?

Synthetic's `/v2/quotas` API tells you _current_ usage — but not historical trends, cycle-by-cycle consumption, or projected usage. SynTrack fills this gap:

- **Track usage over time** — see how consumption changes across reset cycles
- **Understand reset cycles** — subscription (~5h), search (hourly), and tool call discounts all reset independently
- **Get insights** — projected usage, average per cycle, peak consumption
- **Instant visibility** — color-coded progress bars show which quotas approach limits
- **Background agent** — runs silently with ~10 MB RAM, logs to `.syntrack.log`
- **Session tracking** — each agent run gets a session ID; track consumption per session

---

## Features

| Feature | Description |
|---------|-------------|
| Background polling | Polls `/v2/quotas` at configurable intervals (default: 60s) |
| Three quota types | Tracks subscription, search (hourly), and tool call discounts independently |
| Reset detection | Detects when quotas reset and tracks per-cycle usage |
| Session tracking | Each agent run = one session; tracks max consumption per session |
| Live countdown | Real-time countdown to next reset for all 3 quota types |
| Material Design 3 | Clean dashboard with dark + light mode toggle |
| Usage insights | Plain English descriptions of what your consumption means |
| Time-series chart | Chart.js area chart with 1h, 6h, 24h, 7d, 30d ranges |
| Cycle history | Table of historical reset cycles with peak and total usage |
| SQLite storage | Append-only data log, WAL mode, ~2 MB memory |
| Single binary | No runtime dependencies, all assets embedded |

---

## Quick Start

### 1. Install

```bash
# From source
git clone https://github.com/onllm-dev/syntrack.git
cd syntrack
go build -ldflags="-s -w" -o syntrack .

# Or with make
make build
```

### 2. Configure

```bash
cp .env.example .env
```

Edit `.env` with your Synthetic API key:

```env
SYNTHETIC_API_KEY=syn_your_api_key_here
SYNTRACK_ADMIN_USER=admin
SYNTRACK_ADMIN_PASS=your_secure_password
```

### 3. Run

```bash
# Background mode (default) — logs to .syntrack.log in working directory
./syntrack

# Foreground/debug mode — logs to stdout, stays attached to terminal
./syntrack --debug

# Custom interval (30 seconds)
./syntrack --interval 30

# Custom port
./syntrack --port 9000

# All options combined
./syntrack --interval 30 --port 9000 --db ./data/track.db --debug

# Check logs when running in background
tail -f .syntrack.log
```

### 4. Open Dashboard

Navigate to `http://localhost:8932` and log in with your admin credentials.

---

## CLI Options

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--interval` | `SYNTRACK_POLL_INTERVAL` | `60` | Polling interval in seconds (min: 10, max: 3600) |
| `--port` | `SYNTRACK_PORT` | `8932` | Dashboard HTTP port |
| `--db` | `SYNTRACK_DB_PATH` | `./syntrack.db` | SQLite database file path |
| `--debug` | — | `false` | Run in foreground, log to stdout (default: background, log to `.syntrack.log`) |
| `--version` | — | — | Print version and exit |
| `--help` | — | — | Print help and exit |

**Background vs Debug mode:**
- **Background (default):** Process detaches from terminal. All logs written to `.syntrack.log` in the working directory. Use `tail -f .syntrack.log` to monitor.
- **Debug (`--debug`):** Process stays attached to the terminal. Logs go to stdout. Useful for development and troubleshooting.

**Environment variables** (set in `.env` file):

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SYNTHETIC_API_KEY` | Yes | — | Your Synthetic API key |
| `SYNTRACK_ADMIN_USER` | No | `admin` | Dashboard login username |
| `SYNTRACK_ADMIN_PASS` | No | `changeme` | Dashboard login password |
| `SYNTRACK_LOG_LEVEL` | No | `info` | Log level: debug, info, warn, error |

CLI flags override environment variables.

---

## Dashboard

The dashboard provides a real-time view of your Synthetic API usage with Material Design 3 styling.

### Quota Cards

Three cards showing each quota type:

- **Subscription** — Main API request quota (~5h reset cycle)
- **Search (Hourly)** — Search endpoint quota (1h reset cycle)
- **Tool Call Discounts** — Tool call requests (independent reset cycle)

Each card displays:
- Current usage vs. limit with progress bar
- Color-coded status: green (<50%), yellow (50-79%), red (80-94%), critical (95%+)
- Live countdown to next reset (updates every second)
- Absolute reset time (e.g., "Feb 6, 4:16 PM")
- Status badge with icon (accessible — never color-only)
- Current consumption rate and projected usage

### Usage Insights

Plain English descriptions like:
> "You've used 47.1% of your tool call quota. At current rate (~1,834 req/hr), projected ~12,102 before reset (74.7% of limit)."

### Time-Series Chart

- Area chart showing all 3 quotas as percentage of their limits
- Time range selector: 1h, 6h, 24h, 7d, 30d
- Vertical markers at reset events
- Hover tooltips with absolute values

### Reset Cycle History

Table of completed cycles with:
- Start/end times, duration
- Peak usage, total requests, average rate
- Filterable by quota type

### Dark/Light Mode

- Toggle via sun/moon icon in header
- Detects system preference on first visit
- Persists choice across sessions

---

## Session Tracking

Each time you start the SynTrack agent, it creates a new **session** with a unique UUID:

```
Session: a3f7c2d1-...  Started: Feb 6, 10:30 AM  Duration: 2h 15m
  Subscription: max 342 requests (consumed during this session)
  Search:       max 45 requests
  Tool Calls:   max 8,102 requests
  Snapshots:    135 polls recorded
```

**How it works:**
- Every poll, SynTrack compares the current request count with the session's stored maximum
- If `current > max`, the max is updated
- The max value = total consumption during that session
- Even at 1-minute granularity, this captures accurate per-session usage

**Why this matters:**
- Synthetic doesn't provide "total tokens used in the last 30 days"
- With SynTrack sessions, you get per-session consumption data
- Sum max values across sessions to calculate usage over any time period
- Compare sessions to understand which work periods consume the most

**Session vs Reset Cycles:**
- **Sessions** = agent runtime periods (your work sessions)
- **Reset cycles** = Synthetic's quota refresh windows (~5h for subscription)
- These are independent — a session may span multiple reset cycles
- Both are tracked and viewable in the dashboard

### How Reset Cycle Tracking Works

Synthetic quotas reset independently:
- **Subscription:** ~every 5 hours
- **Search:** every hour
- **Tool Call Discounts:** independent cycle

SynTrack detects resets by watching the `renewsAt` timestamp. When it changes:
1. The current cycle is closed (peak and total recorded)
2. A new cycle begins

Since we poll every 60 seconds (default), we capture granular per-cycle data that Synthetic doesn't provide natively. Over time, this builds a comprehensive usage history:

- **Per-cycle averages** — how much you typically use per reset window
- **Peak cycles** — your highest consumption periods
- **30-day totals** — accumulated usage across all cycles
- **Session consumption** — per-session tracking with max values

---

## Architecture

```
┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐
│ Synthetic │────>│  Agent   │────>│  Store   │<────│  Web     │
│ API       │     │ (poller) │     │ (SQLite) │     │ (server) │
└──────────┘     └──────────┘     └──────────┘     └──────────┘
  /v2/quotas       60s tick       WAL mode         :8932
                     │                               │
                     v                               v
                 ┌──────────┐                  ┌──────────┐
                 │ Tracker  │                  │Dashboard │
                 │ (cycles) │                  │ (HTML/JS)│
                 └──────────┘                  └──────────┘
                 Reset detection            Material Design 3
                 Usage calculation            Dark/Light mode
```

**RAM budget:** ~10 MB idle, ~15 MB during dashboard render.

---

## API Endpoints

The dashboard communicates via these JSON endpoints (all require Basic Auth):

| Endpoint | Description |
|----------|-------------|
| `GET /api/current` | Latest snapshot with computed summaries, countdowns, insights |
| `GET /api/history?range=6h` | Historical data for charts (1h, 6h, 24h, 7d, 30d) |
| `GET /api/cycles?type=subscription` | Reset cycle history by quota type |
| `GET /api/summary` | Computed usage summaries for all quota types |
| `GET /api/sessions` | Session history with per-session max consumption |

---

## Development

### Prerequisites

- Go 1.22+
- No CGO required (pure Go SQLite driver)

### Commands

```bash
# Run all tests with race detection
make test

# Build production binary
make build

# Build and run
make run

# Clean artifacts
make clean
```

### Project Structure

```
syntrack/
├── main.go                    # Entry point, lifecycle management
├── internal/
│   ├── config/                # .env + CLI flag loading
│   ├── api/                   # Synthetic API client + types
│   ├── store/                 # SQLite storage layer
│   ├── tracker/               # Reset detection + usage calculation
│   ├── agent/                 # Background polling agent
│   └── web/                   # HTTP server, auth, handlers, templates
├── static/                    # CSS + JS (embedded in binary)
├── design-system/             # UI/UX design specifications
├── .env.example               # Configuration template
└── Makefile                   # Build targets
```

### Testing Philosophy

- **TDD-first** — every function has tests written before implementation
- **Table-driven tests** — Go idiomatic `[]struct{name, input, want}` pattern
- **Real SQLite** — tests use `:memory:` databases, no mocks
- **httptest** — all HTTP tests use Go's `net/http/httptest`
- **Race detection** — all tests run with `-race` flag

---

## Security

- API keys loaded from `.env` (never committed)
- API keys redacted in all log output
- HTTP Basic Auth with constant-time password comparison
- All SQL queries are parameterized (no injection risk)
- `.gitignore` excludes: `.env`, `*.db`, binaries, screenshots, logs

---

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. Write tests first (TDD)
4. Make your changes
5. Run tests: `make test`
6. Commit with conventional format: `feat: add feature X`
7. Push and create a Pull Request

### Commit Convention

```
feat: new feature
fix: bug fix
test: add/update tests
docs: documentation changes
refactor: code restructuring
perf: performance improvement
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.

---

## Acknowledgments

- Powered by [onllm.dev](https://onllm.dev)
- [Synthetic](https://synthetic.new) for the API
- [Chart.js](https://www.chartjs.org/) for lightweight charts
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) for pure Go SQLite
