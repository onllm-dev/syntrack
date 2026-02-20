# Codex OAuth Integration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Codex as a first-class onWatch provider using OAuth auth-state detection and usage polling, with uniform backend/API/UI behavior matching Anthropic/Copilot.

**Architecture:** Implement Codex as a dynamic-quota provider (`five_hour`, `seven_day`) with provider-specific API/store/tracker/agent modules, then wire it through existing provider dispatch (`current/history/cycles/summary/sessions/insights/cycle-overview`) and dashboard rendering. Reuse Anthropic/Copilot patterns for snapshot normalization, cycle tracking, and dynamic cards/charts. Keep V1 OAuth-only and resilient: no CLI fallback path.

**Tech Stack:** Go 1.25+, net/http, SQLite (`modernc.org/sqlite`), html/template + embedded static JS, existing onWatch agent/tracker/store architecture, TDD with `go test`.

---

### Task 1: Add Codex usage types and snapshot normalization

**Files:**
- Create: `internal/api/codex_types.go`
- Create: `internal/api/codex_types_test.go`
- Reference: `internal/api/anthropic_types.go`, `internal/api/copilot_types.go`

**Step 1: Write the failing test**

```go
func TestCodexUsageResponse_ToSnapshot_PrimaryAndSecondary(t *testing.T) {
    payload := []byte(`{
      "plan_type": "pro",
      "rate_limit": {
        "primary_window": {"used_percent": 22.5, "reset_at": 1766000000, "limit_window_seconds": 18000},
        "secondary_window": {"used_percent": 41.0, "reset_at": 1766400000, "limit_window_seconds": 604800}
      },
      "credits": {"balance": 123.4}
    }`)

    resp, err := ParseCodexUsageResponse(payload)
    if err != nil {
        t.Fatalf("ParseCodexUsageResponse: %v", err)
    }

    snap := resp.ToSnapshot(time.Unix(1765900000, 0).UTC())
    if len(snap.Quotas) != 2 {
        t.Fatalf("quota len = %d, want 2", len(snap.Quotas))
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestCodexUsageResponse_ToSnapshot_PrimaryAndSecondary -v`
Expected: FAIL with undefined Codex types/functions.

**Step 3: Write minimal implementation**

```go
type CodexUsageResponse struct {
    PlanType  string         `json:"plan_type"`
    RateLimit codexRateLimit `json:"rate_limit"`
    Credits   *codexCredits  `json:"credits,omitempty"`
}

type CodexSnapshot struct {
    ID             int64
    CapturedAt     time.Time
    Quotas         []CodexQuota
    PlanType       string
    CreditsBalance *float64
    RawJSON        string
}

func ParseCodexUsageResponse(data []byte) (*CodexUsageResponse, error) { /* ... */ }
func (r CodexUsageResponse) ToSnapshot(capturedAt time.Time) *CodexSnapshot { /* ... */ }
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/api -run TestCodexUsageResponse_ToSnapshot_PrimaryAndSecondary -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/api/codex_types.go internal/api/codex_types_test.go
git commit -m "feat(codex): add OAuth usage response normalization"
```

---

### Task 2: Add Codex credentials detection and auth file parsing

**Files:**
- Create: `internal/api/codex_credentials.go`
- Create: `internal/api/codex_credentials_test.go`
- Reference: `internal/api/anthropic_token.go`, `internal/api/anthropic_token_unix.go`

**Step 1: Write the failing tests**

```go
func TestDetectCodexCredentials_ParsesOAuthTokens(t *testing.T) { /* temp auth.json -> access/refresh/account */ }
func TestDetectCodexCredentials_ParsesAPIKey(t *testing.T) { /* OPENAI_API_KEY path */ }
func TestDetectCodexToken_PrefersAccessTokenOverAPIKey(t *testing.T) { /* token selection rule */ }
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/api -run TestDetectCodex -v`
Expected: FAIL with undefined detection helpers.

**Step 3: Write minimal implementation**

