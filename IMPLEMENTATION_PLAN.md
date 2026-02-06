# SynTrack — Implementation Plan

> **Source of truth** for the phased TDD build. Each phase has a Definition of Done.
> No phase proceeds until the previous phase's tests are 100% green with `-race`.

---

## Real API Response (captured 2026-02-06)

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

**Key facts for all phases:**
- `requests` is a `float64` (e.g., 154.3)
- Three independent quota types with **independent** `renewsAt` timestamps
- `toolCallDiscounts` has its **own** reset cycle (NOT same as subscription!)
- Subscription resets ~every 5 hours, search hourly, toolcall independently
- The `/v2/quotas` endpoint does NOT count against quota

**Agent execution modes:**
- **Background (default):** Process daemonizes, all logs go to `.syntrack.log` in working directory
- **Foreground (`--debug`):** Stays attached to terminal, logs to stdout
- **CLI flags (3 core + debug):** `--interval`, `--port`, `--db`, `--debug`

**Session tracking:**
- Each agent start creates a new session (UUID v4)
- Session tracks max observed request count for each quota type
- max(requests) in a session = total consumption during that session
- Sessions are independent of reset cycles — they track agent runtime periods
- Dashboard shows session history: start, end, duration, consumption per quota

---

## Phase 1: Foundation (Config + Types + Store)

### Definition of Done
- [ ] Go module initialized with exact dependencies listed
- [ ] `.env` loading works with `godotenv`
- [ ] CLI flags override env vars (flags take precedence)
- [ ] CLI accepts: `--interval`, `--port`, `--db`, `--debug`
- [ ] Config validation: rejects empty API key, interval < 10s, invalid port
- [ ] `--debug` flag controls log destination (stdout vs `.syntrack.log`)
- [ ] All API response types defined matching real API shape
- [ ] JSON unmarshal handles: float requests, ISO 8601 timestamps, nested search.hourly
- [ ] SQLite store creates tables with WAL mode, bounded cache
- [ ] Store can insert snapshots, query latest, query by time range
- [ ] Store can insert/update/query reset cycles
- [ ] Store can create/close/query sessions with max request tracking
- [ ] 100% test coverage on config, types, and store packages
- [ ] All tests pass with `go test -race ./...`
- [ ] No secrets in any committed file

### 1.1 Project Init

```bash
go mod init github.com/onllm-dev/syntrack
go get modernc.org/sqlite
go get github.com/joho/godotenv
```

**Files created:** `go.mod`, `go.sum`

### 1.2 Config Package — `internal/config/`

