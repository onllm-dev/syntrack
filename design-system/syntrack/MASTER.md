# SynTrack - Design System Master

> **LOGIC:** When building a specific page, first check `design-system/pages/[page-name].md`.
> If that file exists, its rules **override** this Master file.
> If not, strictly follow the rules below.

---

**Project:** SynTrack
**Updated:** 2026-02-06
**Design Language:** Google Material Design 3 (Material You) — adapted for monitoring dashboards
**Theme:** Dual-mode (Dark + Light) with system preference detection

---

## Design Philosophy

SynTrack follows Google Material Design 3 principles adapted for a monitoring dashboard:

1. **Elevated surfaces** — Cards float above the background using Material elevation system
2. **Dynamic color** — Theme-aware colors using CSS custom properties
3. **Motion** — Purposeful transitions (200ms ease), not decorative animation
4. **Density** — Comfortable density for data-heavy dashboard (not compact, not spacious)
5. **At-a-glance clarity** — Users must immediately see: (a) what's near limit, (b) when it resets

---

## Color System (Material Design 3)

### Dark Mode (Default)

| Role | Hex | CSS Variable | Usage |
|------|-----|--------------|-------|
| Surface | `#121212` | `--md-surface` | Page background |
| Surface Container | `#1E1E1E` | `--md-surface-container` | Card backgrounds |
| Surface Container High | `#2C2C2C` | `--md-surface-container-high` | Elevated cards, hover |
| On Surface | `#E6E1E5` | `--md-on-surface` | Primary text |
| On Surface Variant | `#CAC4D0` | `--md-on-surface-variant` | Secondary/muted text |
| Outline | `#938F99` | `--md-outline` | Borders, dividers |
| Outline Variant | `#49454F` | `--md-outline-variant` | Subtle borders |
| Primary | `#D0BCFF` | `--md-primary` | Primary accent (buttons, links) |
| Primary Container | `#4F378B` | `--md-primary-container` | Primary container fills |
| On Primary | `#381E72` | `--md-on-primary` | Text on primary |
| Secondary | `#CCC2DC` | `--md-secondary` | Secondary accent |
| Error | `#F2B8B5` | `--md-error` | Error states, danger (>80% usage) |
| Error Container | `#8C1D18` | `--md-error-container` | Error fills |

### Light Mode

| Role | Hex | CSS Variable | Usage |
|------|-----|--------------|-------|
| Surface | `#FEF7FF` | `--md-surface` | Page background |
| Surface Container | `#F3EDF7` | `--md-surface-container` | Card backgrounds |
| Surface Container High | `#ECE6F0` | `--md-surface-container-high` | Elevated cards |
| On Surface | `#1D1B20` | `--md-on-surface` | Primary text |
| On Surface Variant | `#49454F` | `--md-on-surface-variant` | Secondary text |
| Outline | `#79747E` | `--md-outline` | Borders |
| Outline Variant | `#CAC4D0` | `--md-outline-variant` | Subtle borders |
| Primary | `#6750A4` | `--md-primary` | Primary accent |
| Primary Container | `#EADDFF` | `--md-primary-container` | Primary fills |
| On Primary | `#FFFFFF` | `--md-on-primary` | Text on primary |
| Secondary | `#625B71` | `--md-secondary` | Secondary accent |
| Error | `#B3261E` | `--md-error` | Error states |
| Error Container | `#F9DEDC` | `--md-error-container` | Error fills |

### Quota Status Colors (Both Modes)

These are semantic colors for the usage meters — the core visual feature of SynTrack.

| Status | Condition | Dark Hex | Light Hex | CSS Variable |
|--------|-----------|----------|-----------|-------------|
| Healthy | 0-49% usage | `#4ADE80` | `#16A34A` | `--status-healthy` |
| Warning | 50-79% usage | `#FBBF24` | `#D97706` | `--status-warning` |
| Danger | 80-94% usage | `#F87171` | `#DC2626` | `--status-danger` |
| Critical | 95-100% usage | `#EF4444` | `#B91C1C` | `--status-critical` |
| Resetting Soon | < 30min to reset | `#38BDF8` | `#0284C7` | `--status-resetting` |

