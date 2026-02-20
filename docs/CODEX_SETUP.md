# Codex Setup Guide

Track Codex quota usage in onWatch (`v2.10.4`).

---

## Prerequisites

- Codex account access with a valid OAuth auth state
- Codex auth file present at `~/.codex/auth.json` (or `$CODEX_HOME/auth.json`)
- onWatch installed ([Quick Start](../README.md#quick-start))

---

## Step 1: Confirm Codex Auth File Exists

macOS / Linux:

```bash
ls -la ~/.codex/auth.json
```

If you use a custom Codex home:

```bash
ls -la "$CODEX_HOME/auth.json"
```

Windows (PowerShell):

```powershell
Get-Item "$env:USERPROFILE\.codex\auth.json"
```

---

## Step 2: Get the Access Token

macOS / Linux (default path):

```bash
python3 -c "import json,os; p=os.path.expanduser('~/.codex/auth.json'); print(json.load(open(p))['tokens']['access_token'])"
```

Custom `CODEX_HOME`:

```bash
python3 -c "import json,os; p=os.path.join(os.environ['CODEX_HOME'],'auth.json'); print(json.load(open(p))['tokens']['access_token'])"
```

Windows (PowerShell):

```powershell
(Get-Content "$env:USERPROFILE\.codex\auth.json" | ConvertFrom-Json).tokens.access_token
```

---

## Step 3: Configure onWatch

Add token to `.env`:

```bash
cd ~/.onwatch
```

Set:

```bash
CODEX_TOKEN=your_codex_oauth_access_token
```

Notes:
- If Codex is your only provider, `CODEX_TOKEN` must be set so startup validation passes.
- If another provider is already configured, onWatch can auto-detect Codex auth when `CODEX_TOKEN` is omitted.
- While running, onWatch re-reads Codex credentials and can pick up token rotation from `auth.json`.

---

## How Codex Auth Resolution Works

onWatch follows this order:

1. Use `CODEX_TOKEN` from `.env` (or environment) if set.
2. If missing, try Codex auth state from:
   - `CODEX_HOME/auth.json` (when `CODEX_HOME` is set)
   - `~/.codex/auth.json` (default)
3. During runtime, keep checking auth state and refresh token usage automatically when credentials change.

This is aligned with the Anthropic provider behavior: explicit env token first, local auth-state detection as fallback, and runtime refresh for credential changes.

---

## Installation Scenarios

### Codex-only install

Set `CODEX_TOKEN` in `.env`, then start onWatch.

### Multi-provider install

If another provider key is already set, Codex can be enabled via auth-state auto-detection even without `CODEX_TOKEN`.

### Custom Codex home

Set `CODEX_HOME` to your custom Codex directory so onWatch reads `CODEX_HOME/auth.json`.

---

## Step 4: Restart onWatch

```bash
onwatch stop
onwatch
```

Or run in foreground:

```bash
onwatch --debug
```

---

## Step 5: Verify in Dashboard

Open `http://localhost:9211` and select the **Codex** tab.

You should see:
- **5-Hour Limit** utilization
- **Weekly All-Model** utilization
- Reset timers, usage history, and projections

---

## Troubleshooting

### "failed to load config: at least one provider must be configured"

Set at least one provider token in `.env` (for Codex-only, set `CODEX_TOKEN`).

### "Codex polling paused due to repeated auth failures"

Refresh your Codex login so `auth.json` has a new access token, then restart onWatch.

### Token security

- Keep `.env` out of version control
- onWatch only sends the token to Codex usage endpoints
- Usage history stays local in SQLite

---

## See Also

- [README](../README.md) — Quick start and provider overview
- [Development Guide](DEVELOPMENT.md) — Build and internals
