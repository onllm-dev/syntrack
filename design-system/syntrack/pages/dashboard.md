# Dashboard Page Overrides

> **PROJECT:** SynTrack
> **Updated:** 2026-02-06
> **Page Type:** Real-time Monitoring Dashboard

> This file **overrides** the Master file (`design-system/syntrack/MASTER.md`).
> Only deviations from the Master are documented here. For all other rules, refer to the Master.

---

## Layout Override

- **Max Width:** `1400px` (wider than typical — dashboard needs horizontal space)
- **Layout:** CSS Grid — 3-column for quota cards, full-width chart below
- **Content Density:** Medium — balance between information density and readability
- **Padding:** `px-4 md:px-6 lg:px-8` (responsive padding from Tailwind guidelines)

```
+-------------------------------------------------------------------+
|  [S] SynTrack        Last updated: 12:34    [Theme] [Status dot]  |
+-------------------------------------------------------------------+
|                                                                    |
|  +------------------+ +------------------+ +------------------+   |
|  | SUBSCRIPTION     | | SEARCH (HOURLY)  | | TOOL CALL DISC.  |   |
|  | [Progress bar]   | | [Progress bar]   | | [Progress bar]   |   |
|  | 154/1350 (11.4%) | | 0/250 (0%)       | | 7635/16200 (47%) |   |
|  | Resets in 2h 14m | | Resets in 42m    | | Resets in 1h 8m  |   |
|  +------------------+ +------------------+ +------------------+   |
|                                                                    |
|  +--------------------------------------------------------------+ |
|  |  USAGE INSIGHTS                                               | |
|  |  "You've used 11.4% of subscription. At current rate..."     | |
|  +--------------------------------------------------------------+ |
|                                                                    |
|  +--------------------------------------------------------------+ |
|  |  USAGE OVER TIME          [1h] [6h] [24h] [7d] [30d]        | |
|  |  [Chart.js area chart with all 3 quota lines]                | |
|  +--------------------------------------------------------------+ |
|                                                                    |
|  +--------------------------------------------------------------+ |
|  |  RESET CYCLE HISTORY     [Subscription ▼]                    | |
|  |  Cycle | Start | End | Duration | Peak | Total | Rate        | |
|  |  #47   | 11:16 | 16:16 | 5h 0m | 342  | 312   | 62/hr      | |
|  |  #46   | 06:16 | 11:16 | 5h 0m | 298  | 287   | 57/hr      | |
|  +--------------------------------------------------------------+ |
+-------------------------------------------------------------------+
```

## Component Overrides

### Quota Cards — Enhanced for Visual Urgency

Each quota card MUST show ALL of the following. This is the core value proposition:

1. **Quota type name** — e.g., "Subscription Quota"
2. **What this quota means** — one-line plain English description
   - Subscription: "Main API request quota for your plan"
   - Search Hourly: "Search endpoint calls, resets every hour"
   - Tool Call Discounts: "Discounted tool call requests"
3. **Progress bar** — color-coded by threshold
4. **Usage fraction** — `154.3 / 1,350` (monospace, keep decimal for subscription)
5. **Usage percentage** — `11.4%` (large, bold, color-coded)
6. **Reset countdown** — "Resets in 2h 14m" (live countdown, monospace)
7. **Absolute reset time** — "Feb 6, 4:16 PM" (for planning ahead)
8. **Status badge** — "Healthy" / "Warning" / "Danger" / "Critical" with icon
9. **Cycle stats** (if tracking data exists) — avg per cycle, current rate

### Tool Call Discounts — Special Attention

The Tool Call Discounts quota has its own `renewsAt` that is **different** from subscription.
This MUST be shown clearly:
- Same card format as other quotas
- Its own independent countdown timer
- Its own reset cycle tracking
- Clearly labeled so users don't confuse with subscription

### Usage Insights Panel

Plain English insights that help users **understand** their consumption:

```
"Subscription: You've used 11.4% of your 1,350 request quota.
 At your current rate (~26 req/hr), you'll use approximately 130 requests
 before the next reset in 2h 14m. You're well within limits."

"Tool Calls: You've used 47.1% of your 16,200 tool call quota.
 This is approaching the halfway mark. Your average usage per cycle is
 ~8,100 calls. Consider monitoring if usage increases."

"Since tracking began (3 days ago), you've completed 14 subscription
 reset cycles. Average usage per cycle: 312 requests (23.1% of limit)."
```

### Chart — Time Series Area Chart

- **Default view:** Last 6 hours
- **Series:** All 3 quota types shown as percentage of their limit (normalized 0-100%)
- **Why percentage:** Makes different quotas comparable on same axis
- **Toggle:** User can show/hide individual series
- **Time ranges:** 1h, 6h, 24h, 7d, 30d (buttons, Material filled-tonal style)
- **Vertical lines:** Mark reset events (dashed, `--md-outline-variant`)
- **Tooltip:** Shows absolute values + percentage + time

### Reset Cycle History Table

- **Filter:** Dropdown to select quota type
- **Default:** Show subscription cycles
- **Columns:** Cycle #, Start Time, End Time, Duration, Peak Usage, Total Delta, Avg Rate
- **Highlight:** Current (ongoing) cycle at top with "Active" badge
- **Empty state:** "No cycle data yet. Tracking begins on first poll."

### Session History Section

Each agent run is a tracked session. Displayed below the cycle history:

- **Columns:** Session ID (short UUID), Started, Ended (or "Active"), Duration, Sub Max, Search Max, Tool Max, Snapshots
- **Active session:** Highlighted with "Active" badge (green)
- **Max values:** The highest request count observed during that session (= consumption)
- **Use case:** "During my last coding session (2h), I consumed 342 subscription requests"
- **Empty state:** "No sessions recorded yet."

## Spacing Overrides

- Cards: `gap: 16px` on desktop, `gap: 12px` on mobile
- Sections: `margin-bottom: 24px`
- Card internal padding: `20px`

## Performance Notes for Dashboard

- Chart.js loaded via CDN with `defer` (not blocking)
- Auto-refresh via `setInterval` matching poll interval (default 60s)
- Only fetch delta from `/api/history` (not full dataset each time)
- DOM updates use `requestAnimationFrame` for smooth rendering
- Countdown timers use single shared `setInterval(1000)` for all 3 countdowns
- Theme switch uses CSS custom properties (instant, no re-render)
