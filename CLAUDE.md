# onWatch

Ultra-lightweight Go CLI tracking Anthropic/Synthetic/Z.ai/GitHub Copilot (Beta)/Antigravity quota usage. Polls endpoints → SQLite → Material Design 3 dashboard. Single binary via `embed.FS`, <50 MB RAM, TDD-first.

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

**Antigravity** `POST https://127.0.0.1:{port}/exa.language_server_pb.LanguageServerService/GetUserStatus` (local Connect RPC)
```json
{"userStatus":{
  "email":"user@example.com",
  "planStatus":{"availablePromptCredits":500,"planInfo":{"planName":"Pro","monthlyPromptCredits":1000}},
  "cascadeModelConfigData":{"clientModelConfigs":[
    {"label":"Claude Sonnet","modelOrAlias":{"model":"claude-4-5-sonnet"},
     "quotaInfo":{"remainingFraction":0.75,"resetTime":"ISO8601"}}]}}}
```
Note: Local language server API. Auto-detected via process scanning (extracts `--csrf_token` and `--extension_server_port` from command line). Headers: `Content-Type: application/json`, `Connect-Protocol-Version: 1`, `X-Codeium-Csrf-Token: {token}`. For Docker, set `ANTIGRAVITY_BASE_URL` and `ANTIGRAVITY_CSRF_TOKEN` env vars.

## Commands
```bash
./app.sh --build        # Production binary
./app.sh --test         # go test -race -cover ./...
./app.sh --release      # Cross-compile 5 platforms → dist/
go test -race ./...     # ALWAYS before commit
```

**IMPORTANT:** This is a compiled Go project. ALWAYS run `./app.sh --build` before starting/testing the application. Never run `./onwatch` without building first - changes won't be reflected otherwise.

## Release Process (MANDATORY)

When creating a new release, ALWAYS include binaries and Docker files:

```bash
# 1. Update VERSION file
echo "X.Y.Z" > VERSION

# 2. Cross-compile for all platforms
./app.sh --release

# 3. Create GitHub release with ALL artifacts
gh release create vX.Y.Z \
  dist/onwatch-darwin-arm64 \
  dist/onwatch-darwin-amd64 \
  dist/onwatch-linux-amd64 \
  dist/onwatch-linux-arm64 \
  dist/onwatch-windows-amd64.exe \
  install.bat \
  Dockerfile \
  docker-compose.yml \
  .env.docker.example \
  --title "vX.Y.Z - Release Title" \
  --notes "Release notes here"
```

**Required release artifacts:**
- `onwatch-darwin-arm64` (macOS Apple Silicon)
- `onwatch-darwin-amd64` (macOS Intel)
- `onwatch-linux-amd64` (Linux x64)
- `onwatch-linux-arm64` (Linux ARM64)
- `onwatch-windows-amd64.exe` (Windows x64)
- `install.bat` (Windows installer launcher)
- `Dockerfile`
- `docker-compose.yml`
- `.env.docker.example`

**NEVER create a release without binaries.** Users depend on pre-built binaries for installation.

**Windows Installation:**
- Users can run `install.bat` (downloads and runs PowerShell installer)
- Or run directly in PowerShell: `irm https://raw.githubusercontent.com/onllm-dev/onwatch/main/install.ps1 | iex`
- The binary now shows setup instructions if double-clicked without configuration

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

## Antigravity Mappings (must match Go + JS)
| Key | Display | Chart Color |
|-----|---------|-------------|
| `claude-4-5-sonnet` | Claude 4.5 Sonnet | #D97757 |
| `claude-4-5-sonnet-thinking` | Claude 4.5 Sonnet (Thinking) | #A855F7 |
| `gemini-3-pro` | Gemini 3 Pro | #10B981 |
| `gemini-3-flash` | Gemini 3 Flash | #3B82F6 |

Note: Model IDs are dynamic. Auto-detected from local language server process. For Docker environments, configure via environment variables (see Docker Configuration section below).

## Docker Configuration (Antigravity)
For containerized deployments where the Antigravity language server runs on the host:

| Environment Variable | Description | Example |
|---------------------|-------------|---------|
| `ANTIGRAVITY_BASE_URL` | Base URL of the language server | `https://host.docker.internal:42100` |
| `ANTIGRAVITY_CSRF_TOKEN` | CSRF token from host process | `abc123...` |

To get the CSRF token from the host:
```bash
ps aux | grep antigravity | grep -o '\-\-csrf_token[= ][^ ]*' | sed 's/--csrf_token[= ]//'
```

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
| Antigravity API | `internal/api/antigravity_client.go`, `antigravity_types.go` |
| Antigravity store | `internal/store/antigravity_store.go` |
| Antigravity tracker | `internal/tracker/antigravity_tracker.go` |
| Antigravity agent | `internal/agent/antigravity_agent.go` |
| Container detection | `internal/config/config.go:IsDockerEnvironment()` |
| Design system | `design-system/onwatch/MASTER.md` |
