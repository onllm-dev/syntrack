# onWatch

Ultra-lightweight Go CLI tracking Anthropic/Synthetic/Z.ai/GitHub Copilot (Beta) quota usage. Polls endpoints → SQLite → Material Design 3 dashboard. Single binary via `embed.FS`, <50 MB RAM, TDD-first.

## Stack
Go 1.25+ | SQLite (`modernc.org/sqlite`) | `net/http` | `html/template` + `embed.FS` | Chart.js (CDN) | `log/slog`

## RAM Efficiency (Background Daemon Constraint)
- **Target:** ~35 MB idle, <50 MB under load
- **GOMEMLIMIT:** `debug.SetMemoryLimit(40MiB)` + `GOGC=50` for aggressive GC
- **SQLite:** Single connection (`SetMaxOpenConns(1)`), 512KB cache (`cache_size=-500`), WAL mode
- **HTTP:** Lean transports (`MaxIdleConns:1`), 10s client timeout, 5s shutdown
- **Bounded queries:** Cycles capped at 200, insights at 50
- **No leaks:** Always use `context.Context`, no unbounded buffers

## Structure
```
main.go                    # Entry, CLI, lifecycle
internal/{config,api,store,tracker,agent,notify,update,web}/
internal/web/static/       # Embedded assets (sole source of truth)
internal/web/templates/    # dashboard.html, settings.html, login.html
```

## API Responses

**Synthetic** `GET https://api.synthetic.new/v2/quotas` (Bearer token)
```json
{"subscription":{"limit":1350,"requests":154.3,"renewsAt":"ISO8601"},
 "search":{"hourly":{"limit":250,"requests":0,"renewsAt":"..."}},
 "toolCallDiscounts":{"limit":16200,"requests":7635,"renewsAt":"..."}}
```

**Z.ai** `GET {ZAI_BASE_URL}/monitor/usage/quota/limit` (no Bearer prefix)
```json
{"code":200,"data":{"limits":[
  {"type":"TIME_LIMIT","usage":1000,"currentValue":19},
  {"type":"TOKENS_LIMIT","usage":200000000,"currentValue":200112618,"nextResetTime":1770398385482}
]}}
```
Note: `usage`=limit, `currentValue`=consumed. `nextResetTime` is epoch ms (only on TOKENS_LIMIT).

**Anthropic** `GET https://api.anthropic.com/api/oauth/usage` (Bearer + `anthropic-beta: oauth-2025-04-20`)
```json
{"five_hour":{"utilization":45.2,"resets_at":"ISO8601","is_enabled":true},
 "seven_day":{"utilization":12.8,"resets_at":"...","is_enabled":true},
 "monthly_limit":{"utilization":67.3,"resets_at":"...","is_enabled":true}}
```
Note: Dynamic keys, `utilization` is %, null entries skipped.

**GitHub Copilot (Beta)** `GET https://api.github.com/copilot_internal/user` (Bearer PAT with `copilot` scope)
```json
{"copilot_plan":"individual_pro","quota_reset_date_utc":"2026-03-01T00:00:00.000Z",
 "quota_snapshots":{
   "premium_interactions":{"entitlement":1500,"remaining":473,"percent_remaining":31.5,"unlimited":false},
   "chat":{"entitlement":0,"remaining":0,"percent_remaining":100,"unlimited":true},
   "completions":{"entitlement":0,"remaining":0,"percent_remaining":100,"unlimited":true}}}
```
Note: Undocumented internal API. Monthly reset via `quota_reset_date_utc`. `premium_interactions` is the main quota (300 for Pro, 1500 for Pro+). `chat`/`completions` are typically unlimited.

## Commands
```bash
./app.sh --build        # Production binary
./app.sh --test         # go test -race -cover ./...
./app.sh --release      # Cross-compile 5 platforms → dist/
go test -race ./...     # ALWAYS before commit
```

## CLI
| Flag | Default | Description |
|------|---------|-------------|
| `--interval` | 60 | Poll seconds |
| `--port` | 9211 | Dashboard port |
| `--db` | ~/.onwatch/data/onwatch.db | SQLite path |
| `--debug` | false | Foreground mode |
| `stop/status/update` | - | Subcommands |

## Rules (MANDATORY)

**TDD:** Test first → fail → implement → pass → refactor. Use `:memory:` SQLite + `httptest`. Run `-race` before commit. Shared test vars need `sync/atomic` or `sync.Mutex`.

**Security:** Never commit `.env`/`.db`/binaries. Never log API keys. Parameterized SQL only. `subtle.ConstantTimeCompare` for credentials. HTTP timeouts: 10s client, 5s shutdown.

**Pre-Commit:**
```bash
go test -race -cover ./... && go vet ./...  # Must pass
```

**Style:** `gofmt`/`govet`, error wrap `fmt.Errorf("x: %w", err)`, conventional commits (`feat:`/`fix:`/`test:`). Use `-` (hyphen), not `—` (em-dash) in text.

**Container Detection:** `IsDockerEnvironment()` must detect ALL container runtimes - Docker (`/.dockerenv`), Kubernetes (`KUBERNETES_SERVICE_HOST`, `/var/run/secrets/kubernetes.io/serviceaccount`), and explicit (`DOCKER_CONTAINER` env). Containers always run in foreground mode (no daemon fork).

**Temp files:** All in `temp/` (gitignored). Release screenshots only in `docs/screenshots/`.

## Anthropic Mappings (must match Go + JS)
| Key | Display | Chart Color |
|-----|---------|-------------|
| `five_hour` | 5-Hour Limit | #D97757 |
| `seven_day` | Weekly All-Model | #10B981 |
| `seven_day_sonnet` | Weekly Sonnet | #3B82F6 |
| `monthly_limit` | Monthly Limit | #A855F7 |
| `extra_usage` | Extra Usage | #F59E0B |

Dashboard thresholds (defaults, customizable via `/settings`): green (0-49%), yellow (50-79%), red (80-94%), critical (95%+).

## Copilot Mappings (must match Go + JS)
| Key | Display | Chart Color |
|-----|---------|-------------|
| `premium_interactions` | Premium Requests | #6e40c9 |
| `chat` | Chat | #2ea043 |
| `completions` | Completions | #58a6ff |

## Code References
| Feature | Location |
|---------|----------|
| DB schema | `internal/store/store.go:createTables()` |
| Self-update | `internal/update/updater.go` |
| Token refresh | `internal/agent/anthropic_agent.go:SetTokenRefresh()` |
| Auth/sessions | `internal/web/middleware.go` |
| Notifications | `internal/notify/notify.go`, `smtp.go`, `crypto.go` |
| Settings API | `internal/web/handlers.go` (GET/PUT `/api/settings`) |
| Quota tracking | `internal/tracker/tracker.go` |
| Copilot API | `internal/api/copilot_client.go`, `copilot_types.go` |
| Copilot store | `internal/store/copilot_store.go` |
| Copilot tracker | `internal/tracker/copilot_tracker.go` |
| Copilot agent | `internal/agent/copilot_agent.go` |
| Container detection | `internal/config/config.go:IsDockerEnvironment()` |
| Design system | `design-system/onwatch/MASTER.md` |