```go
type CodexCredentials struct {
    AccessToken  string
    RefreshToken string
    APIKey       string
    AccountID    string
}

func DetectCodexCredentials(logger *slog.Logger) *CodexCredentials { /* read CODEX_HOME/auth.json else ~/.codex/auth.json */ }
func DetectCodexToken(logger *slog.Logger) string { /* choose access token only (V1 runtime) */ }
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/api -run TestDetectCodex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/api/codex_credentials.go internal/api/codex_credentials_test.go
git commit -m "feat(codex): detect credentials from Codex auth file"
```

---

### Task 3: Add Codex OAuth usage client

**Files:**
- Create: `internal/api/codex_client.go`
- Create: `internal/api/codex_client_test.go`
- Reference: `internal/api/copilot_client.go`, `internal/api/anthropic_client.go`

**Step 1: Write failing tests for HTTP behavior**

```go
func TestCodexClient_FetchUsage_Success(t *testing.T) { /* validates headers + parse */ }
func TestCodexClient_FetchUsage_Unauthorized(t *testing.T) { /* 401 -> sentinel */ }
func TestCodexClient_FetchUsage_ServerError(t *testing.T) { /* 5xx -> sentinel */ }
func TestCodexClient_FetchUsage_InvalidJSON(t *testing.T) { /* parse error */ }
func TestCodexClient_FetchUsage_ContextCancelled(t *testing.T) { /* cancellation */ }
```

**Step 2: Run tests to verify failure**

Run: `go test ./internal/api -run TestCodexClient -v`
Expected: FAIL with undefined client/errors.

**Step 3: Write minimal implementation**

```go
var (
    ErrCodexUnauthorized    = errors.New("codex: unauthorized")
    ErrCodexForbidden       = errors.New("codex: forbidden")
    ErrCodexServerError     = errors.New("codex: server error")
    ErrCodexNetworkError    = errors.New("codex: network error")
    ErrCodexInvalidResponse = errors.New("codex: invalid response")
)

type CodexClient struct { /* httpClient, token, baseURL, logger, accountID */ }
func NewCodexClient(token string, logger *slog.Logger, opts ...CodexOption) *CodexClient { /* ... */ }
func (c *CodexClient) SetToken(token string) { /* ... */ }
func (c *CodexClient) SetAccountID(accountID string) { /* ... */ }
func (c *CodexClient) FetchUsage(ctx context.Context) (*CodexUsageResponse, error) { /* bounded read + status handling */ }
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/api -run TestCodexClient -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/api/codex_client.go internal/api/codex_client_test.go
git commit -m "feat(codex): add OAuth usage HTTP client"
```

---

### Task 4: Add Codex provider config wiring

**Files:**
- Modify: `internal/config/config.go` (Config fields, env load, provider selectors)
- Modify: `internal/config/config_test.go`

**Step 1: Write failing config tests**

```go
func TestConfig_HasProvider_Codex(t *testing.T) { /* cfg.CodexToken non-empty -> true */ }
func TestConfig_AvailableProviders_IncludesCodex(t *testing.T) { /* providers list contains codex */ }
func TestConfig_HasMultipleProviders_WithCodex(t *testing.T) { /* codex + synthetic -> true */ }
```

**Step 2: Run tests and confirm failure**

Run: `go test ./internal/config -run Codex -v`
Expected: FAIL with missing `CodexToken` fields/logic.

**Step 3: Implement minimal config changes**

```go
type Config struct {
    // ...existing fields...
    CodexToken     string
    CodexAutoToken bool
}

cfg.CodexToken = strings.TrimSpace(os.Getenv("CODEX_TOKEN"))

// in HasProvider / HasMultipleProviders / AvailableProviders
case "codex": return c.CodexToken != ""
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/config -run Codex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): register codex provider availability"
```

---

### Task 5: Add Codex store schema and queries

