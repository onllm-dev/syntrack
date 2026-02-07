# Z.ai API Integration

> **Status:** Implemented. Z.ai runs as a parallel provider alongside Synthetic.
>
> **API tested:** 2026-02-06

This document covers the Z.ai monitoring API endpoints used by SynTrack and how they map to the internal architecture.

---

## Authentication

- **Header:** `Authorization: <ZAI_API_KEY>` (no Bearer prefix)
- **Key source:** `ZAI_API_KEY` in `.env`
- **Error handling:** The API returns HTTP 200 even for auth failures. SynTrack checks the `code` field in the response body:
  ```json
  {"code": 401, "msg": "token expired or incorrect", "success": false}
  ```

---

## Base URLs

Both return identical data:

| Platform | Base URL |
|----------|----------|
| Z.ai (default) | `https://api.z.ai/api` |
| ZHIPU (mirror) | `https://open.bigmodel.cn/api` |

Configure via `ZAI_BASE_URL` in `.env`.

---

## Endpoint: Quota / Limits

**Used by SynTrack for polling.**

```
GET /monitor/usage/quota/limit
```

No parameters. Returns the current quota state.

### Response

```json
{
  "code": 200,
  "msg": "Operation successful",
  "success": true,
  "data": {
    "limits": [
      {
        "type": "TIME_LIMIT",
        "unit": 5,
        "number": 1,
        "usage": 1000,
        "currentValue": 19,
        "remaining": 981,
        "percentage": 1,
        "usageDetails": [
          { "modelCode": "search-prime", "usage": 16 },
          { "modelCode": "web-reader", "usage": 39 },
          { "modelCode": "zread", "usage": 79 }
        ]
      },
      {
        "type": "TOKENS_LIMIT",
        "unit": 3,
        "number": 5,
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

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `TIME_LIMIT` or `TOKENS_LIMIT` |
| `usage` | number | Total quota budget |
| `currentValue` | number | Current consumption (can exceed `usage`) |
| `remaining` | number | `usage - currentValue` (floor 0) |
| `percentage` | number | Usage percentage (0-100, integer) |
| `nextResetTime` | number | Epoch milliseconds. Present on `TOKENS_LIMIT` only. |
| `usageDetails` | array | Per-model breakdown. Present on `TIME_LIMIT` only. |

### Quota Types

**TIME_LIMIT** — Tool call budget (search, web-reader, zread). Tracks per-tool counts via `usageDetails`. No `nextResetTime` observed; reset cycle unclear.

**TOKENS_LIMIT** — Token consumption budget (200M tokens). `currentValue` can exceed `usage`. Convert `nextResetTime`: `new Date(1770398385482)` gives `2026-02-06T22:49:45Z`.

---

## Endpoint: Model Usage (Time-Series)

```
GET /monitor/usage/model-usage?startTime={start}&endTime={end}
```

| Parameter | Format | Example |
|-----------|--------|---------|
| `startTime` | `YYYY-MM-DD HH:mm:ss` | `2026-02-05 00:00:00` |
| `endTime` | `YYYY-MM-DD HH:mm:ss` | `2026-02-06 23:59:59` |

Returns hourly buckets of API call counts and token usage. Both parameters required.

### Response

```json
{
  "code": 200,
  "data": {
    "x_time": ["2026-02-05 00:00", "2026-02-05 01:00"],
    "modelCallCount": [null, 20],
    "tokensUsage": [null, 144154],
    "totalUsage": {
      "totalModelCallCount": 10296,
      "totalTokensUsage": 360784945
    }
  }
}
```

Granularity is always hourly. `null` means no activity, not missing data. Arrays are parallel: `x_time[i]` corresponds to `modelCallCount[i]`.

---

## Endpoint: Tool Usage (Time-Series)

```
GET /monitor/usage/tool-usage?startTime={start}&endTime={end}
```

Same parameters as model-usage.

### Response

```json
{
  "code": 200,
  "data": {
    "x_time": ["2026-02-05 00:00"],
    "networkSearchCount": [null],
    "webReadMcpCount": [null],
    "zreadMcpCount": [null],
    "totalUsage": {
      "totalNetworkSearchCount": 16,
      "totalWebReadMcpCount": 1,
      "totalZreadMcpCount": 0,
      "totalSearchMcpCount": 17,
      "toolDetails": [
        { "modelName": "search-prime", "totalUsageCount": 16 },
        { "modelName": "web-reader", "totalUsageCount": 1 }
      ]
    }
  }
}
```

---

## SynTrack Implementation

### Source Files

| File | Purpose |
|------|---------|
| `internal/api/zai_types.go` | Response types and snapshot conversion |
| `internal/api/zai_client.go` | HTTP client for Z.ai quota endpoint |
| `internal/agent/zai_agent.go` | Background polling agent |
| `internal/store/zai_store.go` | SQLite CRUD for Z.ai snapshots and cycles |
| `internal/web/handlers.go` | Provider-aware API handlers |

### How Z.ai Maps to SynTrack

| SynTrack Concept | Synthetic API | Z.ai Equivalent |
|-----------------|---------------|-----------------|
| Real-time snapshot | `GET /v2/quotas` | `GET /monitor/usage/quota/limit` |
| Primary quota | `subscription` (requests/limit) | `TOKENS_LIMIT` (currentValue/usage) |
| Secondary quota | `search.hourly` | `TIME_LIMIT` (tool calls) |
| Reset time | `renewsAt` (ISO 8601) | `nextResetTime` (epoch ms) |

### Dashboard

The dashboard shows two quota cards for Z.ai:

- **Tokens:** Current token consumption vs. budget, with countdown to reset
- **Time (Tools):** Tool call count vs. limit, with per-model breakdown

Switch between Synthetic and Z.ai via the provider dropdown. The URL parameter `?provider=zai` controls which data the API returns.

---

## API Behavior Notes

- Auth errors return HTTP 200 with `code: 401` in the body
- No rate limiting observed
- Time-series always returns hourly buckets
- `null` in arrays means zero activity
- 30-day queries work (744 data points)
- `currentValue` can exceed `usage` (no hard cap)
- Both base URLs (`api.z.ai`, `open.bigmodel.cn`) return identical data

---

## Open Questions

1. What do `unit` and `number` mean in quota/limit responses?
2. Does `TIME_LIMIT` have a reset cycle? No `nextResetTime` observed.
3. Is there a hard cutoff when `currentValue` exceeds `usage`?
4. Are there webhook or push notification endpoints?