**Tests FIRST (`config_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestConfig_LoadsFromEnv` | All fields read from .env file |
| `TestConfig_DefaultValues` | Defaults applied when env vars missing (interval=60s, port=8932, loglevel=info) |
| `TestConfig_FlagOverridesEnv` | CLI flag `--interval 30` overrides `SYNTRACK_POLL_INTERVAL=60` |
| `TestConfig_ValidatesAPIKey_Required` | Returns error if `SYNTHETIC_API_KEY` is empty |
| `TestConfig_ValidatesAPIKey_Format` | Accepts keys starting with `syn_`, rejects garbage |
| `TestConfig_ValidatesInterval_Minimum` | Rejects interval < 10 seconds |
| `TestConfig_ValidatesInterval_Maximum` | Rejects interval > 3600 seconds (1 hour) |
| `TestConfig_ValidatesPort_Range` | Rejects port < 1024 or > 65535 |
| `TestConfig_AdminCredentials_Defaults` | Default admin/changeme when not set |
| `TestConfig_DBPath_Default` | Defaults to `./syntrack.db` |
| `TestConfig_RedactsAPIKey` | `String()` method shows `syn_***...***` |
| `TestConfig_DebugMode_Default` | Debug defaults to false (background mode) |
| `TestConfig_DebugMode_Flag` | `--debug` sets DebugMode to true |
| `TestConfig_LogDestination_Background` | When debug=false, log destination is `.syntrack.log` |
| `TestConfig_LogDestination_Debug` | When debug=true, log destination is stdout |

**Implementation (`config.go`):**

```go
type Config struct {
    APIKey       string        // SYNTHETIC_API_KEY
    PollInterval time.Duration // SYNTRACK_POLL_INTERVAL (seconds → Duration)
    Port         int           // SYNTRACK_PORT
    AdminUser    string        // SYNTRACK_ADMIN_USER
    AdminPass    string        // SYNTRACK_ADMIN_PASS
    DBPath       string        // SYNTRACK_DB_PATH
    LogLevel     string        // SYNTRACK_LOG_LEVEL
    DebugMode    bool          // --debug flag (foreground mode)
}

func Load() (*Config, error)           // Load from .env + parse flags
func (c *Config) Validate() error      // Fail fast on bad config
func (c *Config) String() string       // Redacted display for startup banner
func (c *Config) LogWriter() io.Writer // Returns os.Stdout (debug) or file (.syntrack.log)
```

### 1.3 API Types — `internal/api/types.go`

**Tests FIRST (`types_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestQuotaResponse_UnmarshalJSON_RealData` | Exact real API response parses correctly |
| `TestQuotaResponse_FloatRequests` | 154.3 parses as float64, not rounded |
| `TestQuotaResponse_ISO8601_Parsing` | `renewsAt` parses as `time.Time` in UTC |
| `TestQuotaResponse_AllThreeQuotaTypes` | subscription + search.hourly + toolCallDiscounts all present |
| `TestQuotaResponse_ToolCallDiscounts_IndependentRenewsAt` | Tool call renewsAt differs from subscription |
| `TestQuotaResponse_SearchNested` | `search.hourly` nested structure parses correctly |
| `TestQuotaResponse_ZeroRequests` | Handles `"requests": 0` (not null) |
| `TestQuotaResponse_UnknownFields_Ignored` | Extra fields don't cause unmarshal error |

**Implementation:**

```go
type QuotaResponse struct {
    Subscription      QuotaInfo  `json:"subscription"`
    Search            SearchInfo `json:"search"`
    ToolCallDiscounts QuotaInfo  `json:"toolCallDiscounts"`
}

type QuotaInfo struct {
    Limit    float64   `json:"limit"`
    Requests float64   `json:"requests"`
    RenewsAt time.Time `json:"renewsAt"`
}

type SearchInfo struct {
    Hourly QuotaInfo `json:"hourly"`
}

// Snapshot is the storage representation (flat, for SQLite)
type Snapshot struct {
    ID          int64
    CapturedAt  time.Time
    Sub         QuotaInfo
    Search      QuotaInfo
    ToolCall    QuotaInfo
}
```

### 1.4 Store Package — `internal/store/`

**Tests FIRST (`store_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestStore_CreateTables` | Schema created without error on `:memory:` |
| `TestStore_InsertSnapshot` | Insert returns ID, data retrievable |
| `TestStore_InsertSnapshot_Concurrent` | 10 goroutines inserting — no data corruption |
| `TestStore_QueryLatest` | Returns most recent snapshot |
| `TestStore_QueryLatest_EmptyDB` | Returns nil/error gracefully on empty DB |
| `TestStore_QueryRange` | Filters snapshots between start and end time |
| `TestStore_QueryRange_Empty` | Returns empty slice when no data in range |
| `TestStore_QueryRange_Pagination` | Handles limit/offset for large ranges |
| `TestStore_InsertResetCycle` | Insert new cycle row |
| `TestStore_CloseResetCycle` | Updates cycle_end, peak_requests, total_delta |
| `TestStore_QueryActiveCycle` | Returns cycle with NULL cycle_end |
| `TestStore_QueryCycleHistory` | Returns completed cycles for a quota type |
| `TestStore_CreateSession` | Insert new session with UUID, start time |
| `TestStore_CloseSession` | Updates ended_at on session close |
| `TestStore_UpdateSessionMaxRequests` | Updates max_sub/search/tool if current > stored |
| `TestStore_IncrementSnapshotCount` | snapshot_count increments on each poll |
| `TestStore_QuerySessionHistory` | Returns sessions ordered by started_at DESC |
| `TestStore_QueryActiveSession` | Returns session with NULL ended_at |
| `TestStore_SessionMaxTracking` | Max is updated correctly: 100→200→150 → max stays 200 |
| `TestStore_WALMode` | Confirms `PRAGMA journal_mode` returns `wal` |
| `TestStore_BoundedCache` | Confirms `PRAGMA cache_size` is set |
| `TestStore_ParameterizedQueries` | Confirms no SQL injection possible |
| `TestStore_Close` | Clean shutdown, no locked files |

**Implementation files:**

- `migrations.go` — Schema creation, version tracking, `PRAGMA` settings
- `queries.go` — All SQL as named constants (never inline SQL)
- `store.go` — `Store` struct with methods: `Insert`, `Latest`, `Range`, `InsertCycle`, `CloseCycle`, `ActiveCycle`, `CycleHistory`, `CreateSession`, `CloseSession`, `UpdateSessionMax`, `SessionHistory`, `ActiveSession`, `Close`

**SQLite configuration (RAM efficiency):**
```go
// On connection open:
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA cache_size=-2000;    // 2MB max cache
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;   // 5s retry on lock
```

---

## Phase 2: API Client + Agent + Tracker

### Definition of Done
- [ ] HTTP client fetches from Synthetic API with Bearer auth
- [ ] Client handles: 200 OK, 401 Unauthorized, 500 Server Error, timeout, network error, bad JSON
- [ ] Client sets User-Agent: `syntrack/1.0`
- [ ] Client has 10-second timeout
- [ ] Client NEVER logs the API key (redaction verified by test)
- [ ] Tracker detects reset for ALL THREE quota types independently
- [ ] Tracker handles: first snapshot (no previous), normal increment, reset (renewsAt change), missed polls
- [ ] Tracker calculates: peak usage, total delta, average per cycle
- [ ] Agent polls at configured interval using `time.Ticker`
- [ ] Agent stores every snapshot, even if tracker processing fails
- [ ] Agent handles API errors gracefully (logs, continues, doesn't crash)
- [ ] Agent shuts down cleanly on `context.Cancel`
- [ ] All tests pass with `go test -race ./...`

### 2.1 API Client — `internal/api/client.go`

**Tests FIRST (`client_test.go`) — all use `httptest.NewServer`:**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestClient_FetchQuotas_Success` | Parses real-shape response from mock server |
| `TestClient_FetchQuotas_Unauthorized` | Returns specific error on 401 |
| `TestClient_FetchQuotas_ServerError` | Returns specific error on 500 |
| `TestClient_FetchQuotas_Timeout` | Respects 10s timeout (test with 1s for speed) |
| `TestClient_FetchQuotas_NetworkError` | Handles connection refused |
| `TestClient_FetchQuotas_MalformedJSON` | Handles invalid JSON body |
| `TestClient_FetchQuotas_EmptyBody` | Handles empty 200 response |
| `TestClient_SetsAuthHeader` | Bearer token present in request |
| `TestClient_SetsUserAgent` | User-Agent: syntrack/1.0 |
| `TestClient_NeverLogsAPIKey` | Capture log output, assert no `syn_` prefix match |
| `TestClient_RespectsContext` | Cancelled context aborts request |

**Implementation:**
```go
type Client struct {
    httpClient *http.Client
    apiKey     string
    baseURL    string
    logger     *slog.Logger
}

func NewClient(apiKey string, logger *slog.Logger, opts ...Option) *Client
func (c *Client) FetchQuotas(ctx context.Context) (*QuotaResponse, error)
```

### 2.2 Tracker — `internal/tracker/tracker.go`

The core intelligence for reset cycle detection and usage calculation.

**Tests FIRST (`tracker_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestTracker_FirstSnapshot_CreatesThreeCycles` | First poll creates active cycle for sub, search, toolcall |
| `TestTracker_NormalIncrement_UpdatesDelta` | requests goes 100→150, delta += 50 |
| `TestTracker_DetectsSubscriptionReset` | sub.renewsAt changes → new sub cycle |
| `TestTracker_DetectsSearchReset` | search.renewsAt changes → new search cycle |
| `TestTracker_DetectsToolCallReset` | tool.renewsAt changes → new toolcall cycle |
| `TestTracker_IndependentResets` | Tool resets but sub doesn't → only toolcall cycle changes |
| `TestTracker_SimultaneousResets` | All three reset at once → 3 new cycles |
| `TestTracker_RequestsDropToZero` | requests 500→0 with same renewsAt → delta is 0 (mid-cycle anomaly) |
| `TestTracker_PeakTracking` | requests 100→200→150 → peak is 200 |
| `TestTracker_MissedPolls_GapDetection` | capturedAt gap > 2x interval → logs warning |
| `TestTracker_MissedPolls_ResetDuringGap` | renewsAt changed during gap → still detects reset |
| `TestTracker_UsageSummary_SingleCycle` | Summary for 1 completed cycle |
| `TestTracker_UsageSummary_MultipleCycles` | Avg, peak, total across 5 cycles |
| `TestTracker_UsageSummary_NoCycles` | Graceful empty response |
| `TestTracker_ProjectedUsage` | Current rate extrapolated to reset time |
| `TestTracker_RateCalculation` | requests / hours_since_cycle_start |

**Implementation:**
```go
type Tracker struct {
    store  *store.Store
    logger *slog.Logger
}

func New(store *store.Store, logger *slog.Logger) *Tracker

// Process compares current snapshot with previous, detects resets, updates cycles
func (t *Tracker) Process(ctx context.Context, snapshot *api.Snapshot) error

// UsageSummary returns computed stats for a quota type
func (t *Tracker) UsageSummary(ctx context.Context, quotaType string) (*Summary, error)

type Summary struct {
    QuotaType       string
    CurrentUsage    float64
    CurrentLimit    float64
    UsagePercent    float64
    RenewsAt        time.Time
    TimeUntilReset  time.Duration
    CurrentRate     float64       // requests per hour
    ProjectedUsage  float64       // estimated total before reset
    CompletedCycles int
    AvgPerCycle     float64
    PeakCycle       float64       // highest total_delta in any cycle
    TotalTracked    float64       // all-time total across all cycles
    TrackingSince   time.Time
}

type ResetEvent struct {
    QuotaType   string
    OldRenewsAt time.Time
    NewRenewsAt time.Time
    PeakUsage   float64
    CycleDelta  float64
}
```

### 2.3 Agent — `internal/agent/agent.go`

**Tests FIRST (`agent_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestAgent_PollsAtInterval` | Mock API called N times in N*interval duration |
| `TestAgent_StoresEverySnapshot` | DB has N rows after N polls |
| `TestAgent_ProcessesWithTracker` | Tracker.Process called for each snapshot |
| `TestAgent_APIError_Continues` | API returns error → agent logs, continues next tick |
| `TestAgent_StoreError_Continues` | Store returns error → agent logs, continues |
| `TestAgent_TrackerError_StillStoresSnapshot` | Tracker fails → snapshot still saved |
| `TestAgent_GracefulShutdown` | Context cancel → Run() returns nil within 1s |
| `TestAgent_GracefulShutdown_MidPoll` | Cancel during HTTP request → clean exit |
| `TestAgent_FirstPollImmediate` | First poll happens immediately, not after interval |
| `TestAgent_LogsEachPoll` | Structured log entry per poll with key metrics |
| `TestAgent_CreatesSessionOnStart` | New session in DB when Run() begins |
| `TestAgent_ClosesSessionOnStop` | Session ended_at set when Run() returns |
| `TestAgent_UpdatesSessionMax` | Session max_requests updated each poll |
| `TestAgent_SessionMaxIsCorrect` | If requests go 100→200→150, max stays 200 |
| `TestAgent_SessionSnapshotCount` | snapshot_count increments per successful poll |
| `TestAgent_BackgroundMode_LogsToFile` | In non-debug mode, logs go to .syntrack.log |
| `TestAgent_DebugMode_LogsToStdout` | In debug mode, logs go to stdout |

**Implementation:**
```go
type Agent struct {
    client    *api.Client
    store     *store.Store
    tracker   *Tracker
    interval  time.Duration
    logger    *slog.Logger
    sessionID string  // UUID v4, created on Run()
}

func New(client, store, tracker, interval, logger) *Agent

// Run starts the polling loop. Creates a session. Blocks until ctx is cancelled. Closes session on exit.
func (a *Agent) Run(ctx context.Context) error

// SessionID returns the current session's UUID (empty if not running)
func (a *Agent) SessionID() string
```

**Polling loop pattern with session management:**
```go
func (a *Agent) Run(ctx context.Context) error {
    // Create session
    a.sessionID = uuid.New().String()
    a.store.CreateSession(a.sessionID, time.Now(), a.interval)
    defer a.store.CloseSession(a.sessionID, time.Now())

    // Poll immediately on start
    a.poll(ctx)

    ticker := time.NewTicker(a.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            a.poll(ctx)
        }
    }
}

func (a *Agent) poll(ctx context.Context) {
    resp, err := a.client.FetchQuotas(ctx)
    if err != nil { /* log, continue */ return }

    snapshot := toSnapshot(resp)
    a.store.Insert(snapshot)

    // Update session max values
    a.store.UpdateSessionMax(a.sessionID, snapshot)

    a.tracker.Process(ctx, snapshot)
}
```

**Background/foreground mode (in main.go):**
```go
if !cfg.DebugMode {
    // Redirect stdout/stderr to .syntrack.log
    logFile, _ := os.OpenFile(".syntrack.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
    logger = slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: logLevel}))
    // Note: actual daemonization is OS-dependent; for simplicity we just detach stdio
} else {
    logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
}
```

---

## Phase 3: Web Server + Auth + JSON API

### Definition of Done
- [ ] HTTP server starts on configured port
- [ ] All routes protected by Basic Auth middleware
- [ ] Auth uses `subtle.ConstantTimeCompare` (timing-attack safe)
- [ ] Dashboard HTML page served at `GET /`
- [ ] JSON API endpoints return correct data
- [ ] Time range filtering works for all ranges (1h, 6h, 24h, 7d, 30d)
- [ ] Empty database returns graceful empty responses (not 500)
- [ ] Server shuts down gracefully within 5 seconds
- [ ] All handlers tested with `httptest`
- [ ] All tests pass with `go test -race ./...`

### 3.1 Auth Middleware — `internal/web/middleware.go`

**Tests FIRST (`middleware_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestAuth_ValidCredentials` | 200 with correct user:pass |
| `TestAuth_InvalidPassword` | 401 with wrong password |
| `TestAuth_InvalidUsername` | 401 with wrong username |
| `TestAuth_MissingHeader` | 401 with no Authorization header |
| `TestAuth_MalformedHeader` | 401 with garbage auth header |
| `TestAuth_TimingSafe` | Uses `subtle.ConstantTimeCompare` (inspect implementation) |
| `TestAuth_SetsWWWAuthenticate` | 401 response includes `WWW-Authenticate: Basic` header |
| `TestAuth_StaticAssets_NoAuth` | `/static/*` routes bypass auth (CSS/JS must load for login page) |

### 3.2 API Handlers — `internal/web/handlers.go`

**Routes:**

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/` | Yes | Dashboard HTML page |
| `GET` | `/api/current` | Yes | Latest snapshot + computed summaries |
| `GET` | `/api/history?range=6h` | Yes | Historical snapshots for chart |
| `GET` | `/api/cycles?type=subscription` | Yes | Reset cycle history |
| `GET` | `/api/summary` | Yes | All three quota summaries |
| `GET` | `/api/sessions` | Yes | Session history with per-session consumption |
| `GET` | `/static/*` | No | CSS, JS files (must load for login page) |

**Tests FIRST (`handlers_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestHandler_Dashboard_ReturnsHTML` | GET / → 200, Content-Type text/html |
| `TestHandler_Current_ReturnsJSON` | GET /api/current → JSON with all 3 quotas |
| `TestHandler_Current_IncludesResetCountdown` | Response has `timeUntilReset` for each quota |
| `TestHandler_Current_IncludesToolCallReset` | toolCallDiscounts has its own renewsAt and countdown |
| `TestHandler_Current_EmptyDB` | Returns zero-value response, not 500 |
| `TestHandler_History_DefaultRange` | No range param → defaults to 6h |
| `TestHandler_History_AllRanges` | 1h, 6h, 24h, 7d, 30d all work |
| `TestHandler_History_InvalidRange` | Bad range → 400 with error message |
| `TestHandler_History_ReturnsPercentages` | Each point has percentage of limit |
| `TestHandler_Cycles_FilterByType` | ?type=subscription returns only sub cycles |
| `TestHandler_Cycles_AllTypes` | subscription, search, toolcall all valid |
| `TestHandler_Cycles_InvalidType` | Bad type → 400 |
| `TestHandler_Cycles_IncludesActiveCycle` | Current cycle included with null end |
| `TestHandler_Summary_AllThreeQuotas` | Returns summary for sub, search, toolcall |
| `TestHandler_Summary_IncludesProjectedUsage` | Projected usage before next reset |
| `TestHandler_Summary_IncludesInsightText` | Human-readable insight string |
| `TestHandler_Sessions_ReturnsList` | GET /api/sessions → JSON array of sessions |
| `TestHandler_Sessions_IncludesMaxRequests` | Each session has max_sub/search/tool_requests |
| `TestHandler_Sessions_IncludesActiveSession` | Current running session included |
| `TestHandler_Sessions_EmptyDB` | Returns empty array, not 500 |

**`/api/current` response shape:**
```json
{
  "capturedAt": "2026-02-06T12:00:00Z",
  "subscription": {
    "name": "Subscription",
    "description": "Main API request quota for your plan",
    "usage": 154.3,
    "limit": 1350,
    "percent": 11.43,
    "status": "healthy",
    "renewsAt": "2026-02-06T16:16:18Z",
    "timeUntilReset": "4h 16m",
    "timeUntilResetSeconds": 15378,
    "currentRate": 26.4,
    "projectedUsage": 198.7,
    "insight": "You've used 11.4% of your 1,350 request quota. At ~26 req/hr, you'll use ~199 before reset."
  },
  "search": {
    "name": "Search (Hourly)",
    "description": "Search endpoint calls, resets every hour",
    "usage": 0,
    "limit": 250,
    "percent": 0,
    "status": "healthy",
    "renewsAt": "2026-02-06T13:58:14Z",
    "timeUntilReset": "58m",
    "timeUntilResetSeconds": 3480,
    "currentRate": 0,
    "projectedUsage": 0,
    "insight": "No search requests in this cycle."
  },
  "toolCalls": {
    "name": "Tool Call Discounts",
    "description": "Discounted tool call requests",
    "usage": 7635,
    "limit": 16200,
    "percent": 47.13,
    "status": "healthy",
    "renewsAt": "2026-02-06T15:26:41Z",
    "timeUntilReset": "2h 26m",
    "timeUntilResetSeconds": 8801,
    "currentRate": 1834.2,
    "projectedUsage": 12102,
    "insight": "You've used 47.1% of tool call quota. At current rate, projected ~12,102 before reset (74.7% of limit)."
  }
}
```

### 3.3 Server — `internal/web/server.go`

**Tests FIRST (`server_test.go`):**

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestServer_StartsOnPort` | Listens on configured port |
| `TestServer_ServesHTML` | GET / returns HTML content |
| `TestServer_ServesStaticCSS` | GET /static/style.css → 200, text/css |
| `TestServer_ServesStaticJS` | GET /static/app.js → 200, application/javascript |
| `TestServer_GracefulShutdown` | Shutdown within 5s, in-flight requests complete |
| `TestServer_EmbeddedAssets` | Templates and static files loaded from embed.FS |

---

## Phase 4: Dashboard Frontend (Material Design 3)

### Definition of Done
- [ ] Single-page dashboard with Material Design 3 aesthetic
- [ ] Dark mode AND Light mode with toggle (persists in localStorage)
- [ ] Detects `prefers-color-scheme` on first visit
- [ ] Three quota cards showing: name, description, progress bar, usage, percentage, countdown, absolute reset time, status badge
- [ ] ALL THREE quotas show their own reset time (including Tool Call Discounts!)
- [ ] Progress bars color-coded: green (<50%), yellow (50-79%), red (80-94%), pulsing red (95%+)
- [ ] Status uses color + icon + text (never color alone) for accessibility
- [ ] Live countdown timers update every second for all 3 quotas
- [ ] Usage insights panel with plain English descriptions
- [ ] Chart.js area chart with time range selector (1h, 6h, 24h, 7d, 30d)
- [ ] Chart shows all 3 quotas as percentage (normalized 0-100%)
- [ ] Reset cycle history table with filter by quota type
- [ ] Session history section showing per-session consumption
- [ ] Auto-refresh matching poll interval
- [ ] Responsive at 375px, 768px, 1024px, 1440px
- [ ] All interactive elements have `cursor-pointer`
- [ ] `prefers-reduced-motion` respected
- [ ] Focus states visible for keyboard navigation
- [ ] No emojis as icons (SVG only)
- [ ] Total CSS < 300 lines, total JS < 500 lines

### 4.1 HTML Templates — `internal/web/templates/`

**`layout.html`** — Base layout:
- `<!DOCTYPE html>` with `<html lang="en">`
- Meta viewport for mobile
- CSS custom properties for Material Design 3 tokens
- Theme detection script (runs before body to prevent flash)
- Chart.js CDN with `defer`
- Skip-to-content link for accessibility

**`dashboard.html`** — Extends layout:
- Header bar with title, last-updated time, theme toggle, status dot
- 3 quota cards in CSS Grid (responsive)
- Usage insights panel
- Time-series chart with range selector
- Reset cycle history table with quota type filter
- Auto-refresh logic

**`login.html`** — Shown when Basic Auth fails:
- Clean Material login form
- "SynTrack" branding
- Username + password fields
- Theme toggle available even on login

### 4.2 CSS — `static/style.css`

**Material Design 3 implementation using CSS custom properties:**

```css
/* Theme variables defined on :root and [data-theme="light"] */
:root {
    /* Dark mode (default) */
    --md-surface: #121212;
    --md-surface-container: #1E1E1E;
    --md-on-surface: #E6E1E5;
    /* ... all tokens from design-system/MASTER.md */
}

[data-theme="light"] {
    --md-surface: #FEF7FF;
    --md-surface-container: #F3EDF7;
    --md-on-surface: #1D1B20;
    /* ... light mode overrides */
}
```

**Key CSS features:**
- CSS Grid for card layout with responsive breakpoints
- CSS custom properties for instant theme switching
- `transition: background-color 200ms, color 200ms` on `*`
- Progress bar with `transition: width 500ms ease-out`
- Status colors as semantic variables
- `@media (prefers-reduced-motion: reduce)` — disable transforms
- `@media (prefers-color-scheme: light)` — auto-detect first visit
- Material elevation via surface tint (dark) / box-shadow (light)
- System font stack by default (no external font request)
- Max file size target: < 300 lines

### 4.3 JavaScript — `static/app.js`

**Core functions:**

| Function | Purpose |
|----------|---------|
| `initTheme()` | Detect preference, apply, persist |
| `toggleTheme()` | Switch dark/light, update localStorage |
| `fetchCurrent()` | GET /api/current, update cards |
| `fetchHistory(range)` | GET /api/history, update chart |
| `fetchCycles(type)` | GET /api/cycles, update table |
| `updateCard(quota)` | Update single quota card DOM |
| `updateProgressBar(el, percent)` | Animate width + color |
| `updateCountdown(el, renewsAt)` | Calculate time remaining |
| `startCountdowns()` | Single setInterval(1000) for all 3 |
| `updateInsights(data)` | Render insight text |
| `initChart()` | Chart.js setup with theme colors |
| `updateChart(data)` | Push new data, remove old |
| `startAutoRefresh(interval)` | setInterval for data fetching |

**Chart.js configuration:**
```javascript
{
  type: 'line',
  options: {
    responsive: true,
    scales: {
      y: { min: 0, max: 100, title: 'Usage %' },
      x: { type: 'time' }
    },
    plugins: {
      tooltip: { mode: 'index', intersect: false }
    },
    elements: {
      line: { tension: 0.3 },
      point: { radius: 0, hoverRadius: 4 }
    }
  }
}
```

**Countdown logic (shared timer for all 3):**
```javascript
function startCountdowns() {
  setInterval(() => {
    document.querySelectorAll('[data-renews-at]').forEach(el => {
      const renewsAt = new Date(el.dataset.renewsAt);
      const diff = renewsAt - Date.now();
      if (diff <= 0) {
        el.textContent = 'Resetting...';
        el.classList.add('resetting');
      } else {
        el.textContent = formatDuration(diff);
        el.classList.toggle('resetting-soon', diff < 30 * 60 * 1000);
      }
    });
  }, 1000);
}
```

**Performance (RAM-conscious):**
- No framework (no React, no Vue — vanilla JS)
- Chart.js loaded from CDN (browser caches it, not in Go binary)
- Single shared interval for countdowns (not 3 separate timers)
- DOM updates via `requestAnimationFrame`
- Auto-refresh uses `fetch` with `AbortController` for cancellation
- Max file size target: < 500 lines

---

## Phase 5: CLI Entry Point + Integration Tests

### Definition of Done
- [ ] `main.go` wires all components and manages lifecycle
- [ ] Startup validates config before launching anything
- [ ] Startup prints clean banner with redacted config
- [ ] Background mode (default): logs to `.syntrack.log`, process detaches from terminal
- [ ] Debug mode (`--debug`): logs to stdout, stays in foreground
- [ ] Agent creates session (UUID) on start, closes on stop
- [ ] Agent and server run concurrently in separate goroutines
- [ ] SIGINT/SIGTERM triggers graceful shutdown of both
- [ ] Shutdown order: stop agent (closes session) → shutdown server (5s) → close DB → close log file
- [ ] No goroutine leaks after shutdown
- [ ] Integration tests verify full cycle: poll → store → track → serve
- [ ] Integration tests verify session creation and closure
- [ ] `--help` prints all options with defaults
- [ ] `--version` prints version string
- [ ] Build produces single static binary < 15 MB
- [ ] Makefile has `build`, `test`, `run`, `clean` targets
- [ ] All tests pass with `go test -race ./...`

### 5.1 Main Entry — `main.go`

```go
func main() {
    // 1. Load config (.env + flags: --interval, --port, --db, --debug)
    // 2. Validate config (fail fast)
    // 3. Setup logging:
    //    - --debug: slog.TextHandler → os.Stdout
    //    - default: slog.JSONHandler → .syntrack.log file
    // 4. Print startup banner (redacted)
    // 5. Open SQLite database + run migrations
    // 6. Create components: client, store, tracker, agent, server
    // 7. Setup signal handling (SIGINT, SIGTERM)
    // 8. Start agent in goroutine (creates session)
    // 9. Start web server in goroutine
    // 10. Block on signal
    // 11. Cancel context → stops agent (closes session)
    // 12. Shutdown server (5s timeout)
    // 13. Close database
    // 14. Close log file if applicable
    // 15. Exit
}
```

**Startup banner:**
```
╔══════════════════════════════════════╗
║  SynTrack v1.0.0                     ║
╠══════════════════════════════════════╣
║  API:       synthetic.new/v2/quotas  ║
║  Polling:   every 60s               ║
║  Dashboard: http://localhost:8932    ║
║  Database:  ./syntrack.db            ║
║  Auth:      admin / ****             ║
╚══════════════════════════════════════╝
```

### 5.2 Integration Tests

| Test Name | What It Verifies |
|-----------|-----------------|
| `TestIntegration_FullCycle` | Mock API → agent polls → store saves → handler returns data |
| `TestIntegration_ResetDetection` | Simulate reset → cycle recorded → appears in /api/cycles |
| `TestIntegration_DashboardRendersData` | HTML page contains actual quota values |
| `TestIntegration_ChartDataEndpoint` | /api/history returns correct shape for Chart.js |
| `TestIntegration_GracefulShutdown` | SIGINT → clean stop → DB not corrupted |
| `TestIntegration_ColdStart_EmptyDB` | First run → first poll → dashboard shows data |
| `TestIntegration_ToolCallResetIndependent` | Tool calls reset while sub doesn't → correct behavior |

### 5.3 Makefile

```makefile
.PHONY: build test run clean

VERSION := 1.0.0
BINARY := syntrack
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) .