**Files:**
- Modify: `internal/store/store.go` (createTables schema + indexes)
- Create: `internal/store/codex_store.go`
- Create: `internal/store/codex_store_test.go`
- Reference: `internal/store/anthropic_store.go`, `internal/store/copilot_store.go`

**Step 1: Write failing store tests**

```go
func TestCodexStore_InsertAndQueryLatest(t *testing.T) { /* snapshot + quotas round-trip */ }
func TestCodexStore_QueryRange(t *testing.T) { /* ordered range */ }
func TestCodexStore_CycleLifecycle(t *testing.T) { /* create/update/close/query */ }
func TestCodexStore_UsageSeriesAndQuotaNames(t *testing.T) { /* analytics helper coverage */ }
```

**Step 2: Run tests to confirm failure**

Run: `go test ./internal/store -run Codex -v`
Expected: FAIL with missing tables/methods.

**Step 3: Implement minimal schema + store methods**

```go
// schema additions in createTables:
// codex_snapshots, codex_quota_values, codex_reset_cycles + indexes

func (s *Store) InsertCodexSnapshot(snapshot *api.CodexSnapshot) (int64, error) { /* tx insert */ }
func (s *Store) QueryLatestCodex() (*api.CodexSnapshot, error) { /* latest + quota rows */ }
func (s *Store) QueryCodexRange(start, end time.Time, limit ...int) ([]*api.CodexSnapshot, error) { /* range */ }
func (s *Store) CreateCodexCycle(quotaName string, cycleStart time.Time, resetsAt *time.Time) (int64, error) { /* ... */ }
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/store -run Codex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/store/store.go internal/store/codex_store.go internal/store/codex_store_test.go
git commit -m "feat(store): add codex snapshot and cycle persistence"
```

---

### Task 6: Add Codex tracker for cycle detection and summaries

**Files:**
- Create: `internal/tracker/codex_tracker.go`
- Create: `internal/tracker/codex_tracker_test.go`
- Reference: `internal/tracker/anthropic_tracker.go`, `internal/tracker/copilot_tracker.go`

**Step 1: Write failing tracker tests**

```go
func TestCodexTracker_Process_CreatesInitialCycle(t *testing.T) { /* first snapshot */ }
func TestCodexTracker_Process_AccumulatesDeltaAndPeak(t *testing.T) { /* same cycle updates */ }
func TestCodexTracker_Process_DetectsReset(t *testing.T) { /* resetsAt changed */ }
func TestCodexTracker_UsageSummary(t *testing.T) { /* summary fields populated */ }
```

**Step 2: Run tests and verify fail**

Run: `go test ./internal/tracker -run Codex -v`
Expected: FAIL with missing tracker.

**Step 3: Implement minimal tracker**

```go
type CodexTracker struct {
    store      *store.Store
    logger     *slog.Logger
    lastValues map[string]float64
    lastResets map[string]string
    hasLast    bool
    onReset    func(string)
}

func (t *CodexTracker) Process(snapshot *api.CodexSnapshot) error { /* per-quota cycle logic */ }
func (t *CodexTracker) UsageSummary(quotaName string) (*CodexSummary, error) { /* stats + projection */ }
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/tracker -run Codex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/tracker/codex_tracker.go internal/tracker/codex_tracker_test.go
git commit -m "feat(tracker): track codex reset cycles and summary stats"
```

---

### Task 7: Add Codex polling agent

**Files:**
- Create: `internal/agent/codex_agent.go`
- Create: `internal/agent/codex_agent_test.go`
- Reference: `internal/agent/copilot_agent.go`, `internal/agent/anthropic_agent.go`

**Step 1: Write failing agent tests**

```go
func TestCodexAgent_Poll_StoresSnapshotAndProcessesTracker(t *testing.T) { /* happy path */ }
func TestCodexAgent_Poll_ReportsSessionValues(t *testing.T) { /* session manager receives usage values */ }
func TestCodexAgent_Poll_NotifierCalled(t *testing.T) { /* notification thresholds */ }
```

**Step 2: Run tests to confirm failure**

