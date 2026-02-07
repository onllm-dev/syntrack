# SynTrack

Track your [Synthetic](https://synthetic.new) and [Z.ai](https://z.ai) API quota usage over time. SynTrack polls quota endpoints, stores snapshots in SQLite, and serves a dashboard that shows what your provider doesn't: historical trends, reset cycle detection, consumption rates, and projections.

![Dashboard Dark Mode](./screenshots/dashboard-dark.png)

> Powered by [onllm.dev](https://onllm.dev)

---

## Quick Start

**One-line install** (macOS and Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/onllm-dev/syntrack/main/install.sh | bash
```

This downloads the binary to `~/.syntrack/`, creates a `.env` config, sets up a systemd service (Linux) or self-daemonizes (macOS), and adds `syntrack` to your PATH.

**Or download manually** from the [Releases](https://github.com/onllm-dev/syntrack/releases) page. Binaries are available for macOS (ARM64, AMD64), Linux (AMD64, ARM64), and Windows (AMD64).

**Or build from source** (requires Go 1.25+):

```bash
git clone https://github.com/onllm-dev/syntrack.git && cd syntrack
cp .env.example .env    # then add your API keys
make build && ./syntrack --debug
```

### Configure

Edit `~/.syntrack/.env` (or `.env` in the project directory if built from source):

```bash
SYNTHETIC_API_KEY=syn_your_key_here       # https://synthetic.new/settings/api
ZAI_API_KEY=your_zai_key_here             # https://www.z.ai/api-keys
SYNTRACK_ADMIN_USER=admin
SYNTRACK_ADMIN_PASS=changeme
```

At least one provider key is required. Configure both to track them in parallel.

### Run

```bash
syntrack              # start in background (daemonizes, logs to ~/.syntrack/.syntrack.log)
syntrack --debug      # foreground mode, logs to stdout
syntrack stop         # stop the running instance
syntrack status       # check if running
```

Open **http://localhost:9211** and log in with your `.env` credentials.

---

## Features

```
┌──────────────────────────────────────────────────────────────────┐
│ What your provider shows          │ What SynTrack adds           │
├───────────────────────────────────┼──────────────────────────────┤
│ Current quota usage               │ Historical usage trends      │
│                                   │ Reset cycle detection        │
│                                   │ Per-cycle consumption stats  │
│                                   │ Usage rate & projections     │
│                                   │ Per-session tracking         │
│                                   │ Multi-provider unified view  │
│                                   │ Live countdown timers        │
└───────────────────────────────────┴──────────────────────────────┘
```

**Dashboard** -- Material Design 3 with dark/light mode (auto-detects system preference). Three provider tabs when both are configured:

- **Synthetic** -- Subscription, Search, and Tool Call quota cards
- **Z.ai** -- Tokens, Time, and Tool Call quota cards
- **Both** -- Side-by-side view of all quotas

Each quota card shows: usage vs. limit with progress bar, live countdown to reset, status badge (healthy/warning/danger/critical), and consumption rate with projected usage.

**Insights** -- Cycle utilization, billing-period usage, weekly pace, tokens-per-call efficiency, and per-tool breakdowns (provider-specific).

**Sessions** -- Every agent run creates a session that tracks peak consumption, letting you compare usage across work periods.

**Password management** -- Change your password from the dashboard. The hash is stored in SQLite and persists across restarts (takes precedence over `.env`). To force-reset, delete the row from the `users` table.

---

## Architecture

```
             ┌──────────────┐
             │  Dashboard   │
             │  :9211       │
             └──────┬───────┘
             ┌──────┴───────┐
             │   SQLite     │
             │   (WAL)      │
             └───┬──────┬───┘
         ┌───────┘      └───────┐
    ┌────┴─────┐       ┌────┴─────┐
    │ Synthetic│       │  Z.ai    │
    │  Agent   │       │  Agent   │
    └────┬─────┘       └────┬─────┘
    ┌────┴─────┐       ┌────┴─────┐
    │ Synthetic│       │  Z.ai    │
    │  API     │       │  API     │
    └──────────┘       └──────────┘
```

Both agents run as parallel goroutines. Each polls its API at the configured interval and writes snapshots. The dashboard reads from the shared store.

**RAM:** ~30 MB idle, ~50 MB during dashboard render. Single binary, all assets embedded via `embed.FS`.

---

## CLI Reference

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--interval` | `SYNTRACK_POLL_INTERVAL` | `60` | Poll interval in seconds (10--3600) |
| `--port` | `SYNTRACK_PORT` | `9211` | Dashboard HTTP port |
| `--db` | `SYNTRACK_DB_PATH` | `~/.syntrack/data/syntrack.db` | SQLite database path |
| `--debug` | -- | `false` | Foreground mode, log to stdout |
| `--test` | -- | `false` | Isolated PID/log files for testing |
| `--version` | -- | -- | Print version and exit |

Additional environment variables:

| Variable | Description |
|----------|-------------|
| `SYNTHETIC_API_KEY` | Synthetic API key |
| `ZAI_API_KEY` | Z.ai API key |
| `ZAI_BASE_URL` | Z.ai base URL (default: `https://api.z.ai/api`) |
| `SYNTRACK_ADMIN_USER` | Dashboard username (default: `admin`) |
| `SYNTRACK_ADMIN_PASS` | Initial dashboard password (default: `changeme`) |
| `SYNTRACK_LOG_LEVEL` | Log level: debug, info, warn, error |
| `SYNTRACK_HOST` | Bind address (default: `0.0.0.0`) |

CLI flags override environment variables.

---

## API Endpoints

All endpoints require authentication (session cookie or Basic Auth). Append `?provider=synthetic|zai|both` to select the provider.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Dashboard |
| `/login` | GET/POST | Login page |
| `/logout` | GET | Clear session |
| `/api/current` | GET | Latest snapshot with summaries |
| `/api/history?range=6h` | GET | Historical data for charts |
| `/api/cycles?type=subscription` | GET | Reset cycle history |
| `/api/summary` | GET | Usage summaries |
| `/api/sessions` | GET | Session history |
| `/api/insights` | GET | Usage insights |
| `/api/providers` | GET | Available providers |
| `/api/settings` | GET/PUT | User settings |
| `/api/password` | PUT | Change password |

---

## Data Storage

```
~/.syntrack/
├── syntrack.pid          # PID file
├── .syntrack.log         # Log file (background mode)
└── data/
    └── syntrack.db       # SQLite database (WAL mode)
```

On first run, if a database exists at `./syntrack.db`, SynTrack auto-migrates it to `~/.syntrack/data/`.

---

## Security

- API keys loaded from `.env`, never committed, redacted in all log output
- Session-based auth with cookie + Basic Auth fallback
- Passwords stored as SHA-256 hashes with constant-time comparison
- Parameterized SQL queries throughout

---

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for build instructions, cross-compilation, and testing.

```bash
make build          # Production binary
make test           # Tests with race detection
make run            # Build and run in debug mode
make release-local  # Cross-compile for all platforms
```

---

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. Write tests first, then implement
4. Run `make test` and commit with conventional format
5. Open a Pull Request

---

## License

GNU General Public License v3.0. See [LICENSE](LICENSE).

---

## Acknowledgments

- Powered by [onllm.dev](https://onllm.dev)
- [Synthetic](https://synthetic.new) for the API
- [Z.ai](https://z.ai) for the API
- [Chart.js](https://www.chartjs.org/) for charts
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) for pure Go SQLite
