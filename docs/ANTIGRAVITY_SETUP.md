# Antigravity (Windsurf) Setup Guide

Track your Windsurf AI code editor model quota usage with onWatch.

---

## What is Antigravity?

Antigravity is the internal name for the Windsurf/Codeium AI code editor's language server. onWatch can monitor your model quotas by connecting to the locally running language server process.

---

## Prerequisites

- Windsurf AI code editor installed and running
- onWatch installed ([Quick Start](../README.md#quick-start))

---

## How It Works

onWatch automatically detects the Antigravity language server running on your machine by:

1. Scanning for the `antigravity` process
2. Extracting the CSRF token and port from command-line arguments
3. Connecting via the Connect RPC protocol
4. Polling the `/exa.language_server_pb.LanguageServerService/GetUserStatus` endpoint

No manual configuration is required for local development.

---

## Quick Start (Auto-Detection)

### Step 1: Enable Antigravity in onWatch

Add to your `.env` file:

```bash
cd ~/.onwatch
```

Edit `.env` and add:

```
ANTIGRAVITY_ENABLED=true
```

Or set it as an environment variable:

```bash
export ANTIGRAVITY_ENABLED=true
```

### Step 2: Restart onWatch

```bash
onwatch stop
onwatch
```

Or in debug mode to verify:

```bash
onwatch --debug
```

You should see:

```
Antigravity auto-detection enabled (process scanning mode)
Starting Antigravity agent (interval: 60s)
connected to Antigravity language server port=63516 protocol=https
```

### Step 3: View Dashboard

Open http://localhost:9211 and click the **antigravity** tab.

You'll see quota cards for each model, including:
- Claude Sonnet
- Gemini Pro
- GPT-4 variants
- And other available models

---

## Docker/Container Configuration

In containerized environments where process scanning doesn't work, you can manually configure the connection:

### Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `ANTIGRAVITY_ENABLED` | Enable Antigravity provider | `true` |
| `ANTIGRAVITY_BASE_URL` | Base URL of the language server | `https://127.0.0.1:42100` |
| `ANTIGRAVITY_CSRF_TOKEN` | CSRF token from the process | `your_csrf_token_here` |

### Example Docker Compose

```yaml
services:
  onwatch:
    image: ghcr.io/onllm-dev/onwatch:latest
    environment:
      - ANTIGRAVITY_ENABLED=true
      - ANTIGRAVITY_BASE_URL=https://host.docker.internal:42100
      - ANTIGRAVITY_CSRF_TOKEN=your_csrf_token
    ports:
      - "9211:9211"
```

### Finding the Port and Token

On the host machine, find the running Antigravity process:

```bash
# macOS/Linux
ps aux | grep antigravity | grep -E "csrf_token|extension_server_port"

# Example output:
# /path/to/antigravity --csrf_token=abc123 --extension_server_port=42100
```

Extract the values:
- `--extension_server_port=42100` - Use as the port in `ANTIGRAVITY_BASE_URL`
- `--csrf_token=abc123` - Use as `ANTIGRAVITY_CSRF_TOKEN`

---

## What Gets Tracked

| Metric | Description |
|--------|-------------|
| Model Quotas | Per-model remaining fraction (0.0 to 1.0) |
| Reset Times | When each model's quota resets |
| Prompt Credits | Available credits for your plan |
| Plan Info | Your subscription tier (Free, Pro, etc.) |

The dashboard shows:
- Usage percentage for each AI model
- Remaining quota with color indicators
- Reset countdown timers
- Usage history and projections

---

## Supported Models

onWatch tracks all models available in your Windsurf subscription:

| Model ID | Display Name |
|----------|--------------|
| `claude-4-5-sonnet` | Claude 4.5 Sonnet |
| `claude-4-5-sonnet-thinking` | Claude 4.5 Sonnet (Thinking) |
| `gemini-3-pro` | Gemini 3 Pro |
| `gemini-3-flash` | Gemini 3 Flash |
| (others) | Automatically detected |

---

## Troubleshooting

### "Antigravity agent not starting"

1. Verify Windsurf is running:
   ```bash
   ps aux | grep antigravity
   ```

2. Check if the process has the required arguments:
   ```bash
   ps aux | grep antigravity | grep csrf_token
   ```

3. Ensure `ANTIGRAVITY_ENABLED=true` is set

### "No models showing"

- Make sure you're logged into Windsurf
- Check that your subscription is active
- Run onWatch in debug mode: `onwatch --debug`

### "Connection refused"

The language server might be using a self-signed certificate. onWatch handles this automatically, but if you're using manual configuration:

- Ensure the port is correct
- Try both `https://` and `http://` protocols
- Check firewall settings

### "CSRF token invalid"

The token changes when Windsurf restarts. For auto-detection mode, restart onWatch after restarting Windsurf:

```bash
onwatch stop && onwatch
```

For manual configuration, update `ANTIGRAVITY_CSRF_TOKEN` with the new value.

---

## API Details

onWatch uses the Connect RPC protocol to communicate with the Antigravity language server:

**Endpoint:**
```
POST /exa.language_server_pb.LanguageServerService/GetUserStatus
```

**Headers:**
```
Content-Type: application/json
Connect-Protocol-Version: 1
X-Codeium-Csrf-Token: <token>
```

**Response structure:**
```json
{
  "userStatus": {
    "email": "user@example.com",
    "planStatus": {
      "availablePromptCredits": 500,
      "planInfo": {
        "planName": "Pro",
        "monthlyPromptCredits": 1000
      }
    },
    "cascadeModelConfigData": {
      "clientModelConfigs": [
        {
          "label": "Claude Sonnet",
          "modelOrAlias": {"model": "claude-4-5-sonnet"},
          "quotaInfo": {
            "remainingFraction": 0.75,
            "resetTime": "2026-02-24T12:00:00Z"
          }
        }
      ]
    }
  }
}
```

---

## Security Notes

- onWatch connects only to localhost (or your configured URL)
- The CSRF token is never sent to external servers
- All data stays on your machine (SQLite database)
- Auto-detection only reads process arguments, not memory

---

## See Also

- [Development Guide](DEVELOPMENT.md) - Build from source
- [Copilot Setup](COPILOT_SETUP.md) - Track GitHub Copilot quotas
- [Codex Setup](CODEX_SETUP.md) - Track OpenAI Codex quotas
- [README](../README.md) - Quick start and configuration