Run: `go test ./internal/agent -run Codex -v`
Expected: FAIL with missing agent.

**Step 3: Implement minimal agent**

```go
type CodexAgent struct {
    client       *api.CodexClient
    store        *store.Store
    tracker      *tracker.CodexTracker
    interval     time.Duration
    logger       *slog.Logger
    sm           *SessionManager
    notifier     *notify.NotificationEngine
    pollingCheck func() bool
    tokenRefresh func() string
}

func (a *CodexAgent) Run(ctx context.Context) error { /* immediate poll + ticker */ }
func (a *CodexAgent) poll(ctx context.Context) { /* fetch -> insert -> tracker -> notify -> session */ }
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/agent -run Codex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/agent/codex_agent.go internal/agent/codex_agent_test.go
git commit -m "feat(agent): add codex polling loop"
```

---

### Task 8: Wire Codex runtime in `main.go`

**Files:**
- Modify: `main.go`
- Reference: existing anthropic/copilot wiring blocks

**Step 1: Write failing integration test (or focused compile test if no main tests exist)**

```go
// Add a focused compile-oriented test in an existing package if needed to
// assert codex symbols are wired and build graph is valid.
```

**Step 2: Run targeted command to verify current compile baseline**

Run: `go test ./... -run TestMain_WiresCodex -v`
Expected: FAIL (test missing / wiring absent).

**Step 3: Implement main wiring changes**

```go
if cfg.CodexToken == "" {
    if token := api.DetectCodexToken(logger); token != "" {
        cfg.CodexToken = token
    }
}

var codexClient *api.CodexClient
if cfg.HasProvider("codex") { codexClient = api.NewCodexClient(cfg.CodexToken, logger) }

var codexTr *tracker.CodexTracker
var codexAg *agent.CodexAgent
// notifier + polling + reset callbacks + handler setter + goroutine startup + no-agent check
```

**Step 4: Run package tests for changed areas**

Run: `go test ./internal/api ./internal/tracker ./internal/agent -run Codex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add main.go
git commit -m "feat(runtime): wire codex provider client tracker and agent"
```

---

### Task 9: Extend web handlers for Codex provider routes

**Files:**
- Modify: `internal/web/handlers.go`
- Create: `internal/web/handlers_codex_test.go`
- Reference: `currentAnthropic/currentCopilot`, `historyAnthropic/historyCopilot`, etc.

**Step 1: Write failing handler tests**

```go
func TestHandler_Current_Codex(t *testing.T) { /* provider=codex returns quotas[] */ }
func TestHandler_Providers_IncludesCodexWhenConfigured(t *testing.T) { /* /api/providers */ }
func TestHandler_Current_Both_IncludesCodex(t *testing.T) { /* both payload includes codex */ }
func TestHandler_History_Codex(t *testing.T) { /* flattened history response */ }
func TestHandler_CycleOverview_Codex(t *testing.T) { /* codex cycle overview payload */ }
```

**Step 2: Run tests to verify fail**

Run: `go test ./internal/web -run Codex -v`
Expected: FAIL with missing handler routes/setters.

**Step 3: Implement minimal handler support**

```go
// Handler struct + setter
codexTracker *tracker.CodexTracker
func (h *Handler) SetCodexTracker(t *tracker.CodexTracker) { h.codexTracker = t }

// provider dispatch additions
case "codex": ...

// implement currentCodex/historyCodex/cyclesCodex/summaryCodex/insightsCodex/cycleOverviewCodex
// add codex branches in *Both aggregators
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/web -run Codex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/handlers_codex_test.go
git commit -m "feat(web): add codex provider API endpoints and aggregations"
```

---

### Task 10: Add Codex dashboard template and frontend rendering