test:
	go test -race -cover -count=1 ./...

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY) coverage.out coverage.html
	go clean -testcache
```

---

## Phase 6: Documentation + Release Prep

### Definition of Done
- [ ] README.md complete with: overview, install, usage, config, dashboard preview, contributing
- [ ] LICENSE file (MIT)
- [ ] All tests green: `go test -race -cover ./...`
- [ ] Test coverage > 80%
- [ ] No secrets in any committed file (audit every file)
- [ ] `.gitignore` covers: .env, *.db, *.png, *.jpg, binaries, vendor, IDE
- [ ] `git log` shows clean conventional commits
- [ ] Binary size < 15 MB
- [ ] RAM usage verified < 15 MB during dashboard render

### 6.1 Final Security Audit

```bash
# Check for hardcoded secrets
grep -r "syn_" --include="*.go" . | grep -v "_test.go" | grep -v "syn_your"
# Should return 0 results

# Check .gitignore effectiveness
git status  # No .env, *.db, binary visible

# Check for SQL injection surface
grep -r "fmt.Sprintf.*SELECT\|fmt.Sprintf.*INSERT\|fmt.Sprintf.*UPDATE" --include="*.go" .
# Should return 0 results (all SQL must be parameterized)
```

### 6.2 RAM Verification

```bash
# Build and run
make build && ./syntrack &

