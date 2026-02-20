# Codex OAuth Integration Design (onWatch)

## Status
Approved in discussion.

## Objective
Add Codex usage tracking to onWatch with a provider experience that is uniform with existing Anthropic and Copilot integrations.

## Final Decisions
1. **V1 source mode**: OAuth only.
2. **No CLI fallback in V1**.
3. **Platform support**: do not hardcode mac-only. Support environments where Codex auth credentials are available.
4. **UI shape**: dynamic quota cards/charts, same pattern as Anthropic and Copilot.

## Scope
- Add a new provider: `codex`.
- Fetch Codex usage via OAuth credentials from local Codex auth state.
- Persist snapshots, quota values, reset cycles, and session analytics in SQLite.
- Expose codex through existing `/api/current`, `/api/history`, `/api/cycles`, `/api/summary`, `/api/sessions`, `/api/insights` provider routing.
- Render codex in dashboard provider tabs and in `both` mode.

## Non-goals (V1)
- CLI parsing (`codex /status`).
- Browser cookie based fallback.
- Provider-specific UI that differs from the Anthropic/Copilot dynamic model.

## Provider Data Source Design

### Credential source
- Read credentials from:
  - `CODEX_HOME/auth.json` if `CODEX_HOME` is set.
  - otherwise `~/.codex/auth.json`.
- Parse both auth shapes from file:
  1. `OPENAI_API_KEY`
  2. `tokens.access_token` (+ optional `refresh_token`, `id_token`, `account_id`)
- Runtime polling in V1 uses OAuth access token only (`tokens.access_token`).
  - `OPENAI_API_KEY` is parsed but not used as a fallback auth token for usage polling.

### OAuth refresh
- V1 does not perform OAuth token refresh exchange.
- The agent re-reads local Codex credentials before each poll and retries once on auth failure.
- After repeated auth failures with unchanged credentials, polling is paused until credentials change.

### Usage endpoint
- Fetch usage with Bearer auth using Codex OAuth usage endpoint behavior equivalent to CodexBar.
- Use deterministic endpoint fallback on 404 between `/backend-api/wham/usage` and `/api/codex/usage`.
- Normalize into onWatch internal snapshot model before storage.

## Canonical Codex Quota Mapping (onWatch)
- `five_hour` <- OAuth `rate_limit.primary_window`
- `seven_day` <- OAuth `rate_limit.secondary_window` (when present)

Each mapped quota stores:
- utilization percent
- reset timestamp (if provided)
- derived status (`healthy`, `warning`, `danger`, `critical`)

Optional metadata (not required for cards):
- `plan_type`
- `credits.balance`

## onWatch Integration Architecture

### Config layer
- Extend provider availability logic to include `codex`.
- Ensure provider is considered configured when codex auth source is present or codex is explicitly enabled via settings.
- Keep existing multi-provider behavior (`both`) unchanged.

### API layer
Add Codex API client and types:
- Parse OAuth response payload.
- Convert response into normalized codex snapshot.
- Error taxonomy aligned with Anthropic/Copilot clients:
  - unauthorized
  - forbidden
  - server error
  - invalid response
  - network/context cancellation

### Store layer
Add codex tables similar to Anthropic/Copilot dynamic providers:
- `codex_snapshots`
- `codex_quota_values`
- `codex_reset_cycles`

Add indexes and bounded query paths consistent with current RAM constraints.

### Tracker layer
Add `CodexTracker` with per-quota cycle tracking:
- create first active cycle
- update peak/delta in active cycle
- detect reset by reset timestamp changes and utilization transitions
- close old cycle and open new cycle
- provide summary APIs for handlers

### Agent layer
Add `CodexAgent` poll loop:
1. fetch quotas
2. normalize snapshot
3. insert snapshot
4. process tracker
5. run notifier checks
6. report poll to session manager

### Main wiring
Wire client/tracker/agent in `main.go` with existing provider visibility and polling toggles.

### Handler layer
Extend provider dispatch and `both` aggregations for `codex` in:
- current
- history
- cycles
- summary
- sessions
- insights

Response shape for current must match dynamic providers:
```json
{
  "capturedAt": "...",
  "quotas": [
    {
      "name": "five_hour",
      "displayName": "5-Hour Limit",
      "utilization": 22.0,
      "status": "healthy",
      "resetsAt": "..."
    }
  ]
}
```

### UI layer
Use Anthropic/Copilot dynamic rendering pattern:
- add Codex tab label and subtitle name mapping
- add codex single-provider container
- add codex column in both-view when configured
- add codex display name map and stable color map in `app.js`
- use dynamic history chart dataset construction
- include codex renewal category and overview display-name mapping

## Error Handling and Resilience
- Provider errors must not crash daemon.
- Codex fetch failures degrade only codex view, not other providers.
- API handlers return valid empty/default structures when no codex data exists.
- Preserve stale indicator behavior in frontend when fetch fails.

## Security Requirements
- Never log tokens or raw auth file content.
- Keep HTTP timeouts and bounded response reads.
- Use parameterized SQL only.
- Keep auth/session middleware unchanged and shared.

## Performance and Constraints
- Follow existing memory budget and bounded query rules.
- Keep cycle/history/insight queries capped consistently with current providers.
- Avoid unbounded in-memory structures for codex datasets.

## TDD Test Plan
1. **API types tests**: response parse and quota window mapping.
2. **Credential tests**: auth.json parsing for API key and OAuth token shapes.
3. **API client tests** (`httptest`): headers, 2xx, 401/403, 5xx, invalid JSON, canceled context.
4. **Store tests**: insert/query snapshots, range queries, cycle persistence.
5. **Tracker tests**: first snapshot, delta accumulation, reset transition, jitter tolerance.
6. **Handler tests**: provider routing for codex and `both` aggregations.
7. **Config tests**: codex provider availability logic.
8. **Frontend integration checks**: codex dynamic cards/charts/maps.

## Rollout Notes
- V1 is OAuth-only by design.
- CLI fallback can be added later as a separate enhancement once OAuth path is stable in production.

## Success Criteria
- Codex appears as a first-class provider in dashboard and API.
- Metrics and visuals are uniform with Anthropic/Copilot dynamic provider UX.
- Polling remains stable under provider failures.
- Full test suite passes with race detector and vet checks.