**Files:**
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/static/app.js`

**Step 1: Write failing frontend-focused assertions (Go-side where possible)**

```go
// Add template-level checks in web tests where practical:
// - codex tab label rendered when provider exists
// - codex both-view container present
```

**Step 2: Run tests to confirm failure**

Run: `go test ./internal/web -run Dashboard.*Codex -v`
Expected: FAIL before template/handler data model updates.

**Step 3: Implement UI changes**

```js
// app.js additions:
const codexDisplayNames = { five_hour: '5-Hour Limit', seven_day: 'Weekly All-Model' };
const codexChartColorMap = { five_hour: {...}, seven_day: {...} };

function renderCodexQuotaCards(...) { ... }
function updateCodexCard(...) { ... }

// fetchCurrent branches for provider==='codex' and data.codex in both mode
// fetchHistory dynamic dataset path for codex
// settings provider toggles + overrides include codex
```

```html
<!-- dashboard.html additions -->
{{else if eq . "codex"}}Codex{{...}}
{{if .HasCodex}} ... quota-grid-codex-both ... {{end}}
{{else if eq .CurrentProvider "codex"}}
<div class="quota-grid" id="quota-grid-codex" data-provider="codex"></div>
```

**Step 4: Run tests/build sanity**

Run: `go test ./internal/web -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/web/templates/dashboard.html internal/web/static/app.js
git commit -m "feat(dashboard): render codex quota cards charts and settings toggles"
```

---

### Task 11: Wire Codex provider visibility and settings defaults

**Files:**
- Modify: `internal/web/handlers.go` (settings payload shaping if needed)
- Modify: `internal/web/handlers_test.go`
- Modify: `internal/web/static/app.js` (provider toggles + override provider list)

**Step 1: Write failing tests**

```go
func TestHandler_Providers_RespectsCodexVisibility(t *testing.T) { /* provider_visibility codex dashboard=false */ }
func TestHandler_UpdateSettings_AcceptsCodexVisibility(t *testing.T) { /* store round-trip */ }
```

**Step 2: Run tests and verify fail**

Run: `go test ./internal/web -run Visibility.*Codex -v`
Expected: FAIL before codex is included consistently.

**Step 3: Implement minimal changes**

```go
// ensure codex flows through existing provider_visibility maps and dashboard filtering
// no custom schema changes required (stored as JSON map)
```

**Step 4: Re-run tests**

Run: `go test ./internal/web -run Visibility.*Codex -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/handlers_test.go internal/web/static/app.js
git commit -m "feat(settings): include codex in provider visibility controls"
```

---

### Task 12: Full verification, formatting, and final safety pass

**Files:**
- Modify as needed from previous tasks

**Step 1: Run gofmt on changed Go files**

Run: `gofmt -w internal/api/*.go internal/store/*.go internal/tracker/*.go internal/agent/*.go internal/web/*.go main.go`
Expected: no output; files formatted.

**Step 2: Run focused package tests**

Run: `go test ./internal/api ./internal/store ./internal/tracker ./internal/agent ./internal/web -v`
Expected: PASS.

**Step 3: Run full project test suite with race and coverage**

Run: `go test -race -cover ./...`
Expected: PASS.

**Step 4: Run vet**

Run: `go vet ./...`
Expected: PASS.

**Step 5: Commit final integration**

```bash
git add main.go internal/api internal/store internal/tracker internal/agent internal/web docs/plans/2026-02-17-codex-oauth-integration-implementation.md
git commit -m "feat(codex): integrate OAuth provider across backend and dashboard"
```

---

## Notes for the implementing engineer

- Keep bounded reads for all Codex HTTP responses (`io.LimitReader(..., 1<<16)`).
- Do not log token material or raw auth file contents.
- Preserve existing provider response contracts for non-codex providers.
- Keep query limits bounded (history/cycles/insights) consistent with existing handlers.
- No CLI fallback in this plan.
- Runtime auth is OAuth access-token only in V1; parse API-key shape but do not use it for polling auth.
- If Codex endpoint variant differs by account (`/wham/usage` vs `/api/codex/usage`), add deterministic fallback inside `CodexClient` with explicit tests.
- V1 does not include OAuth refresh-token exchange; token recovery is based on credential re-read + retry.