**Accessibility rule:** Color is NEVER the only indicator. Always pair with:
- Text label ("Healthy", "Warning", "Danger", "Critical")
- SVG icon (checkmark, warning triangle, alert circle)
- Numeric percentage

---

## Typography

### Font Stack

```css
/* Primary: Inter (Material-compatible, clean, readable) */
--font-primary: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;

/* Monospace: For numbers/data */
--font-mono: 'JetBrains Mono', 'Fira Code', 'SF Mono', monospace;
```

**Google Fonts import (optional — system fonts work fine for minimal RAM):**
```css
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap');
```

**RAM Note:** System font stack is preferred for background-agent use. Google Fonts only loaded when dashboard is actively viewed (lazy-load in template).

### Type Scale (Material Design 3)

| Role | Size | Weight | Line Height | Usage |
|------|------|--------|-------------|-------|
| Display Large | 57px | 400 | 64px | Not used (dashboard) |
| Headline Large | 32px | 600 | 40px | Page title "SynTrack" |
| Headline Medium | 28px | 500 | 36px | Section headers |
| Title Large | 22px | 500 | 28px | Card titles |
| Title Medium | 16px | 600 | 24px | Quota type labels |
| Body Large | 16px | 400 | 24px | Descriptions, insights |
| Body Medium | 14px | 400 | 20px | Secondary text |
| Label Large | 14px | 600 | 20px | Buttons, badges |
| Label Medium | 12px | 500 | 16px | Captions, timestamps |
| **KPI Value** | 36px | 700 | 1.1 | Usage numbers (monospace) |
| **Countdown** | 20px | 600 | 1.2 | Reset countdown (monospace) |

---

## Elevation System (Material Design 3)

| Level | Dark Mode | Light Mode | Usage |
|-------|-----------|------------|-------|
| Level 0 | Surface `#121212` | Surface `#FEF7FF` | Page background |
| Level 1 | `#1E1E1E` | White + `0 1px 3px rgba(0,0,0,0.12)` | Cards at rest |
| Level 2 | `#2C2C2C` | White + `0 3px 6px rgba(0,0,0,0.16)` | Cards on hover |
| Level 3 | `#353535` | White + `0 6px 12px rgba(0,0,0,0.16)` | Floating panels |

**Dark mode note:** Material Design uses surface tint (lighter surface) instead of shadow in dark mode.

---

## Component Specifications

### Quota Card (Core Component)

The most important UI element. Each card represents one quota type.

```
+---------------------------------------------------------------------+
|  [Icon]  SUBSCRIPTION QUOTA              Resets in 2h 14m  [Clock]  |
|                                                                     |
|     ████████████████░░░░░░░░░░░░░░░░░░░░░░░░  154 / 1,350         |
|     [==========                              ]  11.4%              |
|                                                                     |
|  Status: Healthy                          Resets: Feb 6, 4:16 PM   |
|  Avg per cycle: 312 requests              Rate: ~26 req/hr         |
+---------------------------------------------------------------------+
```

**Card structure:**
1. **Header row:** Quota type label + Reset countdown (right-aligned, monospace)
2. **Progress bar:** Color-coded by threshold, animated width
3. **Usage text:** `X / Y used` (monospace numbers) + percentage
4. **Footer row:** Status badge + Absolute reset time + Cycle stats

**Progress bar behavior:**
- Width animates smoothly (`transition: width 500ms ease`)
- Color transitions at threshold boundaries (smooth CSS transition)
- Pulse animation when > 95% (subtle, respects `prefers-reduced-motion`)

**Reset countdown:**
- Shows "Xh Ym" when > 1 hour
- Shows "Xm Xs" when < 1 hour
- Shows "< 1m" when imminent
- Text turns `--status-resetting` blue when < 30 minutes
- Updates every second via JS `setInterval`

### Theme Toggle

- Sun/Moon icon toggle in header bar
- Persists choice in `localStorage`
- Respects `prefers-color-scheme` on first visit
- Smooth transition: `transition: background-color 200ms, color 200ms`

### Navigation / Header

```
+---------------------------------------------------------------------+
|  [S] SynTrack          Last updated: 12:34:56    [Sun/Moon] [Gear]  |
+---------------------------------------------------------------------+
```