# Check RSS after 1 minute (idle)
ps aux | grep syntrack | grep -v grep
# Expect: RSS < 15 MB

# Open dashboard, check again
curl http://localhost:8932/ > /dev/null
ps aux | grep syntrack | grep -v grep
# Expect: RSS < 20 MB
```

---

## Safety Checks Summary (Multi-Level)

| Level | Category | What |
|-------|----------|------|
| 1 | **Compile** | Strong typing (float64 for requests, time.Time for timestamps), `internal/` visibility |
| 2 | **Test** | TDD, table-driven tests, race detector, in-memory SQLite, httptest |
| 3 | **Runtime** | Config validation, HTTP timeouts (10s client, 5s shutdown), context cancellation |
| 4 | **Data** | Append-only snapshots, WAL mode, transactions for cycle updates, bounded cache |
| 5 | **Security** | Basic Auth + constant-time compare, API key redaction, parameterized SQL, no secrets in repo |
| 6 | **UI/UX** | Color + icon + text for status, WCAG AA contrast, reduced-motion, keyboard-navigable |

---

## Build Order

```
Phase 1 → Config + Types + Store (foundation, pure logic, no I/O besides SQLite)
    ↓
Phase 2 → API Client + Tracker + Agent (network I/O, state machine, background loop)
    ↓
Phase 3 → Web Server + Auth + Handlers (HTTP layer, JSON API)
    ↓
Phase 4 → Dashboard HTML/CSS/JS (Material Design 3, dark+light, charts, countdowns)
    ↓
Phase 5 → main.go + Integration Tests + Makefile (wiring, lifecycle, build)
    ↓
Phase 6 → README + LICENSE + Security Audit + RAM Verification
```

**Each phase gate:** `go test -race -cover ./...` must pass with 0 failures.