- Sticky top bar (not floating — saves layout complexity)
- Connection status: green dot = fresh data, yellow = stale (>2x interval)
- Settings gear: opens inline panel (poll interval, time range, etc.)

### Charts

- **Library:** Chart.js 4.x (via CDN, ~60KB gzipped)
- **Type:** Line chart with fill (area) for usage over time
- **Dark mode:** Chart.js respects CSS variables via `getComputedStyle`
- **Interactions:** Hover tooltip, click to toggle series, time range selector
- **Colors:** Each quota type gets its own hue from the status palette
- **Grid:** `--md-outline-variant` for grid lines
- **Labels:** `--md-on-surface-variant` for axis labels

### Reset Cycle Table

- Material Design data table style
- Striped rows using `--md-surface-container` alternation
- Sortable columns (click header)
- Columns: Cycle #, Start, End, Duration, Peak Usage, Total Requests, Avg Rate
- Mobile: horizontal scroll with `overflow-x-auto` wrapper

---

## Responsive Breakpoints

| Breakpoint | Width | Layout |
|------------|-------|--------|
| Mobile | < 768px | Single column, cards stack, chart below |
| Tablet | 768-1023px | 2-column card grid, chart full width |
| Desktop | 1024-1439px | 3-column card grid (one per quota), chart below |
| Large | >= 1440px | 3-column cards + chart side panel |

---

## Animation Guidelines

| Animation | Duration | Easing | When |
|-----------|----------|--------|------|
| Card hover lift | 200ms | ease | Mouse enter card |
| Progress bar fill | 500ms | ease-out | Data update |
| Theme transition | 200ms | ease | Theme toggle |
| KPI count-up | 400ms | ease-out | Data refresh |
| Countdown tick | none | - | Every second (just text update) |
| Chart data | 300ms | ease | New data point |

**`prefers-reduced-motion: reduce`:**
- Disable all `transform` animations
- Keep opacity transitions (they're low-motion)
- Instant progress bar updates
- Keep countdown text updates (not animation)

---

## Accessibility Requirements

| Requirement | Implementation |
|-------------|---------------|
| Color contrast | 4.5:1 minimum (WCAG AA) for all text |
| Focus states | 2px `--md-primary` ring on all interactive elements |
| Keyboard navigation | Tab order: theme toggle → time range → quota cards → chart → table |
| Screen reader | `aria-label` on progress bars, `role="progressbar"` with `aria-valuenow` |
| Status indicators | Color + icon + text (never color alone) |
| Skip link | "Skip to dashboard" link for keyboard users |
| Reduced motion | `prefers-reduced-motion` query on all animations |
| `lang` attribute | `<html lang="en">` |

---

## Anti-Patterns (DO NOT USE)

- Emojis as icons — use SVG icons (Lucide icon set)
- `outline: none` without replacement focus style
- Color-only status indicators
- Layout-shifting hover effects (`scale` on cards)
- Auto-playing animations that can't be paused
- Horizontal scroll on mobile (except data tables with wrapper)
- Font loading that blocks render (use `font-display: swap`)
- More than 2 external requests for fonts/icons (RAM concern)

---

## Pre-Delivery Checklist

- [ ] Dark mode renders correctly (test on `#121212` background)
- [ ] Light mode renders correctly (test on `#FEF7FF` background)
- [ ] Theme toggle persists across page reload
- [ ] `prefers-color-scheme` detected on first visit
- [ ] All text meets 4.5:1 contrast in both modes
- [ ] Progress bars have `role="progressbar"` + `aria-valuenow`
- [ ] Status uses color + icon + text (not color alone)
- [ ] Countdown timers update every second
- [ ] Reset time shown in both relative ("2h 14m") and absolute ("4:16 PM")
- [ ] Charts render in both dark and light modes
- [ ] Responsive at 375px, 768px, 1024px, 1440px
- [ ] No horizontal scroll on mobile (except tables)
- [ ] `cursor-pointer` on all interactive elements
- [ ] `prefers-reduced-motion` respected
- [ ] No emojis used as icons
- [ ] Focus ring visible on keyboard navigation
- [ ] No external font blocking render (system fonts or `font-display: swap`)
